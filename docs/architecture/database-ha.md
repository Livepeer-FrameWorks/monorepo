# Database HA — YugabyteDB ingress + ClickHouse replication

FrameWorks runs YugabyteDB as a replication-factor-3 cluster (e.g. `yuga-eu-1/2/3`).
The database was always HA; the **way services connected to it** was not — every
service's `DATABASE_URL` pointed at a single node, so one dead node took down every
service that talked to the DB. This note records how client ingress works now.

## Any tserver serves YSQL — there is no "leader" to connect to

YSQL (port 5433) is served by **every** tserver. Clients never need the master
_leader_; the tserver they connect to routes to the relevant tablet leaders
internally. So spreading client connections across all nodes is pure upside, and a
single node being down must not block ingress.

## Smart driver, not a single pinned host

Runtime services use the **YugabyteDB smart driver** (`github.com/yugabyte/pgx/v5`
via its `stdlib` adapter, so connections stay `*sql.DB`) — see
`pkg/database/postgres.go`. For a multi-node cluster, provisioning emits a
multi-host `DATABASE_URL` with load balancing enabled
(`cli/cmd/cluster_provision.go`, `buildDatabaseURL`):

```
postgres://user:pass@yuga-eu-1.internal:5433,yuga-eu-2.internal:5433,yuga-eu-3.internal:5433/db?connect_timeout=5&load_balance=true&sslmode=disable
```

The driver discovers all tservers, balances new connections across them, and skips
a node that fails to connect (`failed_host_reconnect_delay_secs`, default 5s).
Single-node / vanilla Postgres keeps the original single-host URL (no
`load_balance`).

## Failover is connection-level, not query-level

The smart driver does **not** replay an in-flight query whose node dies — that
query errors and must be retried by the caller (`database.RetryPostgres` /
`WithRetryablePostgresTx`, which classify retryable SQLSTATEs via
`database.SQLState`). Three things are therefore load-bearing, not cosmetic:

- `connect_timeout` (in the DSN) — a hung/dead node's dial fails fast.
- Pool recycling — `ConnMaxLifetime` + `ConnMaxIdleTime` (`database.Config`) churn
  connections so the pool rebalances across nodes after a failover, recovery, or
  node addition, and a connection to a degraded node doesn't linger.
- Caller context deadlines on queries.

## Statement caching: we use exec mode

`database.Connect` injects `default_query_exec_mode=exec` into the DSN. The smart
driver's default (`cache_statement`) caches prepared statements server-side per
connection; after online (expand/contract) DDL changes a table's result columns,
the first query on each pooled connection with a stale plan returns **"cached plan
must not change result type"** _to the caller_ — pgx invalidates the cache for the
next call but does **not** transparently retry the failing one, and most query
paths aren't wrapped in `RetryPostgres`. Exec mode (unnamed statements, no cache)
avoids that class entirely and is closest to lib/pq's per-query behavior. It is set
at connect time, not baked into the provisioned `DATABASE_URL`, so it stays out of
psql diagnostics. (`IsRetryablePostgresError` still lists the cached-plan message
as defense-in-depth in case a caller overrides to a cached mode.)

## Driver portability helpers (lib/pq → pgx)

The swap is not a pure drop-in. Three `lib/pq`-isms had to become driver-agnostic,
centralized in `pkg/database`:

- **SQLSTATE classification**: `database.SQLState(err)` reads the code from a
  `*pgconn.PgError` (pgx) or `*pq.Error` (lib/pq). Use it instead of asserting
  `*pq.Error` directly.
