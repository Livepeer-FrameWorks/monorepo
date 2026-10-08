# Database HA — YugabyteDB ingress + ClickHouse replication

FrameWorks runs YugabyteDB as a replication-factor-3 cluster (e.g. `yuga-eu-1/2/3`).
The database was always HA; the **way services connected to it** was not — every
service's `DATABASE_URL` pointed at a single node, so one dead node took down every
service that talked to the DB. This note records how client ingress works now.

## Any tserver serves YSQL — there is no "leader" to connect to

YSQL (port 5433) is served by **every** tserver. Clients never need the master
_leader_; the tserver they connect to routes to the relevant tablet leaders
internally. Connections use the nodes of the service's selected database
deployment, and a single node being down must not block ingress. Spreading
connections across continents introduces remote execution and is not a substitute
for a regional quorum.

## Independent regional SQL deployments

`infrastructure.postgres` remains the default deployment, named `primary`.
`infrastructure.database_deployments.<name>` declares another independent SQL
deployment. A service selects one through `database_deployment`; omission preserves
the primary binding. The CLI provisions distinct master cohorts and routes schema
work, backup, restore, diagnostics and rolling operations to the selected deployment.
An explicit stale `DATABASE_URL` cannot override a service's regional binding.

Each service owns its database. Foghorn aliases also retain separate physical
databases per cell (`foghorn_eu`, `foghorn_us`, etc.), even when cells share one
deployment. A physical database name must have exactly one deployment owner.
Bindings do not copy existing state or replicate it between deployments.

Keep the core control plane and ClickHouse in the EU unless their requirements
change. US-East cells can use an independent US-East Yugabyte universe. A future
US-West cell can share that universe with its own database until latency or outage
requirements justify another local universe. This preserves a service boundary
that also permits a future PostgreSQL deployment; the current PostgreSQL role is
standalone and does not implement Patroni or synchronous-replica HA.

Local SQL does not by itself make a cell independent of the EU control plane.
Admission, authority renewal, identity and configuration dependencies need explicit
outage behavior too. Operations that still synchronously require EU services retain
that dependency even when the cell's database is local. Cross-region replication
also needs an ownership/conflict model; these independent deployments do not make
one service's data writable in every region.

The Yugabyte role supports one master/tserver node (no HA), or three with RF3 for
one-node fault tolerance. Two nodes do not retain a majority after losing one.
Another region is a separate deployment, not another voter appended to the EU
cohort. Named deployments require explicit cloud, region and actual zone labels.
Three VMs in one site give host-level redundancy; invented zone labels do not
protect against a site outage. Site tolerance requires replicas and voters spread
across sufficient independent sites. Regional databases also need off-site backups
and a tested recovery procedure: RF3 does not protect against logical deletion or
loss of the entire region.

An illustrative addition to a complete fleet manifest (declare these hosts and
their mesh identities first):

```yaml
infrastructure:
  # Keep the existing postgres: primary definition here.
  database_deployments:
    us-east:
      enabled: true
      engine: yugabyte
      replication_factor: 3
      placement_cloud: hetzner
      placement_region: us-east
      nodes:
        - { host: yuga-us-1, id: 1, placement_zone: ash-dc1 }
        - { host: yuga-us-2, id: 2, placement_zone: ash-dc1 }
        - { host: yuga-us-3, id: 3, placement_zone: ash-dc1 }
      databases:
        - { name: foghorn, owner: foghorn }
services:
  foghorn-us:
    enabled: true
    deploy: foghorn
    cluster: us
    host: regional-us-1
    database_deployment: us-east
```

The `foghorn` entry is a logical schema template expanded to the aliases bound to
that deployment. US-West can use the same `database_deployment: us-east`, with its
own service alias and cluster identity. Existing cell env files supply their
database credentials; do not put plaintext passwords into this example.

### Moving an existing cell

A binding change alone would point the service at an empty database. Rehearse the
following maintenance-window cutover with a backup first. Keep two complete fleet
manifests: the source topology and the desired topology. Both must contain every
replica of the affected service. Provisioning infrastructure on the destination
must not deploy its applications yet.

1. Provision the destination using the desired manifest and
   `cluster provision --only infrastructure --database-deployment us-east`.
2. Verify destination health, release/schema compatibility, capacity and connectivity
   from every application replica. Remove stale database host/URL overrides.
