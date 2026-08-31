# Cluster access security

FrameWorks treats tenant, cluster, node, billing, and capability as independent
authority dimensions. A request-supplied identifier is a claim; it becomes
authority only after the owning service resolves it.

## Current deployment model

The current BYO product is **tenant-hosted BYO edge**, comparable to a
self-hosted job runner. Tenants may operate media workers for bandwidth,
storage, and processing, while Quartermaster and each virtual Foghorn control
cell remain platform-operated. Cloud, VPC, colocation, and on-premises describe
location, not authorization. “Self-hosted media cluster” is reserved for a
future topology where the media coordination plane is also independently
hostable.

Cluster class, ownership, visibility, and access provenance are separate facts:

| Capacity            | New-work authority                                                                                                                                                                                                                                                           |
| ------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Platform-official   | Active cluster plus the tenant's active, subscribed, unexpired tier grant. Connected routing and signed authority apply this same predicate, including when the official cluster differs from the preferred route. Platform-shared playback is a separate serve-only policy. |
| Tenant-hosted owner | Cluster ownership plus an active, unexpired owner grant. No marketplace subscription or enterprise tier-class gate is required for the owner's own footprint.                                                                                                                |
| Invited private     | Accepted, active, unexpired private-invite grant. Ownership is not inherited.                                                                                                                                                                                                |
| Marketplace         | Eligible billing tier plus a paid/approved, active, unexpired marketplace-subscription grant. Neither proof substitutes for the other.                                                                                                                                       |
| Operator override   | Explicit platform-operator grant, with actor and before/after state recorded transactionally.                                                                                                                                                                                |

Effective grant provenance is typed as `platform_tier`, `owner`,
`private_invite`, `marketplace_subscription`, or `operator_override`. Unknown
provenance fails closed. Existing v0.2 rows are migrated in bounded batches by
`quartermaster_cluster_access_provenance_v0_3_0`. The release CLI requires that
job before postdeploy, and the postdeploy migration independently rejects any
derivable row that still has unknown provenance.

The v0.3 contract migration interprets legacy timestamp-without-time-zone expiry
values as UTC before converting them to `TIMESTAMPTZ`. Because PostgreSQL takes
an exclusive table lock for that type rewrite, operators run the contract phase
in the release maintenance window; it is not part of request-time reconciliation.

Custom or owner-approval marketplace listings enter through Purser like every
other marketplace pricing model. Purser records a proof-bound
`marketplace_subscription` row in `pending_approval` with `is_active=false`;
only the owning tenant's approval transitions that row to active. A pending
request is visible to the owner but does not grant media authority. v0.3 rejects
the combination of monthly checkout plus owner approval until a two-proof state
machine can require both without either transition activating access alone.
Cluster invites are deliberately limited to `tenant_private` capacity. Public,
unlisted, or private visibility changes discovery, not authority: an invite can
never bypass marketplace pricing, operator eligibility, approval, or payment.
An unlisted marketplace cluster is omitted from listings but readable by its
direct cluster ID; its subscription still enters through Purser. A legacy
private cluster whose class is `NULL` may use invites only while its visibility
remains `private`; any other unresolved class fails closed.

## Ownership and predicates

- Quartermaster owns node-to-cluster identity, cluster descriptors, and
  tenant-to-cluster grants.
- Purser owns billing, tier, payment, and subscription facts and materializes
  commercial grants through a proof-bound Quartermaster transition.
- Commodore owns stream and artifact identity.
- Foghorn consumes these decisions and owns runtime placement; it does not
  infer access from names, visibility, reported node tenant, or capacity.

A usable grant has `is_active=true`, `subscription_status='active'`, and no
expired `expires_at`. The same predicate is used for entitlement, peer, primary
routing, and alias decisions. `SubscribeToCluster` cannot materialize access;
commercial creation goes through Purser. `GrantClusterAccess` requires a
platform-operator JWT, validates its full input, and writes audit/outbox state
in the access transaction. Tenant invite/subscription reads and mutations bind
their requested tenant to the authenticated tenant JWT (or an explicit platform
operator); request fields cannot nominate another tenant. Direct
platform-official requests are rejected because only Purser can prove the
tenant's billing tier.

Generic workload access and playback sharing are intentionally distinct.
`ClusterAccessibleForTenant` requires an effective grant and is used for
ingest, processing, storage placement, and other new work. The serve-only
`ClusterServeAccessibleForTenant` may additionally admit a resolved tenant on a
platform-official shared edge; callers must not reuse it to write or process.
Durable writes remain restricted to the tenant's official storage cluster and
the cell's immutable, Quartermaster-adopted backend.

## Node and request identity

