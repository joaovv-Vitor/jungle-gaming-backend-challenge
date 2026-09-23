//go:build integration

package sqsadapter

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"

	platformid "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/platform/id"
)

// This test requires real IAM enforcement and three different AWS principals.
// Use only disposable, empty queues in an isolated test account: it sends one
// message to the input queue and one to the output queue.
func TestAWSIAMQueuePermissions(t *testing.T) {
	settings := map[string]string{
		"IAM_TEST_REGION":            os.Getenv("IAM_TEST_REGION"),
		"IAM_TEST_INPUT_QUEUE_URL":   os.Getenv("IAM_TEST_INPUT_QUEUE_URL"),
		"IAM_TEST_DLQ_QUEUE_URL":     os.Getenv("IAM_TEST_DLQ_QUEUE_URL"),
		"IAM_TEST_OUTPUT_QUEUE_URL":  os.Getenv("IAM_TEST_OUTPUT_QUEUE_URL"),
		"IAM_TEST_APP_PROFILE":       os.Getenv("IAM_TEST_APP_PROFILE"),
		"IAM_TEST_PRODUCER_PROFILE":  os.Getenv("IAM_TEST_PRODUCER_PROFILE"),
		"IAM_TEST_UNTRUSTED_PROFILE": os.Getenv("IAM_TEST_UNTRUSTED_PROFILE"),
		"IAM_TEST_ISOLATED_QUEUES":   os.Getenv("IAM_TEST_ISOLATED_QUEUES"),
	}
	configured := false
	for _, value := range settings {
		configured = configured || value != ""
	}
	if !configured {
		t.Skip("set IAM_TEST_* to verify effective permissions in AWS SQS")
	}
	for name, value := range settings {
		if value == "" {
			t.Fatalf("%s is required when IAM verification is configured", name)
		}
	}
	if settings["IAM_TEST_ISOLATED_QUEUES"] != "yes" {
		t.Fatal("IAM_TEST_ISOLATED_QUEUES must be yes; the test sends real messages")
	}
	for _, name := range []string{"AWS_ENDPOINT_URL", "AWS_ENDPOINT_URL_SQS", "AWS_ENDPOINT_URL_STS"} {
		if os.Getenv(name) != "" {
			t.Fatalf("unset %s: this test must use AWS endpoints, not an emulator", name)
		}
	}
	for _, queue := range []struct{ setting, name string }{
		{"IAM_TEST_INPUT_QUEUE_URL", "wager-transactions.fifo"},
		{"IAM_TEST_DLQ_QUEUE_URL", "wager-transactions-dlq.fifo"},
		{"IAM_TEST_OUTPUT_QUEUE_URL", "wager-events.fifo"},
	} {
		if !strings.HasSuffix(settings[queue.setting], "/"+queue.name) {
			t.Fatalf("%s must identify %s in the isolated account", queue.setting, queue.name)
		}
		parsed, err := url.Parse(settings[queue.setting])
		if err != nil || parsed.Scheme != "https" ||
			(!strings.HasSuffix(parsed.Hostname(), ".amazonaws.com") &&
				!strings.HasSuffix(parsed.Hostname(), ".amazonaws.com.cn")) {
			t.Fatalf("%s must use an HTTPS AWS SQS endpoint", queue.setting)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	type principal struct {
		name, profile, arn, account, userID string
		client                              *awssqs.Client
	}
	principals := []*principal{
		{name: "app", profile: settings["IAM_TEST_APP_PROFILE"]},
		{name: "producer", profile: settings["IAM_TEST_PRODUCER_PROFILE"]},
		{name: "untrusted", profile: settings["IAM_TEST_UNTRUSTED_PROFILE"]},
	}
	for _, p := range principals {
		cfg, err := awsconfig.LoadDefaultConfig(ctx,
			awsconfig.WithSharedConfigProfile(p.profile), awsconfig.WithRegion(settings["IAM_TEST_REGION"]))
		if err != nil {
			t.Fatalf("load %s profile: %v", p.name, err)
		}
		identity, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
		if err != nil {
			t.Fatalf("identify %s profile: %v", p.name, err)
		}
		p.arn = aws.ToString(identity.Arn)
		p.account = aws.ToString(identity.Account)
		p.userID = strings.SplitN(aws.ToString(identity.UserId), ":", 2)[0]
		if p.arn == "" || p.account == "" || p.userID == "" {
			t.Fatalf("%s profile returned an empty principal ARN, account or user ID", p.name)
		}
		p.client = awssqs.NewFromConfig(cfg)
		t.Logf("%s principal: %s", p.name, p.arn)
	}
	if principals[0].arn == principals[1].arn || principals[0].arn == principals[2].arn ||
		principals[1].arn == principals[2].arn {
		t.Fatal("app, producer and untrusted profiles must resolve to distinct IAM principals")
	}
	if principals[0].userID == principals[1].userID || principals[0].userID == principals[2].userID ||
		principals[1].userID == principals[2].userID {
		t.Fatal("app, producer and untrusted profiles must not be sessions of the same IAM identity")
	}
	if principals[0].account != principals[1].account || principals[0].account != principals[2].account {
		t.Fatal("all three IAM principals must belong to the isolated test account")
	}
	app, producer, untrusted := principals[0].client, principals[1].client, principals[2].client
	input := settings["IAM_TEST_INPUT_QUEUE_URL"]
	dlq := settings["IAM_TEST_DLQ_QUEUE_URL"]
	output := settings["IAM_TEST_OUTPUT_QUEUE_URL"]
	for _, queue := range []struct{ url, name string }{
		{input, "wager-transactions.fifo"},
		{dlq, "wager-transactions-dlq.fifo"},
		{output, "wager-events.fifo"},
	} {
		attributes, err := app.GetQueueAttributes(ctx, &awssqs.GetQueueAttributesInput{
			QueueUrl: aws.String(queue.url), AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
		})
		if err != nil {
			t.Fatalf("app cannot inspect queue %s: %v", queue.url, err)
		}
		arn := attributes.Attributes[string(types.QueueAttributeNameQueueArn)]
		parts := strings.SplitN(arn, ":", 6)
		if len(parts) != 6 || parts[0] != "arn" || parts[2] != "sqs" ||
			parts[3] != settings["IAM_TEST_REGION"] || parts[4] != principals[0].account || parts[5] != queue.name {
			t.Fatalf("unexpected queue ARN for %s: %q", queue.name, arn)
		}
	}

	denied := func(name string, err error) {
		t.Helper()
		if !sqsAccessDenied(err) {
			t.Fatalf("%s: expected IAM AccessDenied, got %v", name, err)
		}
	}
	messageID, err := platformid.New()
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"iamVerification":%q}`, messageID)
	send := func(client *awssqs.Client, queueURL string) error {
		_, err := client.SendMessage(ctx, &awssqs.SendMessageInput{
			QueueUrl: aws.String(queueURL), MessageBody: aws.String(body),
			MessageGroupId: aws.String(messageID), MessageDeduplicationId: aws.String(messageID),
		})
		return err
	}
	denied("app sending to input", send(app, input))
	denied("untrusted sending to input", send(untrusted, input))
	denied("producer sending to output", send(producer, output))
	_, err = producer.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{QueueUrl: aws.String(input), WaitTimeSeconds: 0})
	denied("producer receiving input", err)
	_, err = untrusted.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{QueueUrl: aws.String(input), WaitTimeSeconds: 0})
	denied("untrusted receiving input", err)
	_, err = untrusted.GetQueueAttributes(ctx, &awssqs.GetQueueAttributesInput{
		QueueUrl: aws.String(input), AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
	})
	denied("untrusted inspecting input", err)
	_, err = producer.GetQueueAttributes(ctx, &awssqs.GetQueueAttributesInput{
		QueueUrl: aws.String(dlq), AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
	})
	denied("producer inspecting DLQ", err)

	if err := send(producer, input); err != nil {
		t.Fatalf("trusted producer cannot send input: %v", err)
	}
	received := false
	for range 8 {
		batch, err := app.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
			QueueUrl: aws.String(input), MaxNumberOfMessages: 1, WaitTimeSeconds: 2,
		})
		if err != nil {
			t.Fatalf("app cannot receive input: %v", err)
		}
		if len(batch.Messages) == 0 {
			continue
		}
		message := batch.Messages[0]
		if aws.ToString(message.Body) != body {
			t.Fatalf("isolated input queue contains an unexpected message %s", aws.ToString(message.MessageId))
		}
		_, err = producer.DeleteMessage(ctx, &awssqs.DeleteMessageInput{
			QueueUrl: aws.String(input), ReceiptHandle: message.ReceiptHandle,
		})
		denied("producer deleting input", err)
		_, err = untrusted.DeleteMessage(ctx, &awssqs.DeleteMessageInput{
			QueueUrl: aws.String(input), ReceiptHandle: message.ReceiptHandle,
		})
		denied("untrusted deleting input", err)
		_, err = producer.ChangeMessageVisibility(ctx, &awssqs.ChangeMessageVisibilityInput{
			QueueUrl: aws.String(input), ReceiptHandle: message.ReceiptHandle, VisibilityTimeout: 30,
		})
		denied("producer changing input visibility", err)
		_, err = app.ChangeMessageVisibility(ctx, &awssqs.ChangeMessageVisibilityInput{
			QueueUrl: aws.String(input), ReceiptHandle: message.ReceiptHandle, VisibilityTimeout: 30,
		})
		if err != nil {
			t.Fatalf("app cannot change input visibility: %v", err)
		}
		if _, err := app.DeleteMessage(ctx, &awssqs.DeleteMessageInput{
			QueueUrl: aws.String(input), ReceiptHandle: message.ReceiptHandle,
		}); err != nil {
			t.Fatalf("app cannot delete input: %v", err)
		}
		received = true
		break
	}
	if !received {
		t.Fatal("app did not receive the producer's test message")
	}
	if err := send(app, output); err != nil {
		t.Fatalf("app cannot publish output event: %v", err)
	}
	t.Log("effective SQS access confirmed; dispose of the isolated output queue after the test")
}

func sqsAccessDenied(err error) bool {
	var apiError smithy.APIError
	if !errors.As(err, &apiError) {
		return false
	}
	code := strings.ToLower(apiError.ErrorCode())
	return strings.Contains(code, "accessdenied") || strings.Contains(code, "authorizationerror")
}

func TestSQSAccessDeniedDoesNotMistakeMissingQueuesOrNetworkFailuresForIAM(t *testing.T) {
	for _, scenario := range []struct {
		err  error
		want bool
	}{
		{fmt.Errorf("send: %w", &smithy.GenericAPIError{Code: "AccessDenied", Message: "denied"}), true},
		{&smithy.GenericAPIError{Code: "AWS.SimpleQueueService.AuthorizationError", Message: "denied"}, true},
		{&smithy.GenericAPIError{Code: "AWS.SimpleQueueService.NonExistentQueue", Message: "not found"}, false},
		{errors.New("network unavailable"), false},
		{nil, false},
	} {
		if got := sqsAccessDenied(scenario.err); got != scenario.want {
			t.Fatalf("sqsAccessDenied(%v) = %t, want %t", scenario.err, got, scenario.want)
		}
	}
}
