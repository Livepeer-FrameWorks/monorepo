# RFC: Durable Placement Policy Engine (storage, processing, replication, and tiers)

## Status

Proposed — not implemented. This RFC specifies durable storage, processing,
replication, peer, and tier placement beyond the tagged v0.3.0 live-media
`ingest`/`serve` foundation.

The live-media system is implemented separately in
[media placement policy](../architecture/media-placement-policy.md): it includes
tenant/stream policy management, capacity-owner consent, signed authority, cross-cell
routing, exact-destination preparation, and final admission. The feature registry
still marks that system `partial` pending authenticated browser-to-service proof and
release-runtime verification. Neither its implementation nor its remaining verification
activates the durable-copy and processing proposals below.

## TL;DR

- Generalize v0.3.0's one immutable S3 backend per Foghorn cell into a declared
  **storage-backend registry** and a backend-keyed **durable placement ledger**.
- Support independently registered storage providers through capability-based adapters,
  including S3-compatible services and durable local volumes such as RAID arrays.
  Separate durability, access class, and physical medium; no vendor or device defines
  a tier by itself.
- Preserve asset identity, object manifests, ownership history, and cleanup obligations
  across provider migration. Replicate, verify, switch new readers, and retire old copies
  through resumable operations with explicit recovery and cost bounds.
- Extend the shipped placement algebra beyond `ingest` and `serve` with `process`,
  `store`, `replicate`, and `peer`, while preserving the existing meanings and
  enforcement of the live verbs.
- Reuse the Ed25519 media-authority envelope, source revisions, cell audience,
  durable delivery, and capability-attested activation introduced in v0.3.0. Do not
  revive the retired HMAC policy-bundle producer.
- Treat cross-cluster durable replication as a hard prerequisite for remote copies;
  that protocol in turn requires cluster-bound provider identity. Live cross-cell
  routing is not durable-object replication or provider attestation.

## Current State

### Live-media placement in v0.3.0

`pkg/placement` contains the shipped placement compiler and pure evaluator. Its wire
and Go verb sets accept only `ingest` and `serve`. Tenant and stream hard constraints
accumulate; an explicit preference list replaces the inherited ordered groups without
widening an allow/deny boundary. Entitlement and capacity-owner consent are verified
candidate facts, not rights granted by content policy.

Commodore persists tenant/stream policy and reviewed changes in
`commodore.media_placement_policies` and `commodore.media_placement_changes`.
Quartermaster owns tenant-scoped placement inventory and capacity-owner consent.
Foghorn combines signed content authority with authorized observations, prepares an
exact destination, and re-evaluates at routing and final admission. Bridge GraphQL/MCP
and the web application expose the management workflow.

The active distribution mechanism is the Ed25519 `media_authority` envelope, not the
legacy `GetSignedPolicyBundle` RPC. Placement-bearing tenant and media-object payloads
use media-authority schema 2, bind the object policy to its parent tenant revision, and
are durably delivered to an explicit audience cell. A cell attests placement capability
only when every live Foghorn replica in its liveness window reports schema-2 enforcement;
Commodore does not activate a policy revision until the required target cells acknowledge
an enforcing schema-2 authority. The retained HMAC bundle RPC returns `Unimplemented`.

This foundation does **not** implement `process`, `store`, `replicate`, or `peer` policy.
It also does not provide a durable backend registry, a multi-copy ledger, tier occupancy,
or proactive copy reconciliation.

### Durable media storage in v0.3.0

The durable model still records one S3 location per artifact, attributed to its storage
cluster and gated by the artifact's sync/frozen lifecycle, plus N transient full local
node copies. Each Foghorn database belongs to one cell and one immutable S3 descriptor.
The physical `backend_id` fingerprints `(kind, bucket, endpoint, region, prefix)` and is
enforced fail-closed at boot and cleanup. This is a typed single backend, not
`cluster_storage_backends` and not a choice among backends.

The in-cell publication state machine is reusable foundation: Foghorn server-mints an
attempt ID, grants an attempt-scoped staging PUT, HEAD-verifies the staged object,
publishes to a fresh immutable attempt-versioned key, and flips the active pointer under
a guarded transaction. Multi-copy placement should generalize that state machine rather
than inventing a weaker completion path.

Node-copy observations remain separate from durability. Whole-node inventory is
session-owned and ordered by monotonic `report_seq`; an incomplete scan cordons the node
while retaining its last-good inventory, and a non-positive sequence is rejected.
Complete local copies use a source-owned key-scoped counter for deterministic analytics
convergence. Read-through `<asset>.blocks/` caches are deliberately excluded because a
partial cache must never advertise a complete or durable copy. Future tier occupancy may
observe those blocks, but the durability ledger must never read them as evidence.

There is currently no `cluster_storage_backends` table, no `artifact_locations` table,
no durable-copy policy worker, and no per-edge/per-tier occupancy sink.

### Storage-less and thumbnail seams

Thumbnail ownership already demonstrates that serving and byte storage are different
concerns:

- A live stream records a **set** of serving cells in
  `commodore.streams.thumbnail_serving_cluster_ids`, because thumbnails can be minted
  in multiple ingest cells. Register-before-mint is fenced against stream deletion, and
  deletion fans cleanup to every recorded owner.
- Durable artifact rows record a singular `thumbnail_serving_cluster_id` independently
  of the artifact's storage cluster. The served `thumbnails/{id}/{file}` key is
  cluster-agnostic; the serving cell is selected through the Chandler hostname.

