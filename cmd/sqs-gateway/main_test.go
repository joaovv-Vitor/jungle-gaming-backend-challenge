package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

func TestGatewayChecksSignatureAndQueuePolicyBeforeForwarding(t *testing.T) {
	var forwarded atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		forwarded.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	upstream, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	gate := newGateway(upstream, "us-east-1", "000000000000", map[string]principal{
		"producer": {secret: "correct-secret", statements: []statement{{
			Effect: "Allow", Action: []string{"sqs:SendMessage"},
			Resource: "arn:aws:sqs:us-east-1:000000000000:wager-transactions.fifo",
		}}},
		"test": {secret: "test", testQueues: true},
		"test-app": {secret: "test-app-secret", testQueues: true, statements: []statement{{
			Effect: "Allow", Action: []string{"sqs:GetQueueUrl", "sqs:SendMessage"},
			Resource: "arn:aws:sqs:us-east-1:000000000000:wager-events.fifo",
		}}},
	})
	front := httptest.NewServer(gate)
	defer front.Close()
	queue := front.URL + "/000000000000/wager-transactions.fifo"
	for _, scenario := range []struct {
		name, key, secret, target, queue string
		want                             int
		unsignedContentLength            bool
	}{
		{"authorized producer", "producer", "correct-secret", "AmazonSQS.SendMessage", queue, 200, false},
		{"authorized without signed content length", "producer", "correct-secret", "AmazonSQS.SendMessage", queue, 200, true},
		{"incorrect secret", "producer", "wrong-secret", "AmazonSQS.SendMessage", queue, 403, false},
		{"unknown identity", "unknown", "wrong-secret", "AmazonSQS.SendMessage", queue, 403, false},
		{"unauthorized operation", "producer", "correct-secret", "AmazonSQS.DeleteMessage", queue, 403, false},
		{"unauthorized queue", "producer", "correct-secret", "AmazonSQS.SendMessage", front.URL + "/000000000000/other.fifo", 403, false},
		{"test identity cannot publish to main input", "test", "test", "AmazonSQS.SendMessage", queue, 403, false},
		{"test identity can use isolated queue", "test", "test", "AmazonSQS.SendMessage", front.URL + "/000000000000/wager-isolated.fifo", 200, false},
		{"test app cannot publish input", "test-app", "test-app-secret", "AmazonSQS.SendMessage", queue, 403, false},
		{"test app can publish output", "test-app", "test-app-secret", "AmazonSQS.SendMessage", front.URL + "/000000000000/wager-events.fifo", 200, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			body := []byte(`{"QueueUrl":"` + scenario.queue + `","MessageBody":"safe"}`)
			req, err := http.NewRequest(http.MethodPost, front.URL+"/", bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/x-amz-json-1.0")
			req.Header.Set("X-Amz-Target", scenario.target)
			if scenario.unsignedContentLength {
				req.ContentLength = 0
			}
			hash := sha256.Sum256(body)
			if err := v4.NewSigner().SignHTTP(context.Background(), aws.Credentials{
				AccessKeyID: scenario.key, SecretAccessKey: scenario.secret,
			}, req, hex.EncodeToString(hash[:]), "sqs", "us-east-1", time.Now()); err != nil {
				t.Fatal(err)
			}
			if scenario.unsignedContentLength {
				req.ContentLength = int64(len(body))
			}
			if scenario.name == "authorized producer" {
				identity, ok := gate.authenticate(req, body)
				if !ok {
					t.Fatalf("gateway rejected correctly signed request: %s", req.Header.Get("Authorization"))
				}
				action, resource, err := gate.operation(req, body)
				if err != nil || !identity.permits(action, resource) {
					t.Fatalf("operation %q resource %q error %v not permitted", action, resource, err)
				}
			}
			response, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != scenario.want {
				t.Fatalf("status = %d, want %d", response.StatusCode, scenario.want)
			}
		})
	}
	if got := forwarded.Load(); got != 4 {
		t.Fatalf("forwarded requests = %d, want 4", got)
	}
}

func TestGatewayRejectsConflictingQueueIdentifiersBeforeForwarding(t *testing.T) {
	var forwarded atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		forwarded.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	upstream, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	gate := newGateway(upstream, "us-east-1", "000000000000", map[string]principal{
		"app": {secret: "app-secret", statements: []statement{{
			Effect: "Allow", Action: []string{"sqs:SendMessage"},
			Resource: "arn:aws:sqs:us-east-1:000000000000:wager-events.fifo",
		}}},
		"test": {secret: "test", testQueues: true},
		"producer": {secret: "producer-secret", statements: []statement{{
			Effect: "Allow", Action: []string{"sqs:SendMessage"},
			Resource: "arn:aws:sqs:us-east-1:000000000000:wager-transactions.fifo",
		}}},
	})
	front := httptest.NewServer(gate)
	defer front.Close()
	input := front.URL + "/000000000000/wager-transactions.fifo"
	output := front.URL + "/000000000000/wager-events.fifo"
	for _, scenario := range []struct {
		name, key, secret, queueURL, queueName string
		omitQueueName                          bool
	}{
		{"app output name with input URL", "app", "app-secret", input, "wager-events.fifo", false},
		{"test isolated name with input URL", "test", "test", input, "isolated.fifo", false},
		{"producer input name with output URL", "producer", "producer-secret", output, "wager-transactions.fifo", false},
		{"producer URL with query", "producer", "producer-secret", input + "?queue=other", "", true},
		{"producer URL with escaped path", "producer", "producer-secret", strings.Replace(input, "wager-transactions", "wager-%74ransactions", 1), "", true},
		{"producer URL with trailing path", "producer", "producer-secret", input + "/", "", true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			body := `{"QueueUrl":"` + scenario.queueURL + `","MessageBody":"safe"`
			if !scenario.omitQueueName {
				body += `,"QueueName":"` + scenario.queueName + `"`
			}
			wireBody := []byte(body + `}`)
			request, err := http.NewRequest(http.MethodPost, front.URL+"/", bytes.NewReader(wireBody))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Content-Type", "application/x-amz-json-1.0")
			request.Header.Set("X-Amz-Target", "AmazonSQS.SendMessage")
			hash := sha256.Sum256(wireBody)
			if err := v4.NewSigner().SignHTTP(context.Background(), aws.Credentials{
				AccessKeyID: scenario.key, SecretAccessKey: scenario.secret,
			}, request, hex.EncodeToString(hash[:]), "sqs", "us-east-1", time.Now()); err != nil {
				t.Fatal(err)
			}
			if _, ok := gate.authenticate(request, wireBody); !ok {
				t.Fatal("conflicting request must have a valid SigV4 signature")
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
			if response.StatusCode != http.StatusForbidden || !strings.Contains(string(responseBody), "AccessDenied") {
				t.Fatalf("status %d, body %s", response.StatusCode, responseBody)
			}
			if got := forwarded.Load(); got != 0 {
				t.Fatalf("denied request reached upstream %d time(s)", got)
			}
		})
	}
}
