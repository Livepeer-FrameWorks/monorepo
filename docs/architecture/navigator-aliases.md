# Navigator Aliases - Tenant Alias & Custom Domain Intent Pipeline

How tenant-facing hostnames (`{tenant}.cdn.{root}` aliases and tenant-owned custom
domains) travel from Quartermaster's billing/tenant state to live DNS: durable intent
outboxes on the Quartermaster side, and an ACK-driven apply-state pipeline on the
Navigator side. TLS mechanics (issuance, bundles) are covered in `tls.md`; the DNS
reconciler and ACME internals live with Navigator's operator docs.

## Architecture

```
Quartermaster (intent authority)                Navigator (execution authority)
┌──────────────────────────────┐               ┌──────────────────────────────────┐
│ tenant/billing mutations      │   gRPC        │ tenant_aliases                   │
│  └─ enqueue (same tx):        │  Ensure/      │  cert_issuing → cert_issued      │
│     navigator_tenant_alias_   │  Remove       │  (ACME wildcard per alias)       │
│       outbox                  ├──────────────►│ custom-domain state machine      │
│     navigator_custom_domain_  │               │ tenant_edge_apply_state          │
│       outbox                  │               │  pending_distribute →            │
│ outbox drain workers          │               │  pending_apply → applied →       │
│ backstop reconciler (5m)      │               │  in_dns                          │
└──────────────────────────────┘               └───────▲──────────────┬───────────┘
                                                       │ ReportConfig │ AliasApplyStateWorker
                                                       │ SeedApply    │ publishes Bunny smart
                                                       │ Result       ▼ records in cdn.{root}
                             Foghorn ── ConfigSeed (tenant TLS bundles) ──► Helmsman ──► Caddy
                                 ▲◄──────────── apply ACK ────────────────────┘
```

## Service Responsibilities

| Service       | Role                                                                                                             | Data                                                                    |
| ------------- | ---------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------- |
| Quartermaster | Decides _whether_ a tenant should have an alias/custom domain; enqueues durable intent; runs the repair backstop | `navigator_tenant_alias_outbox`, `navigator_custom_domain_outbox`       |
| Navigator     | Executes intent: cert issuance, per-edge readiness tracking, DNS publish                                         | `tenant_aliases`, `tenant_alias_retirements`, `tenant_edge_apply_state` |
| Foghorn       | Distributes tenant TLS bundles to edges in ConfigSeeds; forwards Helmsman ACKs to Navigator                      | in-memory monotonic `seed_version` per node                             |
| Helmsman      | Applies bundles to Caddy; authority on which bundles are actually active                                         | on-edge Caddy config                                                    |

## Quartermaster side: intent outboxes

Every mutation that changes desired alias/custom-domain state enqueues a row **in the
same transaction** as the state change, so intent can never be lost between QM and
Navigator. Two tables, one shared worker pattern (`pkg/outbox`):

- `navigator_custom_domain_outbox` — actions `ensure` | `remove` per
  `(tenant_id, domain)`. Rows drain in `created_at` order, `FOR UPDATE SKIP LOCKED`
  with a 60s lease, so any QM replica can drain.
- `navigator_tenant_alias_outbox` — actions `ensure` | `retire` | `remove` |
  `remove_cluster`. Rows are ordered by a `BIGSERIAL seq` and the claim predicate
  keeps **at most one row per tenant in flight**, serializing per-tenant dispatch
  (seq order, not `created_at` — same-tx rows share a timestamp) while staying
  parallel across tenants. A rename enqueues `retire(old)` then `ensure(new)` as two
  rows in intended order.

