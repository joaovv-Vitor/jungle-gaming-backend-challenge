package sqsadapter

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

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

type brokerTestClient struct {
	receiveInput  *awssqs.ReceiveMessageInput
	receiveOutput *awssqs.ReceiveMessageOutput
	sendInput     *awssqs.SendMessageInput
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
	return nil, errors.New("unexpected GetQueueAttributes")
}

func (c *brokerTestClient) ReceiveMessage(_ context.Context, input *awssqs.ReceiveMessageInput, _ ...func(*awssqs.Options)) (*awssqs.ReceiveMessageOutput, error) {
	c.receiveInput = input
	return c.receiveOutput, nil
}

func (c *brokerTestClient) DeleteMessage(context.Context, *awssqs.DeleteMessageInput, ...func(*awssqs.Options)) (*awssqs.DeleteMessageOutput, error) {
	return nil, errors.New("unexpected DeleteMessage")
}

func (c *brokerTestClient) ChangeMessageVisibility(context.Context, *awssqs.ChangeMessageVisibilityInput, ...func(*awssqs.Options)) (*awssqs.ChangeMessageVisibilityOutput, error) {
	return nil, errors.New("unexpected ChangeMessageVisibility")
}