These are shipped ownership and cleanup seams. A federated thumbnail mint from a
storage-less cell to an official storage cell, destination-side promotion/attestation,
and a topology rule accepting a reachable rather than in-cell Chandler remain unbuilt.

## Problem / Motivation

Operators and tenants need durability across more than one backend or region, cheaper
cold tiers, promotion to hot edge or memory tiers, residency/sovereignty guarantees,
minimum durable-copy policies, and policy-aware processing output placement. v0.3.0 can
choose where live ingest and serving run, but it cannot express or converge where durable
copies must live.

The product must let FrameWorks change providers and let customers attach their own
storage without changing public asset IDs or losing the catalog of existing bytes.
Operators need to drain a provider, replace an array, recover an unavailable site, and
explain the resulting cost. Tenants need control over which entitled storage and compute
pools their content uses, when background work runs, and what happens when preferred
capacity is unavailable. These workflows and their API/CLI/docs are release deliverables.

## Goals

- A durable, backend-keyed record of complete verified objects per artifact.
- A registry describing backend ownership, kind, region, cost, health, and capabilities.
- Explicit durable-copy obligations: minimum copies, allowed/required regions, residency,
  and tier bounds.
- Per-edge/per-tier occupancy for optimization without weakening durability semantics.
- Policy-controlled processing, storage, replication, and peer decisions using the
  shipped placement constraint/preference model where it fits.
- Two-sided authorization: tenant entitlement, capacity-owner consent, and content-owner
  policy must all permit an action.
- Deterministic, replay-safe convergence and placement eviction that preserves the
  required verified surviving set; explicit authorized asset deletion remains separate.
- Provider onboarding, credential rotation, resumable migration, drain, retirement,
  inventory reconciliation, and catalog recovery with operator-visible evidence.
- Consistent web, GraphQL, MCP, and CLI workflows for policy, cost preview, reviewed
  operations, progress, cancellation, and recovery.

## Non-Goals

- Reimplementing or changing the established meanings of v0.3.0 `ingest` and `serve`.
- Treating StreamRegistry presence, node-copy telemetry, caches, or partial holdings as
  durable-placement evidence.
- Making a shared database the coordination mechanism between Foghorn cells.
- Treating the media-authority signing key as proof that a remote storage provider
  observed or retained an object.
- Implementing service identity or the single-destination durable-replication protocol
  inside this RFC; they are prerequisites with their own RFCs.
- Claiming every provider is supported without an adapter and conformance tests, or
  treating successful replication as a guarantee against simultaneous loss of all
  copies, catalog backups, or encryption keys.

## Proposal

### 1. Storage-backend registry (`cluster_storage_backends`)

Quartermaster owns declared backend configuration. Each row identifies a complete-object
namespace: immutable backend ID, owning tenant and cluster, operating cell, adapter kind
(`s3`, `local_volume`, or a later adapter), physical namespace identity, region, failure
domains, access classes, and declared capabilities. Health and free space are separate
timestamped observations; price facts come from Purser or labeled owner estimates.
Credentials are never policy data and remain scoped to the service that operates the
backend.

Foghorn and infrastructure services report observed state through owner APIs or signed
authority; declared and observed facts are reconciled, never conflated. The registry
generalizes the current immutable per-cell S3 descriptor but does not permit silently
repointing that descriptor. Memory, edge caches, and `.blocks` directories are not
durable backends and never appear in this registry.

#### Identity, attachments, and lifecycle

Keep provider/account identity, backend namespace, access endpoint, credential revision,
and cell assignment distinct. Two credentials or aliases for the same bucket/prefix or
volume are one physical copy and cannot manufacture redundancy. A new bucket, prefix,
array, or provider creates a new backend identity; changing the preferred provider only
affects future assignments. Credential rotation preserves the identity and recorded
locations. Endpoint changes require proof of the same namespace; otherwise register a
new backend and migrate. Preserve the v0.3.0 fingerprint and historical routing tuple
for existing rows through the compatibility period.

Backend attachments to authorized clusters and storage pools are versioned owner grants.
The operating cell can change through a fenced handoff: persist the old owner, establish
the new assignment generation, transfer outstanding obligations, fence the old writer,
then activate the new owner. A move never permits two cells to mutate one namespace under
different ownership generations.

Lifecycle states are `draft`, `validating`, `active`, `read_only`, `draining`, and
`retired`. Reachability, degraded health, credential failure, and unknown capacity are
observations rather than automatic retirement. Drain prevents new placement allocations;
explicitly tracked in-flight work settles or aborts. Retirement requires zero remaining
live references, unresolved assignments, reader leases, and cleanup obligations. Keep
the retired backend record and audited location history after secrets are removed.

#### Provider adapter contract

An adapter implements scoped write preparation, resumable transfer, exact-version stat
and verification, ranged reads or mediated streaming, paginated inventory, and idempotent
exact-version deletion. Optional capabilities include multipart upload, server-side copy,
checksums, object versions, restore jobs, and conditional publication/deletion. Unsupported
operations return a typed capability error; the planner chooses a proven fallback or
rejects the operation before allocating work. Presigned S3 URLs are one implementation
of a transfer/read capability, not the universal storage interface.

The baseline portable path streams from an authorized source into destination staging
and verifies the same content generation before activation. S3 ETags are recorded as
provider version evidence, not assumed to be portable content checksums. Use an explicit
digest algorithm and independently verified bytes when provider checksums are insufficient.
An adapter lacking conditional copy must use unique candidate keys and a fenced ledger
activation that never overwrites an active object.

