//go:build integration

package bootstrap_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/aws/smithy-go"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMainSQSRejectsUnauthorizedFinancialMessage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	bin := filepath.Join(t.TempDir(), "wager-service")
	build := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/server")
	build.Dir = filepath.Clean(filepath.Join("..", ".."))
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build server: %v\n%s", err, output)
	}
	port := freePort(t)
	server := startServer(t, ctx, bin, port, 0,
		"APP_SQS_ACCESS_KEY_ID=wager-app-local",
		"APP_SQS_SECRET_ACCESS_KEY=wager-app-local-secret",
	)
	serverStopped := false
	t.Cleanup(func() {
		if !serverStopped {
			server.stop(t)
		}
	})
	server.waitReady(t, ctx)

	issuer := envOr("APP_OIDC_ISSUER", "http://localhost:8081/realms/wagering")
	token := clientToken(t, ctx, issuer, "internal-service", "internal-service-local")
	playerID := newUUID(t)
	opened := requestJSON(t, ctx, port, http.MethodPost, "/wallets", token, "", map[string]any{
		"playerId": playerID, "initialBalance": map[string]string{"amount": "100.00", "currency": "BRL"},
	})
	if opened.status != http.StatusCreated {
		t.Fatalf("open wallet: %d %s", opened.status, opened.body)
	}
	walletID := opened.stringField(t, "id")
	pool, err := pgxpool.New(ctx, envOr("APP_DATABASE_URL", "postgres://wager_app:wager_app_local@localhost:5432/wagering?sslmode=disable"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	endpoint := envOr("APP_SQS_ENDPOINT", "http://localhost:4566")
	client := func(key, secret string) *awssqs.Client {
		t.Helper()
		cfg, err := awsconfig.LoadDefaultConfig(ctx,
			awsconfig.WithRegion(envOr("APP_SQS_REGION", "us-east-1")),
			awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(key, secret, "")),
		)
		if err != nil {
			t.Fatal(err)
		}
		return awssqs.NewFromConfig(cfg, func(options *awssqs.Options) {
			options.BaseEndpoint = aws.String(endpoint)
		})
	}
	producer := client(envOr("SQS_PRODUCER_ACCESS_KEY_ID", "wager-producer-local"),
		envOr("SQS_PRODUCER_SECRET_ACCESS_KEY", "wager-producer-local-secret"))
	queue, err := producer.GetQueueUrl(ctx, &awssqs.GetQueueUrlInput{QueueName: aws.String("wager-transactions.fifo")})
	if err != nil {
		t.Fatalf("authorized producer cannot access input queue: %v", err)
	}
	queueURL := aws.ToString(queue.QueueUrl)
	suffix := strings.ReplaceAll(newUUID(t), "-", "")[:12]
	message := func(label string) (string, string) {
		messageID := "sqs-access-" + label + "-" + suffix
		externalID := "sqs-access-" + label + "-" + walletID
		body := fmt.Sprintf(`{"messageId":%q,"type":"WagerTransactionRequested","occurredAt":%q,"data":{"providerId":"provider-a","externalTransactionId":%q,"idempotencyKey":%q,"playerId":%q,"walletId":%q,"roundId":"sqs-access-round","gameId":"sqs-access-game","kind":"BET","money":{"amount":"10.00","currency":"BRL"}}}`,
			messageID, time.Now().UTC().Format(time.RFC3339Nano), externalID, externalID, playerID, walletID)
		return messageID, body
	}
	send := func(sqsClient *awssqs.Client, label string) error {
		messageID, body := message(label)
		_, err := sqsClient.SendMessage(ctx, &awssqs.SendMessageInput{
			QueueUrl: aws.String(queueURL), MessageGroupId: aws.String(walletID),
			MessageDeduplicationId: aws.String(messageID), MessageBody: aws.String(body),
		})
		return err
	}
	if err := send(producer, "allowed"); err != nil {
		t.Fatalf("authorized publication: %v", err)
	}
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		var balance int64
		if err := pool.QueryRow(ctx, `SELECT balance_minor FROM wallets WHERE id=$1`, walletID).Scan(&balance); err != nil {
			t.Fatal(err)
		}
		if balance == 9000 {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("authorized message did not produce its debit")
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	appClient := client("wager-app-local", "wager-app-local-secret")
	deleteDeadline := time.NewTimer(5 * time.Second)
	defer deleteDeadline.Stop()
	for {
		attributes, err := appClient.GetQueueAttributes(ctx, &awssqs.GetQueueAttributesInput{
			QueueUrl: aws.String(queueURL), AttributeNames: []types.QueueAttributeName{
				types.QueueAttributeNameApproximateNumberOfMessages,
				types.QueueAttributeName("ApproximateNumberOfMessagesNotVisible"),
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if attributes.Attributes["ApproximateNumberOfMessages"] == "0" &&
			attributes.Attributes["ApproximateNumberOfMessagesNotVisible"] == "0" {
			break
		}
		select {
		case <-deleteDeadline.C:
			t.Fatal("authorized message was not deleted from the input queue")
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	server.stop(t)
	serverStopped = true
	for _, scenario := range []struct {
		name, key, secret string
	}{
		{"unknown-key", "invented-key", "invented-secret"},
		{"wrong-secret", envOr("SQS_PRODUCER_ACCESS_KEY_ID", "wager-producer-local"), "incorrect-secret"},
		{"app-cannot-publish", "wager-app-local", "wager-app-local-secret"},
		{"test-identity-cannot-publish", "test", "test"},
	} {
		err := send(client(scenario.key, scenario.secret), scenario.name)
		var apiError smithy.APIError
		if !errors.As(err, &apiError) || apiError.ErrorCode() != "AccessDenied" {
			t.Fatalf("%s: expected AccessDenied, got %v", scenario.name, err)
		}
	}
	for _, scenario := range []struct{ name, key, secret, queueName string }{
		{"app-conflicting-resource", "wager-app-local", "wager-app-local-secret", "wager-events.fifo"},
		{"test-conflicting-resource", "test", "test", "sqs-access-isolated.fifo"},
	} {
		messageID, financialBody := message(scenario.name)
		wireBody, err := json.Marshal(map[string]string{
			"QueueUrl": queueURL, "QueueName": scenario.queueName, "MessageBody": financialBody,
			"MessageGroupId": walletID, "MessageDeduplicationId": messageID,
		})
		if err != nil {
			t.Fatal(err)
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/", bytes.NewReader(wireBody))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/x-amz-json-1.0")
		request.Header.Set("X-Amz-Target", "AmazonSQS.SendMessage")
		hash := sha256.Sum256(wireBody)
		if err := v4.NewSigner().SignHTTP(ctx, aws.Credentials{
			AccessKeyID: scenario.key, SecretAccessKey: scenario.secret,
		}, request, hex.EncodeToString(hash[:]), "sqs", envOr("APP_SQS_REGION", "us-east-1"), time.Now()); err != nil {
			t.Fatal(err)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		responseBody, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusForbidden || !bytes.Contains(responseBody, []byte("AccessDenied")) {
			t.Fatalf("%s: expected AccessDenied, got %d %s", scenario.name, response.StatusCode, responseBody)
		}
	}
	received, err := appClient.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
		QueueUrl: aws.String(queueURL), MaxNumberOfMessages: 10, WaitTimeSeconds: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(received.Messages) != 0 {
		t.Fatalf("denied publication reached LocalStack input queue: %d message(s)", len(received.Messages))
	}
	// A denied request must not have reached the input queue or any financial table.
	var balance, version, ledger, transactions, inbox int64
	if err := pool.QueryRow(ctx, `SELECT balance_minor, version FROM wallets WHERE id=$1`, walletID).Scan(&balance, &version); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id=$1`, walletID).Scan(&ledger); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM wager_transactions WHERE wallet_id=$1`, walletID).Scan(&transactions); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM consumer_inbox WHERE message_id LIKE $1`, "sqs-access-%-"+suffix).Scan(&inbox); err != nil {
		t.Fatal(err)
	}
	if balance != 9000 || version != 2 || ledger != 2 || transactions != 2 || inbox != 1 {
		t.Fatalf("denied publication changed financial state: balance=%d version=%d ledger=%d transactions=%d inbox=%d", balance, version, ledger, transactions, inbox)
	}
}
