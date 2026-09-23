//go:build integration

package bootstrap_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSIGTERMReleasesInFlightSQSMessageForAnotherProcess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	bin := filepath.Join(t.TempDir(), "wager-service")
	build := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/server")
	build.Dir = filepath.Clean(filepath.Join("..", ".."))
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build server: %v\n%s", err, output)
	}
	sdkConfig, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(envOr("APP_SQS_REGION", "us-east-1")),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	if err != nil {
		t.Fatal(err)
	}
	client := awssqs.NewFromConfig(sdkConfig, func(options *awssqs.Options) {
		options.BaseEndpoint = aws.String(envOr("APP_SQS_ENDPOINT", "http://localhost:4566"))
	})
	suffix := strings.ReplaceAll(newUUID(t), "-", "")[:12]
	queueName := "wager-shutdown-" + suffix + ".fifo"
	created, err := client.CreateQueue(ctx, &awssqs.CreateQueueInput{
		QueueName: aws.String(queueName),
		Attributes: map[string]string{
			string(types.QueueAttributeNameFifoQueue):                     "true",
			string(types.QueueAttributeNameContentBasedDeduplication):     "false",
			string(types.QueueAttributeNameVisibilityTimeout):             "30",
			string(types.QueueAttributeNameReceiveMessageWaitTimeSeconds): "1",
		},
	})
	if err != nil {
		t.Fatalf("create isolated queue: %v", err)
	}
	if created == nil || aws.ToString(created.QueueUrl) == "" {
		t.Fatal("create isolated queue: empty URL")
	}
	queueURL := aws.ToString(created.QueueUrl)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := client.DeleteQueue(cleanupCtx, &awssqs.DeleteQueueInput{QueueUrl: aws.String(queueURL)}); err != nil {
			t.Errorf("delete isolated queue: %v", err)
		}
	})
	databaseURL := envOr("APP_DATABASE_URL", "postgres://wager_app:wager_app_local@localhost:5432/wagering?sslmode=disable")
	parsedURL, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsedURL.Query()
	applicationName := "wager-shutdown-" + suffix
	query.Set("application_name", applicationName)
	parsedURL.RawQuery = query.Encode()
	consumerName := "shutdown-consumer-" + suffix
	port := freePort(t)
	overrides := []string{
		"APP_DATABASE_URL=" + parsedURL.String(),
		"APP_SQS_INPUT_QUEUE=" + queueName,
		"APP_SQS_CONSUMER_NAME=" + consumerName,
		"APP_SQS_LONG_POLL=1s", "APP_SQS_VISIBILITY_TIMEOUT=30s",
		"APP_SQS_PROCESSING_TIMEOUT=20s", "APP_SQS_SHUTDOWN_TIMEOUT=2s",
		"APP_SQS_RECEIVE_BATCH=1", "APP_SQS_CONCURRENCY=1",
	}
	active := startServer(t, ctx, bin, port, 0, overrides...)
	t.Cleanup(func() {
		if active != nil {
			active.stop(t)
		}
	})
	active.waitReady(t, ctx)
	issuer := envOr("APP_OIDC_ISSUER", "http://localhost:8081/realms/wagering")
	internalToken := clientToken(t, ctx, issuer, "internal-service", "internal-service-local")
	playerID := newUUID(t)
	opened := requestJSON(t, ctx, port, http.MethodPost, "/wallets", internalToken, "", map[string]any{
		"playerId": playerID, "initialBalance": map[string]string{"amount": "100.00", "currency": "BRL"},
	})
	if opened.status != http.StatusCreated {
		t.Fatalf("open wallet: %d %s", opened.status, opened.body)
	}
	walletID := opened.stringField(t, "id")
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	locked, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Rollback(ctx)
	var lockedID string
	if err := locked.QueryRow(ctx, `SELECT id::text FROM wallets WHERE id=$1 FOR NO KEY UPDATE`, walletID).Scan(&lockedID); err != nil {
		t.Fatal(err)
	}
	messageID := "shutdown-message-" + suffix
	externalID := "shutdown-bet-" + walletID
	messageBody := fmt.Sprintf(`{"messageId":%q,"type":"WagerTransactionRequested","occurredAt":%q,"data":{"providerId":"provider-a","externalTransactionId":%q,"idempotencyKey":%q,"playerId":%q,"walletId":%q,"roundId":"shutdown-round","gameId":"shutdown-game","kind":"BET","money":{"amount":"25.00","currency":"BRL"}}}`,
		messageID, time.Now().UTC().Format(time.RFC3339Nano), externalID, externalID, playerID, walletID)
	if _, err := client.SendMessage(ctx, &awssqs.SendMessageInput{
		QueueUrl: aws.String(queueURL), MessageGroupId: aws.String(walletID),
		MessageDeduplicationId: aws.String(messageID), MessageBody: aws.String(messageBody),
	}); err != nil {
		t.Fatal(err)
	}
	waitForShutdownWalletLock(t, ctx, pool, applicationName)
	stoppingAt := time.Now()
	old := active
	if err := old.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- old.cmd.Wait() }()
	select {
	case waitErr := <-stopped:
		old.log.Close()
		active = nil
		if waitErr != nil {
			logBody, _ := os.ReadFile(old.log.Name())
			if !strings.Contains(string(logBody), "SQS consumer shutdown timeout") {
				t.Fatalf("first process exited unexpectedly: %v\n%s", waitErr, logBody)
			}
		}
	case <-time.After(12 * time.Second):
		old.cmd.Process.Kill()
		<-stopped
		old.log.Close()
		active = nil
		t.Fatal("first process did not stop within 12 seconds")
	}
	var balance, inboxRows int
	if err := pool.QueryRow(ctx, `SELECT balance_minor FROM wallets WHERE id=$1`, walletID).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM consumer_inbox WHERE consumer_name=$1 AND message_id=$2`, consumerName, messageID).Scan(&inboxRows); err != nil {
		t.Fatal(err)
	}
	if balance != 10_000 || inboxRows != 0 {
		t.Fatalf("first process unexpectedly committed: balance=%d inbox=%d", balance, inboxRows)
	}
	// Visibility is 30 seconds; redelivery within five seconds of SIGTERM
	// demonstrates ChangeMessageVisibility(0), not natural expiry.
	releasedCtx, releasedCancel := context.WithTimeout(ctx, 5*time.Second)
	defer releasedCancel()
	var released types.Message
	for releasedCtx.Err() == nil {
		result, err := client.ReceiveMessage(releasedCtx, &awssqs.ReceiveMessageInput{
			QueueUrl: aws.String(queueURL), MaxNumberOfMessages: 1, WaitTimeSeconds: 1,
			VisibilityTimeout:           30,
			MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameApproximateReceiveCount},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Messages) > 0 {
			released = result.Messages[0]
			break
		}
	}
	if released.MessageId == nil || aws.ToString(released.Body) != messageBody || time.Since(stoppingAt) >= 10*time.Second {
		t.Fatalf("message not promptly released after SIGTERM: id=%q elapsed=%s", aws.ToString(released.MessageId), time.Since(stoppingAt))
	}
	if received := released.Attributes[string(types.MessageSystemAttributeNameApproximateReceiveCount)]; received != "2" {
		t.Fatalf("released message receive count = %q, want 2", received)
	}
	if _, err := client.ChangeMessageVisibility(ctx, &awssqs.ChangeMessageVisibilityInput{
		QueueUrl: aws.String(queueURL), ReceiptHandle: released.ReceiptHandle, VisibilityTimeout: 0,
	}); err != nil {
		t.Fatal(err)
	}
	if err := locked.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	active = startServer(t, ctx, bin, port, 1, overrides...)
	active.waitReady(t, ctx)
	waitForCompletedShutdownMessage(t, ctx, pool, consumerName, messageID)
	waitForProcessedSQSLog(t, ctx, active.log.Name())
	assertWalletState(t, ctx, walletID, 7_500, 2, 2, 4)
	var transactions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM wager_transactions WHERE provider_id='provider-a' AND external_transaction_id=$1`, externalID).Scan(&transactions); err != nil || transactions != 1 {
		t.Fatalf("financial transactions = %d, error=%v, want 1", transactions, err)
	}
	active.stop(t)
	active = nil
}

func waitForShutdownWalletLock(t *testing.T, ctx context.Context, pool *pgxpool.Pool, applicationName string) {
	t.Helper()
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	for {
		var waiting int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE application_name=$1 AND wait_event_type='Lock' AND query LIKE '%FOR NO KEY UPDATE%'`, applicationName).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("SQS consumer did not enter the wallet lock wait")
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func waitForCompletedShutdownMessage(t *testing.T, ctx context.Context, pool *pgxpool.Pool, consumerName, messageID string) {
	t.Helper()
	deadline := time.NewTimer(12 * time.Second)
	defer deadline.Stop()
	for {
		var completed int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM consumer_inbox WHERE consumer_name=$1 AND message_id=$2 AND completed_at IS NOT NULL`, consumerName, messageID).Scan(&completed); err != nil {
			t.Fatal(err)
		}
		if completed == 1 {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("second process did not complete the released message")
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func waitForProcessedSQSLog(t *testing.T, ctx context.Context, logPath string) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		body, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "SQS message processed") {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("second process did not confirm SQS delete: %s", body)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}
