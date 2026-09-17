# Media placement evaluation

## Implementation status

`pkg/placement` contains the evaluator, tenant/stream compiler, protobuf validation,
semantic change descriptions and signed review bindings for `ingest` and `serve`.
Commodore implements scoped persistence and gRPC read/review/apply/change-recovery operations.
Foghorn has an unranked observation adapter, shared routing coordinator, federation transport
contracts, a destination discovery pipeline, and destination-keyed pull tracking. Signed-authority
readers accept placement schemas 2 and 3; schema 3 is schema 2 plus selectors that name nodes.
Legacy connected/shadow comparison cannot promote schema-2 readiness. Matching credentials,
billing, grants, playback access or source descriptors does not prove placement enforcement.
Playback, push-ingest, pull-source, managed-input and artifact-source promotion reject mixed-schema
pairs, and reject coherent schema-2 pairs on a replica that has not installed placement enforcement;
empty placement policy is not an exception.

### Activation barrier

Schema-2 issuance and schema-2 readiness are gated by one attestation chain, not by a flag:

- Every Foghorn replica heartbeats `foghorn.control_replicas` (replica id, release, supported
  placement schema, whether this process installed enforcement). Startup sets the enforcement
  state only after the destination, prepared-source admission, both front-door resolvers and
  USER_NEW admission are installed; nothing configurable can set it.
- `ApplyMediaAuthority` acknowledgements carry `MediaCellPlacementCapability`: the acknowledging
  replica reads the ledger and attests `enforcement_ready` only when it enforces itself and every
  replica seen inside the liveness window enforces schema 2. A failed ledger read acknowledges
  without attesting; absent is not ready.
- Commodore records the attestation per cell in `commodore.media_cell_placement_capabilities`.
  A tenant with no schema-2 history receives its first policy-bearing authority only when every
  current target cell is attested; otherwise the refresh keeps schema 1 and each newly ready
  cell enqueues a `cell_placement_capability_ready` refresh for the legacy tenants it holds.
  Schema 2 is monotonic once published, so a later grant on an unattested cell fails publication
  (and retries with backoff) rather than rolling the tenant back.
- On an enforcing replica, an applied schema-2 authority is promoted read/ingest/source-ready in
  the apply transaction. Commodore only issued it because this cell attested, and routing needs
  a ready pair before any connected admission could have compared it. Shadow comparison of
  schema-2 pairs remains available on enforcing replicas as a second observation.
- Rollout state is derived from those same acknowledgements, inside the acknowledgement
  transaction (`reconcilePlacementActivation`). When every cell the current authority targets has
  acknowledged a schema-2 payload from an attested cell, the policy revision that payload carries
  becomes the scope's active revision and its change is `effective`; an unattested target cell
  marks the change `blocked` with that reason; anything else stays `pending`. Tenant scope follows
  the tenant authority, stream scope follows the live-stream object authority (own revision plus
  the parent revision it was compiled against). The policy/change API reports required, applied
  and pending recipients from the same evidence, and a stored `effective` is demoted to
  pending/blocked while a newly targeted cell has not acknowledged, so "effective" never widens
  silently. Saved intent alone never sets these fields.
- Node selectors (schema 3) follow the same chain. Replicas heartbeat placement schema 3, and the
  acknowledging replica sets `MediaCellPlacementCapability.node_placement_ready` only when every
  live replica in the ledger reports at least schema 3; a replica on an older release would reject
  node selectors as unknown fields. The attestation is a separate flag rather than a listed
  version: `supported_schema_versions` stays `[1, 2]`, because a Commodore without node placement
  treats any listed version above 2 as a malformed attestation and would mark the cell not ready.
  Commodore records a cell at schema 3 only when it is enforcement-ready and flagged, and still
  rejects a listed 3. The issued tenant schema is the highest schema every target cell attests, so
  once every target cell attests node placement, tenants move to schema 3; a cell that starts
  attesting enqueues refreshes for tenants still below it. Schema 3 is monotonic like schema 2.
  Commodore refuses to issue a tenant or stream authority whose policy names nodes below schema 3,
  and signed-authority readers reject node selectors in a schema-2 payload.

Edge-node component versions are not part of the attestation: the barrier covers control
replicas. The node-level input placement ingest admission depends on, MistServer's own
connector name as the fourth PUSH_REWRITE payload line, ships with the same release as the
edge protocol v4 hard cut, so an edge that can connect at all emits it. A publisher whose
trigger lacks the line is refused by placement admission rather than admitted on a URL guess.

### Final publisher admission

`PUSH_REWRITE` runs placement admission after the ingest claim and before materialization
(`checkIngestPlacement`, installed by `ConfigureLiveIngestPlacementAdmission` from the same
public runtime as the front-door resolvers). Inputs are the claimed cluster and authenticated
node, the tenant/internal name from the validated key, the publisher address for geography
and `observed_connector` mapped through `mist.IngestProtocol` (RTMP, TSSRT and WebRTC are
publisher connectors; DTSC, HTTP, RTSP and file-style connectors are not evaluable and are
refused). The push URL is never an input. A denial releases the claim through the existing
deferred release, so a refused publisher holds nothing. The real-Mist contract
`make verify-mist-push-connector` proves both an RTMP and an SRT publisher attest their
connector on the image under test.
Readiness mutations bind the media object to its tenant and exact authority version in SQL,
including object-only promotion when the tenant is already ready. Paired promotion commits
both markers together; a mismatched object rolls back the tenant marker as well. These
identity fences do not substitute for schema-2 enforcement capability evidence.
Quartermaster exposes a service-only, tenant-scoped membership inventory; Foghorn can join that
inventory to unfiltered telemetry without treating missing nodes as an empty preferred pool.
Quartermaster also persists capacity-owner consent and includes its observed revision and
permissions in the service-only entitlement RPC. Tenant-owner management gRPC read/review/apply/
recovery operations are implemented. Bridge connects tenant/stream and capacity-owner GraphQL
read/review/apply/recovery to those services.
Authorized cluster/operator/region/node selector options are connected through Bridge and Commodore
to Quartermaster entitlement. Read-only capacity preview is connected through Bridge and
Commodore to every entitled media cell and conditional fresh Purser quotes. Owned push-stream
serving previews additionally observe the current registered publisher. Managed and artifact
source preview remains open: those kinds preview capacity only, even though routing places them.
The web application has shared account/stream editing, explicit preview/review requests, and
same-key save recovery through generated frontend GraphQL operations.
Owned media-cluster detail pages also expose capacity consent read/review/apply/recovery controls.

Bridge's MCP tools call the same placement and consent resolvers as GraphQL, including authorized
options, preview, exact-revision review/apply and same-key recovery. Inputs
share the GraphQL model's camelCase fields and decimal-string revisions. Results use an explicit
`type` discriminator and `value`; typed errors retain recovery fields in structured content and
the JSON text fallback with `isError` set. Tool scope and tenant-action checks are enforced at
the MCP boundary and again by the resolver. Review requires management permission but is
read-only; apply is high-risk, destructive-adjacent and idempotent. API tokens need `mcp:high-risk`
in addition to `placement:write` for apply. Management and recovery remain available without a
funded balance; no MCP call bypasses owner consent, the reviewed command, or fleet activation.

Foghorn startup connects signed candidate discovery to the authenticated federation endpoint,
using Quartermaster membership and local/peer publisher observations. Missing signed authority or
inventory dependencies remain unavailable. Startup also assembles receipt-backed destination
preparation and admission for placement-marked source pulls, installs the HTTP and gRPC ingest and
viewer front-door adapters and the stored-media permitter, and installs final publisher and viewer
admission. Installation is unconditional; what the attestation barrier gates is policy issuance.
The stored-media permitter latches on installation: before startup installs it a replica would
serve storage candidates unfiltered, and once installed a cleared permitter refuses rather than
reverting to that. The guarantee therefore depends on installation preceding listeners, which the
unconditional startup ordering provides.

Viewer admission is for audiences. Two platform reads reach the same Mist triggers (PLAY_REWRITE,
USER_NEW) and are recognised by credential instead: an accepted origin pull (DTSC, `fwsrc.` per-attempt
credential) and a processing source read (`fwproc.`, minted by the dispatching Foghorn for one job,
bound to the tenant, source stream, serving node, artifact and an expiry, signed with the cell's
balancer capability secret). Helmsman stages a clip's source as a Mist `/view` cut of the live buffer,
rolling DVR or chapter on the node that holds those bytes; that node is the ingest node, which a serve
policy preferring another cluster does not permit as a viewer destination. The credential is what lets
the read through: no placement admission, no viewer session, no capacity reservation, nothing
reported to analytics, and the credential is redacted before the trigger leaves the process. Without a
valid credential the read is a viewer and is judged as one.
Managed and artifact source preview, and end-to-end verification of the web editor in an
authenticated browser, still require integration. The coordinator-relative score penalty is removed
from the legacy scorer, which no longer selects live destinations; it now serves only stored-media
ranking and the candidates this cell returns to a querying peer. Current routing is described in [viewer routing](viewer-routing.md).

### Exact-destination ingest preparation

`LiveIngestPreparationRuntime` confirms the selected push-ingest node's current advertised
listener, protocol, heartbeat, capability and signed authority. It runs inside
`PolicyBoundPlacementRuntime`, which reconstructs the complete policy census, checks directional
capacity and the active-ingest ownership fence, and revalidates after preparation and on replay.
`PlacementMediaRuntime` selects the ingest or serving implementation without falling through to
another verb when a handler is unavailable.

An accepted ingest preparation returns a credential-free publishing template with one `$`
placeholder, not a stream key. WHIP public proxy paths, RTMP listener ports and the live application,
and SRT streamid placement use the same parser as ordinary ingest URL resolution. The requesting
front door binds its key with `mist.BindIngestEndpointTemplate` only after receiving the exact-node
acknowledgement. The wire validator rejects concrete credential-bearing URLs, unresolved hosts,
wrong-protocol templates and publisher readiness claims. No source pull or ingest ownership lease
is created by preparation; final publisher admission must still claim ownership atomically.

This preparation runtime and its public routing consumer are installed together at startup, because
the placement contract ships as one piece and a replica that installed only part of it would attest
enforcement it does not perform. HTTP and gRPC ingest front doors share one
prepared-endpoint consumer, installed through `SetIngestPlacementPreparer`. The required-path
marker is sticky: removing its adapter cannot restore legacy ranking. The adapter receives
validated tenant/stream/internal identity and publisher coordinates, never the publishing key.
`IngestPlacementResolver` compiles current signed authority, reads connected ownership and invokes
the common routing/preparation chain, then rechecks authority before returning. It does not use
the connected response's default origin as an ownership decision.

An explicit protocol prepares only that protocol. An unqualified request prepares WHIP, RTMP
and SRT independently within one five-second budget and may omit a protocol with no permitted
destination. Ambiguous preparation errors abort instead of silently trying another protocol or
legacy node. Only confirmed URLs are returned; results for the same exact node are merged, and
different protocol destinations remain separate. Earlier confirmations must still be unexpired
when the combined response is returned. Preparation includes the destination's advertised HTTP(S)
origin separately from its listener template, preserving real node identity in API/Go Live UX
without guessing an HTTP scheme or port from RTMP/SRT. Replay rechecks both fields.

Both front-door setters are installed by startup from the configured destination
(`ConfigureLivePublicPlacement`): the gRPC and HTTP ingest resolvers, the gRPC and HTTP viewer
resolvers and USER_NEW final viewer admission share the destination's policy gate, ownership fence
and discovery router. Installation is unconditional whenever signed media authority is configured;
what remains gated is issuance, described under "Activation barrier" below. These tests prove
listener/receipt contracts, not end-to-end publisher admission or actual media delivery.

The two-cell ingest integration test composes the routing coordinator, authenticated federation
transport, destination discovery, connected ownership reader, policy gate and receipt-backed
ingest runtime. It checks both coordinators against the same US/EU inventory, including separate
east/west nodes inside the US cluster, for WHIP, RTMP and SRT. Coverage includes self-hosted-first,
capacity-only and geo-hole fallback, never-spill, official-cluster exclusion and active ownership.
Unknown preferred-pool capacity cannot authorize conditional spillover; an unrestricted pool may
still use a known permitted node when another cell is unavailable. Authority, membership and
telemetry are controlled inputs and the receipt engine is simulated. This is integration proof
of the routing/preparation chain, not proof of startup wiring, signed rollout or actual media.

### Source-connection admission integration

Mist's existing `CONN_PLAY` hook has a typed Helmsman-to-Foghorn path, separate from
`USER_NEW`. Helmsman bounds requests to 16 KiB and three seconds, forwards DTSC
connections without assigning the infrastructure owner's tenant to the stream, and
accepts only an explicit boolean allow. Empty replies, rewrite URLs, KEEP, errors,
cancellation and missing replies cannot admit a source connection. Ordinary output
connections do not acquire another viewer slot through this hook.

Foghorn uses the emitting node and cluster from the authenticated control connection.
The hook's remote address remains an observation, not a verified pulling-node identity.
The source gate checks the runtime/URL binding and bounded decision validity; an absent
authorizer denies. Retries re-evaluate authorization instead of using the ten-minute
blocking-trigger replay cache. The hook is neither a durable accounting event nor a
first-media acknowledgement, and its request URL is not forwarded to analytics.

The origin admits its own arranged pull without this hook. A peer's replica opens a DTSC
connection at the owning node, which Mist reports through `PLAY_REWRITE` and `USER_NEW` like
any output. Final viewer admission would evaluate the origin node against the viewer policy
and refuse the replica whenever the origin is not the selected viewer destination. Instead,
`StreamRegistry.AcceptedOutboundPull` returns the pull the origin accepted through
`NotifyOriginPull` for the connecting node's current active publisher generation, renewed
within five minutes; a DTSC connector that matches it is the prepared source path and is
neither placed nor fenced as a viewer (`acceptedSourcePull`). Any other node, a superseded
generation or an expired record is a viewer. The exemption binds the connecting destination:
the origin issues a per-attempt credential carried in the DTSC URL query, keyed over tenant,
attempt, source node/generation/revision and the destination cluster/node, and verifies it
before treating an arriving DTSC connection as the prepared source path. A matching remote
address cannot claim another destination's pull. Identity comparisons for that pull are on the
credential-free media path, so reissuing the credential on renewal, or rotating the signing
secret under it, does not read as a different physical pull. The credential travels only to the
destination control plane that must dial with it: routing events, federation events, operator
diagnostics and the tenant playback webhook all carry the base URL, and a refused source pull
that falls through to viewer evaluation has its credential stripped before the webhook sees it.

Foghorn startup connects this gate to the destination's receipt-backed push preparation
(`ConfigureLivePreparedSourceAdmission`), which is why an absent authorizer denies rather
than admits. `CONN_PLAY` itself is not part of Helmsman's default Mist trigger configuration:
an origin admits its own arranged pull through the accepted-pull exemption above, and this
hook remains available where a deployment needs a separate source-connection decision. The
design does not require Mist to understand tenant placement, pricing or capacity-owner policy.

