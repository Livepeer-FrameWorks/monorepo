# Schema Migrations & Consolidation

How FrameWorks manages Postgres (YugabyteDB) and ClickHouse schema over time, and
the **consolidation ritual** that keeps the migration history bounded and the
HA-Replicated ClickHouse baseline authoritative.

## The model: baseline + delta migrations

There are two kinds of schema artifact, and they are **maintained in lockstep**:

- **Baseline schema files** — the authoritative _current_ shape of each database:
  - Postgres: `pkg/database/sql/schema/<db>.sql` (commodore, purser, quartermaster,
    foghorn, navigator, skipper, …).
  - ClickHouse: `pkg/database/sql/clickhouse/periscope.sql` (the `Replicated*` HA
    schema).
    Applied verbatim on a fresh `cluster init`. They use `IF NOT EXISTS` DDL.
- **Versioned delta migrations** — `pkg/database/sql/{migrations,clickhouse/migrations}/<db>/vX.Y.Z/<phase>/NNN_*.sql`,
  in phases `expand` → `postdeploy` → `contract`. They are **deltas applied on top of
  the baseline**, not a from-empty history: base tables are created by the baseline;
  migrations only `ALTER`/extend them. Tracked per-database in a `_migrations` ledger;
  the role hard-fails if an already-applied migration's checksum changes (so migration
  files are immutable once shipped).

**Invariant — every migration's net effect must also be in the baseline, in the same
commit.** A fresh `init` applies the baseline then only post-floor migrations; an
upgrade applies the migration. They must converge. The verification harness (below)
enforces this for post-floor migrations.

## New service databases

A database introduced by a release has a baseline and **no migrations**: its whole
schema lives in `pkg/database/sql/schema/<db>.sql`. Existing clusters receive it through
the release path, not through `cluster provision`:

- `cluster migrate --phase expand` (the first step of `cluster release apply`) probes
  every manifest database with an embedded baseline before the below-floor guard and
  any migration. An absent database, or a present one whose service schema has no base
  tables, is initialized by the postgres/yugabyte role's `init` tag (owner role,
  database, runtime role, ownership) and `schema` tag (current baseline, which writes
  the `_schema_baseline` marker, plus owner and runtime grants), restricted to those
  databases. A database with tables and a marker or `_migrations` rows is initialized
  and receives nothing.
- A database with tables but neither a marker nor `_migrations` rows has unknown
  provenance. Release and migration commands refuse it before baseline SQL unless the
  operator supplies `--complete-interrupted-baselines` after inspecting it. The opted-in
  path completes and verifies it (`InitializeServiceDatabases` in `cli/pkg/provisioner`):
  the marker is set to
  `baseline-verification-pending`; a scratch reference `<db>__baseline_check_<hex>` is
  created on the same server (stale references for that database are dropped first);
  the `schema` tag runs with `reapply` for both the database and the reference; the
  service-schema catalogs (tables, columns, constraints, indexes with validity,
  triggers, routines, views, sequences, types, policies; not ownership or grants) are
  compared; the reference is dropped; and only an identical catalog replaces the
  pending marker with the reference's floor. A difference stops the release with each
  object's expected and actual definition and leaves the pending marker, which the
  initialization probe ignores and every floor reader refuses, so the refusal survives
  reruns and interruptions. The completion flag never accepts a divergent schema.
- A second probe must show every bootstrapped database initialized or the release stops.
- Dry-run sends no role, database, marker, or baseline SQL. It lists the databases it
  would create or complete and excludes them from the floor guard and migration ledger
  checks.
- The pre-deploy gate (`cluster upgrade`) refuses a service whose owned database is
  missing, empty, or unverified, because a database without migrations would otherwise
  pass the ledger checks.
- Tagged-upgrade proofs treat a baseline absent at the tag as created from the current
  baseline, and fail if a post-tag migration targets that database.

Every service baseline must write the `_schema_baseline` marker and be safe to apply
twice: `TestBaselineMarkerFloorMatchesConst` checks the marker, and
`TestPostgresServiceBaselinesApplyTwice` / `TestYugabyteServiceBaselineReapplyAndCompletion`
apply each baseline a second time on a real engine and require an unchanged catalog.
Use `IF NOT EXISTS`, `CREATE OR REPLACE`, or `DROP ... IF EXISTS` before `ADD CONSTRAINT`.

