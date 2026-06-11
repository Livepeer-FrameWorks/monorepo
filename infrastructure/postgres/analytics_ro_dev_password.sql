-- Dev-only: give the operator-analytics read-only role a known password so
-- the dev ClickHouse named collections can authenticate. Production sets
-- the password from manifest env (ANALYTICS_RO_PASSWORD) during
-- `frameworks cluster seed`; this value never leaves docker-compose.
ALTER ROLE frameworks_analytics_ro WITH LOGIN PASSWORD 'frameworks_analytics_dev';
