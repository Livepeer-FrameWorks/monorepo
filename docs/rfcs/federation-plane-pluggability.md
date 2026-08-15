# RFC: Federation Plane Pluggability

## Status

Draft

## TL;DR

- FrameWorks federation today is media-plane federation under a single central authority: every
  cluster — including sovereign/BYOC clusters on operator hardware — depends on the central control
  plane (Commodore, Quartermaster) for identity, entitlement, and topology, and on the central data
  plane (Periscope) for attribution and analytics. This is the **central gatekeeper** model.
- This RFC names that model as the **explicit interim** and proposes **operator-local control/data-plane
  authority** as the federation target: an operator's cluster holds authority over its own domain
  and interoperates with peers through defined plane interfaces, instead of every cluster deferring
  to one central brain.
- The path is phased: first harden the gatekeeper's internal boundaries into owner-RPC interfaces
  (no cross-schema reads, every cross-plane dependency an explicit contract), then move authority
  for specific domains to the operator that owns them. Later phases are deliberately left open.
- Alongside the authority phases, a **federation state synchronization** research track covers the
  mechanism side: decentralized distribution of configuration/state changes across a dynamic set of
  clusters/servers (parties join and leave at will), automatic conflict resolution between
  concurrent changes without a central coordinator, and reconvergence after partitions. Today all
  state distribution is central; this track is research, not roadmap.
- No timeline is attached to any phase, and nothing here should be read as a claim that
  decentralized operation is near-term. The settlement/attribution side of federation is covered
  separately in `federated-settlement-attribution.md`.

## Current State

**Plane terminology** (used consistently below): _control plane_ = the Go services (Commodore,
Quartermaster, Purser, Bridge, …); _media plane_ = MistServer + Livepeer at the edges; _data plane_ =
event/analytics/attribution/metering (Decklog, Periscope, ClickHouse). Network & trust (Navigator,
Privateer) supports all three.

**The media plane already federates.** Foghorn-to-Foghorn federation (`docs/architecture/federation.md`)
is a working peer protocol: QueryStream scoring, NotifyOriginPull replication, PrepareArtifact and
peer-relay grants for cross-cluster artifact access, and a PeerChannel telemetry stream whose
StreamAdvertisement messages let steady-state viewer routing skip Commodore entirely. Two clusters
exchange streams, artifacts, and capacity signals directly.

**Authority does not federate.** That peer protocol runs under a single central authority on both
the control and data planes:

- **Commodore** is the sole authority for tenant/stream identity, stream keys, playback-ID
  resolution, artifact business registries (clips/DVR/VOD), and playback access policy (including
  the signed policy-bundle compiler of `placement-policy-engine.md`). Federation's
  StreamAdvertisement fast path removes Commodore from the steady-state _request_ path, but not
  from the _authority_ path — every identity and entitlement ultimately resolves there.
- **Quartermaster** is the sole authority for the cluster registry, node lifecycle and enrollment,
  tenant-cluster access grants, marketplace subscriptions, peer discovery (`ListPeers`), mesh
  topology (SyncMesh), and bootstrap desired state. A Foghorn cannot even find its peers without
  the central Quartermaster.
- **Periscope/Decklog** are the sole data-plane sink: all analytics events — including the
  routing/federation events that attribute served traffic to serving clusters — flow to the central
  pipeline and central ClickHouse. An operator's own traffic data lives off their infrastructure,
  and the usage attribution that feeds cross-cluster billing is computed centrally.

**State distribution is central.** Configuration and state changes reach clusters and servers
through exactly one path: gitops → central control plane → clusters/servers. There is a single
management point, and there is no conflict-resolution mechanism anywhere in the platform —
concurrent conflicting changes are prevented only by the fact that every change flows through that
one point, which serializes them. Nothing today can accept a change in one place and reconcile it
with a concurrent change accepted somewhere else; a cluster that is temporarily unreachable simply
receives the current central desired state when it reconnects.

**Sovereign/BYOC docs cover a different concept.** `docs/architecture/sovereign-strategy.md` and
`docs/architecture/edge-deployment.md` describe running the _whole stack_ on customer
infrastructure — Shared SaaS, Dedicated SaaS, Self-Hosted. All three models replicate the same
shape: one central control/data plane with edges under it. Self-hosting moves the gatekeeper onto
your premises; it does not decouple the planes. There is currently no documented or implemented way
for an operator's cluster to keep local authority over its own domain while interoperating with
clusters under other authorities. Plane pluggability appears nowhere in the docs corpus today.