Do not ship migrations for a database in the release that introduces it; change its
baseline instead.

## Release catalog and migration version ceiling

`cli/internal/releases/catalog.yaml` declares releasable platform versions and the
safety metadata emitted into their release manifests: the minimum CLI version,
required reconciliation transitions, and rollback restrictions. It is release
authority, not an example file.

Git tags and the catalog represent different moments. A `vX.Y.Z` tag says that
version shipped. During development, its code, baseline SQL, and migrations must
exist before the tag does, so the catalog may declare **exactly one** release newer
than the latest reachable final tag. That entry is the pending release. Every
migration newer than the tag must target it; multiple future migration buckets are
invalid. When the release workflow creates the tag, the same catalog entry becomes
shipped. Code-only work after that does not require immediately declaring another
release.

Any schema migration whose code and baseline are already on `master` **must target the
actual next release declared in that catalog**. Do not stage migrations under a later
hypothetical tag: the current binary would then deploy before an existing cluster is
offered its required schema, while a fresh install would already receive the changed
baseline.

When advancing the next release, update these together:

1. `cli/internal/releases/catalog.yaml` release entry and safety metadata.
2. PostgreSQL and ClickHouse migration version directories.
3. Compiled release-transition `IntroducedIn` values and their tests.
4. Version-specific operator scripts, schema comments, and canonical docs.

`make validate-migrations` enforces this state from two directions. The Git-aware
guard compares the reachable final tag, catalog, full migration tree, and the current
change set; it rejects edits to shipped migrations, more than one pending catalog
release, or unshipped migrations outside that pending version. The embedded CLI
validator independently rejects migrations newer than its highest catalog entry.
`make test-release-state` exercises the guard's pre-tag, tag-cut, post-tag,
wrong-bucket, multiple-pending, and immutable-migration cases in a temporary Git
repository.

PostgreSQL/YugabyteDB files ending in `.notx.sql` are applied in autocommit
mode. The validator permits one or more statements only when every statement is
an idempotent `CREATE [UNIQUE] INDEX CONCURRENTLY IF NOT EXISTS`; the role sends
those statements separately and records one checksum/ledger row for the
unchanged migration file. This preserves an applied migration's identity while
meeting PostgreSQL's requirement that concurrent index creation not run inside
a transaction block. Every concurrent index must also have a same-release
postdeploy check through `pg_index` that rejects a missing, not-ready, or invalid
index. The validator requires the exact index name and validity predicates in
the same postdeploy statement because an interrupted concurrent
build can leave an invalid relation that a later `IF NOT EXISTS` silently skips.

## Dry-run safety

Migration dry-runs inspect the live `_migrations` ledgers and report pending files,
but never submit migration, ledger, ownership, baseline, role, or seed SQL. This is
enforced in the PostgreSQL and YugabyteDB Ansible roles with explicit
`not ansible_check_mode` guards and a repository test that treats database-module
tasks as mutating unless their task name is explicitly audited as read-only.

Rollback is not a dry-run mechanism. In particular, YugabyteDB configurations that
do not provide transactional DDL can retain schema changes after a client rolls back
while also rolling back the corresponding `_migrations` insert. Check mode therefore
must remain read-only regardless of engine capabilities or server flags.

Expand constraints must use `ADD CONSTRAINT … NOT VALID` and pair by exact name
with a same-release postdeploy `VALIDATE CONSTRAINT`; the validator enforces the
pair in both directions and ignores comments and SQL string literals. The only
exception is a compatibility constraint explicitly dropped by a contract migration
in that same release: validating a constraint solely to drop it would add a full
table scan without strengthening the post-release schema.