The online callback path is an integration primitive, not the selected production
latency model. Source authorization should be established during destination
preparation/source resolution where possible, with reuse of an already authorized
pull. A final check must not automatically add another remote Foghorn round trip
or repeat preparation for every viewer. Any local admission cache must be bound to
the exact authorized operation, authority version, node incarnation and expiry;
missing or stale entries cannot widen policy. Hook installation requires a demonstrated
enforcement need and tests of media delivery, pull reuse, reconnect behavior and
control-plane outages. First-frame benchmarking is not an architectural prerequisite;
use it to investigate latency or compare otherwise viable integrations. Timeout ceilings
are not latency targets, and separate hook timeouts must not silently extend the
end-to-end connection budget.

`make verify-mist-source-admission MIST_CONTRACT_IMAGE=<exact-image>` exercises the
existing hook using real DTSC and replica HLS playback. The image must include Mist
and ffmpeg. A fixed allow must deliver MPEG-TS packets; a fixed deny must reach the
connection gate and expose no media segments. This tests the Mist primitive, not
tenant authorization, authenticated peer identity, cross-cell routing, or HA. A local
cached-image pass is not verification of the release image or handler-failure semantics.

Storage and processing placement, proactive replication, and durable-copy tiering remain
separate future work in the [placement RFC](../rfcs/placement-policy-engine.md) and
[replication RFC](../rfcs/stream-replication.md).

## Inputs and ownership

Evaluation consumes an explicit timestamp, the consuming tenant, one media verb, optional
client coordinates, compiled rules, and authorized candidate facts. It performs no I/O,
discovery, reservations, billing, or source preparation. Input candidates must come from
verified capacity/authority observations, never from user-supplied preview facts.

Candidates include node and cluster identity, tenant-relative ownership/class, allowed
verbs, region, telemetry timestamps, node health/capacity, native bandwidth limits,
stream presence/source feasibility, and optionally fresh comparable pricing. Policy does
not grant access to an unsubscribed marketplace cluster or replace capacity-owner consent.
An active ingest cluster is a hard fence; preference cannot move an active claim.

### Public management API boundary

Bridge's `mediaPlacementPolicy`, `reviewMediaPlacementChange`, `applyMediaPlacementChange`
and `mediaPlacementChange` call Commodore's scoped management RPCs. Tenant and actor come from
authenticated context, never policy input. Reads require tenant membership; review/apply also
require owner/admin authorization. API tokens independently need `placement:read` or
`placement:write`. A public-operation allowlist cannot substitute for either token scope.

GraphQL revisions remain canonical decimal strings throughout conversion, including values
above JavaScript's safe-integer range. Inputs preserve absent versus explicit empty allow and
preference lists, reject unknown enums and invalid update shapes, and use the shared compiler
for semantic validation. Requested effective rules use the evaluator's compiler and digest;
the unconfigured nearest-entitled default is distinct from explicit empty groups. Requested
intent is separate from `activeRevision` and rollout status; saving a pending change does not
claim destination enforcement.

Commodore's all-zero activation defaults mean no activation evidence. They produce an absent
active policy, and Bridge exposes null active revisions, including when an older producer sends
an empty active-policy message. An explicitly activated inherited-only stream can have active
stream revision `0` with a positive active account revision; this is distinct from no evidence.
Bridge rejects future active revisions, parent state on account policies, recipient overcounts,
and effective claims whose active policy/parent or recipient state does not match the request.

Review tokens, warning acknowledgements and idempotency keys cross unchanged. Bridge bounds
management calls to six seconds, preserves a shorter caller deadline, and does not retry an
ambiguous write with a new key. Recovery queries the exact scope/key. It verifies response
scope, key and committed revision before presenting success. Typed conflict/stale-review/
idempotency/unavailable results drive the editor state machine; private service diagnostics
are not copied into public error messages. Unsupported downstream states are errors, not
implicit successful or active defaults.

