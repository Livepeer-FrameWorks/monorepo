# FrameWorks Database Setup

This directory contains the database schema and initialization scripts for the FrameWorks platform.

## Quick Setup

FrameWorks uses:
- PostgreSQL for local development (port 5432)
- YugabyteDB for staging/production (port 5433)

### 1. Create Database User

For local development with PostgreSQL:
```bash
# Connect as PostgreSQL superuser
psql -h localhost -p 5432 -U postgres

# Create the frameworks user with a secure password
CREATE USER frameworks_user WITH PASSWORD 'frameworks_dev';

# Grant necessary privileges
ALTER USER frameworks_user CREATEDB;

# Exit PostgreSQL
\q
```

For staging/production with YugabyteDB:
```bash
# Connect as YugabyteDB superuser
ysqlsh -h localhost -p 5433 -U yugabyte

# Create the frameworks user with a secure password
CREATE USER frameworks_user WITH PASSWORD 'frameworks_dev';

# Grant necessary privileges
ALTER USER frameworks_user CREATEDB;

# Exit YugabyteDB
\q
```

### 2. Create Database

For local development with PostgreSQL:
```bash
# Connect as PostgreSQL superuser
psql -h localhost -p 5432 -U postgres

# Create the database with frameworks_user as owner
CREATE DATABASE frameworks OWNER frameworks_user;

# Grant all privileges on the database
GRANT ALL PRIVILEGES ON DATABASE frameworks TO frameworks_user;

# Connect to the frameworks database
\c frameworks

# Grant schema permissions
GRANT ALL ON SCHEMA public TO frameworks_user;
GRANT CREATE ON SCHEMA public TO frameworks_user;

# Grant permissions on existing objects
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO frameworks_user;
GRANT ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public TO frameworks_user;

# Grant permissions on future objects
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON TABLES TO frameworks_user;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON SEQUENCES TO frameworks_user;

# Exit PostgreSQL
\q
```

For staging/production with YugabyteDB:
```bash
# Connect as YugabyteDB superuser
ysqlsh -h localhost -p 5433 -U yugabyte

# Create the database with frameworks_user as owner
CREATE DATABASE frameworks OWNER frameworks_user;

# Grant all privileges on the database
GRANT ALL PRIVILEGES ON DATABASE frameworks TO frameworks_user;

# Connect to the frameworks database
\c frameworks

# Grant schema permissions
GRANT ALL ON SCHEMA public TO frameworks_user;
GRANT CREATE ON SCHEMA public TO frameworks_user;

# Grant permissions on existing objects
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO frameworks_user;
GRANT ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public TO frameworks_user;

# Grant permissions on future objects
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON TABLES TO frameworks_user;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON SEQUENCES TO frameworks_user;

# Exit YugabyteDB
\q
```

### 3. Initialize Schema

For local development with PostgreSQL:
```bash
# From the monorepo root directory
psql -h localhost -p 5432 -U frameworks_user -d frameworks -f database/init.sql
```

For staging/production with YugabyteDB:
```bash
# From the monorepo root directory
ysqlsh -h localhost -p 5433 -U frameworks_user -d frameworks -f database/init.sql
```

You'll be prompted for the password you set for `frameworks_user`.

### 4. Initialize ClickHouse

```bash
# Connect to ClickHouse
clickhouse-client --host localhost --port 8123 --user default

# Create database and user
CREATE DATABASE IF NOT EXISTS frameworks;
CREATE USER IF NOT EXISTS frameworks IDENTIFIED WITH sha256_password BY 'frameworks_dev';
GRANT ALL ON frameworks.* TO frameworks;

# Initialize schema
cat database/init_clickhouse_periscope.sql | clickhouse-client --host localhost --port 8123 --user frameworks --password frameworks_dev
```

## Configuration

### Environment Variables

Update your application's environment variables to use the correct database connections:

```bash
# YugabyteDB connection
DATABASE_URL=postgres://frameworks_user:frameworks_dev@localhost:5433/frameworks?sslmode=disable

# ClickHouse connection
CLICKHOUSE_HOST=localhost
CLICKHOUSE_PORT=8123
CLICKHOUSE_DB=frameworks
CLICKHOUSE_USER=frameworks
CLICKHOUSE_PASSWORD=frameworks_dev
```

### Docker Compose