`make verify-schema-migrations` supplies the complete local schema proof that version checks cannot.
For PostgreSQL it compares both current-baseline replay and the latest tagged
baseline plus pending migrations against the current fresh-install baseline. For
ClickHouse it verifies the current Replicated baseline plus post-floor migrations
and the latest supported tagged Replicated baseline plus pending migrations. It
also applies the complete PostgreSQL and ClickHouse demo seeds, exercises selected
billing-critical runtime statements against the real engines, and verifies the
Purser usage writer against its current `JSONB` constraints. The
generic `PeriscopeMigrationCatalog` and `cluster clickhouse migrate verify` remain
available for operator-directed cross-host moves; historical plain-engine cutover
code is not part of the v0.3 lifecycle. CI checks out full tag
history and runs the same proof as two independently gated jobs whenever release metadata,
migrations, or baseline/provisioner schema code changes: `make verify-schema-migrations-core`
for PostgreSQL and ClickHouse, and `make verify-schema-yugabyte` for YugabyteDB.

The exhaustive Yugabyte target reconstructs every supported tagged/current database and is
therefore a release and scheduled-CI proof, not the default inner-loop check for every Go
change. Use `make verify-yugabyte-service SERVICE=<name>` for query or repository changes in
one of `commodore`, `purser`, `navigator`, `skipper`, `quartermaster`,
`periscope-metering`, `foghorn`, or `lookout`. Use
`make verify-yugabyte-database DATABASE=<name>` when that database's baseline, migrations,
or capability assumptions changed; this runs its tagged/current convergence plus its service
contracts. The database name for Periscope Metering is `periscope`. Both focused targets use
one bounded suite-owned Yugabyte process per service batch and a fresh database per mutating
test. CI follows the same split: shared Yugabyte/schema/harness changes run the exhaustive
and HA gates, while a service-owned repository change runs only that service's
real-Yugabyte contracts. Scheduled
and manually dispatched CI continue to run the exhaustive gates.

## The baseline floor

`schemaMigrationBaselineFloor` in `cli/pkg/provisioner/migrate.go` is the consolidation
line. Migrations with a version **strictly below** the floor are considered _folded into
the baseline_ and are never offered to any cluster (`BuildMigrationItems` /
`BuildClickHouseMigrationItems` / the `Has*` predicates skip them). Fresh installs get
their effect from the baseline; existing clusters already recorded them in `_migrations`.

The floor comes from `schema_migration_floor` in the release catalog; migration
selection, repository validation, tests, and baseline markers consume the same value.
All migration files still present receive the current phase-safety validation rules.

The current floor is **v0.3.0**. Fresh databases are born from the v0.3 baseline. The
only supported in-place source is the corrected, fully migrated **v0.2.96** release.
Executable v0.2 migration bodies are not shipped in the v0.3 CLI.

## Minimum-upgrade-version guard

Because raising the floor would strand a cluster that hasn't applied the folded
migrations, `cluster migrate` runs a **below-floor guard** before applying anything
(`{Postgres,ClickHouse}BelowFloorGap` in `cli/pkg/provisioner/migration_floor_guard.go`,
wired via `runBelowFloorGuard`).

Fresh vs stale is decided by a **durable marker plus compact source certificates**, not
ledger-shape inference. Each
baseline schema file writes a `_schema_baseline` row recording the floor it was born at
(the value is kept in sync with `schemaMigrationBaselineFloor` by
`TestBaselineMarkerFloorMatchesConst`). Per database the guard reads that marker and the
`_migrations` ledger:

- marker floor M present → everything `< M` is folded into the baseline this database was
  born from → **skip** those; migrations in `[M, floor)` are still checked against the
  ledger. A fresh cluster (marker = current floor) skips the whole below-floor set;
- no marker → an existing in-place cluster → the catalog's per-database source
  certificates (version, phase, sequence, SHA-256) must match its `_migrations` ledger.
  For v0.3 these prove the corrected v0.2.96 stepping-stone. They retain no SQL and
  cannot execute old behavior. A missing or mismatched certificate refuses the upgrade.

Fail-closed: an unreadable ledger/marker blocks rather than risk an unsafe upgrade. Because
the marker is persisted (not inferred from ledger emptiness or newest-applied version), a
dropped `_migrations` table or a non-monotonic history cannot fake "fresh".

## Why we consolidate

