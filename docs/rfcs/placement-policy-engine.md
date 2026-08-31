# RFC: Placement Policy Engine (next-release storage tiering & placement)

## Status

Proposed — deferred; NOT part of the current storage-artifact-catalog release. This RFC specifies the
next-release storage-tiering and placement design.

## TL;DR

- Generalize today's single implicit durable copy (one S3 object per artifact) into a **backend-keyed
  placement ledger** (`artifact_locations`) over a **storage-backend registry** (`cluster_storage_backends`).
- Introduce a **tier ladder** — cold-cold (HDD) → cold (S3) → hot (edge disk) → hot-hot (memory) — with
  per-edge/per-tier occupancy emitted by the infra layer (Helmsman retrieval/relay + cleanup).
- Add a **placement policy engine**: a two-sided (cluster-owner + tenant) rule model with selectors,
  verbs, effects, and precedence, distributed as a **signed enforcement bundle**, evaluated at
  serving / ingest / processing / storage / peer hook-ins.
- Keep the current-release invariants intact: node-copy telemetry stays separate from durable
  placement; **cache/partial state never counts toward durability and the durability ledger never
  reads cache state**.
- Resolution ownership is settled (§7): each domain owner compiles its own bundle half
  (Quartermaster: infra/capacity; Commodore: stream/tenant, plus assembly + signing), Foghorn
  resolves at enact time, and Phase 2 moves enforcement edge-local (Helmsman verifier).

## Current State

The shipped model records one durable location per artifact — an S3 object attributed to the artifact's
`storageClusterId`, gated on its sync/frozen lifecycle — plus N transient **local node copies**
(producer/origin or synced cache), observed from `foghorn.artifact_nodes` and projected to analytics
as `ArtifactNodeCopyEvent`. The asset UI composes "one durable S3 location + N local node copies." That
single backend is no longer implicit: it is now a **typed, immutable per-cell descriptor** — one Foghorn
database bound to one immutable S3 backend, its physical identity frozen as a deterministic `backend_id`
fingerprint over `(kind, bucket, endpoint, region, prefix)` and enforced fail-closed at boot and cleanup
(`docs/architecture/durable-media-storage.md`; established by Quartermaster desired-state bootstrap). What still does
not exist is a registry of _multiple durable backends_,
_tiers_, or _where copies should live_ — the descriptor names one backend per cell, not a set, and this
RFC's `cluster_storage_backends` / `artifact_locations` registry and tier ladder generalize it.
Read-through relay block caches (`<asset>.blocks/`) are deliberately excluded from the node-copy index (a
partial cache must never advertise a complete copy — a routing-correctness hazard).

### Seams already in place for storage-less / self-hosted clusters

Thumbnails are the case that constrains the placement design: they are S3+Chandler derivatives, and a
self-hosted cluster may have **neither S3 nor local object storage**. One seam already exists that keeps
that open:

- **Serving cell is decoupled from the storage cell.** `thumbnail_serving_cluster_id` is recorded
  independently of the byte-storage cluster, and the served key `thumbnails/{id}/{file}` is cluster-agnostic
  (the cluster lives in the Chandler hostname, not the key). This is what will let a thumbnail be served from
  a cell that is not where the primary media tier lives — the mechanism a storage-less cluster would use to
  serve its thumbnails from an official rated-S3 cell. It is the decoupling only; end-to-end serving needs the
  federated mint below.

The placement work must ADD (deliberately unbuilt; the fail-closed extension points already exist in code):

- the **federated thumbnail mint** — generate on a storage-less cell, promote to a remote official-S3 cell
  (the destination-side attest/promote/settle path, `StorageMintViaFederation`); and
- relaxing the cell-topology rule from "a Foghorn needs an **in-cell** Chandler" to "a Foghorn needs a
  **reachable** thumbnail-serving cell (in-cell, or a designated official one)", so a storage-less self-hosted
  Foghorn is not rejected outright.

## Problem / Motivation

Operators and tenants need: durability across more than one backend/region; cheaper cold tiers
(HDD/S3) with promotion to hot edge/memory on demand; residency/sovereignty guarantees (BYOC, data
stays in-region); and minimum-durable-copy policies. None of these are expressible today — placement
is implicit and singular.

