-- Read-only plans for the critical worker and audit queries. Planner choices
-- depend on table size and statistics; no plan shape is asserted by tests.

EXPLAIN (COSTS OFF)
SELECT id FROM wager_transactions
WHERE status='PENDING_REFERENCE'
  AND next_attempt_at <= statement_timestamp()
  AND (locked_until IS NULL OR locked_until <= statement_timestamp())
ORDER BY next_attempt_at, id
FOR UPDATE SKIP LOCKED LIMIT 1;

EXPLAIN (COSTS OFF)
SELECT event_id FROM outbox_events
WHERE published_at IS NULL AND next_attempt_at <= statement_timestamp()
  AND (locked_until IS NULL OR locked_until <= statement_timestamp())
ORDER BY next_attempt_at, occurred_at, event_id
FOR UPDATE SKIP LOCKED LIMIT 1;

EXPLAIN (COSTS OFF)
SELECT id FROM wallet_ledger_entries
WHERE wallet_id='00000000-0000-4000-8000-000000000001'
ORDER BY wallet_version DESC, id DESC LIMIT 50;

EXPLAIN (COSTS OFF)
SELECT payload_hash, completed_at, transaction_id
FROM consumer_inbox
WHERE consumer_name='wager-transactions' AND message_id='plan-check'
FOR UPDATE;

EXPLAIN (COSTS OFF)
SELECT COUNT(*), MIN(occurred_at)
FROM outbox_events WHERE published_at IS NULL;