Foghorn constructs an immutable `NodeSession` when a Helmsman connection is
registered. Trigger node and cluster fields are overwritten from that session.
Viewer admission resolves the resource tenant and checks the authenticated
serving cluster before capacity registration, telemetry, session attachment, or
playback-policy side effects. Artifact and relay serving apply the same final
gate; local or co-served placement is not an entitlement exemption.

Node-reported capability is currently availability and scheduling input, not a
complete authorization credential. Operator-owned allowed-role intersection
and exact task capabilities remain future work; see “Remaining boundaries.”

## Hot-path budget

Security checks may fail closed, but they may not create an unbounded
control-plane waterfall.

- Warm viewer and Mist final admission use local validated projections and make
  no synchronous billing or entitlement RPC.
- The durable projection is a signed tenant+object authority stored in the
  Foghorn cell database, not a process-local cache. It survives Foghorn restart
  and remains authoritative through its signed hard-validity bound while
  Commodore, Quartermaster, or Purser is unavailable.
- `USER_NEW` evaluates the authoritative cluster envelope already carried in
  its resolved stream context. It neither depends on which Foghorn replica
  received an earlier request nor starts a control-plane lookup.
- Readiness is a one-time consumer/schema compatibility cutover and persists
  across subsequently verified authority versions. Before that cutover during
  mixed-version rollout, connected admission refresh has one 500 ms
  operation-owned deadline. Independent
  Quartermaster and Purser reads run concurrently and same-tenant misses
  collapse to one fill. Purser reserves only 350 ms for its database query so
  transport and orchestration retain headroom.
- A full cold route build is detached and collapsed per tenant with a 15-second
  build ceiling, while each media caller obeys its own shorter deadline. A
  timed-out viewer or publisher does not wait for unrelated enrichment work.

- Viewer node selection carries the already-resolved official/peer envelope
  into the balancer and evaluates a serve-only predicate without synchronous
  Quartermaster or Purser calls. Work placement uses the distinct cached
  entitlement predicate. A scoped selection with no installed predicate denies
  all candidates.
- If a tenant has neither a routable preferred cluster nor a platform-official
  fallback, routing fails closed; it never synthesizes a grant or writes a
  fallback during a read.
- `GetTenantAdmissionStatus` returns only admission facts and tier identity; it
  does not load allowances, retention, storage pricing, or processing config.
- Signed denials and tombstones replace prior authority and are delivered to
  historical cells. Legacy cache invalidation remains a compatibility
  accelerator only; it cannot erase or extend signed authority. A new x402
  settlement is online-only and waits for authoritative replacement before a
  previously denied local decision becomes an allow.
- A denial precedes rows, claims, routes, URLs, dispatch, outbox events, and
  billable facts.

The detailed blocking-trigger failure contract is in
[Mist trigger contract](mist-trigger-contract.md).
The signed durability, validity, and key boundary is in
[Media-cluster authority and autonomy](media-authority.md).

## Network and mutation boundaries

Foghorn exposes public playback/ingest/health routes on its public HTTP
listener. Debug, node state, compatibility reads, weights, and node-mode routes
use the internal listener. Reads require the service credential or a
platform-operator JWT; mutations require the operator JWT and derive the actor
from its claims. Mist compatibility on the public listener is limited to a
short-lived, node-and-cluster-bound source capability and never exposes weights,
root diagnostics, arbitrary stream balancing, or mutations. Capabilities live
for two hours and rotate every 30 minutes through the lightweight stream-source
update. Missing four consecutive refreshes eventually denies source lookup;
that bounded availability cost prevents a copied URL from becoming permanent
authority.

Deployed Foghorn containers set `GRPC_METADATA_POLICY=deny`. Ambient inbound
tenant or user metadata is therefore discarded unless a method explicitly
authenticates and reconstructs delegated identity; the compose-level default is
not the production service posture.

Helmsman's node mode, Prometheus-node administration, and trigger-WAL controls
use a loopback-only management listener. Unsafe management binds fail startup.

Federation is disabled by default. When enabled, startup requires an active
platform-official cluster descriptor and authenticated TLS outside isolated
development. Every method requires service authentication. With the deployed
`GRPC_METADATA_POLICY=deny` posture described above, Foghorn also drops
delegated tenant/user metadata from shared service credentials. Payload cluster
identity is still not caller-bound; federation therefore remains restricted to
provider-operated Foghorns.

## Remaining boundaries

The following are not represented as completed security properties:

- operator-owned node-role intersection at every final trigger;
- distinct workload credentials and signed delegation replacing ambient
  `SERVICE_TOKEN` authority;
- caller-bound federation cluster identity and tenant-pair authorization;
- typed, versioned media decision contexts and short-lived exact task
  capabilities;
- cross-cluster durable replication with destination verification and provider
  attestation.

These depend on [Service identity and cluster binding](../rfcs/service-identity-and-cluster-binding.md)
and [Cross-cluster durable replication v1](../rfcs/cross-cluster-durable-replication-v1.md).