## Goals

- A durable, backend-keyed record of whole durable objects per artifact (`artifact_locations`).
- A backend registry describing each backend's kind, region, cost, and capabilities.
- Per-edge/per-tier occupancy signal (including `.blocks` partial caches) with bounded churn.
- A policy engine that resolves _where copies must live_ and drives create/evict/migrate actions.
- Two-sided consent: a placement action requires both the capacity-owning cluster's and the tenant's
  policies to permit it.

## Non-Goals

- Implementing any of the above in the current release.
- Merging node-copy (cache/local) telemetry into the durability ledger.
- Counting partial/cache holdings toward durability.

## Proposal

**1. Storage-backend registry (`cluster_storage_backends`).** Rows describe each durable backend — a
backend that holds whole, verifiable durable objects: id, owning cluster, kind (`s3` | `hdd` | …),
region, cost class, and capability flags (writable, residency zone, tier floor). Quartermaster owns
the **declared** configuration; Foghorn/infra report **observed** state — the two are reconciled,
never conflated. Memory and partial/edge caches (`.blocks`) are NOT durable backends and never appear
here or in the `artifact_locations` ledger — they are modeled solely as transient tier/occupancy
state in §3.

**2. Placement ledger (`artifact_locations`).** One row per (artifact, backend) durable object:
backend id, tier, size, checksum, created/verified timestamps, and a source-owned monotonic version
for deterministic convergence. This replaces the singleton durable location; the current `s3_url` /
`storageClusterId` become one row in this ledger.

**3. Tier ladder + occupancy.** cold-cold (HDD) → cold (S3) → hot (edge disk) → hot-hot (memory). The
infra layer (Helmsman retrieval/relay + cleanup) emits tier-transition / occupancy events —
including `.blocks` partial-cache occupancy — into a durable sink (event log and/or current-state
table), owned by the media/analytics plane, NOT the durability ledger.

**4. Policy model.** A rule = {selector, **verb**, **effect**, precedence}.

- **Selector** matches artifacts/tenants/backends by kind, region, tier, and tags.
- **Verb** names the placement DOMAIN the rule governs — one of `ingest | serve | process | store |
replicate | peer`. (These are the six points where a placement decision is actually made; they map
  1:1 to the enforcement hook-ins in §6.)
- **Effect** is the constraint applied in that domain — one of `require | allow | deny | prefer`
  (e.g. `store require region=eu`, `serve deny tier=cold-cold`, `replicate prefer cost=low`,
  `peer allow residency=origin`). Concrete resolved outputs are minimum durable copies, allowed
  regions, residency, and tier floor/ceiling.
- **Precedence is intersection-only.** The resolved constraint is the INTERSECTION of every matching
  rule's effect across owner-policy and tenant-policy — a `deny` from either side always wins, an
  `allow` only widens within what both sides already permit, and `require` floors are unioned
  (the strictest floor holds). There is no "override" that re-permits what another layer denied; the
  safe default when the intersection is empty is retain-don't-delete. Resolution runs against declared
  config (Quartermaster) + observed placement (Foghorn).

**5. Two-sided consent + signed enforcement bundle.** A placement action is permitted only when BOTH
the capacity-owner's policy and the tenant's policy allow it (the intersection rule above). Consent is
not a new subsystem: it EXTENDS the existing `quartermaster.tenant_cluster_access` grant (already the
tenant↔cluster entitlement Commodore reads for the v1 policy bundle) with placement scopes (which
verbs/tiers/regions a tenant may use on that cluster). The resolved policy compiles into the existing
**signed policy bundle** (Commodore `policy_bundle_versions`, with revocations enqueued as
`bundle_revoke`-reason rows in `playback_policy_invalidation_outbox`, schema v2) —
placement rules become additional bundle sections, distributed and revoked through the same signed,
versioned channel; each enforcer verifies the signature and bundle version before acting.

**6. Enforcement hook-ins (one per verb).** Each verb binds to a real decision point:

