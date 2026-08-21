#!/bin/sh
set -eu

# Local development mirrors production's logical database ownership while
# intentionally reusing one local-only password. These names are controlled
# repository data; no user-provided value is interpreted as an identifier.
service_databases="quartermaster purser foghorn commodore periscope navigator skipper"

for service in $service_databases; do
  psql --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
    --set=service="$service" --set=runtime_password="$POSTGRES_PASSWORD" \
    --set=ON_ERROR_STOP=1 <<'SQL'
SELECT format('CREATE ROLE %I LOGIN PASSWORD %L', :'service', :'runtime_password')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'service')
\gexec
SELECT format('CREATE DATABASE %I OWNER %I', :'service', :'service')
WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = :'service')
\gexec
SELECT format('REVOKE CONNECT ON DATABASE %I FROM PUBLIC', :'service')
\gexec
SELECT format('GRANT CONNECT ON DATABASE %I TO %I', :'service', :'service')
\gexec
SQL

  # Extensions are installation-time capabilities. Runtime owners must not be
  # superusers merely because a baseline contains idempotent CREATE EXTENSION.
  case "$service" in
    navigator)
      ;;
    commodore)
      psql --username "$POSTGRES_USER" --dbname "$service" --set=ON_ERROR_STOP=1 \
        --command='CREATE EXTENSION IF NOT EXISTS "uuid-ossp"; CREATE EXTENSION IF NOT EXISTS pgcrypto; CREATE EXTENSION IF NOT EXISTS citext;'
      ;;
    skipper)
      psql --username "$POSTGRES_USER" --dbname "$service" --set=ON_ERROR_STOP=1 \
        --command='CREATE EXTENSION IF NOT EXISTS "uuid-ossp"; CREATE EXTENSION IF NOT EXISTS pgcrypto; CREATE EXTENSION IF NOT EXISTS vector;'
      ;;
    *)
      psql --username "$POSTGRES_USER" --dbname "$service" --set=ON_ERROR_STOP=1 \
        --command='CREATE EXTENSION IF NOT EXISTS "uuid-ossp"; CREATE EXTENSION IF NOT EXISTS pgcrypto;'
      ;;
  esac

  psql --username "$service" --dbname "$service" --set=ON_ERROR_STOP=1 \
    --file="/frameworks-schema/$service.sql"
done

# The operator analytics role is intentionally cross-service. Install its
# grants in each owning database rather than relying on a shared platform DB.
for service in quartermaster commodore purser; do
  psql --username "$POSTGRES_USER" --dbname "$service" --set=ON_ERROR_STOP=1 \
    --file="/frameworks-static-seeds/analytics_ro_$service.sql"
  psql --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
    --set=service="$service" --set=ON_ERROR_STOP=1 <<'SQL'
SELECT format('GRANT CONNECT ON DATABASE %I TO frameworks_analytics_ro', :'service')
\gexec
SQL
done
psql --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" --set=ON_ERROR_STOP=1 \
  --file="/frameworks-postgres/analytics_ro_dev_password.sql"