3. Stop the moving service with the source manifest: `cluster stop foghorn-us`.
   Keep it stopped throughout the final backup and cutover; prevent concurrent
   deploy/restart automation. Other cells can keep serving.
4. Take and verify the final backup of `foghorn_us` from the source deployment.
   The backup's database identity stays the same across the move.
5. Using the desired manifest, restore that database with
   `cluster restore --database-deployment us-east --database foghorn_us --from <backup> --leave-stopped`.
   Restore verifies schema provenance, row counts and cell identity and fences
   restored media authority before the swap. It leaves writers stopped even if
   the operation fails.
6. Provision the affected service with the desired manifest, without the
   `--database-deployment` flag. Its service binding now renders the new endpoints.
   Check all replicas' effective endpoints, health, authority recovery, admission,
   ingest and playback before ending the maintenance window.

Before destination writes begin, rollback can restore the source binding and
redeploy its services. After any destination writes, the old source copy is stale:
stop writers and perform a reconciled reverse move instead of switching back to
it. Keep the old data and verified backup until the cutover is accepted. Do not
run native `restore-fence complete` to restart old service configurations during
this connection-setting cutover.

### Catalog preloading

`catalog_preload_additional_tables: true|false` is an optional setting on each
SQL deployment and renders `ysql_catalog_preload_additional_tables` for Yugabyte.
Omission retains the role's existing default. Changing it requires a guarded
rolling tserver restart.

Yugabyte's [catalog-cache tuning guide](https://docs.yugabyte.com/stable/best-practices-operations/ysql-catalog-cache-tuning-guide/)
describes the tradeoff: bulk preloading can reduce on-demand catalog fetches but
uses more backend memory and does work at startup or cache refresh. Pooling still
matters. Measure connection startup plus the first query, warm query latency,
backend count and memory during both normal operation and reconnect bursts.
`make benchmark-yugabyte-catalog-preload` compares isolated release-pinned engines
with the flag off and on; it is a planning/cache probe, not a throughput or WAN
benchmark. Its allocated-backend-memory measurement is not total process RSS.

The 2026-10-08 isolated local-engine probe (ten fresh connections, Purser admission
query with an absent tenant) measured these medians:

| Metric                                      | Preload off | Preload on |
| ------------------------------------------- | ----------- | ---------- |
| Connection startup                          | 13 ms       | 148 ms     |
| First query                                 | 148 ms      | 48 ms      |
| Startup plus first query                    | 162 ms      | 197 ms     |
| Warm query                                  | 3.1 ms      | 3.2 ms     |
| Allocated backend memory after both queries | 3.1 MiB     | 32.1 MiB   |
| First-query catalog reads                   | 355         | 142        |

Keep the default off for this local topology: the first-query saving did not
offset startup work. This probe does not reproduce the three-node production
topology or its 638-read observation. A backend farther from its master may gain
more, but enable the setting there only after measuring the combined cost and
memory at the intended connection count. Pool prewarming can move startup work
away from user requests; it does not remove its CPU or memory cost.

Catalog requests travel from the SQL backend to the master/cache, not from the
application for every catalog read. A US client using EU SQL does not multiply
638 catalog reads by the transatlantic RTT. US-West using US-East SQL retains
its client-to-database latency even with preload enabled. A fully local universe
can still benefit from fewer cold catalog fetches, but its benefit may be smaller.

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

The same connection boundary executes the binary's read-only schema capability
pack before returning the handle. This preserves smart-driver discovery and
load-balancing—capability checking does not create a second connection stack—and a
binary whose live Yugabyte schema or grants are incompatible never becomes ready.

`make verify-yugabyte-ha` exercises this path against the release-pinned image. It
forms an RF=3 three-zone cluster, proves connections are distributed across all
three tservers, injects a real retryable Yugabyte SQLSTATE, isolates the tserver
holding an uncommitted transaction, proves surviving-node reads/writes and atomic
rollback, then heals the node and waits for it to re-enter connection rotation.
The Docker-only address translation lets the host reach discovered container
addresses; the DSN and smart-driver discovery path otherwise match provisioning.

## Failover is connection-level, not query-level

The smart driver does **not** replay an in-flight query whose node dies — that
query errors and must be retried by the caller (`database.RetryPostgres` /
`WithRetryablePostgresTx`, which classify retryable SQLSTATEs via
`database.SQLState`). Three things are therefore load-bearing, not cosmetic:

