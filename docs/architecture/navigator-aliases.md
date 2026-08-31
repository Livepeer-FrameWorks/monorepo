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
| Foghorn       | Distributes tenant TLS bundles to edges in ConfigSeeds; forwards Helmsman ACKs to Navigator                      | durable monotonic `seed_version` and ACK outbox revision per node       |
| Helmsman      | Applies bundles to Caddy; authority on which bundles are actually active                                         | on-edge Caddy config                                                    |

## Quartermaster side: intent outboxes

Every mutation that changes desired alias/custom-domain state enqueues a row **in the
same transaction** as the state change, so intent can never be lost between QM and
Navigator. Two tables, one shared worker pattern (`pkg/outbox`):

- `navigator_custom_domain_outbox` — actions `ensure` | `remove` per
  `(tenant_id, domain)`. Rows drain in `created_at` order, `FOR UPDATE SKIP LOCKED`
  with a 60s lease, so any QM replica can drain.
- `navigator_tenant_alias_outbox` — actions `ensure` | `retire` | `remove` |
  `ensure_cluster` | `remove_cluster`. Rows are ordered by a `BIGSERIAL seq` and
  the claim predicate keeps **at most one committed row per tenant in flight**,
  serializing visible work by sequence while staying parallel across tenants.
  Navigator fences every cluster decision by that sequence and Quartermaster's
  backstop compares the complete desired/applied cluster sets, so concurrently
  committed rows that become visible in the opposite order still converge. A
  rename enqueues `retire(old)` then `ensure(new)` as two rows in intended order.

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
`requested_at` and dropped without touching live records. A rename demotes the
previous alias's edge-apply rows to `pending_distribute`. The retired label is
kept until the replacement certificate revision has been applied by an edge and
that exact revision is present in DNS.
Every rename, teardown, and reactivation advances `authority_version`. ACME
completion and lifecycle writes carry the generation they started under, so a
delayed completion cannot cross a same-label reactivation or an a → b → a ABA
transition. A database-backed issuance lease serializes each certificate or TLS
bundle order across Navigator replicas; the cache is rechecked after acquisition.

Custom-domain lifecycle uses the same authority rule. Re-ensuring a
`tearing_down` domain restores `pending_verification`; verification, issuance
metadata, certificate persistence, and final deletion are all fenced by the
current lifecycle row. A successful custom-domain order refreshes the tenant's
distributed multi-SAN bundle before the domain becomes `cert_issued`; a bundle
refresh failure leaves the domain retryable instead of publishing a false-ready
status. Final deletion removes the domain certificate and deletes
the tenant-scoped ACME account once no other custom domain in `verified`,
`cert_issuing`, `cert_issued`, or `cert_failed` still uses it; pending and
teardown rows cannot preserve an otherwise orphaned account.
Late issuance therefore either commits before teardown and is cleaned by it, or
loses authority and cannot recreate credentials afterward.

## Edge apply-state pipeline

DNS membership for a tenant's alias is derived from **acknowledged TLS bundle state
on each edge**, not from cert issuance alone:

1. **Distribute** — Foghorn composes each edge's `ConfigSeed` and appends one TLS
   bundle per paying tenant subscribed to that cluster (`bundle_id =
"tenant:{tenant_id}"`), pulled from Navigator (`GetTLSBundle`). During a rename,
   Navigator continues returning the last-good bundle and Foghorn renders its
   Caddy site addresses from that bundle's alias SAN pair plus its additional
   custom-domain SANs, never from the replacement alias intent. Each seed carries a
   monotonic per-node `seed_version`. Foghorn allocates and persists this version
   in its database, so a restart or replica handoff cannot move Navigator's ACK
   fence backwards.
