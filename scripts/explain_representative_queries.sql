-- Planner and execution check with synthetic volume. Every row and copied index
-- lives in temporary tables and disappears when the psql session ends.
-- Run with: docker compose exec -T postgres psql -X -v ON_ERROR_STOP=1 \
--   -U wager_admin -d wagering < scripts/explain_representative_queries.sql
-- These timings are local diagnostics, not a production throughput benchmark.

CREATE TEMP TABLE plan_wager_transactions
    (LIKE wager_transactions INCLUDING DEFAULTS INCLUDING INDEXES);
CREATE TEMP TABLE plan_outbox_events
    (LIKE outbox_events INCLUDING DEFAULTS INCLUDING INDEXES);
CREATE TEMP TABLE plan_wallet_ledger_entries
    (LIKE wallet_ledger_entries INCLUDING DEFAULTS INCLUDING INDEXES);
CREATE TEMP TABLE plan_consumer_inbox
    (LIKE consumer_inbox INCLUDING DEFAULTS INCLUDING INDEXES);

INSERT INTO plan_wager_transactions (
    id, origin, provider_id, external_transaction_id, idempotency_key,
    payload_hash, wallet_id, player_id, round_id, game_id, kind, status,
    amount_minor, currency, reference_external_transaction_id,
    result_balance_minor, next_attempt_at, expires_at, created_at, updated_at
)
SELECT md5('wager-' || n)::uuid, 'EXTERNAL', 'provider-a', n::text, n::text,
       decode(repeat('00', 32), 'hex'), md5('wallet-' || (n % 1000))::uuid,
       md5('player-' || (n % 1000))::uuid, 'round', 'game',
       CASE WHEN n % 10 = 0 THEN 'REFUND' ELSE 'BET' END,
       CASE WHEN n % 10 = 0 THEN 'PENDING_REFERENCE' ELSE 'PROCESSED' END,
       100, 'BRL', CASE WHEN n % 10 = 0 THEN 'ref-' || n END,
       CASE WHEN n % 10 <> 0 THEN 100 END,
       CASE WHEN n % 10 = 0 THEN
           CASE WHEN n % 4 = 0 THEN statement_timestamp() - interval '1 minute'
                ELSE statement_timestamp() + interval '1 hour' END
       END,
       CASE WHEN n % 10 = 0 THEN statement_timestamp() + interval '1 day' END,
       statement_timestamp() - interval '1 day', statement_timestamp()
FROM generate_series(1, 100000) AS n;

INSERT INTO plan_outbox_events (
    event_id, aggregate_id, event_type, event_version, correlation_id,
    payload, occurred_at, attempts, next_attempt_at, published_at
)
SELECT md5('event-' || n)::uuid, md5('wallet-' || (n % 1000))::uuid,
       'BET_PROCESSED', 1, n::text, '{}'::jsonb,
       statement_timestamp() - (n % 86400) * interval '1 second', 0,
       CASE WHEN n % 4 = 0 THEN statement_timestamp() - interval '1 minute'
            ELSE statement_timestamp() + interval '1 hour' END,
       CASE WHEN n % 10 <> 0 THEN statement_timestamp() END
FROM generate_series(1, 100000) AS n;

INSERT INTO plan_wallet_ledger_entries (
    id, wallet_id, transaction_id, direction, amount_minor, currency,
    balance_before_minor, balance_after_minor, wallet_version, created_at
)
SELECT md5('ledger-' || n)::uuid, md5('wallet-' || (n % 1000))::uuid,
       md5('wager-' || n)::uuid, 'CREDIT', 100, 'BRL', 0, 100,
       (n - 1) / 1000 + 1, statement_timestamp()
FROM generate_series(1, 100000) AS n;

INSERT INTO plan_consumer_inbox (
    consumer_name, message_id, payload_hash, received_at, completed_at
)
SELECT 'wager-transactions', n::text, decode(repeat('00', 32), 'hex'),
       statement_timestamp(), statement_timestamp()
FROM generate_series(1, 100000) AS n;

VACUUM (ANALYZE) plan_wager_transactions;
VACUUM (ANALYZE) plan_outbox_events;
ANALYZE plan_wallet_ledger_entries;
ANALYZE plan_consumer_inbox;

\echo 'Reference claim: 100000 wagers, 10000 pending'
EXPLAIN (ANALYZE, BUFFERS, COSTS OFF, TIMING OFF)
SELECT id FROM plan_wager_transactions
WHERE status = 'PENDING_REFERENCE'
  AND next_attempt_at <= statement_timestamp()
  AND (locked_until IS NULL OR locked_until <= statement_timestamp())
ORDER BY next_attempt_at, id
FOR UPDATE SKIP LOCKED LIMIT 1;