- `connect_timeout` (in the DSN) — a hung/dead node's dial fails fast.
- Error-driven eviction — database/sql discards a connection whose query fails at
  the transport, the driver pings a connection reused after more than a second idle
  and discards it when the ping fails, and TCP keepalive closes an idle connection
  whose host stopped answering. `make verify-yugabyte-ha` partitions a tserver under
  the services' pool policy and proves the pool serves only the survivors within a
  few requests, without dropping its idle connections by hand.
- Long-lived connections — a new YSQL backend loads each relation's catalog entries
  one read at a time from the master leader on first use: the Purser admission read
  issues 638 such reads on a fresh backend, ~140 ms on a backend beside the master
  leader and ~1.5 s at a 2 ms round trip to it, against 3–16 ms warm
  (`make measure-yugabyte-connection-warmth`, identical for distributed and
  colocated databases). Every connection the pool closes makes a later request pay
  that, so the pool keeps what a burst opened (idle limit = open limit), retires an
  unused connection after 30 min, and retires each connection at its own point in
  the last quarter of its 60 min lifetime, so connections opened together do not go
  cold together. Retries replay on the same pool: a backend refreshes its own catalog
  after a schema or catalog version mismatch. The cost of long lifetimes is slower
  rebalancing: a tserver that rejoins or is added receives connections as the pool
  opens new ones, within the lifetime.
- Caller context deadlines on queries.

## Ordered queries and the real-engine plan gate

A leading timestamp/date index key declares `ASC` or `DESC` explicitly. Yugabyte
otherwise hashes the first key, which cannot serve an ordered range scan. This
matters for claims and retention sweeps even when a colocated database keeps its
hot queue tables distributed. Equality lookups can retain hash distribution.
Globally ordered range indexes can concentrate writes at the newest key range;
measure leader load as queues grow and introduce explicit work partitions when
needed rather than relying on hashing to preserve global ordering.

Index replacement uses a new name in an `expand/*.notx.sql` migration, builds
concurrently, and checks validity, readiness, target table and key columns in
postdeploy. Contract drops the old index after the operator's observation window
and backup gate. During the overlap both indexes consume storage and write work;
run migrations sequentially within a universe and monitor catalog churn and
tablet pressure. A fresh baseline contains only the replacements. Shipped
migrations retain their original checksums.

`make verify-yugabyte-explain-audit` seeds service schemas on the pinned engine in
both declared and distributed layouts. CI and `verify-prepush` enforce accepted
plan flags and scan/request budgets; results and raw plans are artifacts. The
fixture has explicit coverage exceptions, synthetic bindings and queries that
return no rows, all reported separately. Passing it does not prove every runtime
SQL branch or production cardinality was exercised. An unexpected EXPLAIN failure
fails the gate. Accepted flags also include bulk inventory scans and indexed per-candidate or
per-cell probes. They are not equivalent to unbounded queue scans; changed budgets
record their reviewed rationale. The shared outbox claim uses a YSQL index hint
and first-in-aggregate probe to stop in enqueue order. This avoids sorting the
whole pending set but can use more RPCs at small backlog sizes. PostgreSQL ignores
the hint and preserves the same claim and locking semantics.
Budget increases and new coverage exceptions require inspection;
`make explain-audit-budget` is a review aid, not the normal verification target.

## Runtime session limits

