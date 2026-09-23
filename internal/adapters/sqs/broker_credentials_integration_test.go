//go:build integration

package sqsadapter

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestBrokerUsesSDKCredentialChainForLocalStackRequest(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_SESSION_TOKEN", "")
	cfg := integrationConsumerConfig("wager-transactions.fifo")
	cfg.SQSAccessKeyID = ""
	cfg.SQSSecretAccessKey = ""
	broker, err := NewBroker(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	queueURL, err := broker.QueueURL(ctx, cfg.SQSInputQueue)
	if err != nil || !strings.HasSuffix(queueURL, "/"+cfg.SQSInputQueue) {
		t.Fatalf("queue URL using SDK credential chain = %q, error = %v", queueURL, err)
	}
}
