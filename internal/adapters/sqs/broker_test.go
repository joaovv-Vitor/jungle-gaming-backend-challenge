package sqsadapter

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/platform/config"
)

func TestBrokerUsesAWSCredentialChainWithoutCustomEndpoint(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "chain-access")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "chain-secret")
	t.Setenv("AWS_SESSION_TOKEN", "chain-token")
	t.Setenv("AWS_ENDPOINT_URL", "")
	t.Setenv("AWS_ENDPOINT_URL_SQS", "")

	broker, err := NewBroker(config.Config{SQSRegion: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	options := broker.client.(*awssqs.Client).Options()
	if options.BaseEndpoint != nil {
		t.Fatalf("AWS service endpoint was overridden: %q", *options.BaseEndpoint)
	}
	credentials, err := options.Credentials.Retrieve(context.Background())
	if err != nil || credentials.AccessKeyID != "chain-access" || credentials.SecretAccessKey != "chain-secret" ||
		credentials.SessionToken != "chain-token" {
		t.Fatalf("SDK credential chain = %+v, error = %v", credentials, err)
	}
}

func TestBrokerLocalStaticCredentialsOverrideSDKChain(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "chain-access")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "chain-secret")

	broker, err := NewBroker(config.Config{
		SQSRegion: "us-east-1", SQSEndpoint: "http://localhost:4566",
		SQSAccessKeyID: "local-access", SQSSecretAccessKey: "local-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	options := broker.client.(*awssqs.Client).Options()
	if options.BaseEndpoint == nil || *options.BaseEndpoint != "http://localhost:4566" {
		t.Fatalf("local SQS endpoint = %v", options.BaseEndpoint)
	}
	credentials, err := options.Credentials.Retrieve(context.Background())
	if err != nil || credentials.AccessKeyID != "local-access" || credentials.SecretAccessKey != "local-secret" {
		t.Fatalf("local SQS credentials = %+v, error = %v", credentials, err)
	}
}

func TestBrokerReceiveRequestsAndParsesReceiveCount(t *testing.T) {
	client := &brokerTestClient{
		receiveOutput: &awssqs.ReceiveMessageOutput{Messages: []types.Message{{
			MessageId:     aws.String("broker-message-1"),
			Body:          aws.String("{}"),
			ReceiptHandle: aws.String("receipt-1"),
			Attributes: map[string]string{
				string(types.MessageSystemAttributeNameApproximateReceiveCount): "3",
			},
		}}},
	}
	broker := &Broker{client: client}

	messages, err := broker.Receive(context.Background(), "queue-url", 5, 20, 60)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].ReceiveCount != 3 {
		t.Fatalf("messages = %+v, want one message with receive count 3", messages)
	}
	if client.receiveInput == nil || len(client.receiveInput.MessageSystemAttributeNames) != 1 ||
		client.receiveInput.MessageSystemAttributeNames[0] != types.MessageSystemAttributeNameApproximateReceiveCount {
		t.Fatalf("system attributes = %v, want ApproximateReceiveCount", client.receiveInput.MessageSystemAttributeNames)
	}
}

func TestBrokerReceiveDefaultsInvalidReceiveCountToFirstDelivery(t *testing.T) {
	client := &brokerTestClient{
		receiveOutput: &awssqs.ReceiveMessageOutput{Messages: []types.Message{{
			MessageId: aws.String("broker-message-1"), Body: aws.String("{}"), ReceiptHandle: aws.String("receipt-1"),
			Attributes: map[string]string{string(types.MessageSystemAttributeNameApproximateReceiveCount): "invalid"},
		}}},
	}
	messages, err := (&Broker{client: client}).Receive(context.Background(), "queue-url", 1, 0, 60)
	if err != nil {
		t.Fatal(err)
	}
	if messages[0].ReceiveCount != 1 {
		t.Fatalf("receive count = %d, want 1", messages[0].ReceiveCount)
	}
}

