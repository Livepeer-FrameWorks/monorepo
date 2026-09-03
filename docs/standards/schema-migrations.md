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
`periscope-metering`, or `foghorn`. Use
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
service behavior so the single-node tablet safety ceiling cannot be exceeded; mutating
service tests receive isolated databases, and each bounded service fixture discards all of
them with its container. Foghorn's larger schema runs in two bounded batches so neither
tablet accumulation nor repeated distributed teardown can destabilize the single-node test
engine.

PostgreSQL/Yugabyte relation comparison uses `format_type(atttypid, atttypmod)`,
so length, precision, and other typmod drift is significant. Physical relation
column position is deliberately not compared: PostgreSQL appends `ADD COLUMN`
and has no `AFTER` clause, so a tagged upgrade cannot preserve a current
baseline that groups a later-added column mid-table. Composite-type attribute
order remains significant because it is part of that type's value contract.

Moving SQL into a separate file is not itself a test strategy. Keep small queries near
their repository/service when that is clearer. Put a statement in
`pkg/database/queries` when runtime code and a real-engine contract test must share its
exact text, especially for metering and financial writes. Use mock tests for branching
and failure handling, but never treat `sqlmock.AnyArg()` as proof that driver values,
defaults, casts, unique constraints, or `NOT NULL` contracts work on the real engine.

Static PostgreSQL queries should migrate to the owning service's sqlc query tree.
Generated code is committed and CI regenerates it with the pinned tool version.
Use the generated `DBTX` boundary so transaction ownership stays with the caller;
never split a lock/fencing transaction merely to fit generated methods. ClickHouse
uses explicit typed batch writers and live append contracts rather than sqlc.

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
