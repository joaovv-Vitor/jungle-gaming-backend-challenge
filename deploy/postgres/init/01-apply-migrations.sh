#!/bin/sh
set -eu

for migration in /migrations/*.up.sql; do
  psql --set=ON_ERROR_STOP=1 \
    --username "$POSTGRES_USER" \
    --dbname "$POSTGRES_DB" \
    --file "$migration"
done

psql --set=ON_ERROR_STOP=1 \
  --username "$POSTGRES_USER" \
  --dbname "$POSTGRES_DB" \
  --set=app_user="$WAGER_APP_USER" <<'SQL'
GRANT CONNECT ON DATABASE :"DBNAME" TO :"app_user";
GRANT USAGE ON SCHEMA public TO :"app_user";
GRANT SELECT, INSERT, UPDATE ON wallets, wager_transactions, consumer_inbox, outbox_events TO :"app_user";
GRANT SELECT, INSERT ON wallet_ledger_entries TO :"app_user";
GRANT SELECT ON schema_migrations TO :"app_user";
SQL
