# Database HA — client ingress to YugabyteDB

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

The swap is not a pure drop-in. Two `lib/pq`-isms had to become driver-agnostic,
centralized in `pkg/database`:

- **SQLSTATE classification**: `database.SQLState(err)` reads the code from a
  `*pgconn.PgError` (pgx) or `*pq.Error` (lib/pq). Use it instead of asserting
  `*pq.Error` directly.
- **Array scanning**: pgx stdlib cannot scan a Postgres array into `lib/pq` array
  types or a bare slice pointer (jackc/pgx#1556). Scan with
  `database.ArrayScan(&slice)`. **Binding** is unchanged — `pq.Array(value)` (a
  `driver.Valuer`) still works under both pgx and go-sqlmock, so it is kept.

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
