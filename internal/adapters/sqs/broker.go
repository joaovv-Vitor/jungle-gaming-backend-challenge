package sqsadapter

import (
	"context"
	"fmt"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/platform/config"
)

type sqsClient interface {
	GetQueueUrl(context.Context, *awssqs.GetQueueUrlInput, ...func(*awssqs.Options)) (*awssqs.GetQueueUrlOutput, error)
	GetQueueAttributes(context.Context, *awssqs.GetQueueAttributesInput, ...func(*awssqs.Options)) (*awssqs.GetQueueAttributesOutput, error)
	ReceiveMessage(context.Context, *awssqs.ReceiveMessageInput, ...func(*awssqs.Options)) (*awssqs.ReceiveMessageOutput, error)
	DeleteMessage(context.Context, *awssqs.DeleteMessageInput, ...func(*awssqs.Options)) (*awssqs.DeleteMessageOutput, error)
	ChangeMessageVisibility(context.Context, *awssqs.ChangeMessageVisibilityInput, ...func(*awssqs.Options)) (*awssqs.ChangeMessageVisibilityOutput, error)
	SendMessage(context.Context, *awssqs.SendMessageInput, ...func(*awssqs.Options)) (*awssqs.SendMessageOutput, error)
}

type Broker struct {
	client sqsClient
}

type Message struct {
	ID            string
	Body          string
	ReceiptHandle string
	ReceiveCount  int
}

func NewBroker(cfg config.Config) (*Broker, error) {
	loadOptions := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(cfg.SQSRegion)}
	if cfg.SQSAccessKeyID != "" {
		loadOptions = append(loadOptions, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.SQSAccessKeyID, cfg.SQSSecretAccessKey, "")))
	}
	sdkConfig, err := awsconfig.LoadDefaultConfig(context.Background(), loadOptions...)
	if err != nil {
		return nil, fmt.Errorf("load AWS configuration: %w", err)
	}
	clientOptions := []func(*awssqs.Options){}
	if cfg.SQSEndpoint != "" {
		clientOptions = append(clientOptions, func(options *awssqs.Options) {
			options.BaseEndpoint = aws.String(cfg.SQSEndpoint)
		})
	}
	client := awssqs.NewFromConfig(sdkConfig, clientOptions...)
	return &Broker{client: client}, nil
}

func (b *Broker) QueueURL(ctx context.Context, name string) (string, error) {
	result, err := b.client.GetQueueUrl(ctx, &awssqs.GetQueueUrlInput{QueueName: aws.String(name)})
	if err != nil {
		return "", fmt.Errorf("get SQS queue URL: %w", err)
	}
	if result.QueueUrl == nil || *result.QueueUrl == "" {
		return "", fmt.Errorf("get SQS queue URL: empty response")
	}
	return *result.QueueUrl, nil
}

func (b *Broker) Ping(ctx context.Context, queueURL string) error {
	_, err := b.client.GetQueueAttributes(ctx, &awssqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(queueURL),
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
	})
	if err != nil {
		return fmt.Errorf("get SQS queue attributes: %w", err)
	}
	return nil
}

func (b *Broker) ApproximateMessages(ctx context.Context, queueURL string) (int64, error) {
	result, err := b.client.GetQueueAttributes(ctx, &awssqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(queueURL),
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameApproximateNumberOfMessages},
	})
	if err != nil {
		return 0, fmt.Errorf("read SQS queue depth: %w", err)
	}
	if result == nil {
		return 0, fmt.Errorf("read SQS queue depth: empty response")
	}
	count, err := strconv.ParseInt(result.Attributes[string(types.QueueAttributeNameApproximateNumberOfMessages)], 10, 64)
	if err != nil || count < 0 {
		return 0, fmt.Errorf("read SQS queue depth: invalid response")
	}
	return count, nil
}

func (b *Broker) Receive(ctx context.Context, queueURL string, batch, waitSeconds, visibilitySeconds int32) ([]Message, error) {
	result, err := b.client.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
		QueueUrl:                    aws.String(queueURL),
		MaxNumberOfMessages:         batch,
		WaitTimeSeconds:             waitSeconds,
		VisibilityTimeout:           visibilitySeconds,
		MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameApproximateReceiveCount},
	})
	if err != nil {
		return nil, fmt.Errorf("receive SQS messages: %w", err)
	}
	messages := make([]Message, 0, len(result.Messages))
	for _, received := range result.Messages {
		if received.MessageId == nil || received.Body == nil || received.ReceiptHandle == nil {
			continue
		}
		receiveCount, parseErr := strconv.Atoi(received.Attributes[string(types.MessageSystemAttributeNameApproximateReceiveCount)])
		if parseErr != nil || receiveCount < 1 {
			receiveCount = 1
		}
		messages = append(messages, Message{
			ID: *received.MessageId, Body: *received.Body, ReceiptHandle: *received.ReceiptHandle,
			ReceiveCount: receiveCount,
		})
	}
	return messages, nil
}

func (b *Broker) Delete(ctx context.Context, queueURL, receiptHandle string) error {
	_, err := b.client.DeleteMessage(ctx, &awssqs.DeleteMessageInput{
		QueueUrl: aws.String(queueURL), ReceiptHandle: aws.String(receiptHandle),
	})
	if err != nil {
		return fmt.Errorf("delete SQS message: %w", err)
	}
	return nil
}

func (b *Broker) Release(ctx context.Context, queueURL, receiptHandle string) error {
	_, err := b.client.ChangeMessageVisibility(ctx, &awssqs.ChangeMessageVisibilityInput{
		QueueUrl: aws.String(queueURL), ReceiptHandle: aws.String(receiptHandle), VisibilityTimeout: 0,
	})
	if err != nil {
		return fmt.Errorf("release SQS message visibility: %w", err)
	}
	return nil
}

func (b *Broker) Send(ctx context.Context, queueURL, eventID, groupID string, payload []byte) error {
	if queueURL == "" || eventID == "" || groupID == "" || len(payload) == 0 {
		return fmt.Errorf("send SQS event: incomplete event")
	}
	result, err := b.client.SendMessage(ctx, &awssqs.SendMessageInput{
		QueueUrl: aws.String(queueURL), MessageBody: aws.String(string(payload)),
		MessageGroupId: aws.String(groupID), MessageDeduplicationId: aws.String(eventID),
	})
	if err != nil {
		return fmt.Errorf("send SQS event: %w", err)
	}
	if result == nil || result.MessageId == nil || *result.MessageId == "" {
		return fmt.Errorf("send SQS event: empty message ID")
	}
	return nil
}