2. **Apply + ACK** — Helmsman applies the bundles to Caddy and ACKs per bundle.
   Helmsman is the authority on what is actually active: it demotes per-file
   successes when the Caddy reload fails. Foghorn synchronously records the
   latest result per node in `config_seed_apply_ack_outbox`, then returns control
   of the media-local stream; a worker forwards `applied_bundle_ids` to Navigator
   via `ReportConfigSeedApplyResult`, including the opaque revision of each
   attempted bundle. Navigator accepts DNS eligibility only when that revision
   equals the currently published bundle. Navigator or Quartermaster outages retain
   the local obligation and retry it without reconnecting the edge. An RPC
   response with `Accepted=true` means every tenant result was durably classified
   as applied, stale, or missing-authority; all three are terminal for the outbox.
   A partial
   failure still publishes the healthy bundles. Foghorn retains one durable
   per-node projection even after delivery: exact replays remain idempotent,
   while a changed applied/failed bundle set advances the row's serialized
   revision. That per-node revision is also the delivery sequence; it does not
   depend on a database-global sequence whose allocation order can differ across
   Yugabyte sessions. Observation timestamps and diagnostic text do not advance
   the revision. The active lease prevents ordinary concurrent delivery;
   Navigator's persisted delivery sequence is the final fence if an RPC outlives
   its lease and arrives after a replacement for the same seed version. Invalid
   persisted bytes are quarantined in place and may be repaired by a later ACK at
   the same or a newer seed; transient delivery failures continue retrying.

   During a rolling upgrade, an older Helmsman omits bundle revisions. Navigator
   still records that ACK under the seed/delivery ordering fence. A failed apply
   may demote legacy state that has no sequenced successor, but an empty revision
   cannot grant membership or satisfy readiness. Rows already `in_dns` when the
   revision column is expanded are grandfathered only for continuity. A
   revision-bearing ACK is required for new membership or replacement readiness.
   At the same seed, a sequence-zero result cannot overwrite a sequenced
   delivery; a newer seed remains the rolling-upgrade generation boundary.

   Backup/restore and node re-homing must preserve this fence relationship. Restore
   Foghorn and Navigator from a mutually consistent point, or remove the node's old
   Navigator edge-apply rows before enrolling that stable `node_id` into a different
   Foghorn control cell. Copying/reusing a `node_id` while retaining the old
   Navigator rows is unsupported: the new cell's per-node seed/outbox counters may
   begin behind the retained fence, so Navigator correctly discards those ACKs as
   stale until a newer seed is established.

