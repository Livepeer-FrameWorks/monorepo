# RFC: Stream Replication Orchestration

## Status

Draft

## TL;DR

- All stream replication today is on-demand: a viewer (or a MistServer source request) triggers a
  pull; nothing replicates ahead of demand, and producers are served straight from whichever edge
  received their push.
- Propose the proactive half: **pre-emptive spread** (push a stream to a cell/region before the
  audience arrives), an **explicit hop topology** (origin/hub/edge roles per stream, multi-hop
  distribution between regions), and a **per-stream replication policy**
  (`max_replicas_total`, `max_replicas_per_region`, `allowed_regions`).
- Policy intent is homed in Commodore (the stream owner — `stream_cluster_pins` is the seed),
  entitlement in Quartermaster, enactment in Foghorn against `StreamRegistry.Locations`.
- Replication policy is a consumer of the placement policy engine's `replicate` verb
  (`docs/rfcs/placement-policy-engine.md`); this RFC defines the replication-specific inputs and
  the orchestration that acts on the resolved policy, not a second policy system.

## Current State

The shipped mechanics are canonical in `docs/architecture/stream-replication-topology.md` and
`docs/architecture/federation.md`. Summarized here only to frame the gap:

- **On-demand only.** Every replication is triggered by demand: a viewer request, or a MistServer
  source request (`/?source=` or the `STREAM_SOURCE` trigger). Intra-cluster, edges DTSC-pull from
  the origin when Foghorn's source selection points them there. Cross-cluster, Foghorn arranges an
  origin-pull via the shared `federation.ArrangeOriginPull` helper
  (`QueryStream` → `NotifyOriginPull` → `MarkReplicating`). Nothing replicates ahead of demand.
- **Direct ingest-edge serving.** The origin is whichever node received the producer's push
  (`Inputs > 0, Replicated == false`); viewers on that node are served directly from the ingest
  edge. Within a cluster the topology is a star (all replicas pull the origin); across clusters it
  is single-hop (peer edge → local edge). There are no hub roles and no multi-hop chains.
- **StreamRegistry is the live replication truth.** Per-stream replication state lives in Foghorn's
  `control.StreamRegistry` as per-cluster `Locations`: in-flight inbound pulls
  (`Locations[local].ReplicatingFrom + PullDTSCURL + DestNodeID`), source-side outbound pulls
  (`Locations[local].OutboundPullers`), and federated peer identity/ads
  (`Locations[peer_cluster]`), aged by `SweepStaleLocations` (30s tick / 5-min maxAge). The
  federation cache's earlier per-stream replication records (`ActiveReplicationRecord`,
  `StreamAdRecord`, `PlaybackIndex`) were deleted when this moved into the registry;
  `RemoteEdgeCache` retains only federation telemetry, including the event-fed
  `remote_replications` peer-availability entries used below.
- **Loop prevention exists.** Three layers: the pre-arrangement `RemoteReplicationEntry` check
  (skip arranging a pull from a cluster already replicating from us), `ReplicationEvent`
  broadcast on completion, and the `StreamAdvertisement` directory.
- **No policy surface.** The only per-stream replication controls are the `federated` flag
  (cross-cluster visibility on/off) and `commodore.stream_cluster_pins` (enterprise pinning of a
  stream to an allowed cluster set, applied at resolve time). There is no replica-count cap, no
  region policy, no role assignment, and no way to express "have this stream in region X by
  time T".

## Problem / Motivation

Demand-driven replication means the first viewer in every cell pays the cold-start cost: a
QueryStream fan-out, an origin-pull arrangement, and a cross-internet DTSC pull before the first
segment is served. For predictable events (a scheduled broadcast with a known audience geography)
this is avoidable latency. The star/single-hop shape also concentrates load: a globally popular
stream makes every cluster pull the origin cluster independently, spending the origin's egress
N times instead of cascading through a regional hub. Finally, tenants and operators have no
lever to bound or direct replication — no replica caps, no region allowlists beyond cluster pins,
no way to pre-position a stream — so capacity planning for large events is guesswork.

