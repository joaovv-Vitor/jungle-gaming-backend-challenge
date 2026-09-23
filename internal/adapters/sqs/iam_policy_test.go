package sqsadapter

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// These checks protect the policy templates from accidentally granting a queue
// action to the wrong role. Actual authorization still requires an enforcing
// broker, which the Community LocalStack instance does not provide.
func TestIAMPolicyTemplatesUseLeastPrivilegeQueueActions(t *testing.T) {
	const resource = "arn:aws:sqs:REGION:ACCOUNT_ID:"
	tests := []struct {
		file string
		want map[string][]string
	}{
		{
			file: "iam-policy-app.json",
			want: map[string][]string{
				resource + "wager-transactions.fifo": {
					"sqs:GetQueueUrl", "sqs:GetQueueAttributes", "sqs:ReceiveMessage",
					"sqs:DeleteMessage", "sqs:ChangeMessageVisibility",
				},
				resource + "wager-transactions-dlq.fifo": {
					"sqs:GetQueueUrl", "sqs:GetQueueAttributes",
				},
				resource + "wager-events.fifo": {
					"sqs:GetQueueUrl", "sqs:GetQueueAttributes", "sqs:SendMessage",
				},
			},
		},
		{
			file: "iam-policy-ingest-producer.json",
			want: map[string][]string{
				resource + "wager-transactions.fifo": {
					"sqs:GetQueueUrl", "sqs:SendMessage",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			file, err := os.Open(filepath.Join("..", "..", "..", "deploy", "aws", tt.file))
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			var policy struct {
				Version   string `json:"Version"`
				Statement []struct {
					Sid      string   `json:"Sid"`
					Effect   string   `json:"Effect"`
					Action   []string `json:"Action"`
					Resource string   `json:"Resource"`
				} `json:"Statement"`
			}
			decoder := json.NewDecoder(file)
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&policy); err != nil {
				t.Fatal(err)
			}
			if err := decoder.Decode(new(any)); err != io.EOF {
				t.Fatalf("unexpected content after policy: %v", err)
			}
			if policy.Version != "2012-10-17" || len(policy.Statement) != len(tt.want) {
				t.Fatalf("unexpected policy version or statement count: version=%q statements=%d", policy.Version, len(policy.Statement))
			}
			seen := make(map[string]bool, len(policy.Statement))
			for _, statement := range policy.Statement {
				wantActions, ok := tt.want[statement.Resource]
				if !ok || seen[statement.Resource] || statement.Effect != "Allow" || statement.Sid == "" {
					t.Fatalf("unexpected IAM statement: %+v", statement)
				}
				seen[statement.Resource] = true
				gotActions := slices.Clone(statement.Action)
				wantActions = slices.Clone(wantActions)
				slices.Sort(gotActions)
				slices.Sort(wantActions)
				if !slices.Equal(gotActions, wantActions) {
					t.Fatalf("actions for %s = %v, want %v", statement.Resource, gotActions, wantActions)
				}
			}
		})
	}
}
