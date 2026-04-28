# RFC: Referral attribution + network usage reporting

## Summary

FrameWorks now has the core acquisition-attribution and network-usage plumbing, but it is
not yet exposed through operator GraphQL/UI. Gateway captures attribution for wallet login
paths, Quartermaster persists tenant attribution/referral code usage, Periscope Ingest writes
tenant acquisition events, and Periscope Query has network/acquisition query RPCs. Remaining
work is mainly coverage for all signup paths, operator-facing GraphQL/UI, exports, and
operational validation.

## Goals

- Capture acquisition attribution for **all** signup paths (email/password, wallet browser,
  wallet agent, x402, API).
- Correlate attribution cohorts with usage (viewer hours, egress, processing) to prove
  network-wide impact.
- Provide operator-facing queries for reporting and export, with an optional public stats
  endpoint later.

## Non-goals

- No changes to customer-facing billing UI in this iteration.
- No public stats endpoint in the initial rollout (optional phase 2).

## Current implementation status

Implemented:

- `SignupAttribution` exists in `pkg/proto/common.proto`.
- Quartermaster has `tenant_attribution` and `referral_codes` tables.
- Gateway captures UTM/referrer/landing/referral fields for wallet login paths.
- Quartermaster persists attribution on tenant creation and increments referral-code usage.
- ClickHouse has `tenant_acquisition_events`.
- Periscope Ingest writes `tenant_created` service events into `tenant_acquisition_events`.
- Periscope Query implements `GetNetworkUsage`, `GetAcquisitionFunnel`, and `GetAcquisitionCohortUsage`.

Still open:

- Verify coverage for email/password, x402, and API-created tenants.
- Add operator-only GraphQL and UI/export surfaces.
- Document report date ranges and backfill limitations.

## Proposed data model

### 1) Attribution payload (proto)

`SignupAttribution` exists and should be wired through every tenant creation path so that
registration, wallet login, and x402 provisioning can pass attribution to tenant creation
and event emitters.

Proposed fields:

- `signup_channel`: `web`, `wallet`, `x402`, `api`
- `signup_method`: `email_password`, `wallet_ethereum`, `wallet_base`, `x402_usdc`
- `utm_*`: source, medium, campaign, content, term
- `http_referer`
- `landing_page`
- `referral_code`
- `is_agent` (user-agent inferred)
- `metadata_json` (overflow: wallet chain, device hints, etc.)

### 2) Postgres: tenant attribution + referral codes

In `quartermaster`, the core tables already exist:

- `tenant_attribution` keyed by `tenant_id` (1:1) with attribution columns
- `referral_codes` for partner/affiliate tracking (optional, low risk)

### 3) ClickHouse: acquisition events + network rollups

ClickHouse support exists for:

- `tenant_acquisition_events` for raw acquisition events.
- `network usage` queried by aggregating existing daily rollups (`tenant_viewer_daily`, `processing_daily`, `api_usage_daily`).
- `acquisition cohort usage` computed by joining `tenant_acquisition_events` with daily usage tables.

## Data flow changes

Note: network-wide usage is computed via query-time aggregation of existing daily tables to avoid new rollup jobs during the first rollout.

### Gateway (API)

- Capture UTM parameters, `Referer`, and landing page from HTTP requests. This exists for wallet login paths; remaining signup paths need verification.
- Detect agent logins via user-agent.
- Pass attribution into `Register`, `WalletLogin`, and `WalletLoginWithX402`.

### Control plane (Commodore)

- Propagate attribution to `CreateTenant`.
- Emit `tenant_created` service events with attribution fields for analytics ingestion.

### Tenants (Quartermaster)

- Persist attribution in `tenant_attribution` on tenant creation.
- Increment `referral_codes.current_uses` when a referral code is applied.

### Analytics ingestion (Periscope)

- Service event handling inserts `tenant_created` into ClickHouse `tenant_acquisition_events`.

### Analytics query

- Query endpoints exist for:
  - `GetNetworkUsage`
  - `GetAcquisitionFunnel`
  - `GetAcquisitionCohortUsage`

### GraphQL / Operator UI

- Expose operator-only queries for network usage and acquisition funnel/cohort analytics. This remains open at the GraphQL/UI layer.
- UI can be deferred; data should be usable via Grafana/Metabase.

## Reporting outputs

- Total FrameWorks network usage by period (daily/weekly/monthly).
- Attribution funnel by channel/method/utm/referral.
- Cohort usage correlation (e.g., “wallet-based signups generated X viewer hours”).
- Livepeer vs native processing rollups included in totals.

## Rollout plan (phased)

1. **Schema + proto**: done for `SignupAttribution`, Quartermaster tables, and ClickHouse `tenant_acquisition_events`.
2. **Capture**: partially done; verify and fill all signup paths.
3. **Ingest + rollups**: Periscope ingest/query support is present.
4. **Query**: Periscope RPCs exist; operator GraphQL remains open.
5. **UI** (optional): internal dashboard or public stats endpoint remains open.

## Risks and mitigations

- **Low data quality from missing UTM params**: store empty values; rely on `referer` and
  landing page to fill gaps.
- **Privacy concerns**: limit to coarse attribution (UTM + referrer), avoid PII in metadata.
- **Backfill**: historical acquisition attribution won’t exist; initial reports should
  call out a “data from <start date>” disclaimer.

## Success metrics

- All new tenant creations store attribution rows.
- ClickHouse acquisition events reflect tenant creation events with attribution.
- Operator can query network-wide usage and attribute it to channels/cohorts.