Without folding, a fresh node replays the entire migration history on top of the
baseline. For ClickHouse this is actively dangerous: a historical `contract` migration
that `DROP`s and recreates a table as a **plain** `MergeTree` would downgrade the
HA-Replicated baseline to non-replicated. Folding + the floor make the baseline the
single source of truth and stop the replay.

## The bounded-history release process

For an ordinary release that ships schema changes:

1. Declare `vX.Y.Z` and its safety metadata in the release catalog.
2. Add migrations only under that pending version.
3. Fold their net effect into the baseline in the same commit.
4. Keep the floor unchanged while any supported source still needs those executable
   deltas.
5. Keep `make validate-migrations` and `make verify-schema-migrations` green.

At a deliberate lifecycle boundary, after every supported live cluster has crossed the
chosen stepping-stone:

1. Set `schema_migration_floor` to the boundary release.
2. Record a minimal exact-checksum source certificate for each database in
   `schema_migration_sentinels`.
3. Set every baseline `_schema_baseline` marker to the same floor.
4. Delete executable SQL strictly below the floor.
5. Remove release-specific handlers, exceptions, and operator scripts whose invariant is
   now baseline state.

This bounds code and migration history without creating a permanent CLI handler for
every release. Generic transition and data-migration engines remain; individual handlers
disappear at the next certified lifecycle boundary.

## Deletion safety

Because migrations are deltas-on-baseline, replay alone cannot prove the baseline
captured all folded history. Before deleting folded directories:

1. `make verify-schema-*` is green.
2. The baseline is compared with a fully migrated live schema and its ledgers.
3. Exact source certificates are captured before deleting the corresponding SQL.
4. The stepping-stone tag remains available for recovery and convergence tests.

Only then delete `< floor`. Repository validation permits deletion only when the new
floor is itself the one pending catalog release; modifying shipped SQL remains forbidden.

## Verification harness — `make verify-schema-{postgres,clickhouse}`

Docker-backed Go integration tests (build tag `schema_verify`,
primarily under `cli/pkg/provisioner/` plus service-owned writer contracts) that,
against real engines:

- apply the baseline to one database, the baseline + every **post-floor** migration to
  another, and assert the two are logically equal;
- for ClickHouse, normalize _only_ the `Replicated*` engine prefix + injected
  zk-path/replica args (the deliberate HA divergence), preserving everything else
  (`ORDER BY`/`PARTITION BY`/`TTL`/`SETTINGS`/version columns/TABLE-vs-VIEW kind);
- for Postgres, compare columns, indexes, full constraints, triggers, routines,
  view/materialized-view bodies, sequences, extensions, user-defined types,
  ownership, grants/default ACLs, and RLS state/policies (order-independent);
- apply every service-owned PostgreSQL demo seed twice against only its owning
  baseline, catching cross-service assumptions, stale column lists,
  invalid `ON CONFLICT` targets, missing foreign keys, and non-idempotent upserts;
- apply the complete ClickHouse demo seed and assert lifecycle/attribution
  invariants required by regional metering;
- execute billing-critical statements from `pkg/database/queries` using the same
  SQL text imported by the runtime; and
- exercise service-owned write paths where Go driver conversion, nullable values,
  defaults, JSON encoding, or constraints are part of the contract;
- run `verify-periscope-metering-chain` so raw/final/ledger projection replay,
  delayed and duplicate delivery, correction adjustments, Kafka-before-cursor
  fencing, and reservation persistence/release execute on real ClickHouse plus
  PostgreSQL. The relational transition pack also runs on supported Yugabyte.