For `local_volume`, bind the backend to a stable volume identity and configured root.
Verify the mount identity before every mutation; a missing mount must not redirect writes
to the host root filesystem. Fence paths, partial writes, atomic publication, persistence,
and concurrent owners. An enrolled storage agent operates the volume with scoped commands.
A RAID set is one backend in its actual host/site failure domain; member disks are not
independent replicas. Array assembly and hardware repair remain infrastructure operations.

Provider-specific credential validation and disposable-object probes run only on the
selected storage worker inside a dedicated namespace. Customer endpoints require scoped
egress policy (including approved private BYOC networks), redirect/DNS controls, and
redaction. Probing must not turn the central API into an arbitrary network client.

### 2. Durable placement ledger (`artifact_locations`)

One logical row per `(tenant, artifact, content generation, backend, manifest object ID)`
records a durable object: backend, tier, exact object key/version, provider-observed size,
content digest and its verification method, separate opaque provider version/ETag evidence,
lifecycle state, created/verified timestamps, source assignment, and a source-owned
monotonic version. Object roles distinguish media, `.dtsh`, and other required companions;
the manifest object ID distinguishes multiple segments with the same role. Neither a
partial set nor opaque version evidence alone establishes a complete verified location.

The artifact-owning Foghorn cell owns the logical placement view. A destination cell
owns its assignment, verification, and attestation records and exposes them through the
cross-cluster durable-replication protocol; the origin converges its ledger from those
records. Cells do not read one another's databases. The current artifact S3 object is
backfilled as one local ledger location only when its recorded backend evidence is
unambiguous.

An asset ID and playback ID survive storage moves. An immutable content manifest enumerates
required objects, sizes, digests, and relationships; finalized DVR generations include
the relevant segments/chapters and indexes. A generation is complete only when all required
objects are verified at the intended destination. Regenerable thumbnails carry their own
manifest and serving ownership; their absence is visible and does not masquerade as loss
of primary media. Open recordings require a sealed generation or a checkpoint-and-catch-up
protocol before a move can complete.

Location states distinguish planned, copying, verifying, verified, unavailable, corrupt,
deleting, and deleted. An unreachable provider retains its catalog entries and last
verification evidence; it is never reported as an empty inventory. Track desired replicas,
last verified replicas, currently readable replicas, and verification freshness separately.
Persist pending assignments before issuing write capabilities, and keep losing candidates
and incomplete uploads as cleanup obligations. Preserve tenant and provider attribution on
every operation independently of routing changes.

Catalog backup and restore are part of durability. Back up the origin ledger, destination
assignments, manifests, tombstones, authority watermarks, backend mapping, and required
key-recovery references outside the failure domain they describe, using tenant-appropriate
encryption and access control. Export a versioned manifest so an authorized operator can
reconcile provider inventory after control-plane loss. Inventory alone cannot reconstruct
tenant ownership or policy and must never auto-adopt unknown objects. Quarantine unknown,
missing, or mismatched objects; revalidate restored generations before serving or deleting.
Prove recovery with database-loss and stale-backup drills, including tombstone recovery
that prevents deleted content from being resurrected. Report the tested recovery point
and recovery time; do not promise lossless recovery beyond retained evidence.

### 3. Tier state and occupancy

Access classes describe behavior: archive/requires-restore, online durable, edge cache,
and memory working set. Media such as HDD, SSD, and remote object storage are separate
facts. An SSD array can be durable, and an S3 service can expose several access classes;
neither a provider name nor a nominal tier establishes durability or read latency.
Record restore delay, minimum retention/early-removal charges, and range-read capability
where applicable. A durable archive can satisfy an explicitly permitted retention
obligation while failing an immediate-playback requirement. Return restore progress rather
than silently making an unavailable archive the only interactive source.

Durable classes describe complete verified objects; transient tiers and partial
caches are separate occupancy facts. Helmsman emits bounded whole-node tier snapshots or
transitions using the established connection/session fence and monotonic report ordering.
The analytics/media sink retains current state and the event history needed for capacity,
cost, and policy decisions; high-cardinality raw churn has finite retention.

Occupancy can recommend promotion or eviction. It can never satisfy minimum-copy or
residency obligations unless it names a complete object on a registered durable backend
with current verification evidence.

### 4. Policy model

The future schema extends the v0.3.0 `PolicySet` rather than defining a competing policy
language:

- `ingest` and `serve` keep their shipped semantics and enforcement paths.
- `process`, `store`, `replicate`, and `peer` become additional verb-scoped rule sets in
  a new placement schema version. Today's evaluator must continue rejecting those verbs
  until their handlers and capability attestations ship.
- Existing hard constraints retain intersection semantics: allow layers narrow each
  other, denies accumulate, and preferences cannot widen permission. Ordered preference
  groups remain selection/scoring policy rather than authorization.
- Durable desired state is explicit rather than encoded as a preference: minimum
  complete copies, required residency groups, allowed regions, and tier floor/ceiling
  compile into obligations that the reconciler must satisfy before an eviction can run.

An action is eligible only when all three sources agree: Quartermaster entitlement,
capacity-owner consent for the verb/backend, and the tenant/media-object policy. Missing
or stale authority fails closed for creation or migration. If policy becomes unsatisfiable,
the safe state is retain and alert—never delete down to the new target spec.

#### Entitled storage and compute pools

