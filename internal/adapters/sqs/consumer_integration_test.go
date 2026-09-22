//go:build integration

package sqsadapter

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/jackc/pgx/v5/pgxpool"

	postgresadapter "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/adapters/postgres"
	applicationingestion "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/application/ingestion"
	applicationwagering "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/application/wagering"
	applicationwallet "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/application/wallet"
	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/domain/money"
	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/platform/config"
	platformid "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/platform/id"
	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/platform/metrics"
)

func TestConsumerRedrivesInvalidMessageToDLQ(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	client := integrationSQSClient(t, ctx)
	suffix := integrationSuffix(t)
	dlqURL := createIntegrationQueue(t, client, "wager-redrive-dlq-"+suffix+".fifo", fifoAttributes())
	dlqARN := queueARN(t, ctx, client, dlqURL)
	sourceAttributes := fifoAttributes()
	sourceAttributes[string(types.QueueAttributeNameVisibilityTimeout)] = "1"
	sourceAttributes[string(types.QueueAttributeNameReceiveMessageWaitTimeSeconds)] = "1"
	sourceAttributes[string(types.QueueAttributeNameRedrivePolicy)] = fmt.Sprintf(
		`{"deadLetterTargetArn":%q,"maxReceiveCount":"3"}`, dlqARN,
	)
	sourceName := "wager-redrive-" + suffix + ".fifo"
	sourceURL := createIntegrationQueue(t, client, sourceName, sourceAttributes)

	cfg := integrationConsumerConfig(sourceName)
	cfg.SQSVisibility = time.Second
	cfg.SQSProcessing = 500 * time.Millisecond
	broker, err := NewBroker(cfg)
	if err != nil {
		t.Fatal(err)
	}
	instrumentation := metrics.New()
	consumer := newConsumer(broker, panicIngester{}, cfg, discardLogger(), instrumentation)
	stop := startIntegrationConsumer(t, ctx, consumer)

	sendIntegrationMessage(t, ctx, client, sourceURL, "invalid", "invalid-"+suffix, `{}`)
	dlqMessage := receiveIntegrationMessage(t, ctx, client, dlqURL)
	if aws.ToString(dlqMessage.Body) != `{}` {
		t.Fatalf("DLQ body = %q, want invalid source body", aws.ToString(dlqMessage.Body))
	}
	stop()

	result, err := client.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
		QueueUrl: aws.String(sourceURL), MaxNumberOfMessages: 1, WaitTimeSeconds: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Messages) != 0 {
		t.Fatalf("source queue still contains %d message(s) after redrive", len(result.Messages))
	}
	metricsBody := gatherMetrics(t, instrumentation)
	if !strings.Contains(metricsBody, `wager_sqs_consumer_messages_received_total{delivery="redelivery"} 2`) {
		t.Fatal("redelivery metric did not record the retries preceding the DLQ")
	}
	if !strings.Contains(metricsBody, `wager_sqs_consumer_messages_processed_total{result="invalid"} 3`) {
		t.Fatal("invalid processing metric did not record every redrive attempt")
	}
}