The four `clusterMediaConsent` management fields use Quartermaster's owner boundary separately
from consuming-tenant placement. Service/wallet identities cannot act as capacity owners, and
platform-operator status does not bypass tenant ownership or role checks. Source permission
booleans, exact revisions and review identity are preserved. Where a stream's source may run is
part of its own ingest rules, read and written through the stream's source location (see
[Source location](#source-location)); there is no separate pin surface in GraphQL or MCP.

GraphQL integration tests execute serialized read/review/apply/recovery operations for both
policy and consent, exercising non-null lists, typed unions, exact revisions, explicit false
flags and pending rollout. Resolver tests additionally cover missing actor/tenant, role/token
scope separation, operator bypass refusal, cross-scope replies, malformed downstream state,
redaction and ambiguous-write recovery. Downstream services are test doubles here; this is not
authenticated browser-to-database/media proof. Demo management currently refuses explicitly
without contacting real services; a complete interactive demo remains required. Managed and
artifact source preview, and verification of the authenticated editor and owner workflows in a
real browser, remain open.

### Capacity-owner web controls

The owned edge-cluster detail page mounts `MediaCapacityConsentEditor`. Infrastructure ownership
controls page visibility, while the consent API's independent `canManage` result controls editing;
a broad operator role does not grant capacity-owner authority. The three permissions are accepting
publishers, serving viewers, and pulling sources from other clusters. They neither subscribe a
tenant nor change prices or credentials. Consumer policy may narrow, never override, owner consent.

`ConsentSession` binds requests to authenticated tenant/actor/role and cluster. Editing invalidates
reviews and acknowledgements. A conflict retains the draft and requires an explicit current-revision
reload and re-review. Review expiry and all required warnings gate apply. Ambiguous saves freeze
the exact command and key; recovery may resubmit it only after a missing receipt, never silently
mint a replacement. Receipts must match cluster, key, reviewed digest and the next exact decimal
revision. Navigation warns before losing drafts or in-memory recovery; pending state is not durable
across a closed page. Logout or identity changes discard local state and suppress old responses.

The UI shows requested revision and recipient rollout separately from save success, including
incomplete impact counts. No custom rules at stream scope does not mean account rules are absent
or already enforced; inherited requested and active account revisions remain distinct. Idle
pending/blocked and inherited-unconfirmed rollout refreshes with bounded backoff without replacing
drafts or uncertain writes. Component/state tests and the labelled loopback fixture cover these
controls; authenticated browser-to-Quartermaster and enforcement acknowledgements remain gates.

### Authorized selector options

`mediaPlacementOptions` accepts tenant/stream scope, optional text/kind/class filters and cursor
pagination. Bridge enforces membership and `placement:read` for API tokens; Commodore derives the
tenant from authenticated context and checks stream ownership before calling Quartermaster's
service-only entitlement API. Each page reconstructs the current authorized catalogue. It neither
initializes policy rows nor subscribes to capacity, obtains billing quotes, reserves a slot or
prepares a source.

The allowed-cluster list must exactly match effective-access rows. Missing, duplicate, expired,
inactive or unclassified grants are unavailable, not a confident empty result. Only media-edge
clusters become selector options. NODE options list the registered nodes of edge clusters the
tenant owns (tenant-private class), read from Quartermaster's placement inventory per control
cell, each carrying its `clusterId`; the `clusterId` filter limits NODE options to one cluster.
An incomplete or mismatched inventory makes the page unavailable rather than listing fewer nodes.
Platform-official and marketplace node IDs are never offered. Ownership is relative to the consuming tenant: an owner's own
marketplace cluster is private for that owner, while official identity remains official. Region
IDs come from the owning cluster's SQL-to-entitlement projection; unknown regions are omitted,
not inferred from the resolving Foghorn. Region edits enqueue subscriber authority refreshes so
signed region constraints do not wait for an unrelated metadata change.

Projection exposes authorized IDs and labels, optional class/region/operator, and coarse owner
permission status. It omits nodes of clusters the tenant does not own, addresses, source URLs and
commercial details. Mixed
operator/region classes are null rather than arbitrarily choosing a class. `eligible` means an
entitled target has observed owner permission for at least one of ingest or serve; it is **not**
current node availability, both-verb consent or a successful policy evaluation. Missing consent
is unknown; explicit denial remains visible with an explanation. Selecting a currently unusable
target can express future intent but never grants admission.

Pages default to 50 and are capped at 100. Opaque cursors bind authenticated tenant, exact scope,
normalized filter, deterministic catalogue digest and position. They are positions, not authority:
the service rechecks current access on every request. Cross-scope/filter cursors are invalid;
catalogue changes return a typed revision conflict requiring a fresh search. The web selector
clears stale results/cursors on failures, ignores superseded replies, retains draft selections
and presents an explicit empty-search state. Service calls are bounded to three seconds in
Commodore and four in Bridge, or the caller's shorter deadline.

Tests cover redaction, tenant-relative ownership, complete grants, stable ordering and cursor
isolation; PostgreSQL/Yugabyte contracts cover stream isolation and read-only policy behavior.
Quartermaster SQL and RPC tests cover region projection, while real engines verify region-triggered
authority refresh. Component tests cover pagination, catalogue changes and revoked access. These
seams are not a substitute for the remaining authenticated browser-to-live-placement proof.

### Read-only capacity observation for preview

Foghorn's internal `MediaPlacementControlService.ObserveMediaPlacementCapacity` is a
service-authenticated management read, registered independently of active signed placement
policy. Its request carries consuming tenant, exact control cell and cluster census, media verb,
protocol and an optional caller-verified owned stream name for node allowlists. It carries no
draft rules, prices, source credentials, preparation identity or admission token.

The observer obtains fresh entitlement and complete membership from Quartermaster, then joins
that membership with the unfiltered Foghorn telemetry snapshot. Owner permissions and region
come from entitlement, never node telemetry. It preserves missing/stale nodes, independent
listener freshness, directional bandwidth and unknown membership; it neither ranks nor
geo-prunes. Without a stream name, a node restricted to named streams remains unknown, not
available or confidently exhausted. Quartermaster reconnects update the atomic client holder;
each observation uses one captured client for both owner reads.

Observation is bounded to three seconds and expires within the earliest relevant access or
membership lifetime. The response decoder binds exact tenant/cell/cluster/verb/protocol/stream
scope, caps node count and lifetime, and rejects source-presence, source-feasibility or commercial
claims. The capacity service has no media preparation, reservation or receipt dependency.

`placement.EvaluateCapacity` uses the same policy, entitlement, price, freshness, geo, health,
ingest fence and spillover evaluation as live routing, but returns a distinct `CapacityDecision`.
It discards unevaluated source facts rather than fabricating `SourceFeasible`, and cannot emit
a `RequiresPull` claim. Live `Evaluate` still requires actual source evidence for serving. This
is a capacity-only observation, **not** a source-aware preview or media admission.

Bridge's `previewMediaPlacement` query calls Commodore's `PreviewMediaPlacement` RPC. Reads require
tenant identity and placement-read authorization; API tokens need `placement:read`. An optional
draft changes only the selected verb and requires both exact base revisions. Tenant drafts tested
against an owned stream still intersect that stream's stored hard constraints. Stream ownership
and revision checks precede all external inventory reads. The read never initializes policy rows,
saves a draft, promotes authority, subscribes capacity, reserves resources or prepares media.

Commodore verifies current tenant/billing admission, builds the complete entitled edge-cluster
census and queries every control cell with at most four concurrent calls. An unavailable address,
timeout, invalid scope, or changed owner/region/consent makes that cell unknown, not empty or full.
The observation census is bounded to 64 cells and 4096 nodes. Price-sensitive rules additionally
obtain a current Purser quote bound to tenant, preview scope, draft digest, base revisions, exact
cluster census and each distinct comparison basis. The entitlement digest must match the captured
owner evidence. Missing quotes remain unknown and cannot become free or cheapest capacity.

After observation, Commodore rechecks saved intent and stream identity/ownership. Concurrent edits
return a revision conflict. A current push-ingest claim fences the preview to its existing cluster;
an expired claim does not. Output expires at the earliest observation, access, quote or claim
deadline. The public query is bounded to nine seconds around Commodore's eight-second deadline.
Reasons, transitions, geographic distances and applicable comparison prices come from the shared
evaluator. Private node IDs require infrastructure-read authorization and ownership of that cluster
(or platform-operator authorization); ordinary users see cluster-level explanations.

Streamless previews remain capacity-only and report `sourceEvaluated: false`. Owned push-stream
SERVE previews use a separate service-authenticated `ObserveMediaPlacementPushSource` RPC at the
current ingest claim's cell. It joins registered membership to telemetry and reuses the live
publisher reader's tenant, generation, revision, withdrawal, heartbeat, full-buffer and listener
checks. It accepts no draft authority and returns no source URL or credentials. A replica or an
unregistered/disabled node cannot become the publisher. A configured registry-cell alias keeps
the local source namespace distinct from the control-cell identity used for RPC authorization.

The source observation and every capacity observation carry complete owner-consent evidence,
including revision and `allow_external_source`, which must match Commodore's captured entitlement.
Missing maps, changed consent and unknown future wire fields fail observation rather than combining
new telemetry with older permission. Source evidence is independently bounded by registered
membership, owner access and media observation lifetimes; it cannot extend the preview deadline.

When verified source evidence exists, the shared live evaluator checks source paths as well as
capacity. The exact publisher has presence; another node needs a valid pull path. Cross-cluster
pulls require destination external-source consent, while another node within the publisher's
cluster does not become an external cluster. Destination preference exclusions do not eliminate
an otherwise entitled ingest-only source. `sourceEvaluated: true` and `requiresSourcePull` describe
these observations, not reservations, completed pulls or first-frame proof.

Missing/expired ingest claims or unavailable publisher evidence leave owned-stream SERVE preview
incomplete with no selected destination; they do not fall back to a capacity-only playback answer.
The editor distinguishes unevaluated source paths from observed paths that have not started media.
Non-push managed-source previews return typed unsupported pending their source-specific adapters.
Managed and artifact source preview remains an explicit full-feature gate; it is the preview
surface that is push-only, not routing.

### Capacity-owner persistence

Quartermaster stores `allow_ingest`, `allow_serve`, and `allow_external_source` independently
of a consuming tenant's placement preferences and access grant. Revision zero is the explicitly
observed unconfigured default: all three permissions remain allowed. This preserves existing
behavior during migration; it does not create a tenant entitlement. A missing consent message
remains unknown, not an implicit allow. External-source permission governs whether this
destination cluster may source content from another virtual cluster; it is separate from
the private-network pull-source setting.

`MediaConsentStore` reads only currently owned clusters. Apply locks the cluster, checks its
immutable record identity and expected revision, validates the review against locked state,
then commits consent, an immutable command receipt, and subscriber authority-refresh outbox
entries together. The caller still must authenticate and authorize owner-management access.
An exact committed retry can recover after its review expires; changing its actor, permissions,
review digest or warning acknowledgements conflicts. Ownership transfer prevents both former-owner
recovery and disclosure of the old owner's commands to the new owner. Recreating a public cluster
name cannot replay the previous cluster record's command. Platform-owned clusters without a
tenant owner are not accessible through this tenant-owner store.

Consent and entitlement are projected by the same `ListTenantEffectiveAccess` query. Inactive,
expired, pending, and unknown-source grants are not made eligible by permissive owner settings.
Commodore's signed policy-review context includes observed entitlement consent, so changing owner
permissions invalidates an unapplied review even while live signed authority still uses schema 1.
The v0.3.0 baseline, expand/postdeploy migration, generated queries and startup capability checks
carry this persistence contract. Saving consent is not an enforcement acknowledgement: the compiled
rules must still be issued to every target cell and acknowledged there before the change is
effective. The public owner API and web controls are implemented; authenticated browser-to-owner
and live enforcement proof remain.

Quartermaster's `GetClusterMediaConsent`, `ReviewClusterMediaConsentChange`,
`ApplyClusterMediaConsentChange`, and `GetClusterMediaConsentChange` require an authenticated
tenant user. Members/viewers may read their tenant's owned-cluster consent; owner/admin roles
may manage it. API tokens additionally require `placement:read` or `placement:write` for the
respective operation. Service tokens cannot act as a consent owner, and these methods do not
offer a platform-operator cross-tenant or null-owner override.

Reviews are signed by a dedicated Ed25519 key configured through
`CAPACITY_CONSENT_REVIEW_KEY_ID` and `CAPACITY_CONSENT_REVIEW_PRIVATE_KEY_PEM_B64`. Both may
be omitted, leaving read and committed recovery available but new review/apply unavailable;
partial or malformed configuration prevents startup. The CLI's
`cluster secrets generate-capacity-consent --out <new-file>` produces a private, non-overwriting
fragment. Fresh `generate-shared` includes an independent signer. Production rendering strips
the tuple from every service other than Quartermaster; Compose passes only the explicitly
configured tuple and has no implicit development signer.

Review tokens bind actor, tenant, cluster record identity, current consent, proposed permissions,
and required warning acknowledgements. Each changed permission has a semantic difference and
required consequence warning. Impact remains explicitly incomplete, not an invented count of
affected viewers. Apply revalidates the signed review against locked state. Exact committed
retries may recover with no token or current signing key, but still require current owner and
actor authorization plus the same command and acknowledgements. Reads and receipts report
`not_configured` or `pending`; neither claims completed edge enforcement.

### Commercial quote projection

Purser's `pricing.PlacementCommercialFacts` projects an already resolved tenant tariff into
charging classification and optional comparable prices. It uses the same decimal rating engine
as invoice/prepaid calculations, not a parallel unit-price formula. The projection and Purser's
commercial owner RPC, signed object quote validation and Foghorn quote compilation are
implemented. Commodore's schema-2 object persistence consumes policy-required fresh owner quotes and schedules
deadline-driven renewal. Tenant refresh and revocation preserve an established schema-2 history,
and object builders follow that tenant schema. A tenant's first schema-2 issuance waits for an
active refresh and for every target cell to attest enforcement, so revocation semantics never
depend on a barrier. That first issuance is opportunistic: a tenant whose owners have not yet
consented, or whose saved intent is incomplete, keeps its legacy refresh instead of losing every
refresh, while an established schema-2 tenant follows the strict inheritance path.

Self-hosted consumption and explicitly unmetered-free capacity are permanently free. Monthly
access, zero-priced tier rules, disabled subscription metering and beta waivers do not become
permanently free capacity. A quote is the incremental **unwaived media usage charge** for an
explicit additional consumption bundle; it excludes already-paid fixed access/subscription fees,
taxes and unrelated storage/processing. It is not an invoice forecast or permission to subscribe.

Canonical price units preserve the entire comparison basis: `ingest:gib=2` means two additional
GiB ingested; `serve:minutes=60;gib=2.25` means sixty delivered minutes plus 2.25 GiB delivered.
Despite their `_gb` names, the ingress/egress meters use GiB. Parsing rejects ambiguous forms,
noncanonical decimal spelling, negative quantities and unknown verbs. No allowance consumption
is inferred from the unit. Two different quantities/ratios have different comparison units.

Serve quotes require both minutes and egress quantities, including explicit zero where known.
Included allowances additionally require observed usage for their actual allowance period.
Missing metering-switch evidence, unknown quantities, missing dimension evidence and custom
meters whose ingest/viewing role is unknown leave the price absent. Classification can remain
known without a quote. Non-integral micro-unit amounts, overflow and rounded unit conversions
also leave the quote unavailable rather than manufacture a cheaper zero value. Inputs and decimal
exponents are bounded; semantic revisions are independent of map/rule/quote-collection order and
lease renewal.

Commercial facts carry up to sixteen distinct currency/unit prices per verb, including the
singular compatibility slot used by candidate transport. The owner RPC uses explicit repeated
collections. Purser can project additional ingest and serve bundles from the same resolved
snapshot. Duplicate comparison bases and conflicting observed consumption are rejected.
Signed media objects carry at most one complete `CommercialQuoteResponse` per verb, retaining
scope, entitlement digest, usage evidence and unavailable reasons. Tenant grant commercial facts
are **classification only**: comparison prices on them are rejected by signing and compilation.
All prices in each object quote share that commercial snapshot's revision and expiry.

The evaluator selects the exact fresh currency/unit quote configured for each candidate's
first matching preference group; it does not rewrite candidate facts or compare prices across
groups. Capacity spillover uses the next group's own basis. An unavailable preferred quote
remains uncertainty, not evidence of exhausted capacity permitting a spill.

The owner RPC provides an entitled, transaction-consistent tier/override/history/usage snapshot
and caps expiry at known pricing/access boundaries. Public comparison-basis options and live
policy activation remain open; supporting quote collections is not
live commercial routing activation. Pricing ownership lookup rejects responses
for another cluster, zero owner UUIDs and noncanonical owner identities.

`pricing.ReadPlacementTariffs` now provides the tariff portion of that snapshot. It reads the
database clock, active subscription, effective tier/rules/overrides/entitlements and every
requested cluster's pricing history in one read-only repeatable-read transaction. Ownership
must be captured from Quartermaster before opening it; the transaction-only resolver has no
network client. Retryable database failures replay the entire snapshot and clock, never just
the failed cluster, and no result escapes a failed read or commit. Existing invoice/prepaid
resolver entry points retain their behavior and share the same tariff resolution logic.

The snapshot also reads the billing period and pending tier change for the **exact subscription
ID** selected by tier resolution, plus each requested cluster's next history start/end boundary.
These reads stay in the same transaction. Missing periods remain unknown; no calendar-month
fallback or zero allowance usage is invented. `AllowancePeriod` accepts only an explicitly
observed current window.

`QuoteExpiry` caps the one-minute tariff evidence lease at independently validated access expiry,
the selected cluster's next scheduled pricing change, the current allowance reset and any pending
tier change. An overdue or incomplete pending change cannot produce a usable quote. Another
cluster's earlier change does not shorten this cluster's lease. An exact deadline is expired.

Allowance-sensitive tariffs also read exact recorded consumption through the latest closed
five-minute cutoff in the same transaction. Totals aggregate canonical deltas and applied
correction deltas as decimals, never through dashboard float conversions. They retain invoice
window-overlap semantics rather than prorating buckets. Reporting coverage requires positive
required-source evidence and every overlapping window, including partially overlapping source
activation/deactivation windows; open tenant/global anomalies prevent known totals. Unsupported
units, malformed windows, negative totals and unattributed usage cannot become known zero.
Only a covered empty ledger establishes explicit zero for the selected clusters/media meters.
Missing/currently unclosed periods and periods beyond the bounded one-year read remain unknown.

The returned coverage status and `Through` timestamp are essential evidence: this is recorded
consumption through a cutoff, **not instantaneous consumption after it**. The RPC copies covered
totals only into the corresponding cluster/meter comparison basis, requires the exact current
billing period, and lists each allowance-sensitive price in `recorded_usage_bases`. Missing
required totals stay unavailable even when the overall coverage status is covered. Explicitly
zero additional consumption does not require allowance evidence for that meter. Signed quote
evidence and API/UX must retain this cutoff and estimate distinction before live integration.

The snapshot read is not entitlement or consent approval or a deployable signed quote. The RPC
captures and revalidates access separately. PostgreSQL tests prove that concurrent
tier/override/history/usage updates cannot produce a mixed snapshot, that scheduled boundaries
are preserved, and that corrections, coverage gaps and tenant attribution remain distinct.

The bounded collection describes one effective policy's comparison bases, not every possible
stream override in a tenant. `PlacementCommercialRequest` derives the complete, deduplicated
comparison set from the effective policy and includes all tenant grants, regardless of health,
constraints or consent-based destination eligibility. The authority join rederives that request
from the exact tenant/object policy pair, verifies the response, and rejects different policy
digests, revisions, parents or cluster sets. A missing object quote leaves prices unknown; it
cannot fall back to another stream's ratio or tenant-wide prices. Artifact objects support serve
quotes, not ingest quotes.

`ClusterPricingService.GetMediaPlacementQuote` accepts only service authentication. Requests
bind a canonical tenant UUID, object ID, ingest/serve verb, policy/parent revisions, policy digest,
the complete requested cluster set and up to sixteen distinct currency/unit comparison bases.
The typed Purser client sends service credentials even when its caller carries a user JWT; no
delegated user/tenant identity headers leak onto that request. This is a read-only billing API,
not an end-user authorization check or permission to subscribe to another cluster.

The RPC has a five-second budget and surrounds its database snapshot with one-second
Quartermaster entitlement reads. Both reads occur outside the database transaction. Requested
clusters require active membership, valid access provenance, ownership and explicit owner
consent evidence. The before/after semantic revision binds these facts and their explicit access
expiry, but excludes health and display metadata. Changed facts abort the quote; revoked access
denies it. No access mutation, marketplace subscription or capacity reservation occurs. Consent
is bound as evidence, not treated as media admission: final admission must enforce its verb flags.

Responses carry canonical request scope/digest, an entitlement digest, database observation time, expiry, per-cluster
charging/prices and explicit unavailable reasons (currency mismatch, unsupported basis, unknown
usage or unsupported tariff). There is no currency conversion or omitted-basis success. Access
evidence has at most a thirty-second lease and may expire sooner; tariff, scheduled change and
billing-period limits also apply. The typed client rejects mismatched scope, incomplete cluster
or basis coverage, unknown wire fields, wrong-verb prices, invalid semantic revisions, expired
evidence and allowance-based facts extending beyond the billing period. Protobuf preserves
integer micro-unit values above JavaScript's safe-integer range; public API mapping must do so too.

The shared entitlement digest binds tenant identity and the selected clusters' class, owner,
active status, access source, explicit access expiry and owner consent. It ignores health, display
metadata and a reader's lease-renewal clock. Foghorn reconstructs it from signed tenant grants;
matching cluster IDs alone cannot join prices observed before an ownership or consent change.
The media object's signed lifetime cannot exceed any embedded quote's expiry. The compiler
retains a detached complete quote, so destination revalidation observes price and usage-evidence
changes, not only policy revisions. Deadline renewal has a separate worker from ordinary
schema-1 reconciliation; sustained scheduling and delivery capacity still require verification
before live schema-2 issuance is enabled.

Commodore's active schema-2 object persistence refreshes required quotes from the typed Purser
service client before opening its write transaction. Quote requirements follow each verb's
effective policy: any inherited allow/deny charging selector, effective preference charging
selector, or price ordering requires commercial evidence. Stream preference replacement removes
replaced preference requirements but cannot remove inherited hard constraints. Geography,
ownership and cluster-class-only policies do not require billing quotes. Such objects clear
obsolete quotes, retain their normal tenant-bounded lease and do not claim a Purser source
revision. Missing charging remains unknown, never permanently free or a zero price.

Live objects collect the required ingest/serve quotes concurrently; artifacts consider serve
only. A noncommercial verb does not request a quote. The collection has a six-second overall
budget and each required owner call is capped at five seconds. A failed, expired or mismatched
required response aborts the whole collection without modifying the caller's object. Previously
embedded quotes are replaced, not renewed by changing their timestamps.

After collection, the publisher locks the tenant's current-authority pointer with a tenant-scoped
shared row lock. It rechecks the captured parent payload and validity before allocating an object
version, signing or enqueuing deliveries. Parent changes abort publication; the lock prevents a
concurrent parent update during the object write. No billing RPC runs while that transaction is
open. Noncommercial publication performs the same scope validation and parent lock/recheck even
without a configured billing client. The transaction itself is deadline-bound by signed validity.
The stored object lease is capped by all required quotes and tenant validity; its refresh timestamp is halfway through a short lease, capped
at the normal refresh interval. The same transaction enqueues a version-bound deadline refresh;
failure to schedule rolls back the authority version and deliveries. Schema-1 publication retains
its current behavior. Tombstone compilation clears commercial quotes so revocation does not
depend on refreshing billing evidence.

Deadline jobs use the durable refresh inbox with separate tenant and object claim workers. A job
binds authority kind, authority ID and version; its tenant-scoped current-version lookup discards
superseded or missing objects without starting another renewal chain. Claims use leases and
`SKIP LOCKED`. Object compiles have a twelve-second budget inside a twenty-second lease, with up to
eight concurrent object jobs. Tenant jobs have their own eight-way worker, a twenty-five-second
compile budget and a thirty-five-second lease; sequential tenant identity lookup plus parallel
owner reads do not consume object renewal slots. Ordinary refresh, delivery, tenant renewal and
object renewal have independent tick loops. These limits are implementation bounds, not a proven
production throughput guarantee.

Active schema-2 tenant publication schedules a version-bound renewal atomically, halfway through
a short grant lease or at the normal refresh interval for a longer lease. Its write transaction
cannot outlive the signed grant expiry. Inactive tenants and tombstones do not start another
deadline chain. A deadline-driven tenant publication also commits an ordinary dependent-object
fanout job in the same transaction. Failed fanout enqueue rolls back parent publication and its
next renewal; failed enumeration is retried independently without recompiling the parent.
Fanout-only jobs do not advance the tenant compile generation. This keeps an older deadline job
becoming stale, or a slow dependent enumeration, from discarding or fencing out renewal work.

Object policy compilation reads requested scope and parent policy in one repeatable-read
snapshot and requires the parent to match the signed tenant policy. Live streams and derived
artifacts retain stream overlays; standalone artifacts have an empty overlay and inherit tenant
policy. Public stream management and live compilation reject deleted parents. Artifact compilation
can read a retained, tenant-scoped parent overlay after soft or hard deletion; missing or foreign
history still fails closed. The deletion transaction preserves the existing policy row or inserts
an explicit revision-zero empty overlay for an unconfigured stream. Finalization repeats this
idempotently for previously queued deletion jobs before removing the stream. Policy rows and their
audit receipts do not cascade with stream deletion. This preserves restrictions on independent VOD
assets, which stream cleanup deliberately leaves untouched; it does not exempt clips/DVR from the
existing deletion saga. Retained overlays still combine with current signed tenant policy, never
freeze old tenant permissions. Pre-existing orphans without retained history cannot be inferred as
unrestricted and require explicit repair. Failed compilation leaves the input unchanged.
This conditional object compiler does not itself enable tenant schema 2 or mark a policy effective.

Tenant refresh likewise follows stored schema history; requested placement rows alone do not
activate schema 2. For an established schema-2 tenant, the compiler reads requested tenant policy
in a repeatable-read snapshot, rejects revision rollback or changed content at the same revision,
and attaches the selected grants' region and explicit owner consent from Quartermaster. Membership
and peer IDs must be unambiguous; every selected grant must match its owner projection and allowed
membership. The shared entitlement digest checks that conversion preserves ownership, access
provenance, original expiry and consent. Missing or unknown consent does not become permission.
Comparison prices and charging classification are not invented by this compiler; object-scoped
Purser quotes supply commercial evidence. Signed source revisions include the Commodore policy hash.

Tenant publication rechecks current schema and policy after acquiring the authority version-counter
lock, before signing or advancing the current pointer. Concurrent first publications are serialized
by that lock; schema or policy rollback aborts the transaction. Suspension, inactivity and deletion
retain the stored policy revision with no positive grants and do not read mutable policy rows or
add a quote dependency. Suspension retains the tenant compiler's existing owner reads; inactivity
and deletion do not require billing or entitlement reads. Sustained renewal and delivery capacity,
the recipient capability barrier and rollout promotion each govern a first-time cutover
independently; preserving an established schema is not approval for one.

Delivery validates the queued kind, object/version and destination against the signed envelope
before resolving a cell. Its context uses the earlier of the caller deadline and signed expiry;
expired or malformed envelopes do not perform network work. A transport success returned after
that deadline cannot become an acknowledgment. The destination still verifies the signature and
complete authority. Only explicit `applied` or `duplicate` outcomes with the exact kind/ID/version
are acknowledgments; an unknown outcome cannot advance distribution state.

Schema-2 deliveries whose original signed lifetime is at most one minute use a separate queue
claim from ordinary deliveries. Eight independently refilling workers claim one unlocked head per
cell. An active short-lease claim excludes that cell's other short deliveries, while long-lived
delivery to the same cell does not consume the short-lease slots. The outer claim rechecks row
eligibility after locking; workers cannot skip a locked head to claim another row from that cell.
Each attempt has a five-second budget, a twenty-second claim lease and a one-second claim-query
budget. A healthy worker refills immediately instead of waiting for a slow peer's batch to finish.
Empty queues return to the regular one-second polling loop.

PostgreSQL and isolated single-node Yugabyte tests with simulated cell services over real local gRPC hold long and short deliveries open to one cell while a
healthy cell drains three signed deliveries and commits distribution state. Concurrent workers
use only one short-delivery slot for the slow cell. Separate query contracts cover the one-minute
classification boundary, locked heads, acknowledgment-driven progress, expired leases and
superseded rows. `make verify-placement-yugabyte-db` runs generated query preparation,
these delivery contracts and Foghorn's joined authority lookup contracts against its own
ephemeral Yugabyte engine. These are isolation and
correctness proofs, not multi-node failover or sustained-throughput certification.

The PostgreSQL backlog contract captures and explains the actual generated claim with 20,000
acknowledged historical deliveries, 2,000 pending deliveries and 20 cells. A local warm-cache
sample measured 7.403 ms execution, with 12 subsequent claims at 6.958 ms median and 8.660 ms
maximum within the one-second worker budget. The plan scanned authority-version history; zero
shared-read blocks mean this does not establish cold-cache behavior. History growth/retention,
larger and sustained workloads, Yugabyte load behavior, multi-node failure and live activation
remain release gates. Avoiding unnecessary commercial quotes reduces renewal churn but does
not establish a bound on retained authority history.

Tests exercise actual local gRPC transport through the production Purser interceptor chain,
service credential isolation, mutation/expiry rejection by the typed client, consent/ownership
changes between reads, transaction failure and pending-tier boundaries. These checks establish
the owner RPC contract. Signed round-trip and compiler tests additionally cover retained cutoff/
precision, object/parent/census/ownership rebinding, unknown nested fields, duplicate verb quotes
and authority lease extension. PostgreSQL publication tests verify persisted signed quote
payloads and rejection of a parent consent change during collection; lock tests prove the parent
pointer cannot update while the object write holds its shared lock. Renewal tests exercise the
actual builder, scoped policy reads, both fresh quotes and atomic future-job persistence, including
concurrent claiming, superseded jobs and scheduling failure. Separate policy tests exercise stream
and artifact inheritance and parent rejection on PostgreSQL. They do not prove schema-2 activation
or physical media placement.

### Authority compilation

Foghorn's placement authority reader uses joined, tenant-scoped queries for an exact
authority ID/internal name or for the tenant/internal name supplied at final admission.
The latter derives the object identity from the same snapshot, without an unscoped name
lookup. Both validate payload hashes and bound identities and retain separate tenant/object
versions, readiness
markers and expiry timestamps. The census compiler requires schema 2, matching policy
parent revisions, active billing/object authority and readiness for the requested verb;
managed inputs additionally require source readiness. Soft-expired authority remains usable
only until its signed hard expiry. A missing or mixed projection is not default policy.

The signed grant projection groups virtual clusters by their owning control cell without
consulting peer health. Unreachable cells remain in the discovery census. Owner-denied
verbs are excluded, external-source consent remains distinct, and price/charging facts
retain the billing owner's revision and expiry. Peer queries must match the local policy
digest, tenant/object identity, revisions, verb and complete cluster set for that cell.
Node observation expiry is bounded by the joined authority as well as telemetry freshness.

The membership inventory is owned by Quartermaster, not inferred from Foghorn's live cache.
Its single query requires active, unexpired, known-source grants and active clusters in the
requested control cell. It preserves empty clusters and all registered edge nodes, including
offline nodes; a node is admission-enabled only while its registry status is `active`. Mesh
nodes reach that status through the WireGuard agent's heartbeat, and token-enrolled
self-hosted edges (which never join the mesh) are recorded `active` at enrollment, so operator
transitions (drain, remove, evict) remain the only path out of admission. Missing or revoked
requested clusters reject the request rather than certifying a smaller pool. The service bounds each request to 4,096 clusters and nodes; oversized results
are rejected, never silently truncated.

Foghorn's inventory reconciliation preserves missing telemetry as unavailable placeholders and
honors administrative disablement without fabricating fresh metrics. Unregistered runtime nodes
are excluded and make the observation incomplete; healthy registered neighbors remain usable.
Conflicting node identities reject the snapshot. The inventory-bound observation requires the
exact tenant and cluster set and expires no later than the 30-second membership observation,
even when newer metrics arrive. This reconciliation is an integration building block, not an
activated alternative to the live front doors described above.

`PlacementDiscovery` composes the signed authority reader, membership inventory and node-path
evidence under one one-second deadline. It verifies policy identity/revisions and the exact
owning-cell census before reading node data. Source generation, path expiry and membership
must match; dependency errors cannot leak credentials or private database details. Cancellation
stops between dependencies. The result preserves every registered node, including cold and
unavailable nodes, without top-ten truncation. A real loopback gRPC test verifies that the same
pipeline produces identical local and authenticated federation observations and that an incoming
viewer bearer cannot replace the service credential. This proves discovery transport parity,
not source preparation or first-media delivery. Push, configured-input and artifact source readers
are all available and are dispatched by signed object kind and ingest mode, and startup bootstraps
the runtime that consumes them, so this pipeline is live routing. Generation-bound replica presence
and first-media delivery are still established separately.

`LivePushPlacementPaths` uses a detached, tenant-scoped current registry snapshot without hydration
and an unfiltered local node snapshot. When the shared registry is configured, it reads that snapshot
directly under the caller's deadline; a missing record, corrupt identity, cancellation or store outage
cannot reuse process-local ownership. Discovery, source-aware preview, preparation and final source/
viewer checks use this same reader. Source ownership and destination-pull resolution share the
underlying current-entry read, so a lagging replica cannot ignore a persisted withdrawal.
The signed authority projection retains source grants separately
from serving destinations: an ingest-only EU cluster can supply a cold US serving node without
becoming a viewer destination. Cross-virtual-cluster source paths require the destination owner's
`allow_external_source` consent; same-cluster transfer does not. This consent is not the separate
private-network URL permission used for configured pull sources.

Publisher observations require the requested generation, a positive ownership revision, the
entitled source cell/virtual cluster and a live origin, not a replica. Local evidence also matches
the instance tenant and current inputs. The highest observed revision wins; ambiguous ownership
fails closed, and local withdrawals fence equal or older advertisements. The ownership projection's
age is not a liveness timeout. Current heartbeat/source-instance observations independently bound
its usefulness. Peer advertisements preserve independent source-instance and DTSC-listener clocks;
repeating an advertisement does not refresh either. DTSC paths must use reported listeners and the
correct runtime identity, without credentials or query parameters.

The adapter identifies discovery feasibility, not final admission or generation-bound first media.
A present publisher can serve locally without a DTSC listener, but cannot supply another node
without a valid upstream endpoint. Replica buffers are not promoted to present from an origin
advertisement or an unbound old buffer. Exact pull-attempt/lifecycle binding and destination
preparation must prove that separately. Native, configured-pull and artifact sources are explicitly
rejected by this push-specific adapter rather than assigned fictional publisher generations.

Protocol availability is resolved inside the observation adapter from each node's reported Mist
listeners, not asserted by the source-path provider or inferred from a public hostname. Listener
timestamps independently bound each candidate's lifetime; fresh metrics cannot refresh stale
listener evidence. Missing, malformed or unsupported listeners remain unavailable members of the
pool, and cannot justify capacity spill. One stale node does not shorten its neighbors' evidence.
Ingest capability checks do not receive publishing credentials. Playback templates preserve public
proxy prefixes and reported direct-protocol ports, escape media identity, and resolve WebRTC HTTP
signalling separately from its UDP listener. The derived player output catalog is not used as
protocol-capability evidence.

A disposable real-Mist contract test checks HLS/CMAF, WHIP/WHEP, custom RTMP/SRT ports and WebRTC
withdrawal. It also exposes a remaining telemetry integration requirement: the tested metrics
report omits configured MP4 because its URLs are method-level capabilities rather than a connector
`url_rel`. MP4 must remain unavailable to this discovery path until explicitly reported capability
evidence is supplied; the test does not certify progressive playback or first-media delivery.

Cell-level observations carry `observed_at` and `expires_at` independently of candidate timestamps.
An empty cluster therefore cannot provide an indefinitely valid completeness proof. The envelope
expires at the earliest membership, authority or path-evidence deadline. Missing, future, expired
or unbounded cell observations cannot authorize capacity spillover. The router bounds candidate
lifetime by the cell envelope, rechecks completeness on every preparation retry, and rejects an
accepted preparation if the decision evidence expired while it was in flight.

`PreparePlacementRequest.expires_at` carries that deadline to the exact destination. The router,
local transport and authenticated federation server bound preparation dispatch by it. The server
uses the absolute deadline, not a recomputed duration that could extend the evidence lifetime.
Missing, expired or overlong deadlines are rejected before dispatch. Responses must preserve the
tenant, object, generation, cluster/node, protocol, policy/parent revisions, digest and attempt;
they may shorten but never extend expiry. Refusals cannot return an endpoint or claim readiness.
Late success after cancellation is rejected, including when a delegate ignores its expired
context. These are transport guards, not a substitute for durable attempt reconciliation or
final policy/source admission at the destination.

Preparation attempts use canonical UUIDv7 identifiers. The embedded issuance millisecond bounds
the request to at most 30 seconds; the router also caps the evidence deadline to that horizon.
Issuance tolerates at most one second of pairwise clock skew, but expiry is never extended.
Canonical request identity includes the complete query, exact target, attempt and expiry, with
cluster-set order normalized. Changing a deadline or protocol is a different intent, not a retry.
An expired identifier cannot become valid again after its coordination receipt has been collected.

`PlacementReceiptStore` atomically records intent in shared Valkey before preparation effects.
Records are scoped by owning cell, consuming tenant and attempt, and expire 35 seconds after
issuance without renewal on reads or writes. A physical pull binding is immutable and separate
from the viewer's placement attempt: sharing an existing pull must retain its source cell, virtual
cluster, node, generation, exact integer revision and physical attempt. Finishing requires the
same binding and records an immutable, fully bound acknowledgement. A duplicate pending receipt
requires reconciliation; a stored acknowledgement is not permission to bypass current admission.
No publishing credential is part of these records.

`PlacementDestination` composes discovery and receipt-backed preparation for the local and
federated transports. It requires a runtime enforcement adapter; missing enforcement or mismatched
discovery/receipt cells refuse preparation. Reconciliation checks current admission before media
work; the destination revalidates afterward, including current generation-bound evidence when an
outcome claims readiness. The policy-bound runtime performs one global evaluation before media
checks/work and one after work, without a duplicate preflight evaluation. Physical admission
refusal, cancellation or expired policy cannot proceed to media work.
Completed receipts are revalidated and reconfirmed against coordination storage before replay;
expiry, engine change or a lost outcome during revalidation cannot return cached success.
Pending attempts retain their physical binding after ambiguous work. The runtime must reconcile
that binding under its physical-operation fence before starting or reusing media; the receipt's
fresh flag is never a start permission. Competing outcomes cannot overwrite a completed attempt.
Startup connects discovery to the federation server before its listener opens. Physical
preparation remains unavailable without the concrete admission/media executor; live routing
cutover and generation-bound first-media proof remain gates.

`LivePushPreparationRuntime` connects receipt-backed serving preparation to the existing
origin-pull arrangement. It checks signed push identity, the current publisher generation,
exact destination membership in the authority, reported playback listener, source feasibility
and external-source consent. Its endpoint uses the signed public playback identity, never a
source URL. The policy-bound wrapper remains responsible for global preference and capacity
evaluation; the media adapter cannot replace it.

Arrangement binds the physical attempt to the placement receipt before notifying the source.
A new viewer on the same destination binds the existing physical attempt without notifying the
source again. Another destination gets its own pull. A lost notification reply retains the
pending binding; retry uses that same attempt. Current pull reads consult shared registry state
when configured, so missing/corrupt/unavailable storage and lagging replica caches cannot return
cached success. Clearing a pull on another replica invalidates replay.

The replication watcher records `DestinationObserved` when the exact destination appears live;
it does not clear the inbound source binding. The observation marker suppresses duplicate
availability hints and survives shared-state merge/restart. Warm preparation and source resolution
retain the same physical attempt. Explicit clearing is revocation and keeps its tombstone/fence;
a late observation cannot mark a cleared or replaced attempt. A new source attempt starts
unobserved. The marker is not generation-bound first-media evidence and cannot make a preparation
`ready=true`. Mixed-version workers that still clear on observation are not safe for activation.

Preparation on the publisher itself requires no source notification or self-pull. Accepted
responses from this adapter have `ready=false`: arranging a source is not evidence of received
media. Replay checks current publisher, listener and physical pull binding. Startup installs this
adapter for destination preparation and connects consumption of marked pulls to its receipts.
The same startup connects public routing, the ingest/managed/artifact execution paths and final
admission; policy issuance is separately gated by the attestation barrier. Functional media
delivery still requires real-Mist cross-cell verification.

Same-cell arrangement dispatches directly to the source preparation implementation, with no
peer address lookup, loopback RPC or fabricated service-auth context. Both virtual clusters must
be served by the cell; the exact destination must have current healthy, probe-verified node
state and must not be in maintenance. These checks also precede warm reuse. The publisher itself
does not arrange a self-pull. Source notification and local dispatch share tenant/source checks,
the generation-fenced outbound write and a bounded deadline.

This in-process path is available with shared coordination even when federation is disabled.
Its local source handler is not registered as an external service. Remote `NotifyOriginPull`
still requires service authentication and enabled federation. Source requests accept the configured
control-cell identity and its explicit legacy cluster alias, used by current peer advertisements.
Acknowledgements preserve the requested alias exactly; unrelated cell IDs are rejected. The
legacy cluster namespace continues to own registry keys. The source's virtual media cluster is
checked independently against the source node; it is not inferred from either cell alias.

`STREAM_SOURCE` reads publisher ownership and all destination pull records from one current
shared registry snapshot, including clearing tombstones. It does not return an old process-local
pull after another replica clears it. A cleared pull, unavailable coordination or reassigned
destination returns an explicit offline source, not an empty response that would execute Mist's
configured upstream. If only another edge has a pull, a node-bound balancer URI lets this edge
resolve and arrange its own source; without that resolver it is explicitly offline. A current
local publisher claim bypasses retired replica-pull state and continues normal publisher setup.

Source resolution bypasses the ten-minute mutation replay cache, like connection admission.
Retries re-read state rather than replaying an old DTSC URL. The authenticated control registration
must remain current before and after resolution; a retired connection receives no source URL.
These checks protect the handoff and tracking state. They do not by themselves prove media arrived
from the intended generation.

Startup installs `ConfigureLivePreparedSourceAdmission` from the same destination preparation
runtime and shared receipt store. Every tracked inbound pull requires this admission; there is no
legacy unmarked bypass. Placement-created pulls carry a durable `PlacementRequired` marker in
their first registry record, and adopting an existing pull sets it with an exact-attempt
compare-and-set before returning its URL. The marker is required even before the separate
request-context retention write completes. Neither the marker nor retained context is permission:
missing handlers or receipts reject the source. Re-recording the same pull cannot clear its
requirement, and replacement attempts receive their own marker. Edge and core therefore roll out
together rather than supporting mixed enforcement generations.

`MediaSourcePlacementAdapter` resolves tenant-scoped signed object identity and dispatches by the
signed ingest mode. Push publishers use `LivePushPreparationRuntime.ResolvePreparedSource`.
Configured `pull` and `mist_native` streams that must relay another cell's live origin use the
configured serving preparation, which creates its own receipt-bound pull. Configured inputs that
the destination may originate remain pull-free, as do stored artifacts. Final resolution joins
completed placement evidence with current signed authority, the exact current inbound pull,
destination connection and current source path, then reconfirms the receipt epoch and expiry. A
DTSC URI with matching source IDs but a different advertised endpoint is not accepted. The
handler requires the returned URI and attempt to match its current arrangement, checks the bounded
lifetime, and returns an explicit offline source on failure; clearing an installed adapter does
not restore unbound tracked-pull access.

Pull-backed preparation receipts also bind the destination control-connection fence. Preparation
reads existing shared connection ownership, including when another Foghorn replica owns the node;
unavailable or absent shared ownership cannot fall back to local registration. Without shared
ownership configured, only a current authenticated local registration can supply the fence. The
source adapter reads the emitting node's authenticated local fence, not a trigger field, and
resolution checks shared ownership again before returning. Replaying a receipt after reconnect
fails even if node ID, source and policy are unchanged. A fresh preparation may authorize the new
connection to reuse the same physical pull and attempt without another publisher notification.
This binds permission, not the lifetime of the media process, and requires no additional Mist RPC.

The destination bootstrap also retains minimal viewer request context with an accepted physical
pull: tenant/object identity, playback protocol, original client coordinates and source generation.
It omits policy revisions, grant inventory and credentials. This context is not permission. It is
written only after the completed receipt is durable, fenced to the current physical attempt, and
removed on explicit clearing; replacement pulls do not inherit it. A failed context write makes
preparation unavailable, and replay retries that write without another publisher notification.

`ResolveOrReauthorizeSource` takes the existing completed-evidence fast path without a federation
census. Missing/expired evidence, including a changed connection fence or renewed authority version,
requires fresh exact-node preparation. The resolver reconstructs policy/revisions/grants from current
signed authority while retaining the original viewer geography and protocol. It cannot substitute
the edge's location, choose another destination, or extend an old receipt. Storage errors do not
become missing evidence. Successful fresh preparation must still resolve to the same physical pull
and DTSC endpoint under the current connection; consent withdrawal, unknown inventory, listener
withdrawal and changed source identity fail closed. The source-trigger path allows five seconds
for fresh admission while individual registry/authority reads remain bounded to one second.

Startup supplies the source adapter with its destination so every tracked pull uses this resolver.
Push publisher pulls and configured cross-cell relays are both reauthorized from retained viewer
context; local configured inputs and stored artifacts present no pull attempt. Direct publisher
ingest remains a separate final-admission path based on Mist's observed RTMP, SRT or WebRTC/WHIP
connector and does not consume a serving receipt. Retained context alone does not demonstrate active
viewer demand or revoke an already-running media process. No new Mist trigger, DTSC extension, or
separate viewer network callback is required by this flow.

The shared router also exposes read-only `Evaluate`, which uses the same bounded cross-cell
discovery and policy evaluation as `Route` without invoking preparation. Its result lifetime
bounds all returned choices and the evidence supporting group transitions. Missing, stale or
unreachable preferred cells cannot certify capacity spillover. Routing retries continue to
reevaluate that census after an exact destination refusal.

`PlacementPolicyGate` uses that path to revalidate a selected destination. It reconstructs all
cells from current signed authority, binds the incoming query to this cell's complete authorized
cluster set, and checks that the exact node remains in the selected policy group. It rereads
authority and ingest ownership after observation; changed owner consent, commercial facts,
policy or active ingest cluster invalidates the result. Unknown ingest ownership fails closed;
a verified absence of active ownership is distinct. The result expiry is capped by the original
attempt, authority and observation lifetimes. This implements the policy portion of runtime
revalidation, not a physical preparation, capacity reservation, atomic publisher claim or
generation-bound media proof; connecting those enforcement pieces remains required.

Its `Admit` entry point accepts trusted connection identity rather than a client-supplied policy
query: tenant/object/internal name, admitted edge cluster/node, verb/protocol, trusted client
geography and owner-resolved source generation. It rebuilds the policy digest, revisions and this
cell's grant census from signed authority, then applies the same global gate to the exact node.
This also supports direct connections that have no resolver-issued preparation receipt. Serving
requires a known generation; new ingest may have none but still requires the current ownership
check. The whole admission check has a three-second budget, its initial authority lookup has a
one-second budget, and its result is bounded by authority and observation expiry. It performs no
preparation or capacity mutation and cannot replace viewer authentication, tenant capacity,
atomic publisher ownership or physical readiness checks.

Direct admission carries signed tenant/object authority versions separately from policy revisions.
Owner-resolved source evidence may supply its paired expected versions; a mismatch is rejected
before the global census. The outer identity lookup and the final policy assessment must also use
the same versions, even when policy revisions and digest did not change. A successful decision
retains these versions for its consumer; the viewer handler rejects missing or nonpositive versions
before capacity/enrichment side effects.

Tests cover direct-destination admission for ingest and serving across a preferred and fallback
cell, including verified-empty versus unreachable preferred capacity, identity/schema/readiness
failures and a policy change between reads. `USER_NEW` has a startup-configurable viewer-placement
adapter hook after playback authentication and load checks, before viewer capacity and enrichment.
The executable bootstrap installs the concrete adapter from the public runtime
(`ConfigureLiveViewerPlacementAdmission`), sharing the destination's gate and source paths.
It receives server-resolved tenant/internal name and edge cluster/node, plus Mist's connector and
client address; URL policy parameters and prefilled geo never enter the adapter. Its returned
decision must match the connection, serving verb, bounded lifetime and canonical policy digest.
Installing even a nil adapter requires placement and fails closed; clearing an installed adapter
cannot restore the public-marker path. A configured hook runs for cached public playback too.
On the Mist side a PLAY_REWRITE denial now routes through the generic failure path: HTTP outputs
send a complete 404 body and close the connection, and HLS answers a trigger denial with that
plain 404 instead of its 200 error playlist, so a refused viewer fails over instead of hanging
on a response that never completes (`make verify-mist-viewer-credentials` on the image under test).

The viewer adapter binds its owner-source lookup to those signed versions. After the global
decision, it rechecks the owner-derived source generation without repeating the census. Withdrawal,
replacement or an expired source rejects admission; a shorter source-evidence lifetime shortens
the result, and refreshed evidence cannot extend the initial decision. This is a current-source
check, not proof of actual received media or a replacement for destination process fencing.

`ViewerPlacementAdapter` implements that hook using the joined name lookup, canonical Mist
connector mapping, trusted client IP and `PlacementPolicyGate.Admit`. It rejects hostnames,
address/port pairs, scoped IPv6 and unknown or ambiguous connector labels; IPv4-mapped IPv6
is normalized for GeoIP lookup. Missing GeoIP remains unknown: an unbounded policy may admit
it, but a hard distance bound cannot. Connector mapping is not listener-capability evidence;
discovery must independently prove a supported playback endpoint and source path, including
transport variants that share a Mist statistics label.

For push streams, `LivePushPlacementPaths.ResolveSourceGeneration` shares discovery's source
owner selection: exact durable revisions, withdrawals, newer dry owners, duplicate-owner
rejection and fresh source/advertisement evidence. A replica buffer cannot establish the
publisher generation. The adapter derives this generation before global revalidation and
bounds the resulting decision by its source evidence expiry as well as authority and discovery
expiry. It does not expose source credentials or assert that physical preparation succeeded.

For capped viewers, `TryRegisterViewerBefore` checks expiry atomically before any Redis script
mutation, including duplicate-session renewal. Local capacity checks expiry under its lock. The
successful reservation is the admission commit, not the later telemetry delivery or response
transport; this does not end already-admitted sessions when a short quote expires. Uncapped
viewers consume the decision at the final pre-enrichment clock check. A late/ambiguous capacity
RPC never triggers an unfenced session release that could erase a concurrent successful retry;
existing capacity lease expiry bounds abandoned reservations. Clock synchronization remains
required across authority, Foghorn and coordination engines.

The existing capacity manager's keys are namespaced by Foghorn cluster, as configured at startup.
These checks do not establish a tenant-wide cap across separate control cells or separate
coordination engines. Cross-cell capacity ownership and accounting must be resolved and verified
as part of the global-routing cutover; a same-cell HA test cannot prove that property.

Handler tests run `USER_NEW` through the concrete adapter, push-source reader and actual global
gate with simulated authority/registry/cell observations and verify forbidden spill reserves
no viewer capacity. Adapter tests additionally cover address/protocol rejection, mapped IPv4,
unknown/invalid/distant geography, authority identity/readiness, missing or expired generation,
source expiry binding and cancellation. PostgreSQL and Yugabyte contracts exercise both joined
lookup forms and reject tenant/name and projection-version mismatches. A real Valkey contract
executes the writer with expired authority and verifies both new and duplicate sessions leave
all capacity structures unchanged. Media classification uses URL paths, not metadata-looking
query text, hostname or stream-name substrings: adding `poster=.jpg` or `metaeverywhere=1` to an
HLS request cannot skip viewer authentication/admission. Metadata and thumbnail enforcement
remain independent surfaces, not proof supplied by the viewer classifier.

The hook is installed by production bootstrap together with the front-door resolvers, and the
control-replica attestation chain (see "Activation barrier") gates schema-2 issuance on it.
Managed and artifact sources reach it through their own source readers, selected by signed
object kind and ingest mode. Complete listener/transport coverage, signed/outage behavior and
the sibling-Mist observed-connector proof remain open. A processor without the
placement runtime keeps schema-1 behavior and reports itself as non-enforcing, which withholds
its cell's attestation; that is the mechanism, not a permission, that keeps schema 2 unissued.

### Managed-source materialization

Mist-native sources retain their explicit single-source-cluster election and placement count.
The managed reconciler does not use publisher geography or move a source to a different cluster
because a preference changes. Hard ingest constraints are evaluated per node: before the
deterministic stable-hash election, the reconciler drops every node of the source cluster that
the signed ingest policy denies, so a stream is never elected onto a node its admission would
refuse while a permitted node exists. Every Foghorn reads the same signed pair, so the filtered
election stays identical across peers. A node whose verdict needs facts that are unavailable
makes the tick transient for that stream rather than shrinking the pool. Admission then checks
entitlement, ownership and intersected hard ingest constraints for the exact elected cluster and
node; it does not manufacture telemetry or run a new preference election. An explicit empty
preference list still denies all placement. Preference ordering, geo spill and price ordering are
not a managed-source migration mechanism.

When the local authority store is configured, both local-context and connected-context
materialization read the exact tenant/object/internal-name pair before populating caches or
starting DVR. For schema 2 and 3, the check requires ingest and source readiness, active unexpired
authority, matching parent policy revision, ingest consent and hard constraints for the exact node. It then opens
the same object snapshot's sealed source definition and compares source spec, kind, always-on,
placement count and the single allowed source cluster against the reconciler row. The source
definition is never included in failure logs. The read/check has a one-second context budget.

The schema-1 connected-versus-signed source-readiness comparison precedes the policy
check, so a fresh legacy projection can become usable. It cannot promote schema 2; that schema
needs placement-aware readiness proof, and a readiness marker does not override placement
permission. Explicit hard, lifecycle or billing denial uses the reconciler's denied/retract outcome. Missing,
inconsistent, expired or unreadable evidence is transient: preserve prior applied state and
retry, without materializing or sending a new Apply. With a configured store, a missing joined
pair cannot fall back to a positive connected answer. A matched schema-1 pair retains schema-1
behavior; an unconfigured store remains the legacy runtime, not a schema-2 activation option.

`ApplyManagedStream` dispatch repeats the check after materialization, closing the gap where
policy or source definition changes during those side effects. Tests cover hard deny/deny-all,
owner consent, source mismatch, expired/mixed/unready authority, cancellation and retained source
election. Integration tests exercise local and connected materialization plus Apply dispatch
with the concrete authority reader and mocked database payloads, proving policy denial reaches
neither caches nor DVR nor command dispatch.

For schema 2, the dispatch check also produces a `ManagedStreamAdmission` attached to the
command. It binds object/tenant authority versions, policy/parent revisions and digest, exact
destination node/cluster, and a domain-separated SHA-256 of the deterministic command without
the admission field. The hash includes source, identity, flags and tags. The lifetime is bounded
by the joined authority and thirty seconds. This is an attestation over the authenticated control
channel, not an independently signed bearer token or a public source-start API.

Helmsman verifies the canonical receiving node, payload hash and lifetime before media work.
Issuance tolerates at most one second of clock skew; hard expiry is not extended. Its
`managed-placement` state under `HELMSMAN_STATE_DIR` retains per-name identity and authority
high-water marks. File locks span command dispatch; the fence is persisted by file sync, rename
and directory sync before dispatch. Directory creation uses the existing durable helper.
Older object/tenant/policy revisions, same-object-version configuration changes, identity
changes, corrupt state and later unbound commands are refused. Fences retain hashes rather than
source credentials and are not removed after transient failure or ordinary source retraction.
Missing state-directory configuration refuses a bound command.

The admission is checked again after filesystem work and its deadline covers Mist authentication,
AddStreams and Save. Cancellation before authentication completes sends no media mutation;
cancellation after sending a command remains an ambiguous physical outcome and is not followed
by unfenced deletion or rollback. Tests cover payload/lifetime binding, persisted replay and
downgrade rejection, overlapping file locks, unchanged state on refusal, actual handler dispatch
and HTTP cancellation before/during mutations. These are mocked Mist API tests, not a real
source-generation or first-media acknowledgement.

`RetractManagedStream` carries a fresh, at-most-thirty-second cleanup deadline plus the exact
node/cluster/tenant/stream identity, authority/policy versions and original Apply command hash.
Its deadline is independent of the old Apply lease. The sidecar shares the same per-name file
lock, checks the target against recorded authority, and persists retirement before Delete/Save.
A retirement arriving before its Apply records a tombstone without touching unrelated media;
the delayed Apply cannot start afterward. Duplicate matching cleanup can retry, while an old
cleanup cannot delete a newer admitted version. Even unbound cleanup must match the managed
stream ID, not only its name. Delete and Save use the cleanup deadline. Retirement uses local
fence format 2 so a format-1 reader refuses it instead of ignoring the retired state; this is
separate from media-authority schema versions and does not make fence-unaware sidecars safe.

Configuration acknowledgements carry the exact applied admission and tenant identity through
heartbeat/Register. Foghorn preserves them through its reconnect hydration and uses them for
bound cleanup; a mismatched acknowledgement is not a match for the desired admitted config.
Expired bound leases trigger fresh admission dispatch, but unchanged media configuration remains
idempotent at the sidecar. A newly sent admission must be acknowledged before active-cluster
pinning. Recorded binding cannot downgrade if the authority reader disappears. Retired authority
versions cannot be reused: resumption requires a newer authoritative version, not a cached Apply.

Sidecar fences also retain the original admission and a domain-separated digest of the concrete
Mist configuration, including its normalized ownership tags. Bound configurations carry the
`fw:placement:bound` marker. On sidecar restart, configuration hydration joins the current Mist
configuration to the durable fence and recovers the exact tenant/admission only when identity,
versions and configuration digest agree. Missing, corrupt or mismatched state is not adopted as
an unbound stream. Expired admissions remain available as cleanup identity, without renewal or
positive dispatch. Retirement retains this metadata; heartbeat/Register explicitly marks a
retired configuration so Foghorn can recover cleanup without verifying an active Apply.

Tests cover clearing sidecar memory, re-reading persisted fences and mocked Mist configuration,
recovering active and retired bindings, exact subsequent cleanup, expired retained leases and
refusal of changed source/flags/tags/identity or absent state. This is configuration recovery,
not permission to start media during an offline restore. It does not fence a Mist request already
sent before a timeout. Version-bound retraction remains dependent on the broader source-instance
and physical execution contract for full release proof. A pre-dispatch fence whose configuration
never reached Mist cannot be advertised as applied merely because its admission was persisted.

Activation still requires updated-sidecar capability enforcement,
config-seed/cold-restore enforcement, state-loss and node-incarnation/reassignment handling, bounded
fence retention, exact source generation/attempt acknowledgement, managed serving observations
and artifact/hydration admission. An older sidecar can ignore the optional field, and an entirely
unrecorded source still has a legacy unbound path; neither is an acceptable schema-2 rollout.
Filesystem rollback/loss is not certified by the file reopen tests. Already-issued Mist requests
also need media-engine fencing before claiming physical execution-order guarantees.

### Receipt-backed runtime and source ownership

`PolicyBoundPlacementRuntime` connects that gate to receipt-backed preparation and a required
media executor. Fresh exact-node capacity exhaustion or known node unavailability can produce a
fully bound refusal without media work. Missing nodes, omitted path evidence, stale metrics,
unknown source paths, inventory failures and authority errors cannot be converted into such
refusals. Refusal evidence has its own per-node expiry; it is not inferred from a decision error.
An omitted path is reported as unknown capacity, not a known unavailable node.

Accepted outcomes still require the media executor, including revalidation of cached readiness.
The policy wrapper validates the media acknowledgement before capping its lifetime, so an invalid
or overlong acknowledgement cannot be silently repaired. It does not mutate media-owned responses.
A completed outcome carries both signed tenant/object authority versions separately from policy
revisions. The wrapper stamps these from admission, and post-work/replay checks require the same
versions. Even an envelope renewal with unchanged placement intent cannot silently reuse an
outcome produced against a different authority snapshot. Owner consent and commercial authority
are not identified by the policy digest alone.
A source refusal requires actual media-side evidence; a missing source-feasibility observation
alone is not one. Live push/ingest executors are assembled by the destination bootstrap, and the
same bootstrap installs the public front-door adapters and final admission that consume them.

`PlacementReceiptStore.CompletedPull` provides a local, read-only lookup of completed evidence
for an exact physical pull and current signed authority versions. `Finish` atomically publishes
that index only for accepted, version-bound results with a physical pull binding; recording intent
or arranging a pending pull does not publish source evidence. Tenant, internal stream, destination
cluster/node/control-connection fence, physical attempt, source cell/cluster/node/generation/revision and both authority
versions are part of its key. The stored request retains the original client location and policy
binding, so a consumer need not reinterpret viewer geography as media-node geography.

Warm placements can index a later completed receipt for the same pull. An older replay cannot
replace longer-lived evidence or renew its expiry. Reads check the underlying immutable receipt,
response expiry against engine time, and the cell epoch; a dangling index never recreates intent.
This is evidence for the existing source-resolution integration, not a standalone permission or
a live authorization switch. The consumer still needs current signed authority, exact destination
registration/incarnation and current physical-source checks. The push-source adapter supplies the
current-authority/physical/connection checks through the source handler's installable gate. Fresh
authorization uses retained request context and a new preparation, not this index as permission.
Live wiring, actual-media reconnect proof and schema-2 activation remain open; no mandatory Mist
callback or DTSC change is introduced by this store.

`ConnectedPlacementIngestFence` supplies the connected ownership read through Commodore's existing
non-claiming stream-context API. It asserts no candidate cluster and takes no ingest lease. Tenant,
exact signed live-stream object identity, push mode, admission and billing state must match before
a missing active-cluster field can mean no current publisher. Denied/partial responses and
control-plane failure remain unknown.
This reader does not replace signed outage ownership or the final atomic publisher claim.

Every receipt operation atomically checks the engine identity, primary replication identity,
eviction counter and Redis time. A restart, primary change, eviction or missing cell epoch fences
attempts issued before a new watermark; it cannot turn a lost receipt into a fresh start. The
request which establishes a new epoch is refused, as are earlier-issued requests; subsequent
requests need newly issued attempts beyond the two-second skew guard. That guard covers both
coordinator-to-old-engine and old-to-new-engine clock differences. Coordinators, destination
Foghorns and coordination engines must keep pairwise clocks within one second; this mechanism
does not certify an arbitrarily unsynchronized or backward-jumping clock. The non-expiring epoch
uses the same cell hash slot as its
receipts. Missing INFO access, a replica, corrupted state or storage failure fails closed without
an in-process fallback. This avoids relying on every-second AOF or asynchronous replication to
retain every acknowledged receipt. It does not replace downstream physical-pull reconciliation,
split-brain fencing, fresh authority checks, or exact-generation first-media proof. The destination
worker, both live front doors and prepared-source admission share one instance of this store,
bound to the same control cell.

There is no coordinator identifier or remote-candidate flag in the decision input.
Changing which Foghorn asks the question cannot change the answer for identical facts.
A cold nearby destination is eligible for serving when its source path is feasible;
the decision reports `RequiresPull` separately from its rank.

## Policy semantics

Tenant and stream hard constraints are intersected. An overlay cannot erase a tenant
deny. Within a selector, fields combine with AND and values with OR. A selector without
fields matches everything. An omitted allow set is unrestricted; an explicit empty allow
set permits nothing. Missing facts needed by a hard selector fail closed.

Selectors name `cluster_ids`, `node_ids`, `owner_ids`, `regions`, `classes` and `charging`, each
bounded to 128 values. A `node_ids` selector matches only a candidate that carries a node identity
in that list; a cluster-level candidate with no node identity is `PolicyFactsUnavailable` against
it, so it can neither satisfy an allow nor escape a deny that names nodes. Node IDs are sorted and
deduplicated like other selector values, compare last in canonical selector ordering, and are
omitted from the canonical encoding when empty, so policies without node selectors keep their
digests. A tenant may name only nodes of edge clusters it owns (Commodore checks them against
Quartermaster's placement inventory at review, apply and stream source-location writes). Because
selector fields intersect, a node in an allow alternative or preference group match that also
names `cluster_ids` must belong to one of those clusters; nodes in deny selectors (avoided nodes)
need ownership only. The system tenant, which owns bootstrap and managed streams, may name any
node; the bootstrap renderer already maps each declared node to a listed manifest cluster. Node selectors are
refused with `FailedPrecondition` carrying a `google.rpc.ErrorInfo` (domain
`placement.frameworks.network`, reason `NODE_PLACEMENT_NOT_READY`) until every media cell serving
the tenant attests node placement (schema 3, see [Activation barrier](#activation-barrier)); the
Gateway reports it as `UNSUPPORTED`, not as a stale review. `commodore bootstrap` applies the same
gate to declared source locations before writing them.

Preferences are ordered groups. The first matching group owns a candidate, so overlapping
groups cannot manufacture a fallback from the same capacity. A stream can inherit the
tenant group list or replace it as a whole. An explicit empty list denies all placement;
it is not reset-to-default. Invalid overridden rules still fail compilation.

Each group selects distance-first or price-first ordering and chooses whether to spill
on capacity exhaustion, a material geographic hole, both, or neither. Capacity spill
requires complete observations and known exhaustion (including a completely observed
empty preferred pool). A timeout, stale telemetry, missing policy facts, an unreachable
node, or a missing source path is not evidence of capacity exhaustion.

`GeoHoleDistanceKM` defines the soft distance threshold for considering another group;
`MinImprovementKM` requires that group to be meaningfully closer. `MaxDistanceKM` is a
separate hard cap. Unknown geography cannot satisfy a hard cap or justify geographic
spill. Nested fallbacks retain the original minimum-improvement constraint. Failed geo
spill retains feasible preferred capacity rather than treating it as unavailable.

Default ordering compares client-to-node distance, relative bandwidth headroom, CPU,
relative memory headroom, stream presence, then stable cluster/node identity. Relative
bandwidth comparisons retain 128-bit intermediate precision and use each node's own
limit. Price-first ordering requires a fresh price revision on the group's explicit
currency/unit basis, then applies the same distance and resource comparisons. Configure
a hard distance cap when price preference must stay within a geographic quality bound.
These comparison amounts are not invoices, exchange rates, or billing decisions.

“Never rated official” uses both official class and durable charging classification;
an included allowance, waiver, or promotional credit must not be reported as permanently
free capacity by the facts adapter. Unknown or expired charging facts cannot pass that
restriction.

## Configured-source dialing

Every node choice for a stream is evaluated against its signed ingest policy with a node
candidate. Push ingest, viewer, DVR and artifact routing evaluate per node through the router.
The paths where a node dials a configured source (a pull input or a managed Mist-native input)
use `control.SourceDialNodePlacement` or the same verdict mapping: `Eligible` permits; a policy,
lifecycle or billing denial denies; unready or inconsistent authority and missing facts
(including an unknown node against a node selector) are unavailable, never a permission. A
schema-1 pair carries no policy and passes this check.

| Path                                                                                          | Behaviour                                                                                                                                                                                                    |
| --------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `STREAM_SOURCE` (`triggers/processor.go`)                                                     | Evaluates the triggering node. Unavailable returns the offline-unavailable source; denied returns an empty source so Mist asks `/source`, which relays from a permitted origin instead of dialing.           |
| `/source` (`handlers/handlers.go`)                                                            | Evaluates the calling node. Denied falls through to the cross-cluster remote-source fallback, which vets every advertised origin node with the same check; when every origin is refused, its relays are too. |
| Federation `SourceFeasible` (`federation/placement_configured_paths.go`)                      | A destination node may originate a configured input only with the input's cluster grant, ingest consent, private-source consent for a private upstream, and an `Eligible` node verdict.                      |
| Managed election and admission (`control/managed_streams.go`, `control/managed_placement.go`) | Denied nodes are removed before election; materialization and `ApplyManagedStream` dispatch check the exact elected node.                                                                                    |

`STREAM_SOURCE` and `/source` run the node check on the signed local authority. A request that
falls back to the connected Commodore lookup (a projection not yet marked ready) is checked
against the source's cluster pin and private-source consent only. Until the pins are retired, the
cluster pin carried in the signed source definition is enforced in addition to policy on every
path above.

## Source location

A stream's source location is the simple view of its own ingest constraints
(`api_control/internal/placementpolicy/source_location.go`):

- **ANY**: the stream has no own ingest constraints.
- **RESTRICTED**: one allow alternative per listed cluster, each optionally narrowed to node IDs of
  that cluster, plus one deny selector listing avoided nodes. At most 32 clusters and 128 nodes per
  list; a node cannot be both allowed and avoided.
- **CUSTOM**: own ingest constraints the two shapes above cannot express (other selector fields,
  several clusters in one alternative, a deny that names clusters, an explicit deny-all). CUSTOM
  is reported, never accepted as input; such rules are edited through placement review/apply.

Commodore's `CreateStream` and `UpdateStream` accept `source_location` for push and pull streams
and every stream read returns it. `UpdateStream` rejects it for managed streams, whose location is
declared by bootstrap. A write replaces only the ingest constraints: ingest preferences and the
serve verb are preserved, and writing over a CUSTOM location is refused with `FailedPrecondition`.
Before opening the stream transaction, Commodore checks listed clusters against the tenant's
eligible edge clusters and, when nodes are named, applies the node ownership and attestation rules
from [Policy semantics](#policy-semantics). A stream read fails rather than reporting ANY when the
stream's policy cannot be read.

The write runs `placementpolicy.ApplySystem` inside the stream's transaction. It takes the same
tenant-then-stream locks, revision CAS, immutable receipt and authority-refresh obligation as a
reviewed apply. The review digest is computed from the command, and the idempotency key from scope,
base revisions and resulting payload, so a retried transaction converges on one receipt. The
receipt actor is the calling user for API writes, `system:bootstrap` for bootstrap and
`system:data-migration` for the pin conversion.

A private or multicast pull source (`pkg/pullsource.Classify`) requires that the effective ingest
policy confines it to clusters with `allow_private_pull_sources` consent: the tenant rules or the
stream's own rules must hold a non-empty allow whose every alternative names only consented
clusters. Layers intersect, so one such layer bounds the whole policy. The rule is evaluated under
the placement locks against the locked tenant policy and the stream's resulting rules, so a
concurrent tenant change cannot slip between validation and commit. The refusal is
`InvalidArgument`; Bridge returns Commodore's `InvalidArgument` and `FailedPrecondition` refusals
as a `ValidationError` carrying the reason.

Until the pin columns are dropped, Commodore mirrors a location's cluster list into the pull
source's `allowed_cluster_ids` (an empty list for ANY) so replicas that still read pins enforce
the same cluster set. Commodore's gRPC `pull_source.allowed_clusters` input is translated into the
equivalent source location and cannot be combined with `source_location`; GraphQL and MCP expose
only the source location.

## Pull-source pin conversion and retirement

Data migration `commodore_pull_source_pins_to_stream_rules_v0_3_8`
(`api_control/internal/placementpolicy/pull_source_pins_migration.go`, required before the contract
phase) converts every non-empty `stream_pull_sources.allowed_cluster_ids` and
`stream_mist_sources.allowed_cluster_ids` of a non-deleted stream into the stream's own ingest
allow:

- Without an own ingest allow, it writes one alternative per pinned cluster, which reads back as a
  RESTRICTED location.
- With an existing allow, it restricts every alternative's clusters to the pins, drops
  alternatives left with no cluster, and gives an alternative that named no cluster the pin list.
- When no alternative survives (the own allow and the pins share no cluster), the pins win: the
  own allow is replaced by one alternative per pinned cluster, keeping deny selectors and
  preferences. The conversion never writes an empty allow.

Each stream converts in its own retryable transaction through `ApplySystem` (actor
`system:data-migration`) behind a stream-ID checkpoint. The update is derived from the pins
re-read under a row lock after the placement locks, the order a stream update takes them, so a
source-location edit committed after the batch was listed is converted from its current pins; a
stream whose pins are now empty is skipped. A stream that already expresses its pins writes
nothing, so reruns converge. A dry run performs the same read without row locks in a read-only
transaction and counts the streams whose rules would change. Verify fails while any pinned stream
lacks an own ingest allow with at least one alternative, every alternative naming only pinned
clusters.

The pin columns, `commodore.stream_cluster_pins`, Foghorn's legacy pin readers and the proto pin
fields are removed in the release after the one that introduces source locations, not in that
release's own contract phase: its Foghorn still enforces pins while cells attest, the conversion
runs after its deploy, and its Commodore mirrors locations into the pin columns for mixed-version
replicas. Retirement requires:

1. `commodore_pull_source_pins_to_stream_rules_v0_3_8` completed and verified.
2. Every media cell attesting node placement (`node_placement_ready`), so tenant authorities are
   issued at schema 3 and no replica depends on pins.

## Outputs and replay

The evaluator returns ordered choices from only the winning group, per-node assessments,
and explicit capacity/geo transitions. Preparation failure requires fresh facts and
reevaluation; it is not permission to use an arbitrary lower-priority destination.
Incomplete observations can still select an observed eligible preferred node, but cannot
authorize spillover.

Compilation deep-copies input rules. `Digest` hashes a versioned, canonical representation:
set ordering, duplicates, and implicit defaults are normalized; preference order and
explicit deny-all semantics are preserved. This evaluation identity is distinct from the
signed authority, activation acknowledgement, and preparation attempt.

## Policy management and recovery

Commodore owns tenant and stream intent in `media_placement_policies`. Its additive v0.3.0
migration retains separate requested and active revisions/payloads. Reads do not initialize
policy rows. Explicit per-verb `SET`/`CLEAR` updates preserve the untouched verb, and a stream
change compares both its own revision and the inherited tenant revision.

Review produces a semantic diff, warnings and a five-minute token bound to actor, scope,
revisions, effective rules, owner facts and required acknowledgements. Impact completeness is
explicit; unassessed counts are not a complete census. Apply checks current authorization and
owner facts, then locks the tenant parent before the stream scope. Policy mutation, immutable
receipt and the authority-refresh obligation commit together. Owner RPCs are outside SQL locks.

An idempotency key identifies one logical command. The receipt retains the previous/new policy,
actor, request hash and review digest. Exact committed retries recover their original result
after review expiry, signing-key rotation, later changes or owner-service outages. Changed
rules, revisions, actor or acknowledgements cannot reuse that key. This recovery cannot create
a new command. A commit reports `pending`, never `effective` merely because storage succeeded.

The shared web editor keeps drafts in memory, independently for both verbs, and submits one atomic
update list. Draft changes invalidate review and preview; hypothetical coordinates invalidate only
preview. Request epochs include authenticated identity and scope so late responses cannot render a
previous tenant's state. Revision conflicts preserve the draft for explicit reload/review. Ambiguous
apply freezes the exact command and key, checks its receipt first, and only retries identical input
after a not-found result. A failed or partial observation is never presented as proof of no capacity.
The editor polls pending rollout only while visible and clean, with bounded backoff.

`pnpm --dir website_application test:components` exercises rendered controls separately from the
node-only unit suite. `pnpm --dir website_application dev:placement-fixture` serves a labelled,
local-only controls/status fixture for responsive inspection; it has no live API or media work and
cannot prove admission or first-media behavior.

Stream setup distinguishes the existing resolver's node-specific recommendation from generic
manual entries. The recommendation query omits credential/tenant metadata echoes, validates the
selected stream, masks credential-bearing URLs, and splits RTMP server/key only when the returned
terminal path segment is exactly that key. A protocol selector can require WHIP, RTMP or SRT;
changing it cancels an in-flight lookup and clears the previous recommendation. Missing protocol
URLs are not reconstructed.
Hosted Go Live prepares a destination explicitly and refreshes it at connection time through an
optional `resolveWhipUrl` SDK callback. This preserves capture/compositor state while changing the
WHIP destination. Resolution failures do not use configured static fallbacks; stop/destroy cancel
pending resolution. These connection improvements still use the existing backend resolver until
the live placement/admission integration below is complete.

The shared router bounds discovery to two seconds, each peer to one second, and the overall
operation to five seconds, leaving time for preparation. An unreachable peer makes observations
incomplete without erasing healthy same-priority candidates. Preparation must acknowledge the
exact tenant/object/source generation, cluster/node, protocol, policy digest/revisions and
attempt. Ambiguous acknowledgement is not capacity exhaustion or permission to reroute elsewhere.

Accepted origin pulls are recorded in the destination-keyed registry without fabricating
a live stream instance or an input counter. Node-reported media state is independent of
preparation, and reusing a prepared pull does not refresh or overwrite that state. A
successful preparation acknowledgement is not proof of first media.

The IPC preserves the observations needed to distinguish buffer health from source identity.
`STREAM_BUFFER.buffer_pid` is the emitter's `X-PID`: Mist's buffer process, not the publisher
connector. Its event UUID/time remain on the enclosing trigger. Periodic and targeted
`active_streams` reads carry `process_observation` with the exact runtime name, reported buffer
PID, sorted track-owner PID census, optional first/last media timestamps, and the original API
read window. Missing or malformed source PID data is unknown; a valid empty array is known empty.
Numbers outside JSON's exact integer range are not retained as process or timestamp evidence.
The converter never invents admission generation, connector identity, or a read window from send
time. Existing request-order and node-runtime cancellation fences still govern publication.

Foghorn retains detached, process-local observations on the authenticated node's stream instance.
These observations do not refresh operational liveness, change source ownership, or make a
preparation ready. Older read windows/buffer events cannot replace newer observations; offline
and inventory-absence transitions clear them. They are deliberately excluded from persisted
instance snapshots: rehydration must obtain a fresh node report. Numeric PID reuse, retained
buffer media and an old in-flight read still require causal generation/physical-pull matching;
this observation path is not that first-media proof. Reported first/last media timestamps are
raw telemetry, not independently verified media boundaries or proof of progress.

Source-side acceptance also records the exact outbound destination before acknowledging
the handoff. The outbound set has its own monotonic revision, separate from publisher
ownership and wall-clock metadata. It preserves an explicitly cleared set during stale
snapshot merging. Expiry compares the observed attempt and last renewal against current
durable state; failures are retried by later sweeps rather than becoming local success.
Live-stream pulls require a matching tenant and a serving instance on the exact source
node, not merely a stream present elsewhere in the cell. Registry source revisions use
exact decimal comparisons in Valkey Lua, including values beyond floating-point precision.

Federation stream discovery advertises a publisher generation and revision only for the
confirmed push publisher on that exact node. Origin and buffer facts come from each node's
stream instance, not the stream-wide last reporter. This applies to both `QueryStream` and
periodic `StreamAdvertisement` messages. Advertisements read a local registry snapshot without
per-node control-plane hydration. Both PeerChannel receive directions share one projection,
preserving RAM, publisher generation/revision and the node's virtual media cluster separately
from the sending control cell. Malformed generation pairs and replicas claiming publisher
generations are omitted; legacy peers remain explicitly unbound. Publisher withdrawal removes
the generation even if an older live buffer observation remains. When discovery supplies a generation,
origin-pull arrangement requires the source to acknowledge that generation/revision, attempt,
tenant, source node and exact destination. The source rechecks the projected publisher during
its outbound-record CAS; a mismatched or unbound acknowledgement cannot create the destination
record. A confirmed newer generation can replace an older preparation from the same source
cell. Completion retains the generation fence, so delayed acceptances cannot downgrade it or
reopen the completed attempt, including after Valkey restart.

Unbound source requests remain supported for sources without push-generation evidence; managed
inputs and artifacts do not acquire an invented push generation. This handshake is not a
policy capability: policy, protocol and expiry are enforced by the placement gate and final
admission, and first-media proof is separate from both.

Origin-pull notification can bind the source control cell and virtual media cluster explicitly.
Both identities must be supplied together with an exact source node and attempt. The source
rejects a wrong cell, mismatched virtual cluster or missing membership before recording a pull;
the destination requires the matching acknowledgement even for sources without a push generation.
The inbound ledger retains its source cell in `SourceClusterID` and its virtual cluster separately
in `SourceMediaClusterID`; outbound records also retain the virtual cluster. Registry replay and
restart preserve both, including clearing tombstones. Missing virtual-cluster metadata is not
inferred from a newer advertisement. The destination revalidates through the source using the
existing physical attempt, then fills the missing binding without starting or relabeling a second
pull. A conflicting bound virtual cluster is refused before notification. A confirmed newer source
generation can replace the old binding; an unchanged source cannot move or erase that binding.
Legacy requests remain explicitly unbound in the acknowledgement and cannot weaken validated
source-side tracking. These checks bind source identity, not current owner consent or first media.
The operator registry debug response exposes source cell and virtual cluster separately, together
with the exact destination, physical attempt and generation. Source revisions are decimal strings
so diagnostic clients do not round high-range integers.

## Runtime assembly

Before listeners start, Foghorn configures the signed-policy destination with one receipt store,
policy gate and verb-dispatched ingest/live-push runtime. It shares the existing source registry
and physical pull arrangement, including the direct same-cell handler. Discovery and that handler
must identify the same control cell. Configuration does not contact peers or promote authority
readiness. Missing Redis or signing configuration does not manufacture a usable preparation path.
Quartermaster inventory and Commodore publisher-ownership reads follow the current connected
clients, including recovery from degraded startup; unavailable clients remain unknown, not empty
inventory or proof of an unowned stream.

The same startup then installs the public ingest and viewer routing adapters on both front doors,
the stored-media permitter, prepared-source admission, and final publisher and viewer admission,
and only then records this replica as enforcing. Assembling the destination does not by itself
activate schema-2 policy: issuance follows the fleet attestation described under "Activation
barrier". Actual media, reconnect and control-plane-outage proof remain required. Cold/warm
first-frame timing is optional diagnostic evidence, not an architecture or release gate.

Quartermaster's `PeerCluster.control_cell_id` carries the canonical cell behind its assigned
Foghorn endpoint. It uses the same explicit-control-cell, cell, then cluster fallback as signed
grant compilation. Leased peer hints preserve that identity together with the selected address;
all replicas import it through shared discovery. Placement uses `GetControlCellAddr`, not
cluster-keyed `GetPeerAddr`. A matching cluster name without explicit cell metadata is not a cell
mapping. When several virtual clusters advertise replicas of one cell, address selection is
deterministic and does not require a live telemetry channel. Reassignment, withdrawal and lease
expiry remove the mapping on peer reconciliation; an older writer without the field cannot
retain another address's cell identity. Receipt-bound source pulls use the same canonical lookup,
and their direct same-cell shortcut requires an exact cell match rather than a registry alias.
Mixed-version peers without this metadata remain unavailable to placement. This does not establish
fleet readiness, change tenant entitlement, or issue policy-bearing authority.

The existing ingest resolver now consumes fresh, node-specific Mist protocol reports instead
of fabricating RTMP/SRT ports or assuming every ingest-capable node offers WHIP. Missing
protocol URLs remain absent; HTTP WHIP redirects filter the protocol before candidate
truncation. Protocol observations have a separate 30-second lifetime and empty reports
withdraw old endpoints. A real Mist container contract covers nonstandard listener ports,
HTTP public-address reconciliation, WHIP HTTP-versus-UDP separation and listener withdrawal.
The additive `shared.IngestEndpointRequest.protocol` enum is forwarded by both shared clients
and Commodore. Foghorn filters it before selection; unknown values are rejected before authority
lookups, and an unavailable requested protocol cannot fall back to another protocol. Unspecified
retains manual discovery of any advertised ingest protocol.
HTTP GET carries the same requirement as one lowercase `protocol` query parameter; WHIP POST
rejects a conflicting parameter. Invalid/duplicate parameters fail before authority lookup.

The GraphQL source adds optional `protocol: MediaIngestProtocol` (`WHIP`, `RTMP`, `SRT`). The
generated frontend operation carries it, hosted Go Live always requests WHIP, and the frontend
also rejects a mismatched response. The generated GraphQL resolver forwards the requested protocol
to the protocol-aware gateway helper; field-level WHIP/RTMP/SRT regressions cover that boundary.
The shared StreamCrafter `IngestClient` also requests WHIP, covering standalone React, Svelte
and Web Components gateway resolution. It rejects an invalid primary WHIP URL, drops unusable
fallbacks and clears prior endpoints before a new lookup. These clients require the matching
GraphQL server schema; they do not retry a downgraded query that omits the protocol requirement.
The shared SDK bounds the whole lookup, including backoff and body decoding, to five seconds.
Replacement/destroy cancels the active request and settles pending backoff; late replies cannot
publish endpoints or status. React/Svelte wrappers fence results by client identity and clear
recommendations when credentials change. Gateway error text is not forwarded into user-visible
errors, where it could expose stream keys. This does not replace connection-time revalidation or
final edge admission.
This is not a global placement or first-media proof.

### Prepared live viewer resolution

HTTP and gRPC have a shared live-push resolver backed by signed placement authority,
source-generation discovery and the coordinator-independent placement router. It rechecks
authority and source generation after exact-node preparation and caps the returned lifetime
by both source observations. A changed authority, source or expired observation cannot return
a playable destination. The destination supplies both its playback URL and public base; the
coordinator does not construct alternative node URLs or overwrite metadata with local buffer state.

The shared viewer RPC accepts a playback `protocol`, forwarded by the Commodore and Foghorn
clients and Commodore proxy. An explicit format is never replaced with another. An unspecified
format negotiates WebRTC then HLS, stopping at the first accepted preparation; an ambiguous
preparation failure does not authorize trying another format. Only the prepared protocol appears
in outputs, using the existing player catalog keys. Additional playback formats need a fresh
resolution; they are not advertised as unprepared fallback URLs. Public format aliases are
normalized independently of trusted Mist connector observations.

GraphQL exposes the optional `protocol: MediaViewerProtocol` argument and the canonical
`ResolveViewerDestination` operation. The generated field forwards every enum value to the
protocol-aware Commodore client; omitting it preserves automatic negotiation. Demo Mode filters
its synthetic outputs to the requested format without live service calls and rejects formats
absent from the fixture. It does not substitute WHEP for WebSocket WebRTC.

The low-level player `GatewayClient` accepts a typed `protocol` configuration. Explicit requests
never retry an unqualified GraphQL query. They reject mismatched protocol, URL scheme, node or
output evidence, retain only the selected URL's matching output, and advertise no fallback nodes.
Changing the configuration aborts prior work and retry delays; late bodies cannot publish an old
destination or clear a newer in-flight request. The controller preserves Gateway/provided playback
URLs during Mist hydration, status polling and cold recovery. Mist contributes track and datachannel
metadata, not additional destinations or formats. Cold recovery retains the selected node and
metadata without requiring Mist to repeat a source catalog. Direct-Mist mode still discovers sources
from Mist. Resolution epochs fence stale replies and recovery across detach/reselection; cleanup
captures the originating clients rather than destroying replacement clients.

The controller supplies a fresh-resolution callback to PlayerManager. Selection excludes failed
player/source combinations, not whole source URLs, so another compatible player can reuse the
prepared endpoint before a new format is requested. After those combinations are exhausted,
Gateway mode requests a typed, previously untried format supported by a registered player.
At most three fresh resolutions are allowed per initialization; resolution failure stops that
attempt, without restoring an old source or issuing an unqualified query. The newly accepted
destination replaces the source set and its status poller. Local media retry and VOD playhead
restoration remain owned by PlayerManager. Metadata updates do not initiate format resolution.

`viewerProtocol` pins the Gateway requirement through controller and vanilla/React/Svelte/web
component construction; automatic fallback cannot change it. Header-auth playback initially
requests HLS, and format fallback filters transports that cannot carry playback headers.
Generated API, low-level client, manager/controller integration and wrapper contracts are tested.
The standalone React `useViewerEndpoints` hook and Svelte `createEndpointResolver` store use the
same GatewayClient, including typed `protocol`, account credentials and `playbackAuth` forwarding.
They clear old destinations on replacement and cannot publish a superseded or destroyed request.
React hides a previous request's result during render, before its effect cleanup. Svelte `update`
and `refetch` cancel previous work; `destroy` is terminal. GatewayClient decodes JSON-scalar output
catalogs before validating an explicit format and rejects malformed catalogs without mutating the
response object. React, Svelte and WC player wrappers replace the active controller when content,
Gateway/Mist entry, account/viewer credentials, required format or supplied endpoints change.
Svelte owns the store across reactive updates and suppresses obsolete attach failures. WC keeps
its container across disconnect/reconnect but does not resolve while disconnected. Equivalent
request fields do not recreate playback. Supplied endpoint objects are immutable inputs: replace
the object to publish a new destination. Svelte retains the store's unproxied identity so it reports
current attach failures while ignoring superseded ones. React, Svelte and WC update debug,
autoplay and muted options without replacing the destination, including during a pending attach.
Only changed option values are forwarded; an unrelated update cannot reset the viewer's own mute
control. Removing these props restores their defaults (debug/muted false, autoplay true). Autoplay
changes govern subsequent autoplay attempts, not an immediate play/pause command. The Svelte store
also retains runtime updates made before attachment. Mounted and controller tests cover these
boundaries; other option-update semantics, real-browser configuration UX and actual media proof
remain open.

Commodore routes the resolution request using the content owner, including for authenticated
viewers belonging to another tenant. A viewer's own tenant cannot substitute its coordinator or
authority. This chooses where to resolve the content, not where the viewer must receive media:
the resulting prepared edge may belong to another authorized cell. The proxy preserves viewer
IP/token, requested protocol, payment headers and the destination returned by Foghorn.

The HTTP path preserves explicit WHEP and resolves manifest-format requests before preparation:
`cmaf/index.mpd` selects DASH, not an HLS URL on the same container path. Conflicting manifest
formats and unsafe path segments are rejected. Unknown geography remains unknown, not `(0,0)`;
public latitude/longitude hints cannot supply trusted location. Unsupported formats receive a client error; unavailable live placement is
retryable. A missing preparer fails closed rather than restoring legacy selection.

Live HTTP resolution preserves an explicitly supplied `jwt` query credential on the selected
playback URL and its output catalog. It clones the response before stamping credentials so shared
catalogs cannot retain another viewer's token. Existing destination query parameters are preserved;
unrelated caller query parameters are not forwarded. Responses use `private, no-store` and
`no-referrer`, and the HTTP routing log omits the credential-bearing redirect URL. Header/cookie
credentials are never converted into URL credentials: integrations using those transports must
attach them to the selected edge explicitly, rather than relying on cross-origin redirect forwarding.
Artifact and object-storage signed URLs are excluded from this stamping path.

Admission credentials are not telemetry. The shared Decklog client sanitizes a copy before the
telemetry RPC, and Decklog repeats that boundary before Kafka serialization. The original viewer
token/request URL remains available to the admission handler. Only validated `fwsid` correlation
survives URL query retention; event, tenant, serving/origin cluster and control-cell identities remain
unchanged. Helmsman omits raw rewrite bodies from logs. Periscope uses the shared URL sanitizer
again when writing diagnostics. This protects new writes, not historical retained records.

The existing Mist HTTP connector accepts `jwt` into its viewer token. With Helmsman's managed
`tknMode=15`, HLS propagates that token as `tkn` in playlist references. The isolated
`make verify-mist-viewer-credentials` contract exercises DTSC-to-HLS media with a fixed-token
`USER_NEW` hook, without `CONN_PLAY`, and checks missing/wrong-token segment rejection.
This is a transport contract, not cryptographic policy or two-cell placement proof. The hook does
not gate every playlist metadata response: final admission must also address pre-session metadata
and source-start behavior; a successful `USER_NEW` test alone cannot establish that boundary.

When viewer placement admission is configured, `PLAY_REWRITE` checks the same exact-destination
adapter before returning the resolved stream name or starting correlated viewer accounting.
Both pre-source and final admission bind the returned protocol to the trusted Mist connector;
an HLS connection cannot consume a WebRTC approval. The whole placement-enabled rewrite has a
three-second budget. Missing identity, unknown metadata-only protocol evidence and unavailable
admission fail closed, rather than inferring permission from the request URL. Metadata/internal
source coverage and avoiding duplicate WAN observation remain activation requirements.

Destination policy revalidation shares complete remote candidate observations for up to one
second through `PlacementObservationCache`; local-cell observations are always fresh. Concurrent
readers share one bounded peer request. The cache is scoped by peer cell/address, tenant/object/
internal name, source generation, protocol, policy digest/revisions and both local signed-authority
versions. Missing authority versions and ingest requests bypass reuse. Viewer coordinates are
excluded from unranked discovery, while every evaluation still uses that viewer's trusted location.
No ranking, permission, preparation or ownership claim is cached. Node and observation expiries
are unchanged; a stale candidate remains stale instead of disappearing from the census.

The cache retains at most 128 observations and 16,384 candidate rows, with at most 128 in-flight
keys. Reads have a one-second peer deadline independent of any one canceled viewer. Incomplete,
failed or expired observations are not retained; a verified complete empty pool can be retained.
New authority versions or a changed peer address require a new read. This bounds repeated remote
fanout; it does not eliminate first/expired observation reads, source-owner checks, exact local
admission or the fleet attestation that gates issuance. The destination startup assembly
installs this reuse for its policy gate; other independently assembled transports must share an
observation cache explicitly.

Helmsman does not recover public rewrites from an unsigned stream-name cache. Unavailable Foghorn
means unavailable new admission, not replay of another viewer's mapping. Foghorn likewise excludes
`PLAY_REWRITE` and `USER_NEW` from generic completed-trigger replay: retries recheck current policy
and the authenticated registration before and after evaluation. This does not replace signed
outage authority, HA failover, or session/correlation-based deduplication of accounting effects.

The extended real-Mist contract uses Helmsman's typed HTTP `deny` action and an independent
`STREAM_SOURCE` audit, with successful source startup as a positive control. The tested local
images `e00ab0d3bc81` and `f80f702773cf` do not pass it: denied HLS returns 404 headers but leaves
the response unfinished, and JSON status does not emit the expected pre-source rewrite. The denied
source did not start. These are functional failures, not first-frame timing targets.

`make verify-mist-current-source` exports a clean local Mist commit without changing its checkout,
builds in a temporary Docker context with network disabled and no dependency fallback, then runs
the same contract against the resulting immutable image ID. Set `MIST_SOURCE_DIR` and
`MIST_CONTRACT_BUILD_IMAGE` to the source checkout and an already cached Linux builder containing
Meson, installed dependencies and ffmpeg. Source revision and builder ID are retained in image
labels; the export and images remain available for diagnosis. This debug contract build disables
libav/ONNX processing and is explicitly not release certification.

The fresh build of Mist `68e5b9c962f643edfabfc061aef638e3e8626c09`, image
`2734bb4ce3e4c13e2ab56d5ce49f8219b6c0d5074f27186e424b6b4279989d68`, verifies the metadata
pre-source gate and authorized DTSC-to-HLS playback/token rejection. An allowed/denied/allowed
metadata sequence on a reused HTTP connection performs a fresh gate check for every request and
does not reuse the previous request's admission. It still fails terminal HLS
denial: 404 headers arrive but the response body never finishes. Neither denied path starts the
source. This isolates a generic HTTP denial-lifecycle correction, not a DTSC protocol enhancement
or a need for mandatory `CONN_PLAY`. Sibling source is unchanged. The fixture must not be relaxed
to treat an unfinished response as a successful final-admission proof.

Executable startup installs the marked-pull source adapter, the ingest and viewer placement
preparers on both front doors, the stored-media permitter, and final publisher and viewer
admission. Every content kind reaches the same signed policy through its own source reader:
an encoder push session, a configured pull input, a managed Mist-native input, an in-progress
recording over its parent stream, and stored artifacts. The older hostname-returning Mist
balancer route is removed; the surviving compatibility surface is the node-bound source lookup
and read-only diagnostics behind an internal-access check. Unit/transport tests and full HTTP
billing/redirect plus authenticated proxy tests establish identity, expiry and front-door
contracts, not first-media delivery. Cold/warm first-frame timing is optional diagnostic evidence,
not an architecture decision or release gate; functional playback, reuse, reconnect and failure
proof is required, as is actual two-cell playback.

Replacing the legacy candidate scorer alone would not reach this evaluator. The live integration
supplies complete coordinator-independent observations, prepares the exact selected destination,
tracks multiple destination pulls independently, and reevaluates on failed preparation without
bypassing policy. HTTP and gRPC routing share that orchestration, and final edge admission and
signed outage ownership enforce the same policy revision.

Signed-authority readers reject unknown fields and schema/revision rollback. Capability
negotiation covers direct URLs and final admission before policy-bearing authority is
distributed; that is the attestation barrier described above. Capacity-owner consent and
effective pricing, complete destination observations and preparation, and rollout observations
are in place. Managed and artifact source preview, verification of the authenticated API and web
editing flows in a real browser, release-runtime verification, user documentation and launch
content are not.

## Verification

### Read-only API demo

Bridge's Demo Mode uses dedicated `internal/demo/media_placement*.go` generators, not SQL seeds.
The shared evaluator and semantic diff functions operate on a fixed synthetic inventory for
official US/EU, tenant-private EU and marketplace US capacity. Demo requests resolve only fixture
stream IDs and owned fixture consent, never caller-supplied real resources. Capacity-only preview
does not invent a source; source-aware serving uses live demo-stream fixtures and reports the
synthetic pull separately. Active demo publishers fence ingest to their synthetic current cluster.
Price comparisons use an explicit, expiring EUR basis (`ingest:minutes=1;gib=1` or
`serve:minutes=1;gib=1`); absent bases are unknown, not zero-price quotes.

The demo remains stateless and read-only. Policy revisions stay zero, actions cannot manage,
no active policy is reported, and Apply returns a typed `UNSUPPORTED` result. Recovery reports
no saved change. Review tokens are deliberately non-admission `demo-preview-only:` markers;
reviews include a simulation warning and do not claim measured impact. Results/candidate names
mark the simulation. Authenticated demo requests are isolated just like anonymous ones: no live
service client, signing key, policy store or preparation is used, and no real identity is replaced
with demo credentials. Normal non-demo permissions and MCP tool access policies are unchanged.

API Explorer's **Media Placement** section provides policy/options reads, US-viewer/EU-publisher
preview, geographic fallback and a no-official review. Canonical GraphQL operations are tested
through the generated schema with no service clients, and MCP tests check read-only demo parity.
These proofs cover simulation/API contracts, not authenticated live webapp or media delivery.

### Placement engine and live integration

Run `make test-pkg GO_TEST_PACKAGES=./placement`. Tests cover private-first fallback,
hard-deny inheritance, stale/unknown facts, price basis, active ingest fencing, cold local
destinations, canonical digests, stable ties, and candidate-order invariance. The latter
also has a fuzz target:

```sh
make test-pkg GO_TEST_PACKAGES=./placement \
  GO_TEST_FLAGS='-fuzz FuzzCandidatePermutation -fuzztime 30s -parallel 2 -timeout 120s'
```

These tests are not a substitute for the two-Foghorn EU-ingest/US-viewer integration proof.

Foghorn's authority/census unit tests run through `make test-foghorn`. The generated-query
and tenant/version-fencing contracts are included in the PostgreSQL and Yugabyte Foghorn
verification targets. `make verify-foghorn-valkey` includes actual container-restart proof
for concurrent destination records, clearing tombstones and late-completion fences. Its federation
selector also includes receipt persistence, exact revisions above 2^53, and pre-restart attempt
fencing on the release-pinned engine. Unit tests cover competing replicas, changed intent, physical
pull conflicts, non-renewing retention, independent Redis expiry, corruption and simulated primary/
eviction changes. These receipt tests do not prove media preparation or final-admission integration.
