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

`make verify-schema-migrations` supplies the schema proof that version checks cannot.
For PostgreSQL it compares both current-baseline replay and the latest tagged
baseline plus pending migrations against the current fresh-install baseline. For
ClickHouse it verifies the current Replicated baseline plus post-floor migrations;
the `v0.2.95` release path is a one-time cross-host migration from its tagged
plain-engine source into a fresh Replicated destination, not an in-place engine
conversion. Its separate `PeriscopeMigrationCatalog` coverage test and `cluster
clickhouse migrate verify` protect that copy/cutover path. Once the latest tagged
baseline is Replicated (`v0.2.96+`), the harness also enforces tagged-baseline plus
pending-migration convergence for ClickHouse automatically. CI checks out full tag
history and runs these gates whenever release metadata, migrations, or
baseline/provisioner schema code changes.

## The baseline floor

`schemaMigrationBaselineFloor` in `cli/pkg/provisioner/migrate.go` is the consolidation
line. Migrations with a version **strictly below** the floor are considered _folded into
the baseline_ and are never offered to any cluster (`BuildMigrationItems` /
`BuildClickHouseMigrationItems` / the `Has*` predicates skip them). Fresh installs get
their effect from the baseline; existing clusters already recorded them in `_migrations`.

This is distinct from `migrationPhaseSafetyFloor` (which gates expand-phase _validation
rules_, not selection).

**SAFETY INVARIANT — the floor must be ≤ one above the highest version that EVERY live
cluster has fully applied.** An existing cluster never re-applies the baseline, so if the
floor is raised past what a cluster has applied, that cluster silently misses the
migrations between its version and the floor. The floor is currently **v0.2.96** because
production is at v0.2.95: this folds the old problematic migrations (incl. the v0.2.82
ClickHouse rollup contract that would downgrade `Replicated*`→plain on a fresh node)
while still _offering_ v0.2.96 to production on its next upgrade. The full v0.3.0
consolidation raises the floor only after production has crossed to v0.2.96.

## Minimum-upgrade-version guard

Because raising the floor would strand a cluster that hasn't applied the folded
migrations, `cluster migrate` runs a **below-floor guard** before applying anything
(`{Postgres,ClickHouse}BelowFloorGap` in `cli/pkg/provisioner/migration_floor_guard.go`,
wired via `runBelowFloorGuard`).

Fresh vs stale is decided by a **durable marker**, not ledger-shape inference. Each
baseline schema file writes a `_schema_baseline` row recording the floor it was born at
(the value is kept in sync with `schemaMigrationBaselineFloor` by
`TestBaselineMarkerFloorMatchesConst`). Per database the guard reads that marker and the
`_migrations` ledger:

- marker floor M present → everything `< M` is folded into the baseline this database was
  born from → **skip** those; migrations in `[M, floor)` are still checked against the
  ledger. A fresh cluster (marker = current floor) skips the whole below-floor set;
- no marker → an existing in-place cluster → **every** below-floor migration must be in
  its `_migrations` ledger, else the guard **refuses** with a stepping-stone message (step
  the cluster up to the floor via an older release whose floor still offered those
  migrations, then upgrade here).

Fail-closed: an unreadable ledger/marker blocks rather than risk an unsafe upgrade. Because
the marker is persisted (not inferred from ledger emptiness or newest-applied version), a
dropped `_migrations` table or a non-monotonic history cannot fake "fresh".

## Why we consolidate

Without folding, a fresh node replays the entire migration history on top of the
baseline. For ClickHouse this is actively dangerous: a historical `contract` migration
that `DROP`s and recreates a table as a **plain** `MergeTree` would downgrade the
HA-Replicated baseline to non-replicated. Folding + the floor make the baseline the
single source of truth and stop the replay.

## The ongoing consolidation ritual (per release)

At each release that ships schema changes:

1. Declare `vX.Y.Z` and its safety metadata in
   `cli/internal/releases/catalog.yaml`.
2. Ship the migrations `vX.Y.Z` (incl. any `contract`).
3. **Fold their net effect into the baseline schema files** (same commit as the
   migration — never let them drift).
4. **Raise `schemaMigrationBaselineFloor` → vX.Y.Z.**
5. **Delete the now-folded migration dirs `< vX.Y.Z`** — but see the two-release safety
   below.
6. Keep `make validate-migrations` and `make verify-schema-migrations` green. Use
   `make verify-schema-postgres` or `make verify-schema-clickhouse` while iterating on
   one engine.

This keeps `contract` migrations transient (they exist only between a release and its
consolidation) and the migration tree small.

## Two-release deletion safety

Folded migration files are **kept for one release** before deletion (Flyway CDRB /
Django squash pattern). Because migrations are deltas-on-baseline, **no replay path can
prove the baseline captured all folded history** — the only authoritative check is
diffing the baseline against a **fully-migrated production database**. So before
deleting folded dirs:

1. `make verify-schema-*` is green (baseline applies cleanly + equals baseline +
   post-floor migrations).
2. The baseline is confirmed against the live production schema (the migration doctor
   ledger check, and/or a direct schema diff), proving production has applied
   everything being folded.

Only then delete the `< floor` dirs. The floor already prevents the migrations from
running, so deletion is pure cleanup; this gate guards against a baseline that silently
omits a folded migration's effect.

## Verification harness — `make verify-schema-{postgres,clickhouse}`

Docker-backed Go integration tests (build tag `schema_verify`,
`cli/pkg/provisioner/schema_squash_*_test.go`) that, against real engines:

- apply the baseline to one database, the baseline + every **post-floor** migration to
  another, and assert the two are logically equal;
- for ClickHouse, normalize _only_ the `Replicated*` engine prefix + injected
  zk-path/replica args (the deliberate HA divergence), preserving everything else
  (`ORDER BY`/`PARTITION BY`/`TTL`/`SETTINGS`/version columns/TABLE-vs-VIEW kind);
- for Postgres, compare `information_schema`/`pg_indexes` introspection (order-independent).

This is the permanent guard: a release that adds a migration but forgets to update the
baseline (or vice versa) breaks the equality. It also smoke-tests that the baselines
apply cleanly on a real engine (incl. ClickHouse Replicated engines + Keeper). Gated
behind `schema_verify` so a plain `make test` needs no Docker; wire into CI once
Docker-in-CI exists.

## Operator pre-flight before a consolidation release

Before deploying a release that raises the floor, confirm every live cluster has applied
the complete pre-floor migration set (so folding doesn't strand a partially-migrated
cluster). With the migration doctor: `cluster doctor` surfaces ledger gaps. A fresh
cluster born from the baseline legitimately has no pre-floor ledger rows and needs none.