func TestThreeConsumersProcessIndependentGroupsAndDeduplicateRedelivery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	client := integrationSQSClient(t, ctx)
	suffix := integrationSuffix(t)
	queueName := "wager-consumers-" + suffix + ".fifo"
	queueAttributes := fifoAttributes()
	queueAttributes[string(types.QueueAttributeNameVisibilityTimeout)] = "10"
	queueAttributes[string(types.QueueAttributeNameReceiveMessageWaitTimeSeconds)] = "1"
	queueURL := createIntegrationQueue(t, client, queueName, queueAttributes)

	setupPool, walletService, _ := integrationFinancialServices(t, ctx)
	t.Cleanup(setupPool.Close)
	fixtures := make([]consumerFixture, 3)
	for index := range fixtures {
		playerID := integrationID(t)
		account, err := walletService.Open(ctx, applicationwallet.OpenInput{
			PlayerID:      playerID,
			Initial:       integrationMoney(t, "100.00"),
			CorrelationID: "open-" + integrationID(t),
		})
		if err != nil {
			t.Fatal(err)
		}
		messageID := "message-" + integrationID(t)
		fixtures[index] = consumerFixture{
			walletID:  account.ID(),
			messageID: messageID,
			body: integrationMessageBody(
				messageID, account.ID(), playerID, "external-"+integrationID(t), "key-"+integrationID(t),
			),
		}
	}

	gate := make(chan struct{})
	entered := make(chan consumeEvent, 6)
	completed := make(chan consumeEvent, 6)
	deleted := make(chan int, 6)
	stops := make([]func(), 0, 3)
	for index := 0; index < 3; index++ {
		pool, _, ingestionService := integrationFinancialServices(t, ctx)
		t.Cleanup(pool.Close)
		cfg := integrationConsumerConfig(queueName)
		cfg.SQSConsumerName = "integration-consumers-" + suffix
		broker, err := NewBroker(cfg)
		if err != nil {
			t.Fatal(err)
		}
		consumerID := index + 1
		consumer := newConsumer(
			&deleteTrackingBroker{Broker: broker, consumerID: consumerID, deleted: deleted},
			&gatedIngester{id: consumerID, delegate: ingestionService, gate: gate, entered: entered, completed: completed},
			cfg, discardLogger(), metrics.New(),
		)
		stops = append(stops, startIntegrationConsumer(t, ctx, consumer))
		sendIntegrationMessage(t, ctx, client, queueURL, fixtures[index].walletID, fmt.Sprintf("initial-%d-%s", index, suffix), fixtures[index].body)
		event := awaitConsumeEvent(t, ctx, entered)
		if event.consumerID != consumerID {
			t.Fatalf("message %d entered consumer %d, want %d", index, event.consumerID, consumerID)
		}
	}

	close(gate)
	for range fixtures {
		event := awaitConsumeEvent(t, ctx, completed)
		if event.err != nil || event.result.Duplicate || event.result.WagerResult.Replay {
			t.Fatalf("initial consume by consumer %d = %+v, error=%v", event.consumerID, event.result, event.err)
		}
	}
	for range fixtures {
		awaitDelete(t, ctx, deleted)
	}

	for index, fixture := range fixtures {
		sendIntegrationMessage(t, ctx, client, queueURL, fixture.walletID, fmt.Sprintf("redelivery-%d-%s", index, suffix), fixture.body)
	}
	for range fixtures {
		event := awaitConsumeEvent(t, ctx, completed)
		if event.err != nil || !event.result.Duplicate || !event.result.WagerResult.Replay {
			t.Fatalf("redelivery consume by consumer %d = %+v, error=%v", event.consumerID, event.result, event.err)
		}
	}
	for range fixtures {
		awaitDelete(t, ctx, deleted)
	}
	for _, stop := range stops {
		stop()
	}

	for _, fixture := range fixtures {
		account, err := walletService.FindByID(ctx, fixture.walletID)
		if err != nil {
			t.Fatal(err)
		}
		if account.Balance().MinorUnits() != 7_500 || account.Version() != 2 {
			t.Fatalf("wallet %s state = %d/v%d, want 7500/v2", fixture.walletID, account.Balance().MinorUnits(), account.Version())
		}
		var inboxRows, ledgerRows int
		if err := setupPool.QueryRow(ctx, `SELECT count(*) FROM consumer_inbox WHERE consumer_name=$1 AND message_id=$2 AND completed_at IS NOT NULL`,
			"integration-consumers-"+suffix, fixture.messageID).Scan(&inboxRows); err != nil {
			t.Fatal(err)
		}
		if err := setupPool.QueryRow(ctx, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id=$1`, fixture.walletID).Scan(&ledgerRows); err != nil {
			t.Fatal(err)
		}
		if inboxRows != 1 || ledgerRows != 2 {
			t.Fatalf("wallet %s inbox/ledger = %d/%d, want 1/2", fixture.walletID, inboxRows, ledgerRows)
		}
	}
}

type consumerFixture struct {
	walletID  string
	messageID string
	body      string
}

type consumeEvent struct {
	consumerID int
	result     applicationingestion.Result
	err        error
}

type gatedIngester struct {
	id        int
	delegate  messageIngester
	gate      <-chan struct{}
	entered   chan<- consumeEvent
	completed chan<- consumeEvent
}

func (g *gatedIngester) Consume(ctx context.Context, consumer string, message applicationingestion.Message) (applicationingestion.Result, error) {
	entry := consumeEvent{consumerID: g.id}
	select {
	case g.entered <- entry:
	case <-ctx.Done():
		return applicationingestion.Result{}, ctx.Err()
	}
	select {
	case <-g.gate:
	case <-ctx.Done():
		return applicationingestion.Result{}, ctx.Err()
	}
	result, err := g.delegate.Consume(ctx, consumer, message)
	completed := consumeEvent{consumerID: g.id, result: result, err: err}
	select {
	case g.completed <- completed:
	case <-ctx.Done():
	}
	return result, err
}

type deleteTrackingBroker struct {
	*Broker
	consumerID int
	deleted    chan<- int
}

func (b *deleteTrackingBroker) Delete(ctx context.Context, queueURL, receiptHandle string) error {
	if err := b.Broker.Delete(ctx, queueURL, receiptHandle); err != nil {
		return err
	}
	select {
	case b.deleted <- b.consumerID:
	case <-ctx.Done():
	}
	return nil
}

type panicIngester struct{}

func (panicIngester) Consume(context.Context, string, applicationingestion.Message) (applicationingestion.Result, error) {
	panic("invalid SQS envelope reached ingestion service")
}

func integrationFinancialServices(t *testing.T, ctx context.Context) (*pgxpool.Pool, *applicationwallet.Service, *applicationingestion.Service) {
	t.Helper()
	databaseURL := os.Getenv("APP_DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://wager_app:wager_app_local@localhost:5432/wagering?sslmode=disable"
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	unit := postgresadapter.NewUnitOfWork(pool)
	wallets := postgresadapter.NewWalletRepository()
	wagers := postgresadapter.NewWagerRepository()
	ledger := postgresadapter.NewLedgerRepository()
	outbox := postgresadapter.NewOutboxRepository()
	wagerService := applicationwagering.NewService(postgresadapter.NewWagerStore(unit, wallets, wagers, ledger, outbox))
	walletService := applicationwallet.NewService(postgresadapter.NewWalletStore(unit, wallets, wagers, ledger, outbox))
	ingestionService := applicationingestion.NewService(
		postgresadapter.NewIngestionStore(unit, postgresadapter.NewInboxRepository(), wallets, wagers, ledger, outbox),
		wagerService,
	)
	return pool, walletService, ingestionService
}

func integrationSQSClient(t *testing.T, ctx context.Context) *awssqs.Client {
	t.Helper()
	cfg := integrationConsumerConfig("")
	sdkConfig, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(cfg.SQSRegion),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.SQSAccessKeyID, cfg.SQSSecretAccessKey, "")),
	)
	if err != nil {
		t.Fatal(err)
	}
	return awssqs.NewFromConfig(sdkConfig, func(options *awssqs.Options) {
		options.BaseEndpoint = aws.String(cfg.SQSEndpoint)
	})
}

func integrationConsumerConfig(queueName string) config.Config {
	endpoint := os.Getenv("APP_SQS_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://localhost:4566"
	}
	return config.Config{
		SQSEndpoint: endpoint, SQSRegion: "us-east-1", SQSAccessKeyID: "test", SQSSecretAccessKey: "test",
		SQSInputQueue: queueName, SQSConsumerName: "integration-consumer",
		SQSLongPoll: time.Second, SQSVisibility: 10 * time.Second, SQSProcessing: 8 * time.Second,
		SQSShutdown: 5 * time.Second, SQSPingTimeout: 2 * time.Second,
		SQSReceiveBatch: 1, SQSConcurrency: 1,
	}
}

func createIntegrationQueue(t *testing.T, client *awssqs.Client, name string, attributes map[string]string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := client.CreateQueue(ctx, &awssqs.CreateQueueInput{QueueName: aws.String(name), Attributes: attributes})
	if err != nil {
		t.Fatal(err)
	}
	queueURL := aws.ToString(result.QueueUrl)
	if queueURL == "" {
		t.Fatal("created queue returned an empty URL")
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := client.DeleteQueue(cleanupCtx, &awssqs.DeleteQueueInput{QueueUrl: aws.String(queueURL)}); err != nil {
			t.Errorf("delete integration queue %s: %v", name, err)
		}
	})
	return queueURL
}

func fifoAttributes() map[string]string {
	return map[string]string{
		string(types.QueueAttributeNameFifoQueue):                 "true",
		string(types.QueueAttributeNameContentBasedDeduplication): "false",
	}
}

func queueARN(t *testing.T, ctx context.Context, client *awssqs.Client, queueURL string) string {
	t.Helper()
	result, err := client.GetQueueAttributes(ctx, &awssqs.GetQueueAttributesInput{
		QueueUrl: aws.String(queueURL), AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
	})
	if err != nil {
		t.Fatal(err)
	}
	arn := result.Attributes[string(types.QueueAttributeNameQueueArn)]
	if arn == "" {
		t.Fatal("queue returned an empty ARN")
	}
	return arn
}

func sendIntegrationMessage(t *testing.T, ctx context.Context, client *awssqs.Client, queueURL, groupID, deduplicationID, body string) {
	t.Helper()
	_, err := client.SendMessage(ctx, &awssqs.SendMessageInput{
		QueueUrl: aws.String(queueURL), MessageGroupId: aws.String(groupID),
		MessageDeduplicationId: aws.String(deduplicationID), MessageBody: aws.String(body),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func receiveIntegrationMessage(t *testing.T, ctx context.Context, client *awssqs.Client, queueURL string) types.Message {
	t.Helper()
	for ctx.Err() == nil {
		result, err := client.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
			QueueUrl: aws.String(queueURL), MaxNumberOfMessages: 1, WaitTimeSeconds: 1, VisibilityTimeout: 10,
		})
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			t.Fatal(err)
		}
		if len(result.Messages) > 0 {
			return result.Messages[0]
		}
	}
	t.Fatalf("receive message from %s: %v", queueURL, ctx.Err())
	return types.Message{}
}

func startIntegrationConsumer(t *testing.T, ctx context.Context, consumer *Consumer) func() {
	t.Helper()
	if err := consumer.start(ctx); err != nil {
		t.Fatal(err)
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		stopCtx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		if err := consumer.stop(stopCtx); err != nil {
			t.Errorf("stop integration consumer: %v", err)
		}
	}
	t.Cleanup(stop)
	return stop
}

func awaitConsumeEvent(t *testing.T, ctx context.Context, events <-chan consumeEvent) consumeEvent {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-ctx.Done():
		t.Fatalf("wait for consumer event: %v", ctx.Err())
		return consumeEvent{}
	}
}

func awaitDelete(t *testing.T, ctx context.Context, deleted <-chan int) {
	t.Helper()
	select {
	case <-deleted:
	case <-ctx.Done():
		t.Fatalf("wait for message delete: %v", ctx.Err())
	}
}

func integrationMessageBody(messageID, walletID, playerID, externalID, idempotencyKey string) string {
	return fmt.Sprintf(
		`{"messageId":%q,"type":"WagerTransactionRequested","occurredAt":%q,"data":{"providerId":"provider-a","externalTransactionId":%q,"idempotencyKey":%q,"playerId":%q,"walletId":%q,"roundId":"round-1","gameId":"game-1","kind":"BET","money":{"amount":"25.00","currency":"BRL"}}}`,
		messageID, time.Now().UTC().Format(time.RFC3339Nano), externalID, idempotencyKey, playerID, walletID,
	)
}

func integrationID(t *testing.T) string {
	t.Helper()
	id, err := platformid.New()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func integrationSuffix(t *testing.T) string {
	t.Helper()
	return strings.ReplaceAll(integrationID(t), "-", "")[:12]
}

func integrationMoney(t *testing.T, amount string) money.Money {
	t.Helper()
	value, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