This is the permanent guard: a release that adds a migration but forgets to update the
baseline (or vice versa) breaks the equality. A schema change that leaves a seed or
covered runtime statement incompatible also breaks the gate. It smoke-tests the
release-pinned PostgreSQL and ClickHouse Replicated engines with Keeper. A separate
supported-Yugabyte lane applies every service baseline, exercises runtime
capabilities, and compares every service's tagged upgrade with its current
baseline whether or not that service has a post-tag migration. PostgreSQL
success never substitutes for Yugabyte proof. The tests remain behind
`schema_verify` so a plain `make test` needs no Docker; CI splits them between
`make verify-schema-migrations-core` and the scoped/exhaustive Yugabyte targets described
above. The exhaustive lane uses separate suite-owned engines for schema convergence and
service behavior to bound pressure on the single-node tablet safety ceiling; mutating
service tests receive isolated databases, and each bounded service fixture discards all of
them with its container. Foghorn and Commodore service contracts run in separate bounded
batches to limit tablet accumulation without repeatedly dropping distributed databases or
raising engine safety limits. Commodore's scoped database gate also separates convergence
from its service batches; its placement-only gate separates control, object/delivery, and
destination-authority contracts. Each batch has a distinct coverage artifact. Explicit
selector checks compare the combined service batches against discovered tests so growth
cannot silently omit a contract. When a retained-database suite outgrows a fixture, split
the suite across fresh engines and preserve its complete selector and coverage union.

PostgreSQL/Yugabyte relation comparison uses `format_type(atttypid, atttypmod)`,
so length, precision, and other typmod drift is significant. Physical relation
column position is deliberately not compared: PostgreSQL appends `ADD COLUMN`
and has no `AFTER` clause, so a tagged upgrade cannot preserve a current
baseline that groups a later-added column mid-table. Composite-type attribute
order remains significant because it is part of that type's value contract.

## Executable query contracts

The same rule that governs schema artifacts governs queries: a statement counts as a
contract only when the runtime and a test execute it against a real engine. Moving SQL
into a separate file, or behind a generated method, is not itself a test strategy. Keep
small queries near their repository or service when that is clearer. Use mock tests for
branching and failure handling, but never treat `sqlmock.AnyArg()` as proof that driver
values, defaults, casts, unique constraints, or `NOT NULL` contracts work on the real
engine.

### PostgreSQL: generated, engine-checked repositories

Each PostgreSQL-backed service owns a sqlc configuration and a service-local query tree:

| Service               | Config                          | Queries                                          | Generated package                                  |
| --------------------- | ------------------------------- | ------------------------------------------------ | -------------------------------------------------- |
| `api_billing`         | `api_billing/sqlc.yaml`         | `api_billing/internal/database/queries/`         | `api_billing/internal/database/purserdb`           |
| `api_control`         | `api_control/sqlc.yaml`         | `api_control/internal/database/queries/`         | `api_control/internal/database/commodoredb`        |
| `api_tenants`         | `api_tenants/sqlc.yaml`         | `api_tenants/internal/database/queries/`         | `api_tenants/internal/database/quartermasterdb`    |
| `api_dns`             | `api_dns/sqlc.yaml`             | `api_dns/internal/database/queries/`             | `api_dns/internal/database/navigatordb`            |
| `api_consultant`      | `api_consultant/sqlc.yaml`      | `api_consultant/internal/database/queries/`      | `api_consultant/internal/database/skipperdb`       |
| `api_analytics_query` | `api_analytics_query/sqlc.yaml` | `api_analytics_query/internal/database/queries/` | `api_analytics_query/internal/database/meteringdb` |
| `api_balancing`       | `api_balancing/sqlc.yaml`       | `api_balancing/internal/database/queries/`       | `api_balancing/internal/database/foghorndb`        |
| `api_incidents`       | `api_incidents/sqlc.yaml`       | `api_incidents/internal/database/queries/`       | `api_incidents/internal/database/lookoutdb`        |

Each config points `schema:` at that database's baseline file under
`pkg/database/sql/schema/`, so generation itself fails when a query no longer matches
desired state. Query files are grouped by repository or transaction boundary, not
mechanically by table, and every query stays owned by the service that owns its
database; cross-service data still goes through `pkg/clients/`.

Generated code is committed so a normal build downloads no generator. `make sqlc`
regenerates with the pinned `SQLC_VERSION` (`v1.31.1` in the `Makefile`) and
`make sqlc-check` re-runs generation, then fails on any diff **or untracked file** in
the generated packages. CI runs it in the Go build lane and again in the schema lane
(`.github/workflows/ci.yml`).