func TestBrokerReadsApproximateDeadLetterQueueDepth(t *testing.T) {
	client := &brokerTestClient{attributesOutput: &awssqs.GetQueueAttributesOutput{Attributes: map[string]string{
		string(types.QueueAttributeNameApproximateNumberOfMessages): "3",
	}}}
	count, err := (&Broker{client: client}).ApproximateMessages(context.Background(), "queue-url")
	if err != nil || count != 3 {
		t.Fatalf("queue depth = %d, error=%v", count, err)
	}
}

type brokerTestClient struct {
	receiveInput     *awssqs.ReceiveMessageInput
	receiveOutput    *awssqs.ReceiveMessageOutput
	sendInput        *awssqs.SendMessageInput
	visibilityInput  *awssqs.ChangeMessageVisibilityInput
	attributesOutput *awssqs.GetQueueAttributesOutput
}

func TestBrokerChangesVisibilityForRetry(t *testing.T) {
	client := &brokerTestClient{}
	if err := (&Broker{client: client}).ChangeVisibility(context.Background(), "queue", "receipt", 20); err != nil {
		t.Fatal(err)
	}
	if client.visibilityInput == nil || client.visibilityInput.VisibilityTimeout != 20 ||
		aws.ToString(client.visibilityInput.ReceiptHandle) != "receipt" {
		t.Fatalf("visibility request = %+v", client.visibilityInput)
	}
}

func TestBrokerSendUsesStableEventIdentity(t *testing.T) {
	client := &brokerTestClient{}
	if err := (&Broker{client: client}).Send(context.Background(), "queue", "event-1", "wallet-1", []byte(`{"eventId":"event-1"}`)); err != nil {
		t.Fatal(err)
	}
	if aws.ToString(client.sendInput.MessageDeduplicationId) != "event-1" ||
		aws.ToString(client.sendInput.MessageGroupId) != "wallet-1" ||
		aws.ToString(client.sendInput.MessageBody) != `{"eventId":"event-1"}` {
		t.Fatalf("send input = %+v", client.sendInput)
	}
}

func (c *brokerTestClient) SendMessage(_ context.Context, input *awssqs.SendMessageInput, _ ...func(*awssqs.Options)) (*awssqs.SendMessageOutput, error) {
	c.sendInput = input
	return &awssqs.SendMessageOutput{MessageId: aws.String("broker-message-1")}, nil
}

func (c *brokerTestClient) GetQueueUrl(context.Context, *awssqs.GetQueueUrlInput, ...func(*awssqs.Options)) (*awssqs.GetQueueUrlOutput, error) {
	return nil, errors.New("unexpected GetQueueUrl")
}

func (c *brokerTestClient) GetQueueAttributes(context.Context, *awssqs.GetQueueAttributesInput, ...func(*awssqs.Options)) (*awssqs.GetQueueAttributesOutput, error) {
	if c.attributesOutput != nil {
		return c.attributesOutput, nil
	}
	return nil, errors.New("unexpected GetQueueAttributes")
}

func (c *brokerTestClient) ReceiveMessage(_ context.Context, input *awssqs.ReceiveMessageInput, _ ...func(*awssqs.Options)) (*awssqs.ReceiveMessageOutput, error) {
	c.receiveInput = input
	return c.receiveOutput, nil
}

func (c *brokerTestClient) DeleteMessage(context.Context, *awssqs.DeleteMessageInput, ...func(*awssqs.Options)) (*awssqs.DeleteMessageOutput, error) {
	return nil, errors.New("unexpected DeleteMessage")
}

func (c *brokerTestClient) ChangeMessageVisibility(_ context.Context, input *awssqs.ChangeMessageVisibilityInput, _ ...func(*awssqs.Options)) (*awssqs.ChangeMessageVisibilityOutput, error) {
	c.visibilityInput = input
	return &awssqs.ChangeMessageVisibilityOutput{}, nil
}
