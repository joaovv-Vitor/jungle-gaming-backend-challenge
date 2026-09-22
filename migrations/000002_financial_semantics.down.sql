BEGIN;

DROP TRIGGER IF EXISTS wager_financial_result_matches_state ON wager_transactions;
DROP FUNCTION IF EXISTS validate_wager_financial_result();

ALTER TABLE wager_transactions
    DROP CONSTRAINT IF EXISTS wager_processed_reference_resolved;

CREATE OR REPLACE FUNCTION validate_ledger_financial_state() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM wallets wallet
        JOIN wager_transactions transaction ON transaction.id = NEW.transaction_id
        WHERE wallet.id = NEW.wallet_id
          AND wallet.currency = NEW.currency
          AND wallet.version = NEW.wallet_version
          AND wallet.balance_minor = NEW.balance_after_minor
          AND transaction.wallet_id = NEW.wallet_id
          AND transaction.currency = NEW.currency
          AND transaction.amount_minor = NEW.amount_minor
          AND transaction.status = 'PROCESSED'
          AND transaction.kind <> 'LOSS'
    ) THEN
        RAISE EXCEPTION 'ledger entry does not match wallet and processed transaction' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

DELETE FROM schema_migrations WHERE version = 2;

COMMIT;