Boundary types are declared in each `sqlc.yaml` rather than left to driver defaults:
`uuid` maps to `github.com/google/uuid`, `jsonb` (nullable and not) to
`encoding/json.RawMessage`, `text[]` to `github.com/lib/pq.StringArray`. Required JSONB
values must be normalized before the driver so a Go `nil` never becomes the contract.

Use the generated `DBTX` boundary so the same query runs through `*sql.DB` and
`*sql.Tx` and transaction ownership stays with the caller; never split a lock or
fencing transaction merely to fit generated methods. Converting a handwritten query
must preserve its transaction, lock, fencing, and error semantics.

Dynamic reporting and filter SQL may stay handwritten where generation is impractical.
Its repository then owns its result types and must execute representative statements
against the real supported engine in CI; the exemption is from generation, not from a
contract.

### ClickHouse: typed batch writers

ClickHouse's native batch API is positional, so sqlc's static analysis would not catch
the failures that actually occur. Each canonical table instead gets, in
`api_analytics_ingest/internal/database/periscopeingestdb/*_writers.go`:

- one explicit `INSERT INTO <table> (columns...)` constant;
- one typed row struct;
- a `Prepare<Table>` function returning the generic `Writer[Row]` from `writer.go`,
  whose `values(Row) []interface{}` function owns positional `Append` order.

Production code does not call variadic `PrepareBatch`/`Append` outside those writers.
`TestEveryTypedWriterAppendsToCurrentClickHouse` in
`writer_realclickhouse_test.go` (build tag `schema_verify`) applies the current
`Replicated*` baseline to a real pinned ClickHouse container and appends through every
registered writer. It also parses the package's `*_writers.go` files and fails on
registry drift, so a new `Prepare*` function cannot ship without a live append.
`pkg/database/observability.go` records each writer's prepare, append, and send failure
into `frameworks_database_failures_total{service,engine,failure}` with a bounded label
set, and deliberately classifies timeouts and dropped connections as operational rather
than as schema mismatches.

### CI routing

Database contracts are path-gated. The `changes` job in `.github/workflows/ci.yml`
defines `release`, `database`, `yugabyte_core`, and per-service `yugabyte_*` filters
covering Ansible database roles, service database packages, baselines, migrations,
seeds, engine configuration, Compose topology, and the contract harnesses. The schema
lane runs when `release` or `database` matched; the Yugabyte lane runs the exhaustive
gates on `yugabyte_core` and only the touched service's contracts otherwise. The
nightly `schedule` trigger and `workflow_dispatch` force the full lane regardless of
path detection, so a filter gap is bounded by one day rather than shipped.

### What these contracts are deliberately not

Each of the following was considered and rejected; reopening one means overturning a
property above, not filling a gap.

- **An ORM (GORM, ent).** It inverts the SQL-first model: the baseline stops being
  desired state and generated DDL becomes a second authority.
- **`golang-migrate`.** It would create a transition authority parallel to the release
  catalog and the phased expand/postdeploy/contract runner.
- **Down migrations.** Rollback safety is declared per release transition in the
  catalog; recovery is usually forward repair, not destructive schema reversal.
- **sqlc for ClickHouse.** It does not validate the positional batch append contract
  that produces the real failures.
- **One repository-wide query package.** It erases service ownership and invites
  cross-service database reads. `pkg/database/queries/periscope_metering.go` is the
  narrow exception: statements whose exact text the runtime and a real-engine contract
  test must demonstrably share, which in practice means metering and financial writes.
- **Immediate blanket row-level security.** RLS adds connection state and
  background-worker failure modes. Service-owned roles and repository conversion land
  first; any tenant policy must be proven on supported YugabyteDB and must account for
  global workers through a separate privileged role.

## Seeds and local engines

Seeds are service-owned deterministic fixtures, executed explicitly, never a database
first-boot side effect. PostgreSQL demo fixtures live at
`pkg/database/sql/seeds/demo/postgres/<database>.sql` and ClickHouse's at
`pkg/database/sql/seeds/demo/clickhouse_demo_data.sql`. A seed uses explicit insert
column lists, keeps identity and join keys deterministic, and is proven against only
its owning baseline (the harness applies it twice, so a non-idempotent upsert or a
borrowed cross-service assumption fails the gate). Derived state should be produced by
the real projection or rebuild path rather than seeded alongside contradictory raw
facts.