**Boundary hygiene is partially ahead of this RFC.** Two production cross-schema DB reads
(Commodore and Foghorn reading Quartermaster-owned tables directly), identified in the 2026-07
platform audit, have been eliminated: both now read through owner RPCs via `pkg/clients` —
Commodore's bundle-mint entitlement read via `GetTenantEntitlement` and Foghorn's served-cluster
load via `ListServiceClusterAssignments`, with zero live `FROM quartermaster.*` reads remaining in
`api_control` / `api_balancing`. That work is the first realized instance of the Phase 1 posture
proposed here — boundary hygiene shipped for these two, not merely planned.

## Problem / Motivation

The sovereign positioning promises operators control of their infrastructure. Today they get
control of the media plane only. An operator who racks hardware, joins the marketplace, and serves
traffic still depends on the central platform for:

- **Identity and entitlement**: which tenants exist, which streams are theirs, who may view what —
  all resolved centrally. A central control-plane outage degrades to cached/advertised state; a
  central control-plane _disappearance_ ends the operator's business.
- **Their own operational data**: node inventory is centrally mastered; the analytics for traffic
  the operator served live in central ClickHouse; the attribution records that determine what the
  operator is owed are produced and held by the party that owes them.
- **Topology**: peer discovery and mesh membership are centrally granted, so two operators cannot
  interoperate except through the central plane both enrolled in.

This is an acceptable and honest interim — a single authority is simpler, and the platform is built
and operated as one system today. But it is a ceiling on the federation story: "federation" that
requires one central gatekeeper is a distribution network, not a federation of operators. Without a
stated target architecture, new features keep hard-wiring central authority deeper (every new
table, RPC, and pipeline assumes it), making eventual decoupling more expensive. This RFC exists to
fix the target so that ongoing work can point at it, not to schedule the target.

## Goals

- Name the central-gatekeeper model as the explicit, documented interim architecture for
  federation.
- Define **operator-local authority** as the target: each plane's authority can live with the
  operator that owns the underlying domain, with interoperation through defined interfaces rather
  than shared central state.
- Make every cross-plane and cross-service dependency an explicit owned interface (owner RPCs,
  signed artifacts, event contracts) so that authority _can_ later move without rewrites.
- Identify which authority domains are candidates for per-operator ownership first, and which
  remain central longest.
- Keep the managed SaaS operating model fully intact throughout — pluggability is a capability,
  not a forced migration.

## Non-Goals

- **No timeline commitments.** This RFC deliberately attaches no dates, releases, or "near-term"
  framing to any phase. Decentralized authority is a target, not a promise.
- No blockchain, token, or global-consensus mechanism. Interfaces and signed artifacts, not
  distributed consensus.
- Settlement mechanics — manipulation-resistant attribution of served traffic, operator credit
  accrual, payout execution. That is `federated-settlement-attribution.md` (authored in parallel);
  this RFC only requires that the data-plane interfaces make settlement inputs exportable and
  verifiable.
- Policy bundle format and enforcement hook-ins — owned by `placement-policy-engine.md`. This RFC
  relies on that mechanism (signed bundles verified at local decision points) as the distribution
  pattern for entitlement, and does not redefine it.
- Removing central services from the managed platform, or supporting fully air-gapped operation of
  a marketplace-connected cluster.

## Proposal

Adopt a phased decoupling in which each phase is useful on its own and none presumes a schedule.
The sequencing principle: **first make the boundaries real, then move authority across them.**

### Phase 1: Central gatekeeper with owner-RPC boundaries

The gatekeeper stays the sole authority, but every dependency on it becomes an explicit, owned
interface:

- **No cross-schema reads, anywhere.** Every service reads another service's domain only through
  that service's RPCs via `pkg/clients`. The first two production cross-schema reads have already
  been eliminated this way (`GetTenantEntitlement`, `ListServiceClusterAssignments`); the CLAUDE.md
  service-boundary rule is now load-bearing for federation, not just hygiene.
- **Enumerate authority surfaces.** For each of Commodore, Quartermaster, and Periscope, document
  the interface through which its authority is consumed: identity/entitlement resolution and
  policy-bundle distribution (Commodore); cluster registry, peer discovery, access grants, mesh
  state (Quartermaster); event ingest contracts and attribution/usage read models (Periscope).
  These enumerations become the contract seam that later phases relocate — the module map work
  (`PLAN_PLATFORM_MODULE_MAP.md`) supplies the inventory.