Clusters remain the access boundary. Quartermaster maps explicitly granted resource pools
to backends or enrolled nodes within a cluster; a tenant may narrow its selection to those
pools without gaining access to any other cluster or node. Provider ownership, cell
ownership, customer tenant, and billing payer remain separate identities. A cluster grant
for serving does not implicitly grant storage or processing. Scope grants by verb,
resource pool, quota, permitted data paths, and expiry, with provider consent and the
existing Purser commercial eligibility flow. Platform-official, private owner-operated,
invited private capacity, and marketplace grants preserve their existing provenance rules.

This permits, for example, one tenant to use an invited archival pool, another a dedicated
GPU pool, and both the same serving cluster. Tags and prices narrow authorized candidates;
they are not grants. Processing admission must authorize source reads, scratch/residency,
compute pool, and output backend as a single feasible job plan before execution.

Revocation stops new use within documented authority expiry bounds; it does not erase
existing custody records. Maintain narrowly scoped settlement/cleanup obligations for
previously accepted objects, without continuing revoked user read/write access. Drain and
contract termination must expose remaining bytes, migration options, deadlines, charges,
and failures. If a provider disconnects or removes credentials, retain the catalog and
report inaccessible copies; administrative revocation cannot make physical cleanup true.

#### Replication, timing, and cost controls

Tenant defaults, stream policies, and explicit asset policies select storage classes,
eligible pools/backends, minimum complete copies, and minimum distinct failure domains
(provider/account, site, host, or volume as relevant). Hard requirements accumulate at
every scope; an asset exception needs an authorized change at the scope owning the bound.
Unknown or shared failure domains cannot count as independent copies. Replication lag and
last verification time are visible, and a scheduled or queued copy never counts as durable.

Policies distinguish replication from time-retained backup versions: two live replicas
that follow deletion are not a backup history. Integrate with the existing retention
authority; provider migration does not reset asset age or expiry. Explicit user/retention
deletion tombstones all generations and cancels in-flight copies. The minimum-copy floor
guards placement eviction, not authorized deletion. Provider retention locks that delay
physical removal are reported as outstanding obligations.

Background work has explicit start windows and timezone, bandwidth/concurrency limits,
maximum staged bytes, verification cadence, and budget rules. State whether urgent repair
may exceed a normal window and within which preapproved limits. A budget or window that
prevents repair reports under-replication and its consequence. Cost preferences never
override residency, consent, minimum copies, or freshness requirements for eviction.

Cost preview itemizes stored bytes and replicas, requests, retrieval/restore, egress,
intermediate transfer, temporary double storage, processing/scratch, and early-removal
charges. Separate FrameWorks charges, direct provider charges, and owner-entered local
hardware/energy estimates. Purser owns billed prices and rating; reuse `pkg/billing`
contracts, extending the simple `storagecost` projection rather than treating missing
prices as free. Include currency, units, horizon, quote revision/expiry, assumptions,
unknown line items, and confidence. Compare only compatible bases; no implicit currency
conversion or owner-margin disclosure to unauthorized tenants.

Enforce operation budgets using reservations, incremental metering, and bounded batches;
estimate-only previews cannot enforce spend limits. Reconcile reservations on settlement
or cancellation, and distinguish unavoidable ongoing custody cost from new transfer spend.
External provider invoices may be delayed or unbounded; label their budgets advisory
unless the provider supplies an enforceable bound. Require an explicit authorized
unknown-cost allowance before proceeding when a requested hard cap cannot be proven.

Predicted workload cost from `workload-cost-model.md` and NAT/network classification from
`nat-traversal.md` are optional scoring or eligibility facts for `process`, `serve`, and
peer paths. They do not grant entitlement and do not become durability evidence.

### 5. Signed authority and activation

Do not revive `policy_bundle_versions` as the placement distribution channel. Reuse the
v0.3.0 Ed25519 media-authority envelope and its cell audience, authority version, source
revisions, refresh/hard-expiry bounds, durable delivery, verifier trust set, and replay
rules.

The proposed reuse is two independently versioned payload families inside that envelope;
the trust/recipient design remains an implementation gate:

- Tenant and media-object authority carry content-owner policy and durable obligations.
  Adding future verbs requires a new payload/placement schema version with strict
  mixed-version rejection.
- A new cluster/backend authority kind carries Quartermaster-owned backend descriptors,
  capabilities, and capacity-owner consent. Commodore remains the initial assembler and
  signer from Quartermaster owner-RPC results so media cells keep one trust and delivery
  mechanism; observed health and occupancy remain short-lived Foghorn/Helmsman facts.

Activation extends the current fleet barrier per capability. A cell may attest a future
verb only when every live Foghorn replica installs its enforcement path and any required
Helmsman protocol minimum is enforced. Commodore must not issue or activate policy-bearing
authority for that verb until every target cell attests it. A schema-2 `ingest`/`serve`
pair does not prove `store` or `process` readiness.

### 6. Enforcement hook-ins

- `ingest` — unchanged live publisher/input placement and final admission. It does not
  mean "write the initial durable copy."
- `serve` — unchanged live/stored-media eligibility and final viewer admission, extended
  only when new schema facts intentionally constrain storage tier or residency.
- `process` — dispatch only to permitted compute and write outputs to permitted durable
  backends; predicted resource cost is an input.
- `store` — create, verify, migrate, and evict durable locations to converge obligations.
  Eviction requires a verified surviving set and a durable cleanup obligation.
