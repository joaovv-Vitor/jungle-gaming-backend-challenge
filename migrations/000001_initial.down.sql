BEGIN;

DROP TABLE IF EXISTS outbox_events;
DROP TABLE IF EXISTS consumer_inbox;
DROP TABLE IF EXISTS wallet_ledger_entries;
DROP TABLE IF EXISTS wager_transactions;
DROP TABLE IF EXISTS wallets;
DROP FUNCTION IF EXISTS protect_outbox_snapshot();
DROP FUNCTION IF EXISTS protect_terminal_wager();
DROP FUNCTION IF EXISTS validate_ledger_financial_state();
DROP FUNCTION IF EXISTS validate_wallet_financial_change();
DROP FUNCTION IF EXISTS reject_ledger_mutation();
DROP TABLE IF EXISTS schema_migrations;

COMMIT;
