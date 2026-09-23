BEGIN;

-- Refuse to bless historical rows that do not reconstruct the stored balance.
LOCK TABLE wallets, wallet_ledger_entries IN SHARE ROW EXCLUSIVE MODE;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM (
            SELECT wallet_id, balance_before_minor,
                   lag(balance_after_minor, 1, 0) OVER
                       (PARTITION BY wallet_id ORDER BY wallet_version) AS expected_before
            FROM wallet_ledger_entries
        ) entries WHERE balance_before_minor <> expected_before
    ) OR EXISTS (
        SELECT 1 FROM wallets wallet
        LEFT JOIN (
            SELECT wallet_id,
                   sum(CASE WHEN direction = 'CREDIT' THEN amount_minor::numeric
                            ELSE -amount_minor::numeric END) AS balance
            FROM wallet_ledger_entries GROUP BY wallet_id
        ) ledger ON ledger.wallet_id = wallet.id
        WHERE wallet.balance_minor::numeric <> coalesce(ledger.balance, 0)
    ) THEN
        RAISE EXCEPTION 'existing wallet and ledger balances diverge' USING ERRCODE = '23514';
    END IF;
END;
$$;

CREATE OR REPLACE FUNCTION validate_wallet_financial_change() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    prior_balance BIGINT;
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
    IF TG_OP = 'INSERT' THEN
        prior_balance := 0;
    ELSE
        prior_balance := OLD.balance_minor;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM wallet_ledger_entries entry
        WHERE entry.wallet_id = NEW.id
          AND entry.wallet_version = NEW.version
          AND entry.balance_before_minor = prior_balance
          AND entry.balance_after_minor = NEW.balance_minor
          AND entry.currency = NEW.currency
    ) THEN
        RAISE EXCEPTION 'wallet financial change has no continuous ledger entry' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

INSERT INTO schema_migrations(version) VALUES (4);

COMMIT;