- `replicate` — authorize proactive or demand-driven complete-copy creation. A block-cache
  fill remains transient and cannot increment durable-copy counts.
- `peer` — authorize cross-cell access and transfer while preserving origin-tenant and
  capacity-owner residency restrictions.

Every mutating hook records the exact authority revisions, selected backend, attempt, and
verification result that justified it. Reconciliation re-evaluates current desired state
but settles an in-flight attempt using its persisted assignment and fencing identity.

### 7. Ownership

- **Quartermaster (`api_tenants`)** — declared backend registry, cluster/backend
  capability, tenant entitlement, and capacity-owner consent; owner RPCs only.
- **Commodore (`api_control`)** — tenant/media-object policy intent, review/apply history,
  authority assembly/signing, durable distribution, activation state, and revocation.
- **Foghorn (`api_balancing`)** — artifact placement ledger, observed placement,
  destination selection, attempt orchestration, reconciliation, and enact-time policy
  evaluation. Each cell owns only its database.
- **Helmsman (`api_sidecar`)** — complete/partial tier inventory, local promotion or
  eviction execution under a fenced lease, and later edge-local verification where the
  operation cannot depend on a control-plane callback.
- **Bridge (`api_gateway`)** — tenant/operator policy, backend, preview, review, apply,
  recovery, and visualization APIs.
- **Periscope (`api_analytics_*`)** — occupancy/current-state analytics and historical
  placement observations, never the durability source of truth.
- **Purser (`api_billing`)** — comparable backend cost facts, customer rating, and
  provider settlement from persisted assignments and provider-observed usage.
- **Chandler (`api_assets`)** — derivative serving/namespace compatibility through
  backend changes, with readiness and cache behavior verified at cutover.
- **CLI/provisioning** — backend lifecycle, credential references, desired-state
  reconciliation, inventory/recovery commands, and audited provider-exit operations.

### 8. Moving assets and retiring providers

Moving storage is a durable job with a stable operation ID. Its reviewed plan binds the
tenant scope, selection snapshot/high-water mark, source and destination generations,
policy/grant revisions, manifest digest, costs and limits, and cutover/cleanup criteria.
New uploads during a provider drain are redirected by a separately reviewed write policy;
existing uploads retain their assigned owner and are included in the drain census.

1. **Plan and admit:** inventory all referenced objects, required companions, incomplete
   uploads, pending jobs, and leases; validate destination capability, consent, space,
   key access, residency, and cost bounds. Unknown state blocks completion.
2. **Copy:** allocate immutable destination candidates, reserve capacity/budget, and
   copy bounded chunks with durable checkpoints. Resume after worker/cell restart;
   use server-side copy only when its scope and integrity semantics are proven.
3. **Verify:** compare the complete content generation and required objects, acquire
   destination evidence/attestation, and retain the source on any mismatch or uncertainty.
4. **Activate:** fence the cutover against content, policy, ownership, and deletion
   revisions. Update the logical location selection; keep public IDs stable and renew
   short-lived playback/source capabilities against the new location. Existing readers
   may finish on the old source under bounded recorded leases.
5. **Retire source copies:** after the observation/reader grace and a fresh surviving-set
   check, record durable exact-version deletion obligations. Retry to provider confirmation;
   distinguish data copied, cutover complete, and source cleanup complete in the UI.
6. **Retire backend:** drain the final inventory delta and all accepted assignments,
   tombstones, leases, and cleanup obligations. Persist a completion receipt and exportable
   manifest before removing credentials or asking the operator to close the provider account.

Deletion after review or during transfer wins the activation fence and enqueues every
candidate for cleanup. Concurrent relocations and evictions serialize on the artifact's
generation and surviving-set revision so they cannot each delete the other's last copy.
Plans for large libraries use keyset checkpoints, bounded worker leases and fair progress;
a poisoned object does not starve healthy work or disappear from the completion report.

Pause stops new batches and settles in-flight work. Cancel preserves verified copies and
records candidate cleanup; it does not undo a completed cutover or resurrect removed bytes.
Rollback while the source is still present requires current policy/consent and a fresh
readability check; after cleanup, moving back is a new copy operation. If a provider has
already disappeared, repair from a verified survivor and retain explicit lost/inaccessible
records for assets with none. No job reports success merely because its queue is empty.

### 9. Management UX and API contract

Deliver management with each usable backend/operation phase. Reuse the placement
preview → review → apply → status/recovery pattern and the existing design system's
slabs, seams, shared forms, and responsive tables. User-facing labels describe storage,
copies, locations, schedules, and costs; signing details and protocol revisions belong
in expandable diagnostics.

| Surface           | Required experience                                                                                                                                                                        |
| ----------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Storage overview  | Desired/verified/readable copies, under-replicated bytes, capacity, queued work, last reconciliation, billed/estimated costs and unknowns; links to explain and repair                     |
| Connect storage   | Provider/adapter choice, owner cluster/pool, secret reference, namespace validation, capabilities, residency/failure-domain declaration, test result and first inventory before activation |
| Policy editor     | Account defaults with stream/asset inheritance, entitled selectors, replica/failure-domain requirements, windows and budgets; explain impossible rules before save                         |
| Asset locations   | Stable asset ID, generation/manifest, current and historical locations, last verification, restore status, active readers, cost, reason for placement, and safe operation actions          |
| Move/drain wizard | Source/destination, affected scope, capacity/egress/double-storage estimate, schedule, review diff, durable progress, pause/resume/cancel, cutover and cleanup shown separately            |
| Operator controls | Pool access/consent/quotas, credential health/rotation, backlog and audit log, inventory reconciliation, catalog export/restore, and backend retirement blockers                           |