`make seed-demo` (`seed-demo-postgres` plus `seed-demo-clickhouse`) is the canonical
workflow. First boot of the local stack only creates structure:
`infrastructure/postgres/init-service-databases.sh` mirrors production's logical service
databases by creating each `<service>` owner and `<service>_runtime` login, the database,
connect grants, and extensions, then applies the baseline as the owner and grants the
runtime role the same DML-only privilege set described under
[PostgreSQL runtime roles](#postgresql-runtime-roles). Extensions are installed by the
bootstrap superuser precisely so a service owner never needs superuser to satisfy an
idempotent `CREATE EXTENSION` in its baseline.

Engine versions are single-sourced. `docker-compose.yml` and the contract harnesses
resolve the same image plus digest from `config/infrastructure.yaml`; YugabyteDB carries
a separate contract pin in `config/schema-contract-engines.yaml` matching its supported
native release build.

## Executable runtime capabilities

Migration ledgers prove recorded transitions; they do not prove that a live role can
execute the reads and writes required by the binary being deployed. Every service
with a PostgreSQL/YugabyteDB or ClickHouse dependency therefore declares a small
read-only capability pack in `pkg/database/capabilities.go`. The shared connection
layer executes the relevant pack after ping and before returning the handle. A
missing table, column, view, type, or grant prevents the binary from becoming ready
and identifies the failed capability.

Capability probes are not a third full schema manifest. They select a few
deploy-critical columns and views that represent the binary contract; current
baseline SQL remains desired state and release-catalogued migrations remain
transition history. Completeness tests require every topology-declared database
dependency to have an engine-specific pack, and real PostgreSQL and ClickHouse tests
execute all packs against the current baselines with broken-column negative controls.

`frameworks cluster doctor --deep` runs these same probes in addition to checking
the migration ledger. PostgreSQL/YugabyteDB probes switch to the prepared runtime
role, so deep doctor detects both live shape drift and missing grants before a
manifest opts service DSNs into that role.

## PostgreSQL runtime roles

Each declared database has an owner/migration login and a separately provisioned
least-privilege runtime login. The conventional runtime name is `<owner>_runtime`;
`runtime_role` may declare an explicit name. Owner and runtime roles must use
different credentials: `DATABASE_PASSWORD` is the owner/migration secret and
`DATABASE_RUNTIME_PASSWORD` is the restricted application secret. Per-database
`POSTGRES_<DATABASE>_RUNTIME_PASSWORD` values may override the shared runtime
secret. Runtime password resolution is identical for role provisioning and service
DSN generation: database override, owner-name alias, named Postgres instance
override, matching cluster `DATABASE_RUNTIME_PASSWORD`, then the shared
`DATABASE_RUNTIME_PASSWORD`. Provisioning rejects an owner/runtime credential
collision before invoking Ansible. Runtime roles receive database connect,
schema usage, table `SELECT`/`INSERT`/`UPDATE`/`DELETE`, sequence usage, and function
execution, including owner default privileges for objects created later. They do not
receive schema creation, object ownership, superuser, database creation, role
creation, or replication privileges.

The infrastructure administrator retains the migration advisory lock and writes
the `_migrations` ledger, but each migration body executes under `SET ROLE` for the
declared owner. Objects created by a migration are therefore owner-owned and inherit
the same runtime default privileges; administrator-owned application tables are a
contract failure.

The manifest field is an explicit rolling cutover. When `runtime_role` is absent,
service DSNs continue to use the owner while provisioning still creates and
reconciles the conventional runtime role. Operators provision first, verify with
`cluster doctor --deep`, then add `runtime_role` and reprovision services. Owner
logins remain enabled during the beta transition; disabling them requires proof that
no deployed revision still uses them.

## Operator pre-flight before a consolidation release

Before deploying a release that raises the floor, confirm every live cluster has applied
the complete pre-floor migration set (so folding doesn't strand a partially-migrated
cluster). With the migration doctor: `cluster doctor` surfaces ledger gaps. A fresh
cluster born from the baseline legitimately has no pre-floor ledger rows and needs none.