Services log in as their database's runtime role, and nothing else does: migrations,
backups, restores, relayout and `cluster doctor` connect as the superuser, and
relayout's probes `SET ROLE`, which does not apply a role's settings. The yugabyte and
postgres roles' `init.yml` attach `statement_timeout = 60s` and
`idle_in_transaction_session_timeout = 60s` to every runtime role (`ALTER ROLE … SET`,
only when the role does not already carry them, because on YugabyteDB the shared-catalog
change invalidates every backend's catalog cache). A transaction a service leaves open
holds its row locks and provisional writes until its session ends; with the limit the
engine ends that session after a minute and writers queued behind it proceed. The
settings apply at login, so a running service takes them up as its pool opens
connections. `WithSessionLock` polls `pg_try_advisory_lock` so a wait for another
holder is bounded by its context, not by the statement limit.

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

With `--deep`, doctor also executes every enabled binary's shared capability pack.
PostgreSQL/Yugabyte probes run under the manifest database's prepared runtime role,
proving actual least-privilege access rather than merely observing objects as the
administrator.

## Owner and runtime authority

Provisioning keeps schema ownership/migrations and application traffic on separate
roles. For owner `purser`, for example, it prepares `purser_runtime` with database
connect, schema usage, table DML, sequence usage, and function execution, but without
schema creation, object ownership, superuser, database creation, role creation, or
replication privileges. Grants and owner default privileges reconcile on every
provision run so later migration-created objects are usable by the runtime role.
The runtime login uses `DATABASE_RUNTIME_PASSWORD`, never the owner role's
`DATABASE_PASSWORD`; provisioning rejects missing or equal credentials before
creating roles.
Migration orchestration keeps its advisory lock and ledger writes under the
administrator, but runs the migration SQL itself under `SET ROLE` for the declared
owner so newly created application objects inherit those grants.

The manifest's `runtime_role` field controls the DSN cutover. It is deliberately
opt-in: existing manifests continue using the owner while provisioning prepares the
restricted role. The safe rolling sequence is provision, run `cluster doctor --deep`,
declare `runtime_role`, then reprovision applications. This works the same for
PostgreSQL and YugabyteDB; the Yugabyte contract lane independently proves supported
YSQL behavior.

## Physical layout: colocated service databases

Each platform database declares its YugabyteDB placement in
`pkg/database/sql/layout/<database>.yaml`. A colocated database keeps its small tables
and their secondary indexes in one parent tablet. Tables that grow with events, media,
sessions, or money movement opt out with `WITH (COLOCATION = false)` and keep their own
tablets, which split automatically as they grow. Colocated tables never split, so a
growing or write-hot table must not stay colocated. Without this layout every table and
index gets its own replicated tablet group, which multiplies tablet peers on a small
cluster for tables that hold a few hundred rows.

| Database      | Layout      | Colocated tables | Distributed tables |
| ------------- | ----------- | ---------------- | ------------------ |
| quartermaster | colocated   | 23               | 7                  |
| navigator     | colocated   | 11               | 2                  |
| periscope     | colocated   | 5                | 1                  |
| commodore     | colocated   | 15               | 29                 |
| foghorn       | colocated   | 17               | 32, plus 1 retired |
| purser        | colocated   | 26               | 52                 |
| skipper       | distributed | 0                | 10                 |
| lookout       | distributed | 0                | 7                  |
| bosun         | distributed | 0                | 9                  |

### Classification criteria

A table is colocated only when all of these hold. Every other table is distributed.

- **Cardinality** is bounded by tenants, streams, or fleet size, never by time or events.
- **Sustained writes** stay near fleet poll cadence, roughly 50 row writes per second or less.
- **Access** is key lookups or small relational joins, never time-range scans over history.
- **Growth** comes from in-place updates and deletes, never append-only inserts.

Outbox and inbox tables are always distributed, and the layout loader rejects them in
`colocated_tables`. Their rows arrive with events, and a colocated parent tablet never
splits, so a keyed or coalesced write pattern today is no guarantee for later.

Per-media-object state, such as authority counters and current pointers keyed by
authority id, is distributed even when each row is small, because its cardinality
follows media activity. Colocated tables updated at fleet poll cadence are listed under
`write_rate_benchmark`; the opt-in real-engine benchmark measures them on the shared
parent tablet before a layout change reaches production. Skipper stays distributed
because almost every Skipper table grows with conversations, crawls, or embeddings.

### Contract and enforcement

- Every table a baseline creates appears in exactly one list. Tables that shipped
  migrations create and later migrations drop stay placed under `retired_tables` so
  clusters replaying those migrations can still apply them.
- Baselines and migrations stay engine-neutral. YugabyteDB targets receive SQL rewritten
  from the layout, and PostgreSQL targets receive the files unchanged. The rewriter
  tokenizes SQL and rejects any table-creating form it cannot place: an unclassified
  table, a clause after the column list, `CREATE TABLE AS`, typed or partitioned tables,
  temporary or unlogged tables, materialized views, top-level `SELECT … INTO`, and
  `CREATE TABLE` inside a function or `DO` body. Tables created through dynamic
  `EXECUTE` strings are invisible to it.
- A baseline applied to a YugabyteDB schema without tables (a fresh install, a new
  service database, the contract fixtures) also has every top-level `CREATE [UNIQUE]
INDEX` rewritten to `CREATE … INDEX NONCONCURRENTLY`. An online build runs a backfill
  whose index states each wait for every tserver; on the pinned engine a non-concurrent
  build loads the Purser baseline in 33 s instead of 67 s, Commodore and Foghorn in
  18 s instead of 38–41 s, with identical index definitions and validity, in colocated
  and distributed databases alike. A non-concurrent build does not index rows written
  while it runs, which a schema without tables cannot receive. A baseline reapplied to
  complete a schema that already has tables, and every migration, keep online builds.
- Provisioning creates a missing colocated database with
  `CREATE DATABASE … WITH OWNER = … COLOCATION = true`. It never alters or recreates an
  existing database, so an existing database keeps its creation-time placement.
- `frameworks cluster doctor` compares declared and observed placement for every table
  and secondary index, and reports distinct tablets and tablet peers per database.
  Primary-key indexes and vector indexes (`ybhnsw`) live with their table and are not
  compared separately. Drift is a warning. `frameworks cluster snapshot` records the
  same placement rows.
- `make verify-schema-yugabyte` proves the layout on the pinned engine: database and
  relation placement, tablet counts, a split of a distributed table inside a colocated
  database while the parent stays one tablet, and equal placement for an upgraded and a
  fresh database.

### Shared-catalog DDL disturbs DDL in other databases

`CREATE DATABASE`, `DROP DATABASE`, `ALTER DATABASE … RENAME` and `CREATE ROLE` raise the
catalog version of every database on the cluster. Measured on the pinned engine
(2026.1.1.2) with baselines loading into four other databases at the same time:
`CREATE DATABASE` itself fails with `Keyspace '<name>' already exists` and the loads are
unaffected; `DROP DATABASE` and `RENAME` abort the concurrent loads with an error; and
`CREATE ROLE` succeeds while leaving online index builds in the other databases invalid
without an error (the `CREATE INDEX IF NOT EXISTS` that YSQL retries internally finds the
aborted index and skips it). `GRANT`/`REVOKE … ON DATABASE`, `COMMENT ON DATABASE` and
`ALTER DATABASE … CONNECTION LIMIT` had no such effect. Provisioning a database, creating
a role, or relaying out a database while another database runs a migration is exposed to
this. For the same reason the Yugabyte contract harness never runs two contract processes on
one engine at once (processes on an engine run one after another, and only separate engines
run concurrently), and a process creates its databases before it runs DDL in several of them.

### DDL aborts DML on colocated siblings

`ALTER TABLE` on any table in a colocated database aborts every open multi-statement
transaction in that database with SQLSTATE `40001`, under Read Committed and Repeatable
Read alike: the server cannot retry a transaction that already returned rows. Measured on
the pinned engine, a single autocommit statement racing the same DDL, or a transaction
racing a sibling `CREATE INDEX`, completes. `TestYugabyteColocatedDDLAbortsAreRetryable`
pins the behaviour.

Expand migrations run while services are live, so the contract is the same on
PostgreSQL and YugabyteDB:

- Every explicit transaction runs through `database.WithRetryablePostgresTx`, which
  replays the whole transaction on `40001`, `40P01`, and Yugabyte schema-version errors.
  The callback touches only the database; RPCs, publishes, and cache updates run after
  it returns. A transaction that cannot be replayed carries a `// Raw transaction:`
  comment stating why. PostgreSQL raises the same codes for serialization failures and
  deadlocks.
- Autocommit statements are not replayed by services, because a failure can land after
  commit. YugabyteDB retries them on the server under Read Committed, which
  `yb_enable_read_committed_isolation=true` enables to match PostgreSQL's default.
- On PostgreSQL a transactional migration item runs as one transaction: it sets `lock_timeout`
  and `statement_timeout` after taking the migration advisory lock, and is replayed only when
  the server cancels it with `canceling statement due to lock timeout`, so a blocked `ALTER`
  never queues live traffic behind it.
- On YugabyteDB DDL runs outside transaction blocks (`ysql_yb_ddl_transaction_block_enabled`
  is off in 2026.1), and a schema change on a colocated database aborts a transaction that
  already wrote rows with `40001`, so an item is never sent as one transaction. The role's
  `frameworks.infra.yugabyte_migration_apply` module holds the advisory lock on one
  autocommit session and runs the item's statements one at a time, each in its own
  transaction, as the owner; the ledger row is written only after the last statement and,
  for a `.notx.sql` item, after every guarded index is valid and ready. A transactional item
  runs each statement under `lock_timeout` 5 s and `statement_timeout` 15 min and replays
  only that statement on `55P03`, `40001` or `40P01`, up to six times, 10 s apart. On the
  pinned engine `lock_timeout` does not end a wait for a row lock. A failed or interrupted
  item keeps the statements it applied and has no ledger row; its rerun executes every
  statement again. Post-floor migrations must therefore be rerun-safe statement by statement
  (see Schema Migrations); statements of shipped files that are not are executed under the
  equivalent guards in `cli/pkg/provisioner/migrate_rerun_safety.go`.
- A `.notx.sql` item runs without timeouts, because a concurrent index build waits for older
  transactions, and is never replayed. An interrupted concurrent build leaves an invalid
  index that `IF NOT EXISTS` would skip, so a `.notx.sql` item drops the invalid indexes it
  builds and then rebuilds them. It does so inside the migration advisory lock, and only
  while the ledger still lacks the item: a build in progress is invalid too, so outside the
  lock a second migrator could drop an index the first is still building. A plain
  `DROP INDEX` takes the table's `ACCESS EXCLUSIVE` lock, so the repair runs between its own
  5 s lock timeout and 60 s statement timeout, reset before the concurrent build that
  follows.

### Changing Yugabyte nodes one at a time

A flag, binary, or OS change restarts a node's master and tserver. `cluster provision`,
`cluster upgrade yugabyte`, `cluster restart yugabyte`, and `cluster os update --apply`
change Yugabyte nodes one at a time through quorum checks:

- Nodes that do not serve YSQL go first, because changing them is how they are
  repaired. YSQL can fail while the node's tserver still holds replicas and its master
  still votes, so the masters decide, read through any node including the one being
  repaired. A tserver and master they no longer count as alive (or never registered)
  lose the universe nothing. Whatever they still count as alive must pass
  `are_nodes_safe_to_take_down`, and a node whose state no node can read is refused.
  Serving nodes follow, by name.
- A serving node changes only when every other node serves YSQL, every master is alive
  with exactly one leader, the masters report no under-replicated tablet
  (`/api/v1/health-check`) and no leaderless tablet (`/api/v1/tablet-replication`), and
  `yb-admin are_nodes_safe_to_take_down` accepts the node's tserver and master together.
  The last check is retried for up to 15 minutes while followers catch up after the
  previous node.
- After a change the node must serve YSQL and the masters must see its tserver and master
  alive. When every node serves, the roll also waits for the universe to be whole again
  and for every other node to be safe to take down, which holds only once the changed
  node's replicas have caught up.
- The first failure stops the roll: no later node is changed, so a change that breaks one
  node never reaches another. A node with nothing to change is not gated.
- `cluster provision` runs nodes in parallel only when every node is readable, no node
  serves YSQL, no master leader is reachable, and no yb-master or yb-tserver process runs
  on any node (checked through systemd and the process table). Missing bootstrap markers
  do not exempt a partially bootstrapped universe from these checks.
- A single-node universe has no quorum to protect, so its only node changes as accepted
  downtime, even when its master is stopped.
- `cluster os update --apply` updates other hosts with the requested `--serial` and
  Yugabyte nodes one at a time. A single-node universe is changed without the gate, and
  goes down until the node is back.

- `cluster upgrade yugabyte` and the Yugabyte stage of `release apply` follow the
  YugabyteDB upgrade order: every master, then every tserver, then finalize. Both run
  the same code (`cli/cmd/yugabyte_engine_upgrade.go`); `release apply` runs it before
  converging Yugabyte configuration whenever a joined node's engine differs from the
  release's pin, and refuses a pin older than a node's engine.
  `config/infrastructure.yaml` pins the native engine archives for both architectures;
  `config/schema-contract-engines.yaml` pins the matching build for real-engine tests.
  Release generation includes the native pins in the platform manifest.
  A release carrying engine-upgrade orchestration changes must set `min_cli_version`
  to a CLI containing those changes; updating only the archive pin cannot protect
  operators using older orchestration. Finish the engine upgrade before scheduling
  relayout.
  Archives are extracted into clean `/opt/yugabyte/releases/<artifact-hash>` directories
  and relocated there before an atomic `current` symlink selects the complete artifact
  for CLI tools. Service units use absolute release paths, not the selector. Neither
  an old release nor an existing in-place installation is overwritten or deleted;
  running processes must retain their libraries and catalog files throughout the roll.
  Configuration remains in `/opt/yugabyte/conf`. A partial unpublished extraction can
  be retried, but a selected incomplete release is refused rather than overwritten.
  1. Each node installs the new engine and restarts only its master, after the masters
     confirm that master can be taken down. Recovery checks master consensus, not YSQL.
     A running tserver keeps its binary; a stopped tserver stays stopped.
     Its systemd unit also stays pinned to the old engine until its admitted restart,
     so an automatic crash restart cannot advance it during the master phase.
  2. A master an interrupted run left on the old binary is restarted.
  3. Each stopped or stale tserver is restarted through the whole-node gate. Staleness
     includes an executable inode mismatch or flagfile/unit hashes differing from the
     receipt recorded after its last start/restart. Rendering desired configuration does
     not update this receipt, so deferred configuration restarts survive interruptions.
     Each process phase has its own ordered work list containing only nodes needing it.
  4. Database initialization and whole-node validation run after both process phases.
     Once every node serves YSQL and the universe is healthy, `yb-admin finalize_upgrade`
     promotes the new AutoFlags and upgrades the YSQL catalog.

  The role refuses to install another engine on a node that already joined the
  universe unless the upgrade asks for it, so provisioning cannot change the engine
  without this order and the finalize. Until then a node can go back to the old
  binary; afterwards it cannot rejoin. Before the first change the upgrade writes
  `/var/lib/yugabyte/data/.frameworks-engine-finalize-pending` on every node and removes
  it only after finalizing succeeds, because a finalized universe looks the same to a
  probe as one still waiting for it. Finalizing is idempotent, so rerunning either command
  finishes an interrupted upgrade: `release apply` resumes when a node's engine differs,
  a process runs the previous binary, or a marker remains. Upgrades across a PostgreSQL major version of YSQL need
  the separate catalog upgrade steps and are not handled; every supported release runs
  YSQL on PostgreSQL 15.

Replacing a node is not supported yet: a node that comes back on a new host or an empty
disk starts a master the running quorum does not know, because its master is never added
with `change_master_config`.

YugabyteDB table-level locks (`enable_object_locking_for_table_locks`) make DDL wait for
open transactions instead of aborting them, but they are Early Access and have open lock
leak and master bootstrap defects (yugabyte-db #33296, #33942) in every released
version, so they stay disabled.

### Relaying out an existing database

Placement is fixed at creation, so an existing database moves with
`frameworks cluster yugabyte relayout`. The command copies the database from its own
schema and data into a shadow database created with the layout, proves the copy, and
renames it into place. Copying the source rather than rebuilding from the baseline
copies grants that seeds added (such as the operator analytics role), runtime ledgers,
and any schema the source carries beyond the baseline.

Services keep running. YugabyteDB spends most of a copy on schema changes: every index is
built online and each schema change waits until every tserver has it, seconds per index
even on an empty table, while the rows themselves load in seconds. So the shadow's whole
schema is built before the fence, and only the data moves while services are shut out.
For the length of `cutover` they cannot connect and see the database as unavailable, and
their in-flight transactions roll back when their sessions end. Afterwards they reconnect
by name and reach the relaid database. The fence is what keeps the source unchanged:
`CONNECT` is checked only when a session starts, so it revokes every connect path and ends
the sessions already open, on every tserver, because sessions are local to a tserver.

A leased journal in the `yugabyte` admin database records each step. The lease is renewed
while a step runs and again right before every rename, drop, create, revoke, or grant, so
a process paused past its lease cannot act after another took over.

A dead invocation can leave scripts running on a host, because a script without a
terminal keeps going when its SSH client dies. Every relayout script is therefore
admitted as a worker of its invocation before it runs: it publishes its process group
atomically under an application name derived from the lease owner, in
`/var/lib/frameworks/relayout-workers`. It refuses the registry when the directory or any
directory above it could be written by anyone but root or the SSH user. It then checks the
invocation's revoked marker. Taking over a lease, or releasing it after a failed step,
first fences the previous invocation:

1. On every manifest host, including those whose tserver is down, it revokes the
   invocation and stops its recorded process groups. An unreachable host blocks the
   takeover.
2. It ends the invocation's database sessions and proves none remains.

The marker is created before the scan and checked after a worker publishes, so a late
worker either never runs or is stopped by the scan. An interrupted step resumes from what
the journal observed:

`planned → prepared → fenced → dumped → restored → verified → renamed_old → renamed_new → access_restored → finished`

- **plan** is read-only: every tserver accepts local superuser YSQL, none routes YSQL
  through the YSQL Connection Manager, the primary has the client binaries and a writable
  dump location, and the source has no unclassified tables or database-level settings.
- **preflight** rehearses the copy from the live database into a scratch database in the
  same order, verifies it, measures each phase, estimates the window, and drops it.
- **prepare** runs while services run. It creates the shadow colocated with the source
  owner and restores the source's `ysql_dump` pre-data and post-data sections, so the
  shadow has every table, index, constraint, and trigger and no rows. Both sections are
  rewritten for the layout: distributed tables get the colocation opt-out, and hash-sharded
  keys on colocated tables and their indexes become range keys, because a colocated
  database rejects hash-partitioned relations. It refuses unless the shadow schema equals
  the source schema; that comparison ignores `HASH`/`ASC` key markers and keeps `DESC`
  significant.
- **cutover** runs the window in one invocation:
  1. It refuses, before fencing, if the source schema no longer equals the shadow's.
  2. **fence** records the database ACL, OID, and comment, revokes all access, records
     `fenced`, terminates sessions, and proves on every tserver that none remain. A session
     that will not go away fails the step with access still revoked; rerun cutover or roll
     back.
  3. **copy** checks the schemas again inside the fence, stages a checksummed data-only
     dump, and loads it with `session_replication_role = replica`. That skips user
     triggers and foreign-key checks for the loading session without DDL, so trigger-fed
     tables are copied rather than refilled. A load interrupted part way empties the shadow
     and loads again.
  4. **verify** compares row counts and checksums, sequence state, trigger enablement,
     constraint validation, effective privileges, identity and generated columns,
     sequence ownership, the migration ledgers' structure and owners, placement, and
     consumer capability probes, then stores the receipt. Privileges are compared as
     grantee, privilege, and grant option, without the grantor: PostgreSQL records a
     superuser's grant as made by the object's owner, so production objects created by the
     migrate role and later handed to the service role come back with the owner as grantor.
  5. It renames the source aside and the shadow into place, then restores the recorded ACL,
     which it grants only to the relaid copy.

  A receipt from an earlier invocation is recomputed, and the renames refuse if either
  database changed.

- Rollback is available until access is restored; after that only a forward fix applies.
  From `prepared` it only drops the shadow. Later it finds the original by its recorded
  OID under whichever relayout name it holds, so an interrupted rollback, or a rename whose
  journal write was lost, converges on a rerun.
- While a relayout sits between prepare and finish or rollback, `cluster init`,
  `cluster migrate`, `cluster upgrade yugabyte`, `release apply`, and `cluster provision`
  refuse to touch Yugabyte databases. The source
  schema must keep matching the prepared shadow, superuser connections are not fenced, and
  the canonical name may be free.
- **finish** drops the pre-relayout database and the staged dump.
- The YSQL Connection Manager pools server connections by database OID and does not evict
  them when a database is renamed (yugabyte-db #33018), so a relayout refuses while any
  tserver runs it. The platform does not enable it.

Moving a table between colocated and distributed placement inside an already colocated
database needs the same relayout. The operator runbook is
[YugabyteDB Physical Layout](../../website_docs/src/content/docs/operators/yugabyte-layout.mdx).

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

Production runs **one** ClickHouse node. The schema, Keeper, and config are N>1-ready,
but the multi-node _bootstrap_ (Keeper-quorum formation ordering, and per-node fan-out
for upgrade/restart/preflight) is not yet built — `cluster init` **refuses** N>1 with a
clear message. Growth to a 3-replica cluster is the documented expansion runbook, not an
automatic flip. Moving a single node's analytics onto a new Replicated node is an
operator-driven `cluster clickhouse migrate` (backfill → sync → verify → cutover); the
write/metering/read endpoint sequence is documented in the operator
[ClickHouse migration runbook](../../website_docs/src/content/docs/operators/clickhouse-migrations.mdx).
Node identity (`id`) is validated unique and positive, shared with the
Postgres/Kafka invariant (`validateClusterNodeIDs`); ClickHouse and Yugabyte additionally
require unique hosts.