Distinguish saved policy, fleet enforcement, and achieved durability. A policy can be
effective while replicas are still converging. Never paint that state as healthy storage.
Show permission-denied, no eligible destination, unknown price, unavailable provider,
insufficient space, checksum mismatch, restore pending, and cleanup pending with useful
next actions. Preserve drafts after conflicts, provide accessible keyboard navigation,
non-color status labels, mobile inspection, and browser refresh recovery by operation ID.

The following are proposed API resources/semantics, not existing endpoints:

- **Backends and pools:** read/list authorized capabilities, validate connection,
  review/apply configuration or grants, rotate secret references, drain and retire.
- **Asset storage:** paginated generations/locations/history, effective obligations,
  verification/restore status, and placement explanations.
- **Policies and previews:** draft evaluation against authorized current inventory,
  inheritance provenance, unmet constraints, costs, observed-at and freshness bounds.
- **Operations:** review/start move, replicate, repair, restore, verify or reconcile;
  read paginated items/events, pause/resume/cancel, recover by idempotency key, export
  an authorized manifest. Backup restore/import is a separate privileged operation.

Owner gRPC services enforce tenant scope; Bridge composes GraphQL, and MCP plus CLI use
the same authorization and operation semantics. Reads, previews, and reviews do not
allocate or write provider objects. Connection probes are explicit bounded mutations.
Apply requires expected revisions and a review binding; retries with the same key return
the same result, and different payloads with the same key conflict. Bind reviews to
backend, grant, policy, selection and quote revisions; revalidate at execution and renew
expired reviews rather than widening scope automatically. Typed errors carry the reason,
retryability, conflict revision or operation ID, and a recoverable next action.

Use cursor pagination, bounded bulk selection/snapshots, canonical decimal-string revisions
and money quantities at JSON boundaries, documented byte/time units, correlation IDs,
and replayable status events. Authorization distinguishes asset read, policy management,
provider/credential management, operation execution, and irreversible deletion; API/MCP
tokens retain their existing scope and high-risk requirements. Secrets and raw provider
credentials never appear in policy results, audit events, manifests, or MCP responses.

### 10. BYOC boundary and delivery documentation

Customer-supplied storage is a resource attachment to the existing cluster/control-cell
model. Scope the data plane to the customer's network and key ownership where requested;
declare permitted workers, transit regions, processing scratch, and catalog/metadata
residency separately from the final object's region. Loss of a customer key or provider
access remains an explicit availability failure. No fallback to platform storage or compute
is permitted without an applicable grant, policy, and cost allowance.

This design supports provider-operated SaaS, current customer-operated edge capacity, and
future storage agents/BYOC pools without implying that whole-stack self-hosted Foghorn is
already supported. Third-party provider service identity, adapter packaging/upgrades,
enrollment, connectivity, and key recovery must pass their own readiness gates.

Release deliverables include canonical architecture and adapter-author documentation;
builder guides for storage policy, costs, inheritance, replication versus backup, and
operation APIs; operator runbooks for S3 and local arrays, provider exit, key rotation,
inventory repair, catalog restore, and incident handling; updated CLI/API/MCP examples;
and a blog post demonstrating a verified provider move and explaining what remains roadmap.
Each usable phase ships its corresponding docs, examples, UX, and support diagnostics.
Capture browser and CLI evidence from real workflows, use synthetic credentials, and
publish only capabilities actually verified for that release.

## Impact / Dependencies

- **Direct hard dependency:**
  [`cross-cluster-durable-replication-v1.md`](cross-cluster-durable-replication-v1.md)
  must exist before a remote ledger location can become verified. Live routing and
  source preparation do not satisfy its mint/verify/attest/status contract.
- **Transitive hard dependency:**
  [`service-identity-and-cluster-binding.md`](service-identity-and-cluster-binding.md)
  remains unimplemented. Media-authority signatures authenticate content policy from
  Commodore; they do not bind a remote provider service to the cluster named in a
  storage attestation.
- **Schemas:** Quartermaster backend/consent state, Foghorn origin ledger and destination
  assignment/attestation state, and ClickHouse occupancy/current-state projections.
- **Proto:** new placement verbs and obligations, backend/cluster authority, durable
  assignment/mint/verify/status messages, occupancy reports, and per-verb capability
  attestation. Edit proto sources and run `make proto`; never edit generated files.
- **Release and migration:** implementation must target the release declared by
  `cli/internal/releases/catalog.yaml` at that time and update its migration/transition
  metadata with the baselines and operator docs. v0.3.0 is tagged: never add this work
  to its immutable shipped migrations. Declare the actual next release before new DDL.
- **Existing foundations:** `BackendFingerprint`, immutable local-backend enforcement,
  `S3Backing.Prefix`, attempt-scoped publication, media-authority delivery, and placement
  activation are reused rather than rebuilt.
- **Packages:** extend `pkg/placement`, `pkg/mediaauthority`, `pkg/clients`, `pkg/proto`,
  `pkg/billing`, and service-owned SQL query sources. Introduce a small provider contract
  with adapters at the storage worker; keep provider credentials/SDK calls out of the
  evaluator and gateway. Use the existing `pkg/graphql` schema/operations and feature
  registry generation paths for user surfaces.