Failure semantics: retry with backoff (2s base, 1h cap on the alias outbox;
lease-expiry pacing on the custom-domain outbox), and rows are **never
auto-completed on failure** — a poison alias row deliberately blocks its tenant's
queue, and both workers alert after 12 attempts ("Navigator reachability
degraded"). Dropping the row would silently drop intent.

The paid/active decision is made at enqueue time; the drain worker dispatches purely
from stored fields to Navigator's `EnsureTenantAlias` / `RemoveTenantAliasSubdomain`
/ `RemoveTenantAlias` / `RemoveTenantAliasCluster` / `EnsureCustomDomain` /
`RemoveCustomDomain` RPCs.

### Backstop reconciler

`runTenantAliasBackstop` (every 5 minutes) recomputes each tenant's intended alias
state — active on an alias-eligible monthly tier AND at least one active cluster
subscription, the same predicate the primary paths use — and compares it against
Navigator's applied state (`GetTenantAliasStatus`). Missing or drifted transitions
are enqueued into the same per-tenant-ordered outbox. It is a repair loop, not the
primary path; tenants with a pending outbox row are skipped so it never fights an
in-flight or operator-blocked queue.

## Navigator side: alias lifecycle

`navigator.tenant_aliases` is keyed by `tenant_id` with lifecycle
`cert_issuing → cert_issued` (or `cert_failed`, retried) and `tearing_down` for
removal. `ProcessPendingTenantAliases` (runs once per DNS-reconcile pass; default
60s via `NAVIGATOR_DNS_RECONCILE_INTERVAL_SECONDS`) runs ACME issuance for the
tenant's wildcard (`*.{subdomain}.cdn.{root}`) and flips status. Because
`tenant_aliases` overwrites `subdomain` in place on a rename, retired labels are
remembered in `tenant_alias_retirements` until their DNS records are cleared; a
stale retirement for a label that was re-pointed back (a → b → a) is detected via
`requested_at` and dropped without touching live records.

## Edge apply-state pipeline

DNS membership for a tenant's alias is derived from **acknowledged TLS bundle state
on each edge**, not from cert issuance alone:

1. **Distribute** — Foghorn composes each edge's `ConfigSeed` and appends one TLS
   bundle per paying tenant subscribed to that cluster (`bundle_id =
"tenant:{tenant_id}"`), pulled from Navigator (`GetTLSBundle`). Bundles still in
   `cert_issuing` are skipped and picked up next cycle. Each seed carries a
   monotonic per-node `seed_version` (in-memory; resets with Foghorn, which is fine
   because ACKs are tied to the live control stream).
2. **Apply + ACK** — Helmsman applies the bundles to Caddy and ACKs per bundle.
   Helmsman is the authority on what is actually active: it demotes per-file
   successes when the Caddy reload fails, so Foghorn forwards `applied_bundle_ids`
   at face value via `ReportConfigSeedApplyResult`. A partial failure still
   publishes the healthy bundles.
3. **Record** — Navigator upserts `tenant_edge_apply_state`
   (`pending_distribute | pending_apply | applied | in_dns` per
   `(tenant_id, node_id, bundle_id)`; `pending_distribute` is the schema-default
   state — no code path writes it as a transition), filtering ACKs to tenants actually active in
   the reporting cluster. Cluster/platform bundles are ignored here.
4. **Publish** — `AliasApplyStateWorker` (periodic, plus an immediate pass for each
   tenant affected by a new ACK) reconciles Bunny smart record sets in
   `cdn.{root}`: apex `{subdomain}` plus one label per alias-able service type. Only
   edges in `applied`/`in_dns` state whose cluster passes tenant-eligibility and
   control-cell-health checks are published; ineligible or address-less edges are
   downgraded out of `in_dns` so API readiness reflects DNS reality. Eligibility
   or health _lookup failures_ preserve current DNS rather than shrinking it.

Teardown clears the label's records first and deletes local state only after Bunny
accepts the removal.

## Key Files

- `api_tenants/internal/grpc/navigator_alias_outbox.go` - per-tenant-ordered alias outbox
- `api_tenants/internal/grpc/navigator_outbox.go` - custom-domain outbox
- `api_tenants/internal/grpc/navigator_alias_backstop.go` - 5m repair reconciler
- `api_dns/internal/logic/tenant_alias.go` - lifecycle worker + ACK recording
- `api_dns/internal/worker/tenant_alias_worker.go` - `AliasApplyStateWorker` (DNS publish, retirements)
- `api_dns/cmd/navigator/main.go` - `ReportConfigSeedApplyResult` and alias RPCs
- `api_balancing/internal/control/apply_state.go` - Foghorn ACK forwarding, seed versions
- `api_balancing/internal/control/server.go` (`fetchTenantBundles`) - ConfigSeed bundle composition
- `pkg/database/sql/schema/quartermaster.sql`, `pkg/database/sql/schema/navigator.sql` - outbox and state tables

## Gotchas

- The alias outbox intentionally blocks per tenant on a failing row. "Stuck alias"
  investigations start at `navigator_tenant_alias_outbox.last_error` for that
  tenant, not at Navigator.
- QM's backstop and the primary enqueue paths converge on the same eligibility
  predicate. If either drifts, the backstop will visibly enqueue repeated repairs —
  treat that as a bug signal, not noise.
- Privateer separately materializes per-tenant wildcard certs on Foghorn hosts for
  nginx SNI paths (see `tls.md`); that is independent of the Helmsman/Caddy bundle
  ACK path documented here.
