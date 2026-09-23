BEGIN;

CREATE OR REPLACE FUNCTION validate_wallet_financial_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND (NEW.id, NEW.player_id, NEW.currency, NEW.created_at)
       IS DISTINCT FROM (OLD.id, OLD.player_id, OLD.currency, OLD.created_at) THEN
        RAISE EXCEPTION 'wallet identity is immutable' USING ERRCODE = '55000';
    END IF;
    IF TG_OP = 'UPDATE' AND NEW.balance_minor = OLD.balance_minor THEN
        IF NEW.version <> OLD.version THEN
            RAISE EXCEPTION 'wallet version changed without balance movement' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;
    IF TG_OP = 'UPDATE' AND NEW.version <> OLD.version + 1 THEN
        RAISE EXCEPTION 'wallet version must advance exactly once per movement' USING ERRCODE = '23514';
    END IF;
    IF TG_OP = 'INSERT' AND NEW.balance_minor = 0 THEN
        RETURN NEW;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM wallet_ledger_entries entry
        WHERE entry.wallet_id = NEW.id
          AND entry.wallet_version = NEW.version
          AND entry.balance_after_minor = NEW.balance_minor
          AND entry.currency = NEW.currency
    ) THEN
        RAISE EXCEPTION 'wallet financial change has no matching ledger entry' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

DELETE FROM schema_migrations WHERE version = 4;

COMMIT;