- **Adjacent proposals:** reconcile the durable-replication RFC's S3-specific assignment
  language with the adapter/manifest contract before its implementation. Coordinate
  processing requirements with `processing-pipeline.md` and `workload-cost-model.md`, and
  grants/BYOC with the marketplace and sovereignty contracts; placement does not acquire
  ownership of subscription billing, hardware provisioning, or compute cost prediction.

## Alternatives Considered

- **Extend `s3_url` with a JSON list of URLs.** Rejected: no backend identity, lifecycle,
  verification, tier semantics, or deterministic per-copy convergence.
- **Reuse node-copy or StreamRegistry presence as the ledger.** Rejected: those holdings
  are transient and can be incomplete; conflation would turn routing observations into
  false durability.
- **Revive the HMAC signed-policy-bundle prototype.** Rejected: its producer is retired,
  it has no placement schema, and v0.3.0 already ships audience-bound Ed25519 authority
  with durable delivery and activation evidence.
- **Put all backend inventory in every tenant authority.** Rejected: backend state changes
  independently and would create tenant-wide re-signing fan-out; use an independently
  versioned cluster/backend authority payload.
- **Use event arrival time for ledger ordering.** Rejected: it is not replay-safe; use a
  source-owned monotonic version and persisted attempt identity.

## Risks & Mitigations

- **Policy error causes data loss** → hard minimum-copy floor, verified surviving set,
  retain-and-alert on unsatisfiable policy, and durable fenced cleanup.
- **Forged remote durability** → cluster-bound service identity plus destination-signed
  attestation; origin never trusts the uploading node's completion claim.
- **Schema/capability mismatch widens policy** → strict mixed-schema rejection and a
  per-verb fleet activation barrier; no fallback after a new schema is activated.
- **Split ownership drifts** → service-owned tables and RPCs, source revisions in signed
  authority, no cross-service database reads.
- **High-cardinality occupancy churn** → whole-node snapshots where possible, aggregation,
  rate limits, finite history, and a separate durability ledger.
- **Reconciler oscillation or cost runaway** → hysteresis, bounded concurrency, attempt
  leases, explicit cost ceilings, and idempotent recovery/status queries.
- **Two-sided-consent deadlock** → explain the conflict, retain existing verified bytes,
  and require an explicit policy/consent change before new placement actions.
- **A hardware failure also destroys the catalog** → independent encrypted metadata
  backup/export, retained tombstones and key references, and measured restore drills.
- **Provider aliases create false redundancy** → canonical namespace identity and
  independently attested failure domains; one array or aliased bucket counts once.
- **Migration changes playback or resurrects deleted media** → stable logical IDs,
  content-generation and deletion fences, bounded reader leases, and provider-confirmed
  cleanup before retirement.
- **Missing prices look free or migration costs spike** → explicit unknowns, reviewed
  quote provenance, bounded reservations/transfers, and estimates for temporary overlap.

### Required acceptance evidence

These are release gates for implemented phases, not test results claimed by this RFC.
Use the repository Makefile wrappers for unit, database, media, and browser verification;
add focused adapter/operation harness targets when no wrapper exists.

| Scenario                                        | Required proof                                                                                                                                                            |
| ----------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Add a different S3-compatible provider          | Same contract tests against each supported implementation/version; capability differences rejected or handled, credentials absent from management outputs                 |
| S3 → S3 and S3 ↔ local volume                   | Full manifest verified, IDs/auth preserved, real playback and processing reads succeed across cutover, delayed old-reader cleanup works                                   |
| RAID mount missing or replaced                  | Writes fail on volume identity mismatch, no root-filesystem fallback, one array never satisfies two failure domains                                                       |
| Retry, crash, partition, and worker failover    | Resume checkpoints without losing records, duplicate activation, untracked candidates, or duplicate settlement; unavailable is not empty                                  |
| Concurrent migration, retention, and deletion   | Deletion wins, last-copy eviction race is fenced, no stale authority/backup resurrection, all candidate cleanup remains discoverable                                      |
| Provider drain with ongoing ingestion           | New-write policy and in-flight census converge, final delta includes late completions, retirement refuses remaining references or unsettled cleanup work                  |
| Origin database lost or restored stale          | Rebuild authorized location state from retained backups/manifests and destination evidence; unresolved ownership is quarantined, tombstones remain effective              |
| Access revoked, keys rotated, budgets exhausted | New unauthorized work denied, custody history retained, pending work has explicit settlement outcomes, no silent paid or cross-region fallback                            |
| Tenant storage/compute isolation                | Storage grant cannot authorize compute/serve, pool grants cannot escape their cluster, inventory/preview/export do not expose other tenants                               |
| Authenticated UI, CLI, GraphQL and MCP          | Same reviewed operation and typed failures; refresh/retry recovers progress, effective policy and actual replica health are distinct, keyboard/mobile workflows work      |
| Catalog upgrade and rollout                     | Latest shipped baseline plus pending migrations converges on PostgreSQL/Yugabyte and ClickHouse as applicable; old readers never claim unsupported adapter/verb readiness |

Publish the tested adapter/version matrix and distinguish deterministic object-store
fakes from real provider, database, and media delivery evidence. High-volume tests must
exercise bounded memory, inventory pagination, fairness, and cancellation; a small happy
path cannot establish safe migration of a large library.

## Migration / Rollout