- **Prefer verifiable artifacts over live lookups** where the pattern already exists: the signed
  policy bundle (compiled centrally, verified locally) is the template. Interfaces that hand a
  peer a signed, versioned artifact rather than requiring a synchronous call to central state are
  directly reusable when the compiling authority moves.

Exit criterion for the phase: a cluster's runtime dependencies on the central plane are fully
described by named interfaces, with no side channels. Behavior is unchanged.

### Phase 2: Per-operator authority for specific domains

Move authority for domains that are naturally operator-owned, one domain at a time, while tenant
identity, marketplace, and billing rating remain central:

- **Infrastructure/topology (Quartermaster domain).** An operator's cluster becomes the authority
  for its own node inventory, lifecycle, and intra-cluster mesh, publishing a signed summary
  (capacity, regions, health class) to the central registry instead of being centrally mastered.
  Central Quartermaster keeps the _directory_ of clusters and the tenant-cluster grant ledger;
  it stops being the writer of record for hardware it does not own.
- **Data plane (Periscope domain).** Per-cluster ingest with operator-local retention: events for
  traffic a cluster serves land in an operator-local pipeline first; a defined export interface
  ships the attribution/usage aggregates (and only those) upward for settlement and tenant-facing
  analytics. The operator holds the complete record of what their infrastructure did; the central
  plane holds what it needs to rate, bill, and report. The export contract's
  manipulation-resistance requirements are specified in `federated-settlement-attribution.md`.
- **Entitlement enforcement (Commodore-compiled, locally verified).** Extend the signed
  policy-bundle path so a cluster can admit and serve within its granted scope using only bundle
  verification — no synchronous central call in the serving path. Commodore remains the compiler
  and signer; the operator's cluster becomes self-sufficient for enforcement between bundle
  updates.

Each domain move is independently shippable and independently reversible; none requires the
others.

### Research track: federation state synchronization

The authority phases above answer _who may decide_; this track is their mechanism-side
counterpart — _how decisions travel_ once more than one party can make them. Today the question
does not arise: distribution is central (see Current State), and the single distribution point is
also the only thing preventing concurrent conflicting changes. But every Phase 2 domain move
creates an additional writer somewhere, and the later-phases direction (operator control planes
interoperating directly) presumes state can move between parties without a central coordinator.
This track is explicitly research — unscheduled, gated on Phase 1/2 interface experience, and
carrying no timeline. Three properties define the problem:

- **Decentralized distribution over dynamic membership.** Configuration/state changes must
  propagate across a set of clusters/servers that is not fixed: parties join and leave at will and
  may be temporarily unreachable. A change accepted anywhere must eventually reach every reachable
  party without routing through a central distribution point.
- **Automatic conflict resolution without a central coordinator.** When multiple parties make
  concurrent, conflicting changes, every node must converge on the same outcome deterministically,
  with no coordinator available to serialize the changes. Which priority mechanism decides the
  outcome — per-domain authority weight, causal ordering with a deterministic tiebreak, CRDT-style
  merge semantics per state type, or a hybrid — is deliberately left open; it is the core research
  question of this track (see Open Questions).
- **Partition healing.** Parts of the federation that operated while disconnected — each accepting
  changes independently — must reconverge to a consistent shared state when connectivity is
  restored, through the same conflict-resolution mechanism rather than manual reconciliation.

The candidate transport is the one layer that already federates: Foghorn's peer channel (see
Owning services below). Nothing in this track changes the authority model — it supplies the
convergence mechanics that Phase 2 domains and any later phase would stand on.

### Later phases: open

Peer-to-peer authority exchange — operator control planes interoperating directly (identity
assertions between operators, cross-operator grants without a central ledger, multiple
marketplaces) — is explicitly out of scope for this proposal. It is the direction the interfaces
point, and it stays unspecified until Phase 2 experience shows which contracts hold up.

### Owning services / modules

**Foghorn** (api_balancing) owns the media-plane federation protocol — the one layer that already
federates — and is the enactor where central authority currently leaks into runtime paths
(Commodore resolution, Quartermaster peer discovery, central event emission). Phase 1 formalizes
those dependencies as interfaces; Phase 2 changes what stands behind them without changing
Foghorn's protocol role. Its PeerChannel is also the candidate transport substrate for the
state-synchronization research track — the one existing channel over which clusters exchange
messages directly, without the central plane in the path.

