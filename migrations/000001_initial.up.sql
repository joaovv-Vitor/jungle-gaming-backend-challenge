BEGIN;

CREATE TABLE schema_migrations (
    version BIGINT PRIMARY KEY,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE wallets (
    id UUID PRIMARY KEY,
    player_id UUID NOT NULL,
    currency CHAR(3) NOT NULL CHECK (currency IN ('BRL', 'EUR', 'USD')),
    balance_minor BIGINT NOT NULL CHECK (balance_minor >= 0),
    version BIGINT NOT NULL CHECK (version >= 1),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT wallets_player_currency_key UNIQUE (player_id, currency),
    CONSTRAINT wallets_time_order CHECK (updated_at >= created_at),
    CONSTRAINT wallets_id_currency_key UNIQUE (id, currency),
    CONSTRAINT wallets_identity_currency_key UNIQUE (id, player_id, currency)
);

CREATE TABLE wager_transactions (
    id UUID PRIMARY KEY,
    origin TEXT NOT NULL CHECK (origin IN ('INTERNAL', 'EXTERNAL')),
    provider_id TEXT,
    external_transaction_id TEXT,
    idempotency_key TEXT,
    payload_hash BYTEA,
    wallet_id UUID NOT NULL,
    player_id UUID NOT NULL,
    round_id TEXT,
    game_id TEXT,
    kind TEXT NOT NULL CHECK (kind IN ('OPENING', 'BET', 'WIN', 'LOSS', 'REFUND', 'ROLLBACK')),
    status TEXT NOT NULL CHECK (status IN ('PENDING', 'PENDING_REFERENCE', 'PROCESSED', 'REJECTED', 'FAILED')),
    amount_minor BIGINT NOT NULL CHECK (amount_minor >= 0),
    currency CHAR(3) NOT NULL CHECK (currency IN ('BRL', 'EUR', 'USD')),
    reference_external_transaction_id TEXT,
    reference_transaction_id UUID REFERENCES wager_transactions(id),
    failure_code TEXT,
    result_balance_minor BIGINT CHECK (result_balance_minor >= 0),
    reference_attempts INTEGER NOT NULL DEFAULT 0 CHECK (reference_attempts >= 0),
    next_attempt_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    lease_token UUID,
    locked_until TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT wager_wallet_identity_fk FOREIGN KEY (wallet_id, player_id, currency)
        REFERENCES wallets(id, player_id, currency),
    CONSTRAINT wager_time_order CHECK (updated_at >= created_at),
    CONSTRAINT wager_amount_by_kind CHECK (
        (kind = 'LOSS' AND amount_minor = 0) OR
        (kind <> 'LOSS' AND amount_minor > 0)
    ),
    CONSTRAINT wager_reference_by_kind CHECK (
        (kind IN ('REFUND', 'ROLLBACK') AND reference_external_transaction_id IS NOT NULL) OR
        (kind = 'WIN') OR
        (kind IN ('OPENING', 'BET', 'LOSS') AND reference_external_transaction_id IS NULL)
    ),
    CONSTRAINT wager_origin_fields CHECK (
        (origin = 'INTERNAL' AND kind = 'OPENING' AND status = 'PROCESSED'
            AND provider_id IS NULL AND external_transaction_id IS NULL
            AND idempotency_key IS NULL AND payload_hash IS NULL
            AND round_id IS NULL AND game_id IS NULL
            AND reference_external_transaction_id IS NULL) OR
        (origin = 'EXTERNAL' AND kind <> 'OPENING'
            AND provider_id IS NOT NULL AND provider_id <> ''
            AND external_transaction_id IS NOT NULL AND external_transaction_id <> ''
            AND idempotency_key IS NOT NULL AND idempotency_key <> ''
            AND payload_hash IS NOT NULL AND octet_length(payload_hash) = 32
            AND round_id IS NOT NULL AND round_id <> ''
            AND game_id IS NOT NULL AND game_id <> '')
    ),
    CONSTRAINT wager_result_by_status CHECK (
        (status IN ('PENDING', 'PENDING_REFERENCE') AND failure_code IS NULL AND result_balance_minor IS NULL) OR
        (status = 'PROCESSED' AND failure_code IS NULL AND result_balance_minor IS NOT NULL) OR
        (status = 'REJECTED' AND failure_code IS NOT NULL AND result_balance_minor IS NOT NULL) OR
        (status = 'FAILED' AND failure_code IS NOT NULL AND result_balance_minor IS NULL)
    ),
    CONSTRAINT wager_pending_reference_schedule CHECK (
        status <> 'PENDING_REFERENCE' OR
        (reference_external_transaction_id IS NOT NULL AND next_attempt_at IS NOT NULL AND expires_at IS NOT NULL)
    ),
    CONSTRAINT wager_reference_not_self CHECK (
        reference_external_transaction_id IS NULL OR reference_external_transaction_id <> external_transaction_id
    ),
    CONSTRAINT wager_id_wallet_currency_key UNIQUE (id, wallet_id, currency)
);

CREATE UNIQUE INDEX wager_provider_idempotency_key
    ON wager_transactions(provider_id, idempotency_key) WHERE origin = 'EXTERNAL';
CREATE UNIQUE INDEX wager_provider_external_transaction_key
    ON wager_transactions(provider_id, external_transaction_id) WHERE origin = 'EXTERNAL';
CREATE UNIQUE INDEX wager_one_opening_per_wallet
    ON wager_transactions(wallet_id) WHERE kind = 'OPENING';
CREATE UNIQUE INDEX wager_one_successful_direct_reversal
    ON wager_transactions(reference_transaction_id)
    WHERE status = 'PROCESSED' AND kind IN ('REFUND', 'ROLLBACK');
CREATE INDEX wager_pending_reference_work
    ON wager_transactions(next_attempt_at, id)
    WHERE status = 'PENDING_REFERENCE';

CREATE TABLE wallet_ledger_entries (
    id UUID PRIMARY KEY,
    wallet_id UUID NOT NULL,
    transaction_id UUID NOT NULL,
    direction TEXT NOT NULL CHECK (direction IN ('DEBIT', 'CREDIT')),
    amount_minor BIGINT NOT NULL CHECK (amount_minor > 0),
    currency CHAR(3) NOT NULL CHECK (currency IN ('BRL', 'EUR', 'USD')),
    balance_before_minor BIGINT NOT NULL CHECK (balance_before_minor >= 0),
    balance_after_minor BIGINT NOT NULL CHECK (balance_after_minor >= 0),
    wallet_version BIGINT NOT NULL CHECK (wallet_version >= 1),
    created_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT ledger_wallet_transaction_key UNIQUE (wallet_id, transaction_id),
    CONSTRAINT ledger_wallet_version_key UNIQUE (wallet_id, wallet_version),
    CONSTRAINT ledger_wallet_fk FOREIGN KEY (wallet_id, currency) REFERENCES wallets(id, currency),
    CONSTRAINT ledger_transaction_fk FOREIGN KEY (transaction_id, wallet_id, currency)
        REFERENCES wager_transactions(id, wallet_id, currency),
    CONSTRAINT ledger_balance_equation CHECK (
        (direction = 'CREDIT' AND balance_after_minor::NUMERIC = balance_before_minor::NUMERIC + amount_minor::NUMERIC) OR
        (direction = 'DEBIT' AND balance_after_minor::NUMERIC = balance_before_minor::NUMERIC - amount_minor::NUMERIC)
    )
);

CREATE INDEX ledger_wallet_page ON wallet_ledger_entries(wallet_id, wallet_version, id);

CREATE TABLE consumer_inbox (
    consumer_name TEXT NOT NULL,
    message_id TEXT NOT NULL,
    payload_hash BYTEA NOT NULL CHECK (octet_length(payload_hash) = 32),
    received_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ,
    transaction_id UUID REFERENCES wager_transactions(id),
    PRIMARY KEY (consumer_name, message_id),
    CONSTRAINT inbox_completion_time CHECK (completed_at IS NULL OR completed_at >= received_at)
);

CREATE TABLE outbox_events (
    event_id UUID PRIMARY KEY,
    aggregate_id UUID NOT NULL,
    event_type TEXT NOT NULL,
    event_version INTEGER NOT NULL CHECK (event_version >= 1),
    correlation_id TEXT NOT NULL,
    causation_id TEXT,
    payload JSONB NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL,
    lease_token UUID,
    locked_until TIMESTAMPTZ,
    published_at TIMESTAMPTZ,
    last_error TEXT
);

CREATE INDEX outbox_pending_work ON outbox_events(next_attempt_at, occurred_at, event_id)
    WHERE published_at IS NULL;
CREATE INDEX outbox_aggregate_audit ON outbox_events(aggregate_id, occurred_at, event_id);

CREATE FUNCTION reject_ledger_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'wallet ledger is append-only' USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER wallet_ledger_no_update_or_delete
    BEFORE UPDATE OR DELETE ON wallet_ledger_entries
    FOR EACH ROW EXECUTE FUNCTION reject_ledger_mutation();
CREATE TRIGGER wallet_ledger_no_truncate
    BEFORE TRUNCATE ON wallet_ledger_entries
    FOR EACH STATEMENT EXECUTE FUNCTION reject_ledger_mutation();

CREATE FUNCTION validate_wallet_financial_change() RETURNS trigger LANGUAGE plpgsql AS $$
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

CREATE CONSTRAINT TRIGGER wallet_financial_change_requires_ledger
    AFTER INSERT OR UPDATE ON wallets
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION validate_wallet_financial_change();

CREATE FUNCTION validate_ledger_financial_state() RETURNS trigger LANGUAGE plpgsql AS $$
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

CREATE CONSTRAINT TRIGGER ledger_requires_financial_state
    AFTER INSERT ON wallet_ledger_entries
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION validate_ledger_financial_state();

CREATE FUNCTION protect_terminal_wager() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.status IN ('PROCESSED', 'REJECTED', 'FAILED') AND NEW IS DISTINCT FROM OLD THEN
        RAISE EXCEPTION 'terminal wager transaction is immutable' USING ERRCODE = '55000';
    END IF;
    IF (NEW.provider_id, NEW.external_transaction_id, NEW.idempotency_key, NEW.payload_hash,
        NEW.wallet_id, NEW.player_id, NEW.round_id, NEW.game_id, NEW.kind, NEW.amount_minor, NEW.currency)
       IS DISTINCT FROM
       (OLD.provider_id, OLD.external_transaction_id, OLD.idempotency_key, OLD.payload_hash,
        OLD.wallet_id, OLD.player_id, OLD.round_id, OLD.game_id, OLD.kind, OLD.amount_minor, OLD.currency) THEN
        RAISE EXCEPTION 'wager transaction identity is immutable' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER wager_protect_identity_and_terminal
    BEFORE UPDATE ON wager_transactions
    FOR EACH ROW EXECUTE FUNCTION protect_terminal_wager();

CREATE FUNCTION protect_outbox_snapshot() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (NEW.event_id, NEW.aggregate_id, NEW.event_type, NEW.event_version, NEW.correlation_id,
        NEW.causation_id, NEW.payload, NEW.occurred_at)
       IS DISTINCT FROM
       (OLD.event_id, OLD.aggregate_id, OLD.event_type, OLD.event_version, OLD.correlation_id,
        OLD.causation_id, OLD.payload, OLD.occurred_at) THEN
        RAISE EXCEPTION 'outbox event snapshot is immutable' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER outbox_protect_snapshot
    BEFORE UPDATE ON outbox_events
    FOR EACH ROW EXECUTE FUNCTION protect_outbox_snapshot();

INSERT INTO schema_migrations(version) VALUES (1);

COMMIT;