1. Settle the identity/manifest, adapter, access, cost, and operation contracts using
   concrete S3-provider replacement and local-array scenarios. Preserve v0.3.0 live
   routing and track its remaining verification separately. Declare the actual pending
   release before implementation migrations.
2. Add the registry and local shadow ledger, preserving the existing single-backend
   binding. Backfill unambiguous records, expose read-only asset inventory and export,
   and prove catalog restore before changing routing/deletion/billing consumers.
3. Implement/test the S3 adapter against multiple implementations and the `local_volume`
   adapter against real filesystems; document unsupported capabilities. Add onboarding,
   validation and credential lifecycle surfaces with tenant-scoped administration.
4. Land provider identity and single-destination transfer/verification/attestation with
   resumable operations, budget accounting and status APIs. Same-cell transfers between
   two authorized backends may exercise the adapter contract before remote identities
   land; they do not by themselves prove independent failure domains or cross-cell
   durability.
5. Deliver the provider-exit vertical slice: inventory/preview/review, copy, verify,
   cutover, pause/resume/recovery, cleanup, and retirement through web/GraphQL/MCP/CLI.
   Demonstrate real media continuity and publish the operator migration runbook.
6. Add replica/failure-domain obligations, scheduled repair, archive/restore capabilities
   for validated adapters, the new signed schemas and capability barrier, and cost-aware
   reconciliation. Activate create/verify before eviction; show convergence and costs
   with every phase.
7. Extend the baseline backend grants required for every earlier write with finer storage
   and processing pool selection and job input/output constraints for BYOC workers.
   Activate each future verb only after its failure, isolation, and mixed-version
   proofs. Rate/settle from unambiguous observed assignments and usage.
8. Complete authenticated UX/API/CLI parity, adapter and recovery evidence, canonical
   and public docs, release notes, and the provider-portability blog. Advance feature
   registry status only for capabilities proven in the release.

Each phase is additive and separately observable. A later phase cannot use a prior
phase's schema presence as evidence that its enforcement or provider-attestation path is
ready.

## Open Questions

- Exact division between tenant/media-object payloads and the new cluster/backend
  authority payload, including recipient targeting and refresh cadence.
- Whether the origin Foghorn ledger is the only logical `artifact_locations` authority
  or whether Commodore also needs a read projection for management APIs.
- The initial supported adapter/vendor matrix and archive/restore support; packaging,
  worker lifecycle, and key custody for BYOC/local volumes and third-party providers.
- Event log, current-state table, or both for tier occupancy; retention and cardinality
  budgets.
- Exact durable-obligation wire shape for required regions, failure domains, companion
  objects, and tier floors/ceilings.
- Eviction hysteresis, grace periods, and behavior during an unsatisfied or partitioned
  state.
- The failure-domain evidence required for independent replicas, verification intervals,
  and accepted catalog recovery point/time for each offered durability policy.
- Commercial custody/exit terms after entitlement revocation and which external provider
  cost components can be capped rather than estimated.

## References, Sources & Evidence

- [Reference] `docs/architecture/media-placement-policy.md` — implemented live
  `ingest`/`serve` policy, routing, admission, management, and activation boundary.
- [Reference] `docs/architecture/media-authority.md` — Ed25519 authority envelope,
  delivery, storage, expiry, and enforcement model.
- [Reference] `docs/architecture/durable-media-storage.md` — immutable per-cell backend,
  publication, deletion, thumbnail ownership, and inventory invariants.
- [Reference] `docs/architecture/analytics-pipeline.md` — node-copy projection and the
  cache-versus-durability boundary.
- [Source] `pkg/placement/types.go`, `compile.go`, and `evaluate.go` — shipped verb,
  policy algebra, candidate facts, and evaluator.
- [Source] `pkg/mediaauthority/authority.go` and `placement.go` — schema support,
  signing/verification, and paired placement authority.
- [Source] `pkg/proto/media_placement.proto` and `media_authority.proto` — current wire
  contracts.
- [Source] `pkg/database/sql/migrations/commodore/v0.3.0/expand/010_media_placement.sql`
  and `pkg/database/sql/migrations/quartermaster/v0.3.0/expand/011_media_capacity_consent.sql`
  — shipped policy/change and owner-consent state.
- [Source] `api_balancing/internal/control/storage_backend_registry.go` and
  `api_balancing/internal/control/server.go` (`FreezeStagingKey`, publication completion)
  — immutable backend identity and in-cell attempt state machine.
- [Evidence] `docs/platform-features.yaml` — `media-placement` is partial and
  `placement-policy` remains roadmap.
- [Evidence] `docs/architecture/module-map.md` — service ownership and the absent typed
  multi-backend registry.
- [Reference] `docs/rfcs/cross-cluster-durable-replication-v1.md` and
  `service-identity-and-cluster-binding.md` — unimplemented prerequisite contracts.
- [Reference] `docs/architecture/marketplace.md` and `sovereign-strategy.md` — cluster
  access provenance and current hybrid-edge versus future BYOC boundaries.
- [Source] `cli/cmd/cluster_storage.go` and `pkg/billing/storagecost/storagecost.go` —
  existing descriptor inspection and simple storage-cost projection to extend.
- [Reference] `docs/standards/design-system.md`, `schema-migrations.md`, and
  `release-notes.md` — required UX, migration, and operator release conventions.
- [Reference] `website_docs/src/content/docs/builders/storage-and-retention.mdx` and
  `website_docs/src/content/docs/operators/media-storage.mdx` — public storage/retention
  documentation to evolve as implementation lands.
