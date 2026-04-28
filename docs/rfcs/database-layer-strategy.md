# RFC: Database Layer Strategy

## Status

Draft

## TL;DR

- Adopt sqlc for type-safe query generation from raw SQL.
- Use golang-migrate for versioned, auditable schema changes.
- Enforce tenant isolation at the database level with PostgreSQL RLS.
- Incremental adoption — no ORM, no big-bang rewrite.

## Current State

Database-backed services mostly use raw `database/sql` with `lib/pq`. SQL is written inline in gRPC handlers and service stores across api_billing, api_control, api_balancing, api_tenants, api_dns, api_consultant, and related packages. Query results are commonly mapped field-by-field via manual `.Scan()` calls. Tenant isolation relies primarily on application-level `WHERE tenant_id = $1` and service-boundary discipline — there is no broad database-level RLS safety net if a query omits the filter.

Baseline schema definitions live in `pkg/database/sql/schema/` as monolithic SQL files (commodore.sql, purser.sql, foghorn.sql, quartermaster.sql, and others). The `pkg/database/sql/migrations/` directory now contains at least one versioned migration (`v0.3.0/001_bootstrap_tenant_aliases.sql`), but migrations are still not the primary schema-authoring model and most schema history remains encoded in the monolithic baseline files.

PG-specific features are used throughout: JSONB columns, `pq.Array` for array types, and pgvector in api_consultant for embedding similarity search. YugabyteDB is listed as a production alternative in cluster manifests and provisioner roles, but compatibility should be validated per feature instead of assumed for every PostgreSQL extension, index type, and RLS policy.

Evidence:

- `pkg/database/postgres.go`
- `pkg/database/sql/schema/`
- `pkg/database/sql/migrations/v0.3.0/001_bootstrap_tenant_aliases.sql`
- `api_billing/internal/grpc/server.go`
- `api_control/internal/grpc/server.go`
- `api_balancing/internal/grpc/server.go`
- `api_consultant/internal/knowledge/store.go`

## Problem / Motivation

Inline SQL scattered across handler files is a growing maintenance burden. Manual `.Scan()` is verbose and error-prone — column order mismatches produce silent bugs that only surface at runtime. Missing `WHERE tenant_id = $1` in any single query is a cross-tenant data leak. Unversioned schema files make it impossible to audit what changed, when, or roll back safely.

## Goals

- Type-safe query generation that catches column mismatches at compile time.
- Versioned migration files with UP/DOWN semantics for every schema change.
- Database-level tenant isolation enforcement as a safety net alongside application-level filtering.
- Centralized query files per service, replacing inline SQL in handlers.

## Non-Goals

- Full ORM adoption (GORM, ent, or similar).
- Rewriting all existing queries in a single pass.
- Abstracting away PG-specific syntax — YugabyteDB is PG-compatible, and we use PG features intentionally.
- Supporting non-PostgreSQL databases.

## Proposal

### sqlc

Generates type-safe Go structs and functions from SQL queries. Keeps raw SQL (matching existing team expertise), adds compile-time column validation, and eliminates manual `.Scan()`. Each service gets a `sqlc.yaml` pointing to its schema path and a `queries/` directory containing annotated SQL files.

### golang-migrate

Version-controlled migration files (numbered UP/DOWN pairs). Provides auditable schema history and safe rollbacks. Replaces monolithic schema files with ordered migration sequences. Supports both CLI usage and library integration for programmatic migration at service startup.

### PostgreSQL RLS

Row-level security policies on all tenant-scoped tables. Each connection sets `app.current_tenant_id` via `SET LOCAL`, and RLS policies enforce `tenant_id = current_setting('app.current_tenant_id')`. This acts as a belt-and-suspenders safety net — even if application code omits the WHERE clause, the database itself blocks cross-tenant reads.

### Repository pattern

Consolidate all SQL for a given service module into dedicated query files with scanners co-located. This can be adopted incrementally before sqlc — it's a code organization improvement that makes the eventual sqlc transition easier.

## Impact / Dependencies

- All services with database access: api_billing, api_control, api_balancing, api_tenants, api_dns, api_consultant, and related packages.
- `pkg/database/` — connection setup needs RLS session variable injection.
- `pkg/database/sql/schema/` — monolithic files become the basis for initial migration sequences.
- CI pipeline — sqlc codegen verification step.
- YugabyteDB compatibility testing for RLS policies.

## Alternatives Considered

- **GORM**: Rejected. Too much implicit behavior, poor performance characteristics at scale, and conflicts with the team's raw SQL expertise.
- **ent**: Rejected. Its code-generation approach generates the schema from Go, which is the inverse of our SQL-first workflow.
- **sqlx**: Considered. Lighter than sqlc — provides named parameter binding and struct scanning — but still requires manual struct mapping and lacks compile-time query validation.

## Risks & Mitigations

- **sqlc learning curve and config overhead.** Mitigation: pilot with one service, document patterns, expand.
- **RLS adds connection-level state management.** Mitigation: centralize `SET LOCAL` in the database middleware layer (`pkg/database/`), not in individual services.
- **Initial migration generation from monolithic schema files requires careful ordering.** Mitigation: generate the baseline migration programmatically, validate against a fresh database.
- **YugabyteDB RLS compatibility is not guaranteed for all policy types.** Mitigation: test RLS policies against YugabyteDB in CI before rollout.

## Migration / Rollout

1. **Make versioned migrations the default for all new schema changes.** Generate or curate a baseline migration from existing schema files, then keep subsequent changes in numbered migration files.
2. **Repository pattern.** Consolidate inline SQL into per-service query files. No tooling change — pure code organization.
3. **sqlc for new queries.** Pilot in one service (api_billing or api_control). New queries use sqlc; existing queries remain unchanged.
4. **Backfill existing queries.** Convert existing inline SQL to sqlc service-by-service.
5. **RLS policies.** Add policies to tenant-scoped tables. Enable after validating that all connections correctly set `app.current_tenant_id`.

## Open Questions

- Should baseline migration files be generated automatically from existing schema files, or hand-written?
- Which service should pilot sqlc first? api_billing has the most queries; api_consultant has the most complex (pgvector).
- How does RLS interact with YugabyteDB's distributed transaction model?
- Should sqlc codegen run in CI as a verification step, or only locally?

## References, Sources & Evidence

- [Evidence] `pkg/database/postgres.go`
- [Evidence] `pkg/database/sql/schema/` (9 schema files)
- [Evidence] `pkg/database/sql/migrations/v0.3.0/001_bootstrap_tenant_aliases.sql`
- [Evidence] `api_billing/internal/grpc/server.go` (inline SQL + manual .Scan)
- [Evidence] `api_control/internal/grpc/server.go` (inline SQL + manual .Scan)
- [Evidence] `api_balancing/internal/grpc/server.go`
- [Evidence] `api_consultant/internal/knowledge/store.go` (pgvector usage)
- [Reference] sqlc documentation: https://docs.sqlc.dev
- [Reference] golang-migrate: https://github.com/golang-migrate/migrate
- [Reference] PostgreSQL RLS: https://www.postgresql.org/docs/current/ddl-rowsecurity.html
