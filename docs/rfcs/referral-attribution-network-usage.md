# RFC: Referral attribution + network usage reporting

## Summary
FrameWorks currently tracks per-tenant usage well but has no acquisition attribution for
how tenants/users arrive (marketing website, wallet login, x402, API, etc.). Operators need
to answer: “where do our users come from?” and “how much FrameWorks network usage (including
Livepeer processing) is attributable to each source.” This RFC proposes a data model and
end-to-end capture pipeline to make attribution and network-wide reporting available for
internal dashboards and exports.

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

## Proposed data model

### 1) Attribution payload (proto)
Add a shared `SignupAttribution` message and wire it through the control plane so that
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
In `quartermaster`, add:
- `tenant_attribution` keyed by `tenant_id` (1:1) with attribution columns
- `referral_codes` for partner/affiliate tracking (optional, low risk)

### 3) ClickHouse: acquisition events + network rollups
Add tables in `periscope.sql`:
- `tenant_acquisition_events` for raw acquisition events
- `network usage` is queried by aggregating existing daily rollups (tenant_viewer_daily, processing_daily, api_usage_daily)
- `acquisition cohort usage` is computed by joining tenant_acquisition_events with daily usage tables

## Data flow changes

Note: network-wide usage is computed via query-time aggregation of existing daily tables to avoid new rollup jobs during the first rollout.


### Gateway (API)
- Capture UTM parameters, `Referer`, and landing page from HTTP requests.
- Detect agent logins via user-agent.
- Pass attribution into `Register`, `WalletLogin`, and `WalletLoginWithX402`.

### Control plane (Commodore)
- Propagate attribution to `CreateTenant`.
- Emit `tenant_created` service events with attribution fields for analytics ingestion.

### Tenants (Quartermaster)
- Persist attribution in `tenant_attribution` on tenant creation.
- Increment `referral_codes.current_uses` when a referral code is applied.

### Analytics ingestion (Periscope)
- Extend service event handling to insert `tenant_created` into ClickHouse
  `tenant_acquisition_events`.

### Analytics query
- Provide query endpoints for:
  - `GetNetworkUsage`
  - `GetAcquisitionFunnel`
  - `GetAcquisitionCohortUsage`

### GraphQL / Operator UI
- Expose operator-only queries for network usage and acquisition funnel/cohort analytics.
- UI can be deferred; data should be usable via Grafana/Metabase.

## Reporting outputs
- Total FrameWorks network usage by period (daily/weekly/monthly).
- Attribution funnel by channel/method/utm/referral.
- Cohort usage correlation (e.g., “wallet-based signups generated X viewer hours”).
- Livepeer vs native processing rollups included in totals.

## Rollout plan (phased)
1. **Schema + proto**: add `SignupAttribution`, DB tables, ClickHouse tables.
2. **Capture**: gateway + control plane pass attribution for all signup paths.
3. **Ingest + rollups**: periscope ingest insertion; add rollups for network usage.
4. **Query**: periscope query endpoints + operator GraphQL.
5. **UI** (optional): internal dashboard or public stats endpoint.

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