**Quartermaster** (api_tenants) is today the central authority for clusters, nodes, grants, mesh,
and peer discovery. Under the target it splits along ownership lines: directory-of-clusters and
grant ledger stay central; authority over an operator's own nodes and intra-cluster topology moves
to that operator's cluster, consumed centrally through a signed summary interface. Quartermaster
is also today's state authority in the distribution sense: configuration/state changes reach
clusters only through the central plane it anchors — the single management point the
state-synchronization research track studies how to do without.

**Commodore** (api_control) holds tenant/stream identity and compiles entitlement. It remains
central authority the longest; its pluggability contribution is compiling authority into
verifiable artifacts (policy bundles) so enforcement decentralizes even while identity does not.

**Periscope** (api_analytics_ingest / api_analytics_query, with Decklog as the ingest edge) owns
the data-plane split: operator-local ingest and retention for locally served traffic, plus the
upward export interface that carries settlement and tenant-analytics aggregates. The settlement
semantics of that export belong to `federated-settlement-attribution.md`; the plane boundary
belongs here.

## Impact / Dependencies

- **Contracts**: new/expanded proto surfaces in `pkg/proto` for authority summaries (cluster
  self-description) and data-plane export; extensions to the policy-bundle schema
  (`placement-policy-engine.md`).
- **Services**: Quartermaster (registry vs. authority split), Periscope Ingest/Query + Decklog
  (per-cluster pipeline + export), Commodore (bundle scope), Foghorn (consume-through-interfaces
  only). Purser is downstream via the settlement RFC.
- **Trust plane**: Navigator/Privateer unaffected in mechanism, but cluster-held signing identity
  (see `service-identity-and-cluster-binding.md`, `token-authority.md`) becomes a prerequisite for
  any operator-signed artifact.
- **Docs**: `federation.md` gains the authority-vs-protocol distinction; `sovereign-strategy.md`
  gains the plane-decoupling target alongside the whole-stack deployment models; the module map
  records per-domain authority status.
- **Cross-RFC**: `federated-settlement-attribution.md` (settlement side of the data-plane split),
  `placement-policy-engine.md` (bundle mechanism), `stream-replication.md` rewrite (replication
  orchestration will consume the same topology interfaces).

## Alternatives Considered

- **Keep the gatekeeper as the permanent architecture.** Simplest; contradicts the sovereign
  positioning and caps federation at a managed distribution network. Rejected as a target, adopted
  as the interim.
- **Jump directly to peer-to-peer operator control planes.** Maximal sovereignty; requires solving
  federated identity, trust, and settlement simultaneously, against interfaces that do not exist
  yet. Rejected — the phased path extracts the same interfaces with working software at each step.
- **Adopt an external federation substrate (blockchain/DHT-based identity and settlement).** Adds a
  consensus dependency and an ecosystem bet orthogonal to the actual problem (interface ownership).
  Out of scope; nothing in the phased design precludes revisiting it in the open later phases.
- **Full-stack replication per operator (status quo self-hosting) as the sovereignty answer.**
  Already supported; does not compose — N self-hosted stacks are N isolated gatekeepers, not a
  federation.
- **Raft-class consensus for federation state.** Proven for replicated state machines, but it
  requires a fixed, known membership set and an elected leader. Both conflict with the
  state-synchronization setting: parties join and leave at will and may be partitioned for long
  periods, so a leader-based quorum either blocks or excludes exactly the members federation
  exists to serve. Rejected for the research track's problem; nothing prevents individual
  services from using consensus internally.
- **Standard database replication for state distribution.** Replication (physical or logical)
  distributes sequentially ordered updates produced under central coordination — it presumes the
  single serialization point the research track is trying to remove. Pointing replication at the
  problem moves it without solving it. Rejected.

## Risks & Mitigations

- **Over-promising decentralization.** The largest risk is reputational: reading this RFC as a
  roadmap commitment. Mitigation: the interim is documented as such everywhere the target is
  mentioned, phases carry no dates, and roadmap surfaces must not represent later phases as
  in-progress.
- **Split-brain authority during Phase 2.** Two writers for one domain (central registry vs.
  operator cluster) would be worse than either alone. Mitigation: authority moves per-domain and
  atomically — a domain is either centrally mastered or operator-mastered with a central read
  model, never both; the signed-summary interface is one-directional.
