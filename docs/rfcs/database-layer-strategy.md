# RFC: Executable Database Contracts

## Status

Accepted; incremental implementation in progress for v0.3.

## Decision

FrameWorks remains SQL-first and engine-aware:

- PostgreSQL service repositories use sqlc for static queries and generated parameter/result types.
- Dynamic PostgreSQL SQL remains handwritten only when generation is impractical and must have a real-engine contract.
- ClickHouse uses explicit typed table writers plus real ClickHouse append tests. sqlc is not used for ClickHouse.
- The existing v0.3 release catalog and migration runner remain the only transition authority.
- Current baseline SQL is desired state; release migrations are transition history.
- Seeds are service-owned, deterministic fixtures executed explicitly, not an implicit database first-boot side effect.
- PostgreSQL, supported YugabyteDB, and ClickHouse are independently tested. Passing on one engine is not evidence for another.

This RFC does not introduce an ORM, `golang-migrate`, down migrations, a second schema manifest, or a repository-wide query package.

## Authorities and ownership

`cli/internal/releases/catalog.yaml` declares the platform release and service-to-database ownership. `pkg/database/sql/schema/` describes a fresh installation at the current source revision. `pkg/database/sql/migrations/<service>/<version>/<phase>/` describes supported transitions from shipped releases.

Those sources have different jobs and must not be collapsed:

- A fresh install applies the current service baseline.
- An upgrade applies immutable, release-catalogued expand/postdeploy/contract transitions.
- Real-engine convergence tests prove both paths produce the same deploy-relevant schema.

Every query and writer remains owned by the service that owns its database. Cross-service data is obtained through `pkg/clients/`; the explicitly allowlisted operator-analytics role is the narrow exception documented in its grant files.

## PostgreSQL query architecture

Each PostgreSQL-backed service owns a sqlc configuration and a service-local query tree. Query files are grouped by repository or transaction boundary, not mechanically by table. Generated code is committed so normal builds do not download a generator; CI regenerates with a pinned sqlc version and rejects differences or untracked generated files.

Generated repositories use sqlc's `DBTX` boundary so the same query is executable through `*sql.DB` and `*sql.Tx`. Conversion must preserve transaction, lock, fencing, and error semantics. Moving a query into a `.sql` file without executing it is not a contract.

Boundary types are explicit:

- UUIDs use `google/uuid` values (and nullable UUID wrappers where the schema is nullable).
- JSONB is normalized before the driver; required JSONB values never depend on a Go `nil` becoming valid SQL.
- Money/usage numerics use intentional decimal or text conversion rather than `float64` scanning.
- Nullable timestamps, arrays, and enums follow schema nullability instead of permissive `any` values.

Dynamic reporting/filter SQL may remain manual. Its repository owns result types and executes representative queries against the real supported engine in CI.

## ClickHouse query and writer architecture

ClickHouse's batch API is a positional runtime contract, so each canonical table gets:

- one explicit `INSERT INTO table (columns...)` statement;
- one typed row structure;
- a writer method that owns positional `Append` order;
- argument-count and zero/nullable/dimensioned cases;
- at least one append through the real pinned ClickHouse engine.

Production code does not call variadic `PrepareBatch`/`Append` directly outside those writers. Financially critical reads live in a small shared exact-query catalog only when runtime code and contract tests must demonstrably execute the same statement.

ClickHouse contracts cover pipeline invariants, not only isolated tables: source fact to final fact, five-minute ledger, reservation publication, and billing rollup attribution.

## Schema equivalence

PostgreSQL convergence compares deploy-relevant logical objects, including:

- columns, indexes, and full constraints;
- triggers, functions, and procedures;
- view and materialized-view definitions;
- sequences, extensions, enums, domains, and composite types;
- schema/relation ownership, grants, and default privileges;
- RLS enablement, force state, and policy definitions.