- **Array scanning**: pgx stdlib cannot scan a Postgres array into `lib/pq` array
  types or a bare slice pointer (jackc/pgx#1556). Scan with
  `database.ArrayScan(&slice)`. **Binding** is unchanged — `pq.Array(value)` (a
  `driver.Valuer`) still works under both pgx and go-sqlmock, so it is kept.
- **JSONB binding**: pgx stdlib wire-encodes `[]byte` as `bytea`, which json/jsonb
  parameters reject (`invalid input syntax for type json`). Bind marshaled JSON
  through `database.JSONText(b)` (string for non-empty, SQL NULL for nil/empty) or
  as a `string`. JSON `driver.Valuer` types (`models.JSONB` etc.) return `string`
  from `Value()`. True BYTEA columns keep binding `[]byte`. **Scanning** is
  unchanged — the JSON `Scan()` implementations accept both `[]byte` and `string`.
  Guarded by `TestPgxJSONBBind`.

## Diagnostics and `psql`

`load_balance` (and `default_query_exec_mode`, if present) are pgx-only
parameters; libpq/`psql` rejects unknown connection options. Multi-host URIs and
`connect_timeout` **are** libpq-safe. Code that shells out to `psql "$DATABASE_URL"`
(the cluster diagnose script's `fw_libpq_url` helper) strips both pgx-only params
first. If you run `psql` against `DATABASE_URL` by hand, drop them. (Note: provisioned
env DSNs only carry `load_balance`; `default_query_exec_mode` is injected at connect
time by `database.Connect`, not stored in the env.)

## Incident-time tooling

`frameworks cluster doctor` probes **every** tserver and reports the first healthy
one (`checkYugabyteCluster` in `cli/cmd/cluster.go`), and the migration check does
the same — so doctor reflects cluster health instead of the status of one pinned
node. The CLI itself still uses `lib/pq` (its own module).

# ClickHouse HA — Replicated cluster + ClickHouse Keeper

ClickHouse is modeled as a **Replicated cluster of N≥1 nodes** — there is no
non-replicated singleton mode. `inventory.ClickHouseConfig` carries only `Nodes`
(`{host, id}`); the old scalar `host:` is a tombstone that fails validation with a
migration hint. A single node is just a one-element cluster that still runs Keeper
and the Replicated schema, so growing to 3 needs no second data backfill.

## Always-replicated schema

`pkg/database/sql/clickhouse/periscope.sql` creates the `periscope` database with
`ENGINE = Replicated('/clickhouse/databases/periscope/{shard}','{shard}','{replica}')`
and every table as a `Replicated*MergeTree` (bare engine; the replica path comes
from `default_replica_path = /clickhouse/tables/{uuid}/{shard}` + `{replica}`). The
`{shard}`/`{replica}` macros are rendered into each node's `config.d/cluster.xml`.
The same DDL runs in dev (single node + embedded Keeper in `infrastructure/clickhouse/config.xml`)
and prod (standalone Keeper). ClickHouse must be **≥24.10** (refreshable MVs only
coordinate across replicas inside a Replicated database from 24.10) — the version
is release-managed (currently `26.3.10.62`), never pinned per-manifest.

## ClickHouse Keeper (not ZooKeeper)

A **standalone `clickhouse-keeper`** process runs colocated on each node (the role
installs it; embedded in-process Keeper is test-only per upstream). The server's
`<zookeeper>` client config points at it (Keeper is wire-compatible). Ports:
**9181** client, **9234** raft — defined once in `pkg/servicedefs` (`ClickHousePorts`),
fed to both port-collision accounting (`cli/pkg/inventory/ports.go`) and the Jinja
templates via `clickhouse_keeper_{client,raft}_port` vars. The server↔Keeper start
order is enforced by a systemd drop-in (`After=/Wants=clickhouse-keeper.service`)
**and** Ansible handler ordering (Keeper handler defined first).

## Bootstrap order: per-node DB join, coordinator-only DDL

The Replicated database engine replicates DDL only among nodes that have already
**joined** the database (its DDL log is per-replica in Keeper). So `cluster init`:

1. runs `CREATE DATABASE … ENGINE = Replicated` on **every** node (each joins the
   replica group), then
2. applies table DDL + migrations **once on the coordinator** — the node with the
   **lowest positive ID** (`ClickHouseConfig.CoordinatorHost()`, deterministic and
   reorder-proof) — which propagates to joined replicas via the Keeper DDL log.

Coordinator-targeted ops: init/schema/migrate/seed/backup/snapshot/restore/grafana.
The `_migrations` ledger is `ReplicatedReplacingMergeTree` (the shared metadata
builder `clickhouseClusterMetadata` feeds keeper topology to provision, init, **and**
migrate so they cannot drift).

## Current state: single node; multi-node bootstrap gated

Production runs **one** ClickHouse node today (`clickhouse-eu-1`). The schema,
Keeper, and config are N>1-ready, but the multi-node _bootstrap_ (Keeper-quorum
formation ordering, and per-node fan-out for upgrade/restart/preflight) is not yet
built — `cluster init` **refuses** N>1 with a clear message. Growth to a 3-replica
cluster is the documented expansion runbook, not an automatic flip. Node identity
(`id`) is validated unique and positive, shared with the Postgres/Kafka invariant
(`validateClusterNodeIDs`); ClickHouse and Yugabyte additionally require unique hosts.
