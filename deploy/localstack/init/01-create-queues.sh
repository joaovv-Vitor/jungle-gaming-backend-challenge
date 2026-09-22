#!/bin/sh
set -eu

dlq_url="$(awslocal sqs create-queue \
  --queue-name wager-transactions-dlq.fifo \
  --attributes FifoQueue=true,ContentBasedDeduplication=false \
  --query QueueUrl --output text)"

dlq_arn="$(awslocal sqs get-queue-attributes \
  --queue-url "${dlq_url}" \
  --attribute-names QueueArn \
  --query 'Attributes.QueueArn' --output text)"

input_url="$(awslocal sqs create-queue \
  --queue-name wager-transactions.fifo \
  --attributes FifoQueue=true,ContentBasedDeduplication=false,VisibilityTimeout=60,ReceiveMessageWaitTimeSeconds=20 \
  --query QueueUrl --output text)"

redrive_attributes="$(printf '{"RedrivePolicy":"{\\"deadLetterTargetArn\\":\\"%s\\",\\"maxReceiveCount\\":\\"5\\"}"}' "${dlq_arn}")"

awslocal sqs set-queue-attributes \
  --queue-url "${input_url}" \
  --attributes "${redrive_attributes}"

awslocal sqs create-queue \
  --queue-name wager-events.fifo \
  --attributes FifoQueue=true,ContentBasedDeduplication=false,VisibilityTimeout=60,ReceiveMessageWaitTimeSeconds=20 \
  >/dev/null