- `serve` — Foghorn's viewer-routing decision. The four current routing actions
  (local-edge / origin-pull / cross-cluster redirect / reject; see `docs/architecture/viewer-routing.md`)
  become policy-gated: route only to permitted tiers/regions.
- `ingest` — place the initial durable copy per policy at capture.
- `process` — processing outputs land on policy-permitted backends.
- `store` — storage/cleanup evicts or migrates to satisfy min-copy / residency (never removing the
  last complete source — the current-release `local_missing` rule generalized).
- `replicate` — cache-fill / origin-pull replication honors tier and cost preferences.
- `peer` — a federated peer honors the ORIGIN tenant's residency; **sovereign / BYOC local-owner
  policy is a first-class selector** (an origin cluster can forbid its bytes leaving its region even
  when a peer would otherwise serve them).

Decision INPUTS at `serve` and `process`: besides declared config and observed placement, the
resolver consumes **predicted workload cost** (per-job CPU/GPU/VRAM/bandwidth estimates;
`docs/rfcs/workload-cost-model.md`) and **NAT-type classification** (per-node OPEN → IMPENETRABLE;
`docs/rfcs/nat-traversal.md`). These are scoring/eligibility signals feeding the decision, not new
verbs — a `serve` choice can prefer OPEN edges for WebRTC viewers, a `process` choice can require a
node whose predicted headroom fits the job.

**7. Policy resolution & ownership (adopted: phased, each owner compiles its own half).** The former
open question — resolution in Quartermaster vs Foghorn — is answered by splitting compilation along
domain-ownership lines and keeping resolution at the enactor:

- **Phase 1 — owner-compiled halves, central enactment.**
  - **Quartermaster** compiles the infra/capacity section (backends, cluster capabilities,
    tenant↔cluster entitlement) and serves it via an owner RPC. The seed of that RPC has landed:
    the narrow **`GetTenantEntitlement`** RPC (`pkg/proto/quartermaster.proto:137`, served by
    api_tenants) already replaced Commodore's direct SQL read of `quartermaster.tenant_cluster_access`
    / `tenants` / `infrastructure_clusters` in the bundle-mint path — Commodore now reads the
    infra-owned entitlement through `qmEntitlements.GetTenantEntitlement`, fail-closed
    (`api_control/internal/grpc/policy_bundle.go:207`). The placement section evolves from that RPC
    rather than adding a parallel one.
  - **Commodore** compiles the stream/tenant side (from `stream_cluster_pins` + `playback_policy`)
    and stays the bundle **assembler and signer** — it owns `policy_bundle_versions` and the
    invalidation outbox, so signing and revocation remain a single authority.
  - **Foghorn** RESOLVES at enact time: it is the only holder of observed placement
    (StreamRegistry.Locations) and hosts all six verb hook-ins, so the intersection of the two
    compiled halves against observed state happens where the decision is enacted.
- **Phase 2 — edge-local enforcement.** Enforcement moves to a **Helmsman bundle verifier** at the
  `store` / `serve` lease decision points, evaluating the same signed bundle locally. This is a
  design constraint on the bundle schema NOW — sections must be self-contained and verifiable
  without a callback to the control plane — with the verifier itself implemented later.

## Owning services / modules

- **Quartermaster** — declared configuration: storage-backend registry, cluster capabilities,
  tenant↔cluster placement grants; compiles and serves the infra/capacity bundle section (§7).
- **Commodore** — stream/tenant policy source (`stream_cluster_pins`, `playback_policy`); bundle
  assembly, signing (`policy_bundle_versions`), and revocation via the invalidation outbox.
- **Foghorn** — observed placement (`artifact_locations` ledger, StreamRegistry.Locations); policy
  resolution at enact time; hosts all six verb hook-ins.
- **Helmsman** — tier transitions and `.blocks` occupancy emission; Phase-2 edge-local bundle
  verifier at store/serve lease points.
- **Bridge** — policy/placement API and visualization surface.
- **Purser** — per-backend cost and settlement.

## Impact / Dependencies