## Goals

- Pre-emptive spread: replicate a stream to a target cell/region ahead of expected demand, on
  operator action, per-stream policy, or predicted audience.
- Explicit per-stream hop topology: origin/hub/edge role assignments and multi-hop distribution
  between regions, recorded and observable rather than implied by pull state.
- Per-stream replication policy: `max_replicas_total`, `max_replicas_per_region`,
  `allowed_regions` — intent owned by the stream owner, entitlement by the capacity owner.
- Reuse the shipped primitives: enactment composes the existing origin-pull handshake and
  `StreamRegistry` state; no parallel replication mechanism.
- Fit the placement policy engine: replication policy resolves through the `replicate` verb's
  rule model rather than a bespoke evaluator.

## Non-Goals

- Replacing MistServer replication mechanics (DTSC transport stays as-is).
- CDN or edge cache design.
- Artifact (VOD/clip/DVR) placement — that is the placement policy engine's `store`/`serve`
  domain; this RFC covers live stream distribution.
- Predictive audience modeling itself. The trigger interface accepts a prediction as input; how
  predictions are produced is out of scope here.

## Proposal

### 1. Pre-emptive spread

Add an orchestration path in Foghorn that establishes replication without a triggering viewer:
given a target (cluster/cell, or region resolved to clusters), the orchestrator runs the same
arrangement used today for demand-driven pulls — select a local edge with capacity on the target
cluster, `NotifyOriginPull` against the source cluster, `MarkReplicating` on the registry — so a
pre-emptive replica is indistinguishable from a demand-driven one in state, sweeping, loop
prevention, and observability. The only new state is the _intent_ ("this stream should be present
in region X"), which the orchestrator reconciles against `StreamRegistry.Locations`: re-arranging
on failure, tearing down when intent is removed, and never fighting the demand-driven path (an
already-present demand replica satisfies the intent).

Triggers, in increasing automation:

- **Operator action** — an explicit "spread stream S to cell/region R" request (API/CLI), the
  minimal useful slice.
- **Per-stream policy** — a standing rule attached to the stream (e.g. "always present in
  `allowed_regions` while live"), evaluated when the stream goes live.
- **Predicted audience** — an external prediction (scheduled event geography, historical viewer
  distribution) submitted through the same intent interface.

### 2. Explicit hop topology

Introduce per-stream role assignments — origin, hub, edge — so distribution can cascade instead
of starring off the origin:

- **Origin** remains what it is today: the ingest node/cluster (state-derived, one per stream).
- **Hub** is a new, assigned role: a cluster (or node) designated as the replication source for a
  region. Hubs pull from the origin (or an upstream hub); edges in that region pull from their
  hub rather than crossing regions to the origin.
- **Edge** replicas behave as today, but source selection consults the topology: prefer the
  region's hub over the origin when one is assigned and live.

The topology is recorded per stream (registry-adjacent, alongside `Locations`) and is explicitly
observable — which cluster is serving as hub for which region, and which hops are active — rather
than reconstructed from `Replicated`/`OutboundPullers` state. Multi-hop chains stay shallow
(origin → hub → edge); the loop-prevention layers already shipped apply per hop, and hub failure
falls back to today's behavior (edges pull the origin directly).

### 3. Per-stream replication policy

Policy fields per stream, with tenant-level defaults:

- `max_replicas_total` — cap on concurrent replicas across the federation.
- `max_replicas_per_region` — cap per region.
- `allowed_regions` — regions the stream may replicate into (complementing
  `stream_cluster_pins`' cluster-level allowlist).

Ownership follows the service-boundary split already established for placement policy:

- **Commodore homes the intent.** Per-stream replication policy is stream-owner data, alongside
  the stream record. `commodore.stream_cluster_pins` is the seed — it already expresses
  "constrain this stream's placement" as a side table applied at resolve time; replication policy
  generalizes that shape (regions and counts, not just cluster ids).
- **Quartermaster homes the entitlement.** Whether a tenant may replicate into a given cluster or
  region at all is capacity-owner data (`tenant_cluster_access` and its placement-scope
  extension per the placement-policy-engine RFC), plus the region metadata for clusters/nodes.
- **Foghorn enacts.** At arrangement time Foghorn resolves the effective policy (intersection of
  intent and entitlement — a deny from either side wins) against live `StreamRegistry.Locations`
  state, and refuses or tears down replication that exceeds caps or leaves allowed regions.

This is deliberately a consumer of the placement policy engine
(`docs/rfcs/placement-policy-engine.md`): the fields above are `replicate`-verb rule inputs
(selectors on stream/tenant, effects `require`/`allow`/`deny`/`prefer` over regions and counts),
distributed through the same signed policy bundle, enforced at the replication hook-in. This RFC
does not introduce a second policy model; it defines what the `replicate` domain needs to express
for live streams and the orchestrator that acts on the resolved result.

### Owning services / modules

- **Foghorn (`api_balancing`)** — replication orchestrator: intent reconciliation, pre-emptive
  arrangement (reusing `federation.ArrangeOriginPull`), hop-aware source selection, policy
  enforcement at enact time; `control.StreamRegistry` remains the live replication truth and
  gains the topology/role records.
- **Commodore (`api_control`)** — per-stream replication policy and spread intent (stream-owner
  side), evolving from `stream_cluster_pins`; policy compiled into the signed bundle it already
  mints (`policy_bundle_versions`).
- **Quartermaster (`api_tenants`)** — region metadata for clusters/nodes and replication
  entitlement (capacity-owner side of the two-sided policy).
- **Helmsman/MistServer (`api_sidecar`)** — unchanged transport: DTSC pulls configured exactly as
  the demand-driven path configures them today.

## Impact / Dependencies

- Foghorn: orchestrator loop, topology records, policy resolution at arrangement time; source
  selection consults hub assignments.
- Commodore: policy/intent schema (generalizing `stream_cluster_pins`), API surface for operator
  spread actions and per-stream policy, bundle compilation.
- Quartermaster: authoritative region metadata on clusters/nodes; entitlement scopes.
- `pkg/proto`: intent/policy messages; federation protocol is expected to need no new RPCs for
  arrangement (pre-emptive spread reuses `NotifyOriginPull`), possibly an advertisement field for
  hub role.
- Placement policy engine RFC: the `replicate` verb's rule vocabulary must cover region/count
  constraints for live streams; resolution ownership is settled there (§7: each owner compiles
  its own half, Foghorn resolves at enact time), which matches the enactment model above.
- Optional GraphQL exposure for topology/intent observability (Bridge).

## Alternatives Considered

- **Keep demand-driven only (status quo)** — cold-start latency and origin egress concentration
  remain; no operator lever for predictable events.
- **DNS/geo steering instead of replication** — steers viewers to where the stream already is;
  cannot place the stream ahead of them, and does nothing for origin fan-out cost.
- **Standalone replication policy store in Foghorn** — rejected: duplicates the policy engine,
  and puts stream-owner intent in the wrong service (violates the Commodore-owns-stream-policy
  boundary).
- **Full mesh pre-replication (push everywhere on go-live)** — simple but wasteful; policy caps
  and targeted spread exist precisely to avoid paying worst-case egress for every stream.

## Risks & Mitigations

- **Wasted capacity from bad predictions or stale intent** — replicas without viewers consume
  edge bandwidth. Mitigate: caps (`max_replicas_*`), intent TTLs, and the existing sweeper model
  tearing down unused pre-emptive replicas.
- **Orchestrator vs demand-path races** — both arrange pulls for the same stream. Mitigate:
  single source of truth (`StreamRegistry.Locations`) consulted by both paths, same
  loop-prevention checks, idempotent arrangement (an existing replica satisfies both).
- **Hub failure amplifies outage** — a dead hub takes its region's edges with it. Mitigate:
  fallback to direct origin pull (today's behavior) when the hub's Location goes stale; hubs are
  an optimization, never a hard dependency.
- **Policy misconfiguration blocks legitimate viewers** — an over-tight `allowed_regions` could
  strand an audience. Mitigate: policy gates _replication_, not viewer redirects; a viewer in a
  disallowed region can still be redirected to an allowed cluster, consistent with the
  `pull+` non-allowed-cluster behavior today.
- **Registry growth** — topology/role records add per-stream state. Mitigate: same sweep/expiry
  budget as existing Locations.

## Migration / Rollout

Additive and phased; each phase useful on its own and behind its own flag. No dates.

1. **Region metadata + policy fields, no behavior change.** Quartermaster region metadata
   authoritative; Commodore schema for per-stream policy (defaults = unlimited, all regions);
   fields visible in policy bundles but unenforced.
2. **Operator-triggered spread.** Explicit spread-to-cell API enacted by the Foghorn
   orchestrator; observability for intent vs actual.
3. **Policy enforcement.** Caps and region constraints enforced at arrangement time;
   per-stream standing policy evaluated on go-live.
4. **Hub topology.** Role assignment, hub-aware source selection, multi-hop between regions.
5. **Predicted-audience triggers.** External predictions feed the same intent interface.

## Open Questions

- Region taxonomy: are regions a first-class Quartermaster entity or a label on clusters/nodes,
  and who validates them?
- Hub granularity: cluster-level hubs only, or node-level hub designation within a cluster?
- Intent API shape: imperative ("spread now") vs declarative ("desired presence set") — the
  reconciler model suggests declarative, but the operator slice may want both.
- Does pre-emptive spread for a not-yet-live stream mean arranging at go-live (subscribe to
  lifecycle) or provisioning ahead (no bytes to pull yet)?
- How do replica caps interact with demand: does a demand-driven viewer in an at-cap region get
  a redirect instead of a new replica?
- Inherited from placement-policy-engine.md: how bundles are versioned for the `replicate`
  domain (resolution ownership is settled there — Foghorn resolves at enact time).

## References, Sources & Evidence

- [Reference] `docs/architecture/stream-replication-topology.md` — canonical shipped mechanics:
  source resolution, origin-pull lifecycle, loop prevention, implicit topology model.
- [Reference] `docs/architecture/federation.md` — federation protocol (QueryStream,
  NotifyOriginPull, PeerChannel), StreamRegistry vs RemoteEdgeCache split, HA model.
- [Reference] `docs/rfcs/placement-policy-engine.md` — the `replicate` verb, two-sided
  (owner + tenant) intersection policy model, signed bundle distribution this RFC consumes.
- [Evidence] `api_balancing/internal/federation/origin_pull.go` — shared
  `ArrangeOriginPull`/`DefaultArrange` helper the orchestrator reuses; pre-arrangement
  `GetRemoteReplications` loop check.
- [Evidence] `api_balancing/internal/control` — `StreamRegistry` per-stream
  `Locations[cluster].{ReplicatingFrom, PullDTSCURL, DestNodeID, OutboundPullers}` (live
  replication truth; replaced the federation cache's deleted `ActiveReplicationRecord` /
  `StreamAdRecord` / `PlaybackIndex`), `SweepStaleLocations` expiry.
- [Evidence] `api_balancing/internal/federation/cache.go` — `RemoteEdgeCache` narrowed to
  federation telemetry; `RemoteReplicationEntry` peer-availability records feeding loop
  prevention.
- [Evidence] `pkg/database/sql/schema/commodore.sql` — `commodore.stream_cluster_pins` (the
  per-stream placement-constraint seed) and `commodore.policy_bundle_versions` (signed bundle
  channel).
- [Evidence] `pkg/proto/foghorn_federation.proto` — QueryStream / NotifyOriginPull /
  StreamAdvertisement contracts.