\echo 'Outbox claim: 100000 events, 10000 unpublished'
EXPLAIN (ANALYZE, BUFFERS, COSTS OFF, TIMING OFF)
SELECT event_id FROM plan_outbox_events
WHERE published_at IS NULL AND next_attempt_at <= statement_timestamp()
  AND (locked_until IS NULL OR locked_until <= statement_timestamp())
ORDER BY next_attempt_at, occurred_at, event_id
FOR UPDATE SKIP LOCKED LIMIT 1;

\echo 'Ledger page: 100 entries per wallet'
EXPLAIN (ANALYZE, BUFFERS, COSTS OFF, TIMING OFF)
SELECT id FROM plan_wallet_ledger_entries
WHERE wallet_id = md5('wallet-1')::uuid
ORDER BY wallet_version DESC, id DESC LIMIT 50;

\echo 'Inbox lookup: 100000 received messages'
EXPLAIN (ANALYZE, BUFFERS, COSTS OFF, TIMING OFF)
SELECT payload_hash, completed_at, transaction_id
FROM plan_consumer_inbox
WHERE consumer_name = 'wager-transactions' AND message_id = '50000'
FOR UPDATE;

\echo 'Outbox backlog: 10000 unpublished events'
EXPLAIN (ANALYZE, BUFFERS, COSTS OFF, TIMING OFF)
SELECT COUNT(*), COALESCE(GREATEST(0,
    EXTRACT(EPOCH FROM (clock_timestamp() - MIN(occurred_at)))), 0)::double precision
FROM plan_outbox_events WHERE published_at IS NULL;

-- A worker can poll while other workers hold all currently due items. This
-- exposes the cost of filtering active leases after an index lookup.
UPDATE plan_wager_transactions
SET locked_until = statement_timestamp() + interval '1 hour'
WHERE status = 'PENDING_REFERENCE' AND next_attempt_at <= statement_timestamp();
UPDATE plan_outbox_events
SET locked_until = statement_timestamp() + interval '1 hour'
WHERE published_at IS NULL AND next_attempt_at <= statement_timestamp();
VACUUM (ANALYZE) plan_wager_transactions;
VACUUM (ANALYZE) plan_outbox_events;

\echo 'Reference claim when every due item has an active lease'
EXPLAIN (ANALYZE, BUFFERS, COSTS OFF, TIMING OFF)
SELECT id FROM plan_wager_transactions
WHERE status = 'PENDING_REFERENCE'
  AND next_attempt_at <= statement_timestamp()
  AND (locked_until IS NULL OR locked_until <= statement_timestamp())
ORDER BY next_attempt_at, id
FOR UPDATE SKIP LOCKED LIMIT 1;

\echo 'Outbox claim when every due item has an active lease'
EXPLAIN (ANALYZE, BUFFERS, COSTS OFF, TIMING OFF)
SELECT event_id FROM plan_outbox_events
WHERE published_at IS NULL AND next_attempt_at <= statement_timestamp()
  AND (locked_until IS NULL OR locked_until <= statement_timestamp())
ORDER BY next_attempt_at, occurred_at, event_id
FOR UPDATE SKIP LOCKED LIMIT 1;

-- Mirror the claimed row's next eligible time to its lease deadline. This is
-- the schedule used by the workers after the claim update below.
UPDATE plan_wager_transactions
SET next_attempt_at = LEAST(locked_until, expires_at)
WHERE status = 'PENDING_REFERENCE' AND locked_until IS NOT NULL;
UPDATE plan_outbox_events
SET next_attempt_at = locked_until
WHERE published_at IS NULL AND locked_until IS NOT NULL;
VACUUM (ANALYZE) plan_wager_transactions;
VACUUM (ANALYZE) plan_outbox_events;

\echo 'Reference claim after aligning the next eligible time with the lease'
EXPLAIN (ANALYZE, BUFFERS, COSTS OFF, TIMING OFF)
SELECT id FROM plan_wager_transactions
WHERE status = 'PENDING_REFERENCE'
  AND next_attempt_at <= statement_timestamp()
  AND (locked_until IS NULL OR locked_until <= statement_timestamp())
ORDER BY next_attempt_at, id
FOR UPDATE SKIP LOCKED LIMIT 1;

\echo 'Outbox claim after aligning the next eligible time with the lease'
EXPLAIN (ANALYZE, BUFFERS, COSTS OFF, TIMING OFF)
SELECT event_id FROM plan_outbox_events
WHERE published_at IS NULL AND next_attempt_at <= statement_timestamp()
  AND (locked_until IS NULL OR locked_until <= statement_timestamp())
ORDER BY next_attempt_at, occurred_at, event_id
FOR UPDATE SKIP LOCKED LIMIT 1;

\echo 'All synthetic tables are dropped at session end.'