- **Prerequisite from the storage-artifact-catalog beta — SHIPPED.** Co-locating two clusters in one bucket under
  different prefixes requires the mint-ROUTING tuple to carry `prefix`, not just the immutable-identity fingerprint
  (`BackendFingerprint`, which already included prefix and compared it exactly). That threading has landed: `prefix` is
  now a field of `storage.S3Backing`, and its `Normalize`/`Equal` compare it BYTE-FOR-BYTE
  (`api_balancing/internal/storage/cluster_resolver.go:22,44`); the `TenantClusterPeer` proto carries `s3_prefix` plus an
  `s3_prefix_present` flag that fails mint routing closed on an incomplete (NULL) prefix rather than collapsing it to
  empty (`pkg/proto/cluster_peer.proto:20-21`); and the local-backing constructors thread it through. A Foghorn
  configured for one prefix therefore no longer classifies a cluster addressing another prefix as locally mintable. The
  shared-bucket / different-prefix topology this engine assumes is now routable at the resolver layer; what remains
  unbuilt is the placement policy that would exercise it.
- **Hard dependency:** [`cross-cluster-durable-replication-v1.md`](cross-cluster-durable-replication-v1.md).
  This engine SELECTS AMONG destinations that protocol authorizes, mints, verifies, attests, and attributes;
  it must not be built before that single-destination contract exists (multi-copy placement generalizes it).
- **Schemas:** new Postgres tables (`cluster_storage_backends`, `artifact_locations`) + a source-owned
  version sequence; new ClickHouse per-edge/per-tier occupancy table(s).
- **Services:** see "Owning services / modules" above; the Quartermaster owner RPC that the Phase-1
  split in §7 stands on (`GetTenantEntitlement`) has landed, so the placement section extends an
  existing entitlement RPC rather than waiting on one to be introduced.
- **Proto:** placement events + policy bundle messages.

## Alternatives Considered

- **Extend the single `s3_url` with a JSON list of URLs** — rejected: no backend identity, no tiers,
  no policy, no per-copy verification/version.
- **Reuse node-copy telemetry as the durability ledger** — rejected: node copies are transient
  cache/local holdings; conflating them with durability breaks the core invariant.
- **Ingest-time (non-source) ordering for placement convergence** — rejected: not replay-safe; use a
  source-owned monotonic version like the artifact node-copy key-scoped counter.

## Risks & Mitigations

- **High-cardinality per-edge/per-tier churn** → bound retention, aggregate, and rate-limit emission.
- **Policy misconfiguration causing data loss** → min-copy is a hard floor; evict/migrate actions are
  gated on a verified surviving copy (mirror the current-release `local_missing` rule: never remove
  the last complete source).
- **Bundle spoofing** → signed bundles, verified at every enforcer.
- **Two-sided-consent deadlock** → explicit conflict precedence + a safe default (retain, don't
  delete).

## Migration / Rollout

Additive and phased, layered under the current single-copy model without changing it until consumers
opt in: (1) backend registry + `artifact_locations` + emission (backfill the existing S3 object as one
ledger row); (2) tier occupancy signal + storage; (3) policy model + signed bundle + enforcement
hook-ins; (4) API + viz. Each phase ships behind its own flag.

## Open Questions

- Event vs current-state (or both) for the per-edge/per-tier signal, and which service owns the sink.
- How bundles are versioned/rotated across the two compiled halves (resolution ownership itself is
  settled — see §7).
- Churn/retention budget for the high-cardinality occupancy signal.
- BYOC key custody for signed bundles.

## References, Sources & Evidence

- [Reference] `docs/architecture/analytics-pipeline.md` — "Artifact node-copy telemetry": the
  current-release scope boundary and the cache-vs-durability invariant this RFC preserves.
- [Reference] `pkg/database/sql/schema/foghorn.sql` — current single-copy storage lifecycle
  (`storage_location`, `s3_url`, `sync_status`, `frozen_at`, `storage_cluster_id`) that
  `artifact_locations` generalizes.
- [Reference] `foghorn.artifact_node_copy_version_counter` — the source-owned monotonic version pattern to
  reuse for placement convergence.
