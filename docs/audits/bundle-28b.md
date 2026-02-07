# Bundle 28B Audit

## Scope

Audit referral attribution capture (register + wallet-login) through persistence and reporting; validate tenant/PII boundaries; review network usage reporting for late/out-of-order events and pagination boundaries; reconcile Skipper token accounting with billing semantics.

## Findings (summary)

1. Email/password registration drops attribution before tenant creation (UTM/referral not persisted).
2. Acquisition reporting is not tenant-scoped and depends on global access control.
3. Billing cursoring does not account for late/out-of-order usage events.
4. Skipper token counts are stored as API complexity, blending units with GraphQL complexity.
5. Landing-page/referrer capture stores full URLs, risking PII in query strings.

## Findings (details)

### 1) Registration attribution lost before tenant creation

**Evidence**

- Gateway collects attribution for registration and sends it to Commodore.【F:api_gateway/internal/handlers/auth.go†L242-L314】
- Commodore Register creates a tenant without forwarding attribution to Quartermaster.【F:api_control/internal/grpc/server.go†L1629-L1639】
- Quartermaster only persists attribution when CreateTenant includes it.【F:api_tenants/internal/grpc/server.go†L935-L964】
- Analytics ingestion uses tenant_created attribution to populate ClickHouse acquisition events.【F:api_analytics_ingest/internal/handlers/handlers.go†L3034-L3082】

**Risk**

- Email/password signups are under-attributed; referral usage counts remain stale.

### 2) Acquisition reporting lacks tenant scoping

**Evidence**

- Acquisition funnel query reads `tenant_acquisition_events` without tenant filters.【F:api_analytics_query/internal/grpc/server.go†L5796-L5822】

**Risk**

- If a tenant-scoped JWT can call this endpoint, it exposes global acquisition metrics.

### 3) Billing cursor skips late/out-of-order events

**Evidence**

- Billing cursor advances to `targetEnd` without lookback/reconciliation logic.【F:api_analytics_query/internal/handlers/billing.go†L534-L610】

**Risk**

- Late ClickHouse events can be missed, under-billing usage.

### 4) Skipper token accounting mixed with API complexity

**Evidence**

- Skipper logs tokens as `TotalComplexity` and emits them as API usage aggregates.【F:api_consultant/internal/skipper/decklog_adapter.go†L21-L36】
- Skipper usage summary maps tokens into `APIComplexity`.【F:api_consultant/internal/metering/tracker.go†L267-L317】
- ClickHouse schema treats `total_complexity` as a generic API complexity metric.【F:pkg/database/sql/clickhouse/periscope.sql†L1183-L1209】
- Billing usage details store `api_complexity` from summaries.【F:api_billing/internal/handlers/jobs.go†L381-L408】

**Risk**

- Tokens and GraphQL complexity share a metric stream, making pricing drift likely if either becomes billable.

### 5) PII risk from full URL capture

**Evidence**

- Attribution capture stores full landing URL and referrer URL.【F:api_gateway/internal/attribution/attribution.go†L24-L64】
- Quartermaster persists landing/referrer URLs to Postgres and emits them in tenant events.【F:api_tenants/internal/grpc/server.go†L935-L955】

**Risk**

- Query strings can contain user identifiers or secrets and flow into analytics storage.

## Fix plan

1. Propagate registration attribution by forwarding `RegisterRequest.Attribution` to Quartermaster CreateTenant.
2. Sanitize landing page and referrer URLs before persistence (strip query/fragment, drop relative URLs).
3. Gate acquisition reporting to service tokens or add explicit tenant scoping.
4. Add a configurable lookback window for billing summarization to include late events.
5. Separate Skipper tokens from `api_complexity` (dedicated usage fields or usage type).

## Missing tests

- Registration attribution propagation integration test (Gateway → Commodore → Quartermaster).
- Acquisition reporting access-control tests (tenant JWT denied; service token allowed).
- Billing cursor lookback/reconciliation test for late events.
- Skipper token accounting tests to keep tokens distinct from GraphQL complexity.