If using Docker Compose, update your `docker-compose.yml`:

```yaml
services:
  yugabytedb:
    image: yugabytedb/yugabyte:latest
    environment:
      POSTGRES_DB: frameworks
      POSTGRES_USER: frameworks_user
      POSTGRES_PASSWORD: frameworks_dev
    volumes:
      - yugabytedb_data:/var/lib/yugabyte
      - ./database/init.sql:/docker-entrypoint-initdb.d/init.sql
    ports:
      - "5433:5433"

  clickhouse:
    image: clickhouse/clickhouse-server:latest
    environment:
      CLICKHOUSE_DB: frameworks
      CLICKHOUSE_USER: frameworks
      CLICKHOUSE_PASSWORD: frameworks_dev
    volumes:
      - clickhouse_data:/var/lib/clickhouse
      - ./database/init_clickhouse_periscope.sql:/docker-entrypoint-initdb.d/init.sql:ro
    ports:
      - "8123:8123"   # HTTP interface
      - "9000:9000"   # Native interface
```

## Database Schema

### PostgreSQL/YugabyteDB Schema (State & Configuration)

Authoritative state, control plane, and billing records. See `database/init.sql`.

Core tables (selection):
- `tenants`, `users`, `sessions`, `api_tokens`
- `streams`, `stream_keys`
- `recordings`, `clips`
- `stream_analytics` (aggregated/control‑plane state)
- Billing domain: `billing_invoices`, `billing_payments`, `crypto_wallets`
- Flexible billing: `billing_tiers`, `cluster_tier_support`, `tenant_subscriptions`, `tenant_cluster_access`
- Usage & drafts (aggregated): `usage_records` (monthly rollups), `invoice_drafts`

Notes:
- No event time‑series are stored in PostgreSQL.
- All cross‑service data access must go through APIs; FKs to other services are avoided across boundaries.

### ClickHouse Schema (Time‑Series Analytics)

High‑volume facts for analytics. See `database/init_clickhouse_periscope.sql`.

Tables:
- `viewer_metrics`
  - `timestamp` DateTime, `tenant_id` UUID, `internal_name` String
  - `viewer_count` UInt32, `connection_type` LowCardinality(String), `node_id` LowCardinality(String)
  - `country_code` FixedString(2), `city` LowCardinality(String), `latitude` Float64, `longitude` Float64
  - `connection_quality` Float32, `buffer_health` Float32
- `connection_events`
  - `event_id` UUID, `timestamp` DateTime, `tenant_id` UUID, `internal_name` String
  - `user_id` String, `session_id` String, `connection_addr` String, `user_agent` String, `connector` LowCardinality(String), `node_id` LowCardinality(String)
  - `country_code` FixedString(2), `city` LowCardinality(String), `latitude` Float64, `longitude` Float64
  - `event_type` LowCardinality(String), `session_duration` UInt32, `bytes_transferred` UInt64
- `node_metrics`
  - `timestamp` DateTime, `tenant_id` UUID, `node_id` LowCardinality(String)
  - CPU/memory/disk, bandwidth in/out, speeds, connections, stream_count
  - health_score/is_healthy, lat/lon, `tags` Array(String), `metadata` JSON
- `routing_events`
  - `timestamp` DateTime, `tenant_id` UUID, `stream_name` String
  - `selected_node`, `status`, `details`, `score`
  - `client_ip`, `client_country`, `client_region`, `client_city`, `client_latitude`, `client_longitude`
  - `node_scores` String, `routing_metadata` String
- `stream_health_metrics`
  - Stream/video/buffer/network metrics, `track_metadata` JSON
- `stream_events`
  - Stream‑scoped events (`event_id`, `timestamp`, `tenant_id`, `internal_name`, `event_type`, `status`, `node_id`, optional subtype fields, `event_data` JSON)
- `track_list_events`
  - Track list snapshots with `track_count` and serialized `track_list`
- `usage_records` (time‑series)
  - `timestamp`, `tenant_id` UUID, `cluster_id` String, `usage_type` LowCardinality(String), `usage_value` Float64, `billing_month` Date, `usage_details` String

Materialized views:
- `viewer_metrics_5m_mv` → `viewer_metrics_5m` (5‑minute viewer rollups)

Notes:
- All ClickHouse tables are partitioned by month and `tenant_id` and use TTLs.
- Analytics primary identifier is `internal_name` (string); no UUID `stream_id` required.