- **Attribution manipulation once operators hold their own data plane.** An operator that masters
  its own usage record can inflate it. Mitigation: out of scope here by design — the export
  contract is specified with manipulation-resistance in `federated-settlement-attribution.md`, and
  the data-plane split does not ship ahead of that contract.
- **Interface ossification.** Enumerating and freezing authority interfaces too early could
  entrench today's service shapes. Mitigation: Phase 1 documents and routes through interfaces
  without freezing them; versioned protos and the existing additive-migration discipline apply.
- **Cost of local pipelines.** Operator-local ingest/retention adds operational surface per
  cluster. Mitigation: Phase 2 domains are opt-in per cluster; the gatekeeper path remains valid
  indefinitely for clusters that do not want local authority.

## Migration / Rollout

Phases as above, sequenced by dependency, with no dates:

1. **Phase 1** rides partly on work already done: cross-schema-read elimination has landed for the
   first two production reads (owner RPCs via `pkg/clients`); the module map's authority-surface
   inventory and continued preference for signed artifacts remain. No behavior change, no
   operator-visible change.
2. **Phase 2** ships per-domain behind explicit cluster-level opt-in (a capability flag on the
   cluster record), with the central path as the default and permanent fallback. Domain order is
   an open question (below), but each domain's move requires: its interface from Phase 1 proven in
   production, a signed-artifact trust path for the operator (cluster-bound identity), and — for
   the data-plane domain — the settlement RFC's export contract.
3. **Later phases** have no rollout plan by design; they enter planning only via a successor RFC
   once Phase 2 domains are in production use.

No data migration is implied by Phase 1. Phase 2 domain moves each define their own migration in
implementation plans (e.g. node-inventory mastership handover), inheriting the platform's
expand/contract migration discipline.

## Open Questions

- Domain order in Phase 2: infrastructure/topology first (smallest blast radius, clear owner) or
  data plane first (largest sovereignty value, but gated on the settlement contract)?
- Where does the tenant-cluster grant ledger ultimately live once grants can span operators —
  central directory, per-operator with counter-signatures, or both?
- What is the minimum cluster-held identity for signing authority summaries — the existing
  cert_sync/cluster-binding path, the token-authority proposal, or a dedicated operator keypair?
- Does per-operator data-plane retention change the tenant-facing analytics contract (tenants
  querying across clusters owned by different operators), and does Periscope Query federate reads
  or rely solely on the exported aggregates?
- How does policy-bundle verification behave for a cluster whose grant is revoked while offline —
  what is the maximum enforcement staleness the platform accepts?
- Which priority/merge mechanism should the state-synchronization research track use to resolve
  concurrent conflicting changes — per-domain authority weight (the domain's owner wins), causal
  ordering with a deterministic tiebreak, CRDT-style merge semantics per state type, or a hybrid?
  Deliberately unresolved; the track exists to answer this before any mechanism is proposed.

## References, Sources & Evidence

- [Source] `docs/architecture/federation.md` — shipped Foghorn federation protocol; StreamAdvertisement
  steady-state independence from Commodore's request path.
- [Source] `docs/architecture/sovereign-strategy.md` — deployment models; whole-stack BYOC scope
  (no plane decoupling).
- [Evidence] `PLAN_PLATFORM_MODULE_MAP.md` (2026-07-22 audit) — central-gatekeeper dependency
  inventory across Commodore/Quartermaster/Periscope; finding that plane pluggability appears in no
  existing doc or RFC; the cross-schema-read findings, whose fix has since shipped — the two reads
  now go through `GetTenantEntitlement` (Commodore) and `ListServiceClusterAssignments` (Foghorn).
- [Reference] `docs/rfcs/placement-policy-engine.md` — signed policy bundle + local verification at
  decision-point hook-ins; the distribution pattern Phase 2 entitlement enforcement extends.
- [Reference] `docs/rfcs/federated-settlement-attribution.md` — settlement/attribution side of the
  data-plane split (authored in parallel with this RFC).
- [Reference] `docs/architecture/cross-cluster-billing.md`, `docs/architecture/routing-events-attribution.md` —
  current centrally-computed usage attribution the Phase 2 export interface must reproduce.
- [Reference] `docs/rfcs/service-identity-and-cluster-binding.md`, `docs/rfcs/token-authority.md` —
  candidate trust paths for operator/cluster-held signing identity.