3. **Record** — Navigator upserts `tenant_edge_apply_state`. Apply ACKs may
   report only `applied` or `pending_apply`; `pending_distribute` and `in_dns`
   are Navigator-owned lifecycle states.
   (`pending_distribute | pending_apply | applied | in_dns` per
   `(tenant_id, node_id, bundle_id)`; `pending_distribute` is the schema-default
   state and is restored when alias authority changes). ACK rows are observations
   below Navigator's separate `tenant_alias_cluster_authority` projection; an ACK
   can never create authority. Quartermaster writes ordered
   `EnsureTenantAliasCluster` and `RemoveTenantAliasCluster` decisions through its
   per-tenant durable outbox, and Navigator retains revocation tombstones until a
   later outbox sequence grants the pair again. A tombstone requires the alias
   intent row to exist (the authority table is FK-anchored to it); a revocation
   delivered while the alias row is absent is a durable no-op that the backstop
   re-drives once the alias exists. The repair loop compares complete
   desired/applied cluster sets, so a missed handoff or late ACK converges without
   a live read on the media path. During rolling upgrade only, a never-seen pair
   may use Quartermaster for first admission and persist a sequence-zero grant; it
   cannot cross any revocation. If that first-admission lookup is unavailable,
   Navigator records locally authorized and non-tenant results before returning
   `Unavailable`; Foghorn retains the delivery so only unresolved pairs remain
   pending on replay. A shorter live list cannot revoke established authority.
   An older seed version is always rejected. For an equal seed version, an ACK
   with a delivery sequence is accepted only when that sequence
   advances the persisted fence; sequence zero retains the legacy transition
   rules during a rolling upgrade, but cannot overwrite a row already fenced by
   a sequenced delivery or lower that fence at a newer seed. A successful replay
   for the current version advances its sequence fence while preserving an
   already-published `in_dns` row; a failed reapply of that same version
   demotes it immediately because the edge is no longer known to terminate TLS.
   A later successful reapply at that version moves `pending_apply` back to
   `applied`, allowing the DNS worker to publish the recovered edge again; it
   still cannot overwrite an existing `in_dns` row as a mere replay.
   Cluster/platform bundles are ignored here. `stale` covers any ACK that cannot
   advance beneath the current alias, bundle revision, and delivery fence;
   `revoked` means the tenant/cluster authority carries a revocation tombstone
   (so a revocation racing an ACK is distinguishable from an ordinary replay);
   `missing_parent` means the alias intent itself has disappeared. None of the
   three triggers a DNS publication
   pass. The insert arm locks the matching tenant
   alias intent `FOR KEY SHARE` and the matching active cluster-authority row
   `FOR SHARE`; an ACK racing teardown or revocation either lands before the
   removal and is cleaned by it (the teardown cascade, or the tombstone-fenced
   delete), or resumes afterward as `missing_parent`/`revoked`. Revocation runs as two statements in one retryable
   transaction: the tombstone first creates or locks the authority row, then a
   tombstone-fenced edge delete removes residual readiness. On PostgreSQL the
   delete takes a fresh statement snapshot and sees an ACK that committed while
   the tombstone was waiting; on Yugabyte a serialization conflict restarts and
   replays the whole transaction with a fresh read time. The
   `FOR KEY SHARE` alias lock serializes ACKs against the final alias delete,
   not against the `tearing_down` status transition — publication and
   readiness independently re-check alias status. Publisher and readiness
   reads join active cluster authority, and the `MarkTenantEdgeInDNS`
   promotion writer enforces it again itself, so a corrupt residual row
   beneath a tombstone can neither stay published nor be promoted.
   `tenant_edge_apply_state.tenant_id` also has an `ON DELETE CASCADE`
   foreign key to the alias intent, so an ACK cannot recreate or retain orphaned
   edge-apply state.
4. **Publish** — `AliasApplyStateWorker` (periodic, plus an immediate pass for each
   tenant affected by a new ACK) reconciles Bunny smart record sets in
   `cdn.{root}`: apex `{subdomain}` plus one label per alias-able service type.
   New membership requires an edge whose non-empty bundle revision equals the
   current Navigator bundle. A versionless row already `in_dns` is retained only
   for mixed-version continuity — bounded at 30 days of row age, after which it
   is demoted like any stale row — and does not satisfy readiness. Non-empty stale
   revisions, unhealthy control cells, and address-less edges are downgraded so
   durable state reflects DNS reality. Tenant cluster authority is the explicit
   ordered Quartermaster → Navigator grant/revocation stream; the publisher does
   not reinterpret a shorter live tenant list as revocation. Health lookup failures
   preserve current DNS rather than shrinking it. The
   worker owns only the DNS-membership transition: after external reconciliation
   it compare-and-sets `state`/`in_dns_at` against the exact seed and delivery
   sequence it observed. If an ACK advances the row while Bunny work is in flight,
   the write affects zero rows and the next pass re-reads it; the worker never
   replays ACK-owned fields. `in_dns_at` records the first successful entry for
   that published snapshot rather than being refreshed on every worker tick.

