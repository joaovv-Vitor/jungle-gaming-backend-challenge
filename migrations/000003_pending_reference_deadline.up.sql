BEGIN;

-- Older pending rows may have been scheduled beyond expiry. Make them due at
-- expiry before enforcing the invariant used by the indexed worker query.
UPDATE wager_transactions
SET next_attempt_at = expires_at
WHERE status = 'PENDING_REFERENCE' AND next_attempt_at > expires_at;

ALTER TABLE wager_transactions
    ADD CONSTRAINT wager_pending_reference_due_before_expiry CHECK (
        status <> 'PENDING_REFERENCE' OR next_attempt_at <= expires_at
    );

INSERT INTO schema_migrations(version) VALUES (3);

COMMIT;