### Demo Seed (dev only)

`init.sql` seeds a minimal, coherent set for local development:
- Cluster: `central-primary`
- Service catalog: `api_tenants` (Quartermaster)
- Tenant: demo tenant (`demo@frameworks.dev` user)
- Access: demo tenant granted to `central-primary`
- Stream: a single demo stream may be created via `create_user_stream`

## Verification

### Test YugabyteDB Connection

```bash
# Test connection
psql -h localhost -p 5432 -U frameworks_user -d frameworks -c "SELECT version();"

# List all tables
psql -h localhost -p 5432 -U frameworks_user -d frameworks -c "\dt"

# Check demo data
psql -h localhost -p 5432 -U frameworks_user -d frameworks -c "SELECT email FROM users;"
```

### Test ClickHouse Connection

```bash
# Test connection
clickhouse-client --host localhost --port 8123 --user frameworks --password frameworks_dev -q "SELECT version();"

# List all tables
clickhouse-client --host localhost --port 8123 --user frameworks --password frameworks_dev -q "SHOW TABLES FROM frameworks;"

# Check materialized views
clickhouse-client --host localhost --port 8123 --user frameworks --password frameworks_dev -q "SHOW MATERIALIZED VIEWS FROM frameworks;"
```

## Troubleshooting

### YugabyteDB Issues

If you get permission denied errors:

```bash
# Connect as YugabyteDB superuser
psql -h localhost -p 5432 -U postgres

# Connect to frameworks database
\c frameworks

# Grant schema permissions
GRANT ALL ON SCHEMA public TO frameworks_user;
GRANT CREATE ON SCHEMA public TO frameworks_user;

# For existing objects
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO frameworks_user;
GRANT ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public TO frameworks_user;

# For future objects
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON TABLES TO frameworks_user;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON SEQUENCES TO frameworks_user;

\q
```

### ClickHouse Issues

If you get connection errors:

```bash
# Check ClickHouse server status
systemctl status clickhouse-server

# Check ClickHouse logs
journalctl -u clickhouse-server

# Verify HTTP interface is accessible
curl "http://localhost:8123/?query=SELECT%201"

# Check user permissions
clickhouse-client --host localhost --port 8123 --user frameworks --password frameworks_dev -q "SHOW GRANTS"
```

## Security Best Practices

1. **Use Strong Passwords**: Never use default passwords in production
2. **Limit User Privileges**: Users should only have access to their specific databases
3. **Enable SSL**: Use `sslmode=require` for YugabyteDB and HTTPS for ClickHouse in production
4. **Network Security**: Restrict database access to necessary hosts only
5. **Regular Backups**: Set up automated database backups for both systems

## Production Deployment

### Secure Password Generation

```bash
# Generate secure passwords
openssl rand -base64 32
```

### SSL Configuration

```bash
# Production DATABASE_URL with SSL
DATABASE_URL=postgres://frameworks_user:frameworks_dev@localhost:5433/frameworks?sslmode=require

# Production ClickHouse with HTTPS
CLICKHOUSE_HOST=https://localhost
CLICKHOUSE_PORT=8443
```

### Backup Commands

```bash
# YugabyteDB backup
psql_dump -h localhost -p 5432 -U frameworks_user -d frameworks > frameworks_backup.sql

# ClickHouse backup
clickhouse-client --host localhost --port 8123 --user frameworks --password frameworks_dev -q "BACKUP TABLE frameworks.* TO '/backup/clickhouse/'"
```

## Docker Integration

The database initialization works seamlessly with Docker Compose. The initialization scripts will be automatically executed when the containers start for the first time.

Make sure your `docker-compose.yml` has the correct environment variables and volume mounts as shown in the Configuration section above.

## Support

If you encounter issues with database setup:

1. Check YugabyteDB logs: `journalctl -u yugabyte-tserver`
2. Check ClickHouse logs: `journalctl -u clickhouse-server`
3. Verify YugabyteDB is running: `systemctl status yugabyte-tserver`
4. Verify ClickHouse is running: `systemctl status clickhouse-server`
5. Test YugabyteDB connection: `psql -h localhost -p 5432 -U yugabyte -c "SELECT version();"`
6. Test ClickHouse connection: `curl "http://localhost:8123/?query=SELECT%201"`

---