Definitions that can span lines are compared by stable digest. Migration bookkeeping is excluded because it intentionally differs between fresh and upgraded installations.

Engine images come from `config/infrastructure.yaml`. Compose and real-engine harnesses must resolve the same image plus digest. YugabyteDB has a separately pinned contract image matching the supported native release build.

## Seeds and local development

PostgreSQL demo fixtures live under `pkg/database/sql/seeds/demo/postgres/<database>.sql`. A seed is tested against only its owning baseline, applies twice, uses explicit insert columns, and keeps identity/join keys deterministic. ClickHouse has its own fixture and invariants.

Local Compose mirrors production's logical service databases and users. Database first boot creates schemas only. `make seed-demo` is the canonical explicit workflow for PostgreSQL and ClickHouse demo rows. Derived state should be built by real projection/rebuild paths instead of seeding contradictory raw and derived facts independently.

Local runtime users are not granted superuser merely to install extensions. Bootstrap/migration authority installs extensions, then service owners apply their schemas. Production evolves further toward separate NOLOGIN owners, migrators, and least-privilege runtime roles.

## CI routing

Database contracts are path-gated on pull requests for service database writers, SQL, schemas, migrations, seeds, shared query catalogs, engine configuration, Compose topology, and contract harnesses. A scheduled full lane runs even when path detection would select nothing.

Required failures include deliberately broken columns, conflict targets, JSONB null handling, scan types/order, view bodies, seed ownership, and ClickHouse append types. PostgreSQL and YugabyteDB lanes are separate jobs/targets even where they execute the same baseline.

## Deployment protection

The migration ledger remains necessary but is not sufficient. Binaries will declare the schema capabilities they require and fail readiness with an actionable diagnostic when those capabilities are absent. `cluster doctor --deep` will inspect capabilities plus the migration ledger; it will not introduce a third committed full-schema fingerprint.

Database failure metrics distinguish undefined objects, type/scan failures, constraint violations, and capability mismatches.

RLS is not a blanket first step. Service-specific roles and repository conversion land first. Any tenant RLS policy must be proven on supported YugabyteDB and must account for background/global workers through a deliberately separate privileged role.

## Rollout

1. Correct runtime topology, CI routing, engine pins, schema introspection, service databases, and seed ownership.
2. Convert Purser completely, beginning with repositories that exercise JSONB, conflicts, leases, and transaction boundaries.
3. Convert Navigator, Skipper, Periscope Metering, Commodore, and Quartermaster; introduce all typed ClickHouse writers and the complete metering query pack.
4. Convert Foghorn after adding combined PostgreSQL and real Valkey/Redis HA contracts for leases, CAS scripts, changelogs, caches, and federation state.
5. Add runtime capabilities, deep doctor checks, role separation, and database failure metrics.

## Rejected alternatives

- ORMs such as GORM or ent: they invert or obscure the SQL-first schema and query model.
- `golang-migrate`: it would create a parallel authority that conflicts with the v0.3 release catalogue and phased migration runner.
- Down migrations: rollback safety is declared per release transition and often requires forward repair rather than destructive schema reversal.
- sqlc for ClickHouse: it does not validate the native batch append contract that causes the relevant failures.
- One monorepo query directory: it erases service ownership and encourages cross-service database reads.
- Immediate blanket RLS: it adds connection state and background-worker failure modes before role boundaries and Yugabyte behavior are proven.

## Evidence

- `cli/internal/releases/catalog.yaml`
- `docs/standards/schema-migrations.md`
- `config/infrastructure.yaml`
- `cli/pkg/provisioner/schema_squash_postgres_test.go`
- `cli/pkg/provisioner/schema_squash_clickhouse_test.go`
- `cli/pkg/provisioner/schema_yugabyte_compat_test.go`
- `api_billing/sqlc.yaml`
- `api_billing/internal/database/queries/`
- `pkg/database/queries/`
- `pkg/database/sql/seeds/demo/`
