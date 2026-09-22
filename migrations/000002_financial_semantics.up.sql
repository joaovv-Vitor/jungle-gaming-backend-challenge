BEGIN;

ALTER TABLE wager_transactions
    ADD CONSTRAINT wager_processed_reference_resolved CHECK (
        status <> 'PROCESSED' OR
        reference_external_transaction_id IS NULL OR
        reference_transaction_id IS NOT NULL
    );

CREATE OR REPLACE FUNCTION validate_ledger_financial_state() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM wallets wallet
        JOIN wager_transactions transaction ON transaction.id = NEW.transaction_id
        LEFT JOIN wager_transactions reference ON reference.id = transaction.reference_transaction_id
        WHERE wallet.id = NEW.wallet_id
          AND wallet.currency = NEW.currency
          AND wallet.version = NEW.wallet_version
          AND wallet.balance_minor = NEW.balance_after_minor
          AND transaction.wallet_id = NEW.wallet_id
          AND transaction.currency = NEW.currency
          AND transaction.amount_minor = NEW.amount_minor
          AND transaction.result_balance_minor = NEW.balance_after_minor
          AND transaction.status = 'PROCESSED'
          AND transaction.kind <> 'LOSS'
          AND (
              (transaction.kind = 'OPENING' AND NEW.direction = 'CREDIT' AND NEW.balance_before_minor = 0) OR
              (transaction.kind = 'BET' AND NEW.direction = 'DEBIT') OR
              (transaction.kind IN ('WIN', 'REFUND') AND NEW.direction = 'CREDIT') OR
              (transaction.kind = 'ROLLBACK' AND (
                  (reference.kind = 'BET' AND NEW.direction = 'CREDIT') OR
                  (reference.kind IN ('WIN', 'REFUND') AND NEW.direction = 'DEBIT')
              ))
          )
          AND (
              transaction.reference_transaction_id IS NULL OR
              (
                  reference.status = 'PROCESSED'
                  AND reference.provider_id = transaction.provider_id
                  AND reference.external_transaction_id = transaction.reference_external_transaction_id
                  AND reference.wallet_id = transaction.wallet_id
                  AND reference.player_id = transaction.player_id
                  AND reference.currency = transaction.currency
                  AND reference.round_id = transaction.round_id
                  AND (
                      transaction.kind = 'WIN' OR
                      transaction.amount_minor = reference.amount_minor
                  )
                  AND (
                      (transaction.kind = 'REFUND' AND reference.kind = 'BET') OR
                      (transaction.kind = 'ROLLBACK' AND reference.kind IN ('BET', 'WIN', 'REFUND')) OR
                      (transaction.kind = 'WIN' AND reference.kind = 'BET')
                  )
              )
          )
    ) THEN
        RAISE EXCEPTION 'ledger entry does not match wallet and processed transaction semantics'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE FUNCTION validate_wager_financial_result() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.status = 'PROCESSED' AND NEW.kind <> 'LOSS' THEN
        IF NOT EXISTS (
            SELECT 1
            FROM wallet_ledger_entries entry
            WHERE entry.transaction_id = NEW.id
              AND entry.wallet_id = NEW.wallet_id
              AND entry.currency = NEW.currency
              AND entry.balance_after_minor = NEW.result_balance_minor
        ) THEN
            RAISE EXCEPTION 'processed wager result has no matching ledger entry'
                USING ERRCODE = '23514';
        END IF;
    ELSIF (NEW.status = 'PROCESSED' AND NEW.kind = 'LOSS') OR NEW.status = 'REJECTED' THEN
        IF NOT EXISTS (
            SELECT 1
            FROM wallets wallet
            WHERE wallet.id = NEW.wallet_id
              AND wallet.currency = NEW.currency
              AND wallet.balance_minor = NEW.result_balance_minor
        ) THEN
            RAISE EXCEPTION 'non-moving wager result does not match wallet balance'
                USING ERRCODE = '23514';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;

CREATE CONSTRAINT TRIGGER wager_financial_result_matches_state
    AFTER INSERT OR UPDATE ON wager_transactions
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION validate_wager_financial_result();

INSERT INTO schema_migrations(version) VALUES (2);

COMMIT;
