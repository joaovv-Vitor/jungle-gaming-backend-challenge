#!/bin/sh
set -eu

# Run inside the disposable MiniStack container started with AUTH=true.
policy_dir=${POLICY_DIR:-/policies}
region=${AWS_DEFAULT_REGION:-us-east-1}
account=$(awslocal sts get-caller-identity --query Account --output text)
suffix="$(date +%s)-$$"
app_user="wager-app-$suffix"
producer_user="wager-producer-$suffix"
untrusted_user="wager-untrusted-$suffix"

input_url=$(awslocal sqs get-queue-url --queue-name wager-transactions.fifo --query QueueUrl --output text)
dlq_url=$(awslocal sqs get-queue-url --queue-name wager-transactions-dlq.fifo --query QueueUrl --output text)
output_url=$(awslocal sqs get-queue-url --queue-name wager-events.fifo --query QueueUrl --output text)
for queue_url in "$input_url" "$dlq_url" "$output_url"; do
    awslocal sqs get-queue-attributes --queue-url "$queue_url" --attribute-names QueueArn >/dev/null
done

awslocal iam create-user --user-name "$app_user" >/dev/null
awslocal iam create-user --user-name "$producer_user" >/dev/null
awslocal iam create-user --user-name "$untrusted_user" >/dev/null

app_policy=$(mktemp)
producer_policy=$(mktemp)
trap 'rm -f "$app_policy" "$producer_policy"' EXIT
sed "s/REGION/$region/g; s/ACCOUNT_ID/$account/g" "$policy_dir/iam-policy-app.json" > "$app_policy"
sed "s/REGION/$region/g; s/ACCOUNT_ID/$account/g" "$policy_dir/iam-policy-ingest-producer.json" > "$producer_policy"
awslocal iam put-user-policy --user-name "$app_user" --policy-name wager-app-sqs \
    --policy-document "file://$app_policy" >/dev/null
awslocal iam put-user-policy --user-name "$producer_user" --policy-name wager-producer-sqs \
    --policy-document "file://$producer_policy" >/dev/null

# Capture each generated pair from one IAM response; never print the secret.
app_pair=$(awslocal iam create-access-key --user-name "$app_user" --output json)
producer_pair=$(awslocal iam create-access-key --user-name "$producer_user" --output json)
untrusted_pair=$(awslocal iam create-access-key --user-name "$untrusted_user" --output json)
app_key=$(printf '%s' "$app_pair" | python3 -c 'import json,sys; print(json.load(sys.stdin)["AccessKey"]["AccessKeyId"])')
app_secret=$(printf '%s' "$app_pair" | python3 -c 'import json,sys; print(json.load(sys.stdin)["AccessKey"]["SecretAccessKey"])')
producer_key=$(printf '%s' "$producer_pair" | python3 -c 'import json,sys; print(json.load(sys.stdin)["AccessKey"]["AccessKeyId"])')
producer_secret=$(printf '%s' "$producer_pair" | python3 -c 'import json,sys; print(json.load(sys.stdin)["AccessKey"]["SecretAccessKey"])')
untrusted_key=$(printf '%s' "$untrusted_pair" | python3 -c 'import json,sys; print(json.load(sys.stdin)["AccessKey"]["AccessKeyId"])')
untrusted_secret=$(printf '%s' "$untrusted_pair" | python3 -c 'import json,sys; print(json.load(sys.stdin)["AccessKey"]["SecretAccessKey"])')
unset app_pair producer_pair untrusted_pair

expect_denied() {
    label=$1
    shift
    if response=$("$@" 2>&1); then
        printf 'FAIL: %s was allowed\n' "$label" >&2
        exit 1
    fi
    case "$response" in
        *AccessDenied*|*AuthorizationError*) printf 'DENIED: %s\n' "$label" ;;
        *) printf 'FAIL: %s returned an error other than AccessDenied: %s\n' "$label" "$response" >&2; exit 1 ;;
    esac
}

app() {
    AWS_ACCESS_KEY_ID="$app_key" AWS_SECRET_ACCESS_KEY="$app_secret" awslocal "$@"
}
producer() {
    AWS_ACCESS_KEY_ID="$producer_key" AWS_SECRET_ACCESS_KEY="$producer_secret" awslocal "$@"
}
untrusted() {
    AWS_ACCESS_KEY_ID="$untrusted_key" AWS_SECRET_ACCESS_KEY="$untrusted_secret" awslocal "$@"
}

app sqs get-queue-url --queue-name wager-transactions.fifo >/dev/null
app sqs get-queue-attributes --queue-url "$dlq_url" --attribute-names QueueArn >/dev/null
producer sqs get-queue-url --queue-name wager-transactions.fifo >/dev/null

expect_denied 'app sending input' app sqs send-message --queue-url "$input_url" \
    --message-body denied --message-group-id verification --message-deduplication-id "app-denied-$suffix"
expect_denied 'untrusted sending input' untrusted sqs send-message --queue-url "$input_url" \
    --message-body denied --message-group-id verification --message-deduplication-id "untrusted-denied-$suffix"
expect_denied 'untrusted reading input' untrusted sqs get-queue-attributes \
    --queue-url "$input_url" --attribute-names QueueArn
expect_denied 'producer reading DLQ' producer sqs get-queue-attributes \
    --queue-url "$dlq_url" --attribute-names QueueArn
expect_denied 'producer receiving input' producer sqs receive-message --queue-url "$input_url"
expect_denied 'producer sending output' producer sqs send-message --queue-url "$output_url" \
    --message-body denied --message-group-id verification --message-deduplication-id "producer-denied-$suffix"

producer sqs send-message --queue-url "$input_url" --message-body allowed \
    --message-group-id verification --message-deduplication-id "producer-allowed-$suffix" >/dev/null
receipt=$(app sqs receive-message --queue-url "$input_url" --wait-time-seconds 1 \
    --query 'Messages[0].ReceiptHandle' --output text)
if [ "$receipt" = None ] || [ -z "$receipt" ]; then
    printf 'FAIL: app did not receive the producer message\n' >&2
    exit 1
fi
expect_denied 'producer deleting input' producer sqs delete-message \
    --queue-url "$input_url" --receipt-handle "$receipt"
app sqs change-message-visibility --queue-url "$input_url" --receipt-handle "$receipt" \
    --visibility-timeout 0 >/dev/null
app sqs delete-message --queue-url "$input_url" --receipt-handle "$receipt" >/dev/null
app sqs send-message --queue-url "$output_url" --message-body allowed \
    --message-group-id verification --message-deduplication-id "app-allowed-$suffix" >/dev/null

printf 'PASS: MiniStack evaluated the SQS policies for app, producer and untrusted identities\n'
