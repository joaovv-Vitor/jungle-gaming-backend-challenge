// The local gateway authenticates signed SQS requests before they reach the
// LocalStack queue engine. Production AWS SQS performs this check itself.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

const maxRequestBody = 1 << 20

var authorizationPattern = regexp.MustCompile(`^AWS4-HMAC-SHA256 Credential=([^/, ]+)/([0-9]{8})/([^/, ]+)/sqs/aws4_request, SignedHeaders=([a-z0-9;-]+), Signature=([0-9a-f]{64})$`)
var queueNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+(\.fifo)?$`)

type statement struct {
	Effect   string   `json:"Effect"`
	Action   []string `json:"Action"`
	Resource string   `json:"Resource"`
}

type principal struct {
	secret     string
	statements []statement
	testQueues bool
}

type gateway struct {
	principals map[string]principal
	region     string
	account    string
	proxy      *httputil.ReverseProxy
	now        func() time.Time
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		response, err := (&http.Client{Timeout: time.Second}).Get("http://127.0.0.1:4566/healthz")
		if err != nil || response.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		response.Body.Close()
		return
	}
	region := getenv("SQS_GATEWAY_REGION", "us-east-1")
	account := "000000000000"
	appPolicy, err := readPolicy("/policies/iam-policy-app.json", region, account)
	if err != nil {
		log.Fatal(err)
	}
	producerPolicy, err := readPolicy("/policies/iam-policy-ingest-producer.json", region, account)
	if err != nil {
		log.Fatal(err)
	}
	upstream, err := url.Parse("http://localstack:4566")
	if err != nil {
		log.Fatal(err)
	}
	server := newGateway(upstream, region, account, map[string]principal{
		getenv("SQS_GATEWAY_ADMIN_ACCESS_KEY_ID", "test"): {
			secret: getenv("SQS_GATEWAY_ADMIN_SECRET_ACCESS_KEY", "test"), testQueues: true,
		},
		getenv("SQS_GATEWAY_TEST_APP_ACCESS_KEY_ID", "wager-test-app-local"): {
			secret:     getenv("SQS_GATEWAY_TEST_APP_SECRET_ACCESS_KEY", "wager-test-app-local-secret"),
			testQueues: true, statements: appPolicy,
		},
		getenv("SQS_GATEWAY_APP_ACCESS_KEY_ID", "wager-app-local"): {
			secret: getenv("SQS_GATEWAY_APP_SECRET_ACCESS_KEY", "wager-app-local-secret"), statements: appPolicy,
		},
		getenv("SQS_GATEWAY_PRODUCER_ACCESS_KEY_ID", "wager-producer-local"): {
			secret: getenv("SQS_GATEWAY_PRODUCER_SECRET_ACCESS_KEY", "wager-producer-local-secret"), statements: producerPolicy,
		},
	})
	log.Fatal(http.ListenAndServe(":4566", server))
}

func getenv(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func readPolicy(path, region, account string) ([]statement, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var policy struct {
		Statements []statement `json:"Statement"`
	}
	contents = bytes.ReplaceAll(contents, []byte("REGION"), []byte(region))
	contents = bytes.ReplaceAll(contents, []byte("ACCOUNT_ID"), []byte(account))
	if err := json.Unmarshal(contents, &policy); err != nil {
		return nil, fmt.Errorf("parse SQS policy: %w", err)
	}
	if len(policy.Statements) == 0 {
		return nil, errors.New("SQS policy has no statements")
	}
	return policy.Statements, nil
}

func newGateway(upstream *url.URL, region, account string, principals map[string]principal) *gateway {
	return &gateway{
		principals: principals, region: region, account: account,
		proxy: httputil.NewSingleHostReverseProxy(upstream), now: time.Now,
	}
}

func (g *gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodPost {
		deny(w)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody+1))
	if err != nil || len(body) > maxRequestBody {
		deny(w)
		return
	}
	identity, ok := g.authenticate(r, body)
	if !ok {
		slog.Warn("sqs request denied", "reason", "authentication")
		deny(w)
		return
	}
	action, resource, err := g.operation(r, body)
	if err != nil || !identity.permits(action, resource) {
		slog.Warn("sqs request denied", "reason", "policy", "action", action, "resource", resource)
		deny(w)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	g.proxy.ServeHTTP(w, r)
}

func (g *gateway) authenticate(r *http.Request, body []byte) (principal, bool) {
	authorization := r.Header.Get("Authorization")
	parts := authorizationPattern.FindStringSubmatch(authorization)
	if parts == nil || parts[3] != g.region || r.Header.Get("X-Amz-Security-Token") != "" {
		return principal{}, false
	}
	identity, ok := g.principals[parts[1]]
	if !ok || identity.secret == "" {
		return principal{}, false
	}
	signedAt, err := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
	if err != nil || parts[2] != signedAt.Format("20060102") || g.now().Sub(signedAt) > 5*time.Minute || signedAt.Sub(g.now()) > 5*time.Minute {
		return principal{}, false
	}
	if !strings.Contains(";"+parts[4]+";", ";host;") || !strings.Contains(";"+parts[4]+";", ";x-amz-date;") {
		return principal{}, false
	}
	clone := r.Clone(context.Background())
	clone.Header = make(http.Header)
	signedHeaders := ";" + parts[4] + ";"
	if !strings.Contains(signedHeaders, ";content-length;") {
		clone.ContentLength = 0
	}
	if r.Header.Get("X-Amz-Target") != "" && !strings.Contains(signedHeaders, ";x-amz-target;") {
		return principal{}, false
	}
	for _, name := range strings.Split(parts[4], ";") {
		if name == "host" || name == "content-length" {
			continue
		}
		values := r.Header.Values(name)
		if len(values) == 0 {
			return principal{}, false
		}
		clone.Header[http.CanonicalHeaderKey(name)] = append([]string(nil), values...)
	}
	clone.URL = cloneURL(r.URL)
	clone.URL.Scheme, clone.URL.Host = "http", r.Host
	clone.RequestURI = ""
	hash := sha256.Sum256(body)
	payloadHash := hex.EncodeToString(hash[:])
	if supplied := r.Header.Get("X-Amz-Content-Sha256"); supplied != "" && supplied != payloadHash {
		return principal{}, false
	}
	err = v4.NewSigner().SignHTTP(r.Context(), aws.Credentials{
		AccessKeyID: parts[1], SecretAccessKey: identity.secret,
	}, clone, payloadHash, "sqs", g.region, signedAt)
	if err != nil || subtle.ConstantTimeCompare([]byte(clone.Header.Get("Authorization")), []byte(authorization)) != 1 {
		return principal{}, false
	}
	return identity, true
}

func cloneURL(original *url.URL) *url.URL {
	copy := *original
	return &copy
}

func (g *gateway) operation(r *http.Request, body []byte) (string, string, error) {
	var action, queueURL, queueName string
	var hasQueueURL, hasQueueName bool
	if r.URL.EscapedPath() != "/" || r.URL.RawQuery != "" || r.URL.ForceQuery {
		return "", "", errors.New("unsupported SQS request path")
	}
	if len(r.Header.Values("X-Amz-Target")) > 1 {
		return "", "", errors.New("duplicate SQS target")
	}
	if target := r.Header.Get("X-Amz-Target"); target != "" {
		const prefix = "AmazonSQS."
		if !strings.HasPrefix(target, prefix) {
			return "", "", errors.New("invalid SQS target")
		}
		action = strings.TrimPrefix(target, prefix)
		input, err := jsonIdentifiers(body)
		if err != nil {
			return "", "", err
		}
		queueURL, queueName = input["QueueUrl"], input["QueueName"]
		_, hasQueueURL = input["QueueUrl"]
		_, hasQueueName = input["QueueName"]
		if input["QueueOwnerAWSAccountId"] != "" && input["QueueOwnerAWSAccountId"] != g.account {
			return "", "", errors.New("unsupported queue owner")
		}
	} else {
		values, err := url.ParseQuery(string(body))
		if err != nil {
			return "", "", err
		}
		for _, entries := range values {
			if len(entries) != 1 {
				return "", "", errors.New("duplicate SQS parameter")
			}
		}
		action, queueURL, queueName = values.Get("Action"), values.Get("QueueUrl"), values.Get("QueueName")
		_, hasQueueURL = values["QueueUrl"]
		_, hasQueueName = values["QueueName"]
		if owner := values.Get("QueueOwnerAWSAccountId"); owner != "" && owner != g.account {
			return "", "", errors.New("unsupported queue owner")
		}
	}
	switch action {
	case "GetQueueUrl", "CreateQueue":
		if hasQueueURL {
			return "", "", errors.New("unexpected QueueUrl")
		}
	case "SendMessage", "ReceiveMessage", "DeleteMessage", "ChangeMessageVisibility", "GetQueueAttributes", "DeleteQueue":
		if hasQueueName || !hasQueueURL {
			return "", "", errors.New("unexpected QueueName")
		}
		var err error
		queueName, err = g.queueNameFromURL(queueURL)
		if err != nil {
			return "", "", err
		}
	default:
		return "", "", errors.New("unsupported SQS action")
	}
	if len(queueName) == 0 || len(queueName) > 80 || !queueNamePattern.MatchString(queueName) {
		return "", "", errors.New("invalid queue name")
	}
	return "sqs:" + action, "arn:aws:sqs:" + g.region + ":" + g.account + ":" + queueName, nil
}

func jsonIdentifiers(body []byte) (map[string]string, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return nil, errors.New("invalid SQS JSON body")
	}
	identifiers := make(map[string]string)
	seen := make(map[string]bool)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key := keyToken.(string)
		for _, name := range []string{"QueueUrl", "QueueName", "QueueOwnerAWSAccountId"} {
			if strings.EqualFold(key, name) && key != name {
				return nil, errors.New("noncanonical SQS resource field")
			}
		}
		if seen[key] {
			return nil, errors.New("duplicate SQS field")
		}
		seen[key] = true
		if key == "QueueUrl" || key == "QueueName" || key == "QueueOwnerAWSAccountId" {
			var value string
			if err := decoder.Decode(&value); err != nil {
				return nil, err
			}
			identifiers[key] = value
		} else {
			var ignored json.RawMessage
			if err := decoder.Decode(&ignored); err != nil {
				return nil, err
			}
		}
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	if decoder.Decode(new(json.RawMessage)) != io.EOF {
		return nil, errors.New("trailing SQS JSON body")
	}
	return identifiers, nil
}

func (g *gateway) queueNameFromURL(queueURL string) (string, error) {
	parsed, err := url.Parse(queueURL)
	if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" ||
		parsed.Opaque != "" || parsed.EscapedPath() != parsed.Path {
		return "", errors.New("invalid QueueUrl")
	}
	segments := strings.Split(strings.TrimPrefix(parsed.Path, "/"), "/")
	if !strings.HasPrefix(parsed.Path, "/") || len(segments) != 2 || segments[0] != g.account {
		return "", errors.New("invalid QueueUrl path")
	}
	return segments[1], nil
}

func (p principal) permits(action, resource string) bool {
	if p.testQueues {
		for _, queue := range []string{"wager-transactions.fifo", "wager-transactions-dlq.fifo", "wager-events.fifo"} {
			if strings.HasSuffix(resource, ":"+queue) {
				if queue == "wager-transactions.fifo" {
					return false
				}
				return p.permitsPolicy(action, resource)
			}
		}
		return strings.HasPrefix(action, "sqs:") && resource != "*"
	}
	return p.permitsPolicy(action, resource)
}

func (p principal) permitsPolicy(action, resource string) bool {
	for _, statement := range p.statements {
		if statement.Effect != "Allow" || statement.Resource != resource {
			continue
		}
		for _, allowed := range statement.Action {
			if allowed == action {
				return true
			}
		}
	}
	return false
}

func deny(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.0")
	w.WriteHeader(http.StatusForbidden)
	_, _ = io.WriteString(w, `{"__type":"AccessDenied","message":"SQS access denied"}`)
}