Teardown clears the label's records first and deletes the alias intent only after
Bunny accepts the removal. The final database statement proceeds only while the
row is still `tearing_down`; a concurrent same-label reactivation restores
`cert_issuing` and fences the stale teardown. The statement atomically cascades
all per-edge apply state, removes the `tenant:{id}` TLS bundle, and removes tenant
certificate rows not backed by an active custom-domain intent. Renewal and
`tenant:` bundle persistence independently require live alias authority. Serving
reads require an alias authority row and preserve its last-good bundle during
`cert_issuing`, `cert_failed`, and the short `tearing_down` interval. Teardown
clears DNS before one transaction deletes both the authority and bundle. A shorter
successful Quartermaster list does not immediately delete still-valid tenant
credentials from an edge: Foghorn retains a tenant bundle omitted by the
Quartermaster list from its durable ConfigSeed only until the certificate's
`expires_at`. An error-free Navigator `GetTLSBundle(found=false)` is different:
it is carried as an authoritative removal and deletes the durable edge bundle on
the next seed. A non-empty response error or malformed empty response is a failed
authority read and preserves valid local material. Explicit cluster
removal has already withdrawn DNS membership, while a transient list gap leaves
the media path operating on valid local material. Serving and issuance have
separate call sites but currently use the same authority-gated bundle read; the
issuance path still uses the stored issuer for CA pinning. Saving a replacement
bundle publishes its opaque revision and included custom-domain certificate
metadata in one database statement. Replacement issuance also publishes
`cert_issued` there; first alias issuance performs its fenced status transition
immediately afterward. A failed or delayed write cannot recreate
retired credentials. Foghorn's refresh fingerprint includes the opaque revision,
so a revision-only migration or repair still reaches Helmsman and produces an
exact-version ACK.
Custom-domain credential retention and tenant-bundle SAN participation are
separate policies: `cert_failed` retains retry authority and its individual
certificate, but is excluded from tenant-wide bundle orders so one broken domain
cannot block renewal of the alias and every healthy custom SAN. Renewal re-derives
this SAN set instead of replaying the stored bundle, and a transition to
`cert_failed` immediately rebuilds the aggregate bundle without that domain. If
the rebuild fails, every `cert_failed` pass retries it before re-verifying and
re-admitting the domain. Immediately
before publishing newly issued material, Navigator renews the exact durable
issuance lease owner; an expired or replaced owner cannot publish even if its
external ACME call eventually succeeds. The renewal worker reclaims expired lease
rows.
ACME completion is fenced by the label and authority generation, so work started
for an old intent cannot overwrite a rename, reactivation, or downgrade decision.

The v0.3 upgrade installs this foreign key as `NOT VALID` during expand, which
immediately fences new orphans while allowing old rows to remain online. Separate
automatic postdeploy migrations remove historical edge-state and credential
orphans and then validate the constraint; cleanup and validation remain distinct
ledger transactions for YugabyteDB. A fresh baseline already contains the
validated constraint, so expand leaves it intact. PostgreSQL and YugabyteDB
contract tests exercise ACK/delete and reactivation/delete interleavings;
correctness does not depend on pinning one global transaction-isolation setting.

## Key Files

- `api_tenants/internal/grpc/navigator_alias_outbox.go` - per-tenant-ordered alias and cluster-authority outbox
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

## Known limitations (open — not yet ruled)

Behaviors reviewed and left in place pending an explicit decision; each is
bounded and self-healing, listed here so audits stop rediscovering them.

- **Rolling-upgrade DNS continuity is age-bounded, not ACK-bounded.** A
  versionless pre-upgrade `in_dns` row rides as a continuity member until a
  revision-bearing ACK replaces it or the 30-day age bound demotes it
  (`legacyContinuityMaxAge`); `navigator_dns_legacy_continuity_rides_total`
  makes a stalled rollout visible.
- **List-gap credential retention runs to certificate expiry.** When
  Quartermaster's tenant list omits a tenant without an authoritative
  Navigator removal, Foghorn retains the last-good bundle in the durable seed
  until `expires_at` (up to the certificate lifetime). DNS membership is
  withdrawn first, so this preserves outage tolerance, not traffic authority.
- **`stale` vs `deduplicated` outbox classification is advisory.** Foghorn's
  enqueue label comes from a second unsynchronized read; a concurrent
  replacement can mislabel the counter. Persistence and delivery are
  unaffected.
