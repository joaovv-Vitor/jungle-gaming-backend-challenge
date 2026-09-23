BEGIN;

ALTER TABLE wager_transactions
    DROP CONSTRAINT IF EXISTS wager_pending_reference_due_before_expiry;

DELETE FROM schema_migrations WHERE version = 3;

COMMIT;
