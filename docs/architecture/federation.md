# Foghorn Federation - Cross-Cluster Stream Delivery

Direct Foghorn-to-Foghorn gRPC protocol for cross-cluster stream replication, artifact access, and real-time telemetry exchange. Enables viewers to be served from the best edge regardless of which cluster hosts the stream.

## Architecture

```
Cluster A (tenant's preferred)              Cluster B (origin)
┌─────────────────────────┐                ┌─────────────────────────┐
│ Foghorn A (leader)      │                │ Foghorn B (leader)      │
│  ├─ PeerManager ────────│── PeerChannel ─│── FederationServer      │
│  ├─ FederationClient    │── QueryStream ─│── LoadBalancer (score)  │
│  ├─ FederationServer    │── NotifyOrigin │── ActiveReplication     │
│  └─ RemoteEdgeCache ◄───│── Telemetry ───│── PrepareArtifact       │
│         │(Redis)         │                │         │(Redis)        │
│  Foghorn A (replica)    │                │  Foghorn B (replica)    │
│  └─ reads RemoteEdgeCache                │  └─ reads shared state  │
│                         │                │                         │
│  Helmsman A1 ── Edge A1 │                │  Helmsman B1 ── Edge B1 │
│  Helmsman A2 ── Edge A2 │                │  Helmsman B2 ── Edge B2 │
└─────────────────────────┘                └─────────────────────────┘
         ↕ (DTSC replication between MistServer instances)
```

## Service Responsibilities

| Component        | Role                                                                                                                                                                                                                                                                                                           | Data                                                                                                                                                                                                                                                                                |
| ---------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| FederationServer | Handles inbound gRPC RPCs (QueryStream, NotifyOriginPull, PrepareArtifact, PeerChannel, CreateRemoteClip, CreateRemoteDVR, ListTenantArtifacts, MigrateArtifactMetadata, ForwardArtifactCommand, MintStorageURLs, DeleteStorageObjects)                                                                        | Reads local LoadBalancer scores; records outbound pulls on StreamRegistry's Location[local].OutboundPullers (NotifyOriginPull); writes federation telemetry to RemoteEdgeCache                                                                                                      |
| FederationClient | Pool wrapper for outbound unary RPCs to peer Foghorns                                                                                                                                                                                                                                                          | Uses FoghornPool lazy connections                                                                                                                                                                                                                                                   |
| PeerManager      | Manages PeerChannel lifecycles, peer discovery, telemetry push/recv, leader election                                                                                                                                                                                                                           | Redis leader lease, peer address map                                                                                                                                                                                                                                                |
| StreamRegistry   | Unified per-stream identity + replication + admission state (control package). Federated peer ads upsert here as `Locations[peer_cluster]`; local in-flight pulls land as `Locations[local].ReplicatingFrom + PullDTSCURL + DestNodeID`; source-side outbound pulls land as `Locations[local].OutboundPullers` | Redis backing (`{cluster_id}:registry:source:*`, `{cluster_id}:registry:artifact:*`) with cross-instance changelog replay (ordered Redis Stream; see foghorn-ha.md); SweepStaleLocations (30s tick / 5-min maxAge) ages stale federated entries + per-OutboundPull entries          |
| RemoteEdgeCache  | Federation telemetry cache plus source-revision-fenced stream peer membership (Redis). Stream identity / playback index / active replication live in StreamRegistry.                                                                                                                                           | remote_edges (30s), remote_replications (5m), edge_summary (60s), remote_live_streams v3 per origin (30s live / 1h offline), remote_artifacts (90s), stream_peer_memberships (non-expiring active records; ended fences use DB-proven coordinated retention), peer_heartbeats (30s) |
| Quartermaster    | Peer discovery via `ListPeers(cluster_id)`                                                                                                                                                                                                                                                                     | Returns peer cluster addresses and shared tenant lists                                                                                                                                                                                                                              |

## Data Flows

### Cross-Cluster Viewer Routing

```
Viewer → Foghorn A (tenant's cluster)

1. Resolve playback_id → stream_name + origin_cluster_id (Commodore, cached)
2. Score local edges (sub-ms, in-memory)
3. Score remote edges from EdgeSummary in Redis (sub-ms, from PeerChannel data)
4. If remote wins and no in-flight replication on StreamRegistry:
   a. QueryStream → Foghorn B: returns scored EdgeCandidates with DTSC URLs
   b. Score remote candidates vs local (CrossClusterPenalty=200)
   c. If origin-pull: NotifyOriginPull → StreamRegistry.MarkReplicating on A + RecordOutboundPull on B → tell Helmsman DTSC source
   d. If redirect: 307 to remote cluster's play endpoint
5. PeerChannel opens (if not already): B pushes EdgeTelemetry (5s), A writes to Redis
6. Steady state: all edges (local + remote) scored on every viewer request from Redis
```

### PeerChannel Telemetry Exchange

PeerChannel is a bidirectional gRPC stream carrying 8 payload types via `oneof`:

| Message               | Interval                 | Direction | Purpose                                                                                                               |
| --------------------- | ------------------------ | --------- | --------------------------------------------------------------------------------------------------------------------- |
| EdgeTelemetry         | 5s                       | Both      | Per-edge BW/CPU/RAM/geo for scoring remote edges                                                                      |
| ReplicationEvent      | On change                | Both      | Origin-pull started/stopped (prevents redirect loops)                                                                 |
| ClusterEdgeSummary    | 15s                      | Both      | Smoothed 30s-avg per-edge data for cheap cluster comparison                                                           |
| StreamLifecycleEvent  | On change + 5s heartbeat | Both      | Stream live/offline (cross-cluster ingest dedup)                                                                      |
| StreamAdvertisement   | 5s                       | Both      | Push-based stream directory with per-edge scoring; builds Adj-RIB-In, eliminates Commodore dependency in steady state |
| ArtifactAdvertisement | 30s                      | Both      | Hot artifact locations on peer edges (avoids S3 round-trips)                                                          |
| PeerHeartbeat         | 10s                      | Both      | Cluster liveness, protocol version, capabilities                                                                      |
| CapacitySummary       | —                        | Both      | Cluster-wide aggregate capacity (proto shell for dCDN bidding)                                                        |

Lifecycle frames carry the publisher source revision. A cell's global PostgreSQL source-revision
allocator is a PostgreSQL sequence shared by old and new replicas. The v0.3 expand migration locks
and advances it above every durable session and effect revision (and the compatibility clock used by
pre-release builds), so rolling Foghorns cannot issue the same token. Repair that must jump above an
external watermark releases its read-phase stream lock, briefly locks and advances the sequence in a
standalone transaction, then reacquires and revalidates the stream before the Redis CAS. Neither slow
Redis I/O nor a stream-lock waiter can overlap the allocator lock, and an unused token after failed
revalidation is an intentional harmless gap. A per-stream repair can never rewind another
stream's ordering fence. Receivers keep a separate Redis record for
each origin cluster and apply the revision CAS only within that origin: lower same-origin revisions
are ignored, and offline wins an equal-revision tie. Per-cell PostgreSQL sequences are not compared
across origins. Lookup returns any origin whose live record remains current. This ordering applies
across PeerChannel reconnects, not only within one gRPC stream; periodic live heartbeats reuse the
active tracked generation's revision. Outbound channels bind lifecycle payloads to the configured
peer cluster. Inbound channels use the first frame's cluster as a channel label and require every
later frame and lifecycle payload to repeat it; this is attribution inside the provider-operated
service-token trust boundary, not cryptographic cluster identity.

### Peer Discovery

1. **Demand-driven** (fast): Stream validation returns `cluster_peers[]` from Quartermaster. The admission-effects obligation persists their complete hints; its leader-owned `TrackStream` step CAS-replaces the generation's complete membership (including an empty set) by `source_revision` and opens required PeerChannel connections before lifecycle broadcast can complete. Ended revisions remain as fences until PostgreSQL proves no pending admission callback can issue an older write. Publisher source revision is not topology authority: conflicting active addresses fail closed unless the current leased Quartermaster snapshot resolves them.
2. **Reconciliation** (5-min polling): PeerManager.refreshPeers calls `Quartermaster.ListPeers(cluster_id)` to catch topology changes.
3. Federation address convention: `TenantClusterPeer.foghorn_grpc_addr` is the internal Foghorn listener for the peer cluster, normally a mesh address on `:18019` with `foghorn.internal` TLS identity. The durable admission hint and `TrackStream` consume that Quartermaster-provided address. Missing peer addresses are a control-plane discovery problem; federation must not silently fall back to the public edge-bootstrap listener.

### Peer Lifecycle Types

| Type          | When                              | Example                                                                         |
| ------------- | --------------------------------- | ------------------------------------------------------------------------------- |
| Always-on     | Official ↔ preferred cluster pair | Coverage PeerChannel for ClusterEdgeSummary                                     |
| Stream-scoped | Other subscribed clusters         | PeerChannel opens on first stream, closes when last stream ends (UntrackStream) |

### Cross-Cluster Artifact Access

Cross-cluster artifact access is a read-through model: no bulk replication, no
copy jobs. The requesting cluster resolves a durable or hot source at the
origin, records a local pointer row, and its Helmsman relay/block cache reads
bytes on demand. Four pieces cooperate:

| Piece                                                        | Where                                                                                              | Role                                                                                                                                                                                                  |
| ------------------------------------------------------------ | -------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `PrepareArtifact` (federation RPC)                           | `api_balancing/internal/federation/server.go`                                                      | Origin-side decision: presigned S3 GET, peer-relay grant, storage redirect, or not-ready                                                                                                              |
| resolve→authorize→adopt front doors                          | `api_balancing/internal/control/cross_cluster_artifact.go`, `playback.go`, `triggers/processor.go` | Requesting-side allowlist enforcement + local pointer-row adoption (`/play` and direct-edge STREAM_SOURCE)                                                                                            |
| `RelayResolve` (Helmsman ⇄ Foghorn)                          | `api_balancing/internal/control/relay_resolve.go`                                                  | Byte-serve-time resolution for Helmsman's `/internal/artifact/*` relay: presigned URLs + sidecars + size from the adopted row                                                                         |
| `MintStorageURLs` / `DeleteStorageObjects` (federation RPCs) | `federation/server.go`                                                                             | Delegated **write** against a storage cluster the caller cannot mint for locally. The whole inbound surface is gated by `FEDERATION_ENABLED` and restricted to provider-operated Foghorns — see below |

#### Storage ownership gate: canLocallyMintFor

One rule decides "can this Foghorn pool sign S3 URLs for that cluster":
the target cluster must be **served locally** (`isServedCluster`) AND the
local S3 client's backing tuple (bucket + endpoint + region + prefix,
full-tuple equality — bucket name alone collides across MinIO/R2/Bunny, and
two clusters can share one bucket/endpoint/region but write under different
prefixes, so prefix is load-bearing) must match the cluster's **advertised
backing** from Quartermaster's `cluster_peers` metadata for that tenant. The same rule is used symmetrically:

- `PrepareArtifact` emits `RedirectClusterId` when the artifact's
  authoritative cluster (`COALESCE(storage_cluster_id, origin_cluster_id)`)
  fails the gate — read redirect.
- `MintStorageURLs` rejects with `storage_not_owned_here` when the caller
  claims a target this pool doesn't own — write validation.

Sharing the rule prevents the asymmetry where a pool would redirect a read
but accept a write to the same cluster.

#### PrepareArtifact decision tree (origin side)

```
Viewer requests clip/VOD on Cluster A, artifact lives on Cluster B:

1. Foghorn A: PrepareArtifact(artifact_hash, tenant_id) → Foghorn B
2. Foghorn B: queries foghorn.artifacts (tenant-scoped, status != 'deleted')
3. If the row's authoritative cluster is one B can't mint for
   (canLocallyMintFor fails): returns RedirectClusterId → A re-issues
   PrepareArtifact to that storage cluster (single hop; chained redirects
   are rejected, and the redirect target is allowlist-authorized BEFORE
   the dial).
4. If sync_status='synced': mints presigned S3 GET URL (15min) → returns to A
5. Else if storage_location is local/freezing and a local origin node has
   the canonical full file on disk (foghorn.artifact_nodes row with
   role='origin', is_complete=true, not orphaned, last_seen < 90s;
   base_url via node_outputs): Foghorn B mints a short-lived opaque
   capability grant (5min TTL, stored in B's Redis, bound to {origin node
   id, artifact_hash, allowed media + .dtsh paths}) and returns
   peer_relay_url + peer_relay_grant_id → A. Cluster A's Helmsman block
   cache fetches blocks directly from origin node's Helmsman with the
   grant id as Authorization: Bearer. No S3 sync wait.
6. Else: returns Ready=false; Foghorn A surfaces 503 to the viewer.
   The freeze pipeline lands the bytes asynchronously; the next viewer
   attempt picks up where the failed one left off.

For clip/VOD: returns a single URL — either S3-presigned or peer-relay —
plus format, stream_internal_name, and size (the block cache needs size
up-front to plan range splits).
```

The peer-relay grant is an opaque random id, not a self-validating token:
the serving edge holds no signing key. When the origin node's Helmsman
receives the pull, it asks its own Foghorn (AuthorizeRelayPull over the
control stream) to confirm the grant against the artifact, serving node,
and exact request path that Foghorn minted. The requesting cluster treats
the id as opaque and forwards it through to its local block cache. This
keeps trust boundaries intact (no key on edges, no cross-cluster key
distribution; a grant's access ends when its short TTL expires, with no explicit revoke) while letting
hot-but-unsynced artifacts serve viewers immediately across the federation.

**Recursion invariant**: the peer-relay fallback only ever points at a
_local_ origin node. A cluster never returns another peer's relay URL, so
resolution cannot chain through clusters that point back at each other.

#### Resolve → authorize → adopt (requesting side)

Both viewer front doors share one path for vod/clip artifacts — including
processed-VOD outputs and finalized DVR-chapter artifacts, which are
`vod`-shaped rows:

- **`/play`** (`playback.go` `resolveRemoteArtifact`): playback_id resolved
  via Commodore names a remote `origin_cluster_id`.
- **Direct-edge STREAM_SOURCE** (`triggers/processor.go`): a viewer landed
  on an edge without going through `/play` (`vod+`/`processing+` stream
  names), so there is no local artifact row and no warm state; STREAM_SOURCE
  is the front door and runs the same chain via
  `control.ResolveAndAdoptRemoteArtifact`.

The chain: enforce the tenant peer allowlist on the origin cluster **and**
on any storage-redirect cluster (before dialing it — a compromised origin
must not point the tenant at a cluster outside its allowlist), federate via
`PrepareArtifact`, then **adopt** a local `foghorn.artifacts` pointer row
via the single shared upsert `adoptRemoteArtifactRow`:
`storage_location='s3'` always (the artifact's home is the authoritative
cluster's S3, which also keeps the row out of the freeze reconciler);
`status='ready'` and `federated_pointer=true` distinguish the local routing
pointer from a locally produced artifact;
`sync_status='synced'` only when origin returned a durable S3 URL, else
`'pending'`; re-adoption ratchets sync_status up to synced, never back down;
identity fields fill blanks only. Adoption and advancement of
`catalog_synced_rev` commit in one local transaction because origin-owned rows
are intentionally excluded from this cell's catalog projector. A delivered
signed artifact tombstone marks the pointer `deleted` and settles the resulting
local revision in the same authority-apply transaction. A bounded pointer purge
then removes the routing row after retention while preserving the signed
object-authority tombstone as the durable version/resurrection fence. A pointer
can own cell-local thumbnails or disposable cache copies; it does not own the
remote parent bytes. Purge installs a durable token and lease under the
shared thumbnail/authority asset lock. Discovery only admits pointer rows in
`status IN ('ready','deleted')`; fencing a ready cache pointer first terminalizes
it, then installs `federated_purge_token` and its lease before any external
cleanup. The worker uses the recorded derivative backend to
sweep bytes while the pointer remains terminal, and then rechecks token ownership
under that lock. A legacy pointer without destination rows may synthesize a local
destination only when its recorded backend fingerprint exactly matches this
cell's immutable backend fingerprint. Successful settlement removes thumbnail control rows and either
hard-deletes the parent or, when newer active authority arrived during cleanup,
restores it with `has_thumbnails=false`. Cleanup failure leaves the pointer
terminal and makes the lease reclaimable. Artifact authority apply takes this
same asset lock before its authority-identity lock, so projection and purge use
one order. Pointer age is read exclusively from
`federated_purge_eligible_at`; node reports, access metadata, storage-location
updates, thumbnail state, and re-adoption do not mutate it. The scheduled pass
runs after locally owned byte cleanup with its own lifecycle-cancelled two-minute
budget through the same bounded worker pool. A separate 30-second recovery
goroutine continues while that scheduled cleanup is busy and resumes every
expired tombstone, stale, and active-restore claim with at most eight concurrent
two-minute candidate budgets; claim release/deferral has its own five-second
database budget. Live artifact-node or chapter-ledger
rows prevent hard deletion. Signed object tombstones are terminal and reject
later active object authority rather than being reversed.
If no tombstone arrives, the same bounded job may evict a pointer past its cache-age threshold once
there is no unexpired active signed authority for its tenant and artifact. That
fallback removes routing metadata and its cell-local derivatives only; a later valid federation response may
recreate it.

Federated pointers are excluded from the local retention selector regardless
of `retention_until`; the origin cell owns byte retention. Re-adoption repairs a
legacy `active` pointer to `ready`, but it cannot revive a pointer covered by a
signed object-authority tombstone. An artifact hash already owned by another
tenant or by a genuine local artifact is never converted into a federated
pointer.

Adoption is **load-bearing, not best-effort**: `RelayResolve` deliberately
never federates by hash on a missing row (it has no requesting-tenant
context to enforce the allowlist), so a failed upsert fails the whole
resolution closed — better a retryable error than a relay URL whose byte
GET will 404 for the resolve-cache TTL with no self-heal.

Front-door **re**authorization: an adopted pointer row persists, but every
new STREAM*SOURCE/`/play` re-checks the row's authoritative cluster against
the tenant's \_fresh* `cluster_peers` envelope, so a revoked peer stops
serving on the next open.

#### RelayResolve (byte-serve time)

Helmsman's `/internal/artifact/*` relay encodes only (kind, hash, ext); on
a cold serve it sends `RelayResolveRequest` up the control stream and gets
back state (`PLAYABLE` / `SOURCE_MISSING` / freezing), a presigned media
GET (1h TTL, above the relay's refresh window), a `.dtsh` sidecar GET URL
(read-only; the durable index is published only via the staged
`TriggerDtshSync` path, never a direct relay PUT), expected size, and the
stream_internal_name for nested clip paths.
Resolution reads the **adopted local row only**. Two fallbacks apply before
404, in order: (1) a local origin node holds the canonical file
(hot-but-unsynced) → peer-relay grant; (2) the row points at a peer
cluster → federate via the adopted-row path (nil redirect authorizer: the
row's clusters were allowlist-checked at adopt time). S3 authority is gated
on `sync_status='synced'` to close the post-processing race where `s3_url`
still points at the original upload while a rewritten container syncs.

#### MintStorageURLs (delegated writes)

The write-side mirror: a Foghorn that cannot mint locally for the tenant's
storage cluster asks the owning pool for a presigned PUT URL. The callee
validates, in order: federation service auth → `canLocallyMintFor`
(ownership claim) → artifact-type guard → tenant ownership + asset context
(local `foghorn.artifacts` first, Commodore
`Resolve*Hash`/`ResolveInternalName` fallback).

**Only `thumbnail` single-PUT (15min expiry) is supported.** Federated
ARTIFACT freeze — `clip`, `vod`, `dvr`, `dvr_segment`, `dvr_manifest` — is
rejected at this boundary with `federated_artifact_freeze_unsupported`:
there is no cross-cluster completion propagation, so the storage-owning
Foghorn's cache-healed row would never observe the upload and would strand
at `pending`. A thumbnail upload has no such round trip — the caller
consumes the returned `S3Key` immediately — so it is the one valid
delegated write. Full artifact federation returns only with the
descriptor/completion protocol. Outcomes are counted per reason
(`accepted`, `storage_not_owned_here`, `tenant_mismatch`,
`federated_artifact_freeze_unsupported`, …).

`DeleteStorageObjects` is the delete counterpart. The inbound cross-cluster RPC
surface is gated as one policy. With `FEDERATION_ENABLED=true`, the entrypoint both
registers the federation server and enables `QueryStream`, `PrepareArtifact`,
`NotifyOriginPull`, `PeerChannel`, `MintStorageURLs`, `CreateRemoteClip`,
`CreateRemoteDVR`, `DeleteStorageObjects`, `ForwardArtifactCommand`,
`MigrateArtifactMetadata`, and `ListTenantArtifacts`. With federation disabled,
the server is not registered and its zero-value gate still fails every RPC closed.

This release's trust boundary is the shared internal service credential held only
by provider-operated physical Foghorns. Cluster IDs are routing attribution among
those trusted services. Self-hosted Helmsmans neither receive that credential nor
participate in Foghorn federation. Third-party or sovereign Foghorns are not
admitted to this boundary; non-forgeable cluster identity and caller-bound tenant
authorization remain prerequisites for that future model in
`docs/rfcs/service-identity-and-cluster-binding.md`.

When enabled, the delete flow is: the caller resolves the target key/prefix
from its **own** authoritative row and sends it; the callee validates
ownership/tenant/path-shape and deletes exactly that target (never
reconstructing from local rows, which may be cache-healed stubs). Not-found
targets return accepted=true for idempotent retries; auth/ownership/shape
failures never collapse into not-found success.

For `artifact_type=thumbnail`, the only accepted target is the exact
hash-scoped prefix `thumbnails/<artifact_hash>/`; it is intentionally not
tenant-prefixed. The authenticated tenant must still own the matching local
artifact row before the prefix can be deleted. Broader prefixes, alternate
hashes, individual keys, and traversal-shaped targets fail closed.

While the mutation surface is disabled, the artifact purge (`purge_deleted.go`)
also **excludes remote-owned rows** from its bytes+rows sweep: their bytes can't
be freed, so reaping them would fail forever and a page of them would starve
reapable local rows. They are retained (never lost) until cross-cluster deletion
is enabled.

#### Rolling DVR (dvr+) cross-cluster arrange

The rolling-DVR surface is not an artifact fetch — it is a live DTSC pull.
When STREAM_SOURCE fires for `dvr+<token>` on a cluster that is not
recording the source stream, `tryArrangeDVRCrossCluster`
(`triggers/processor.go`) consults the StreamRegistry's per-peer
`Locations` for a peer advertising a `RecordingNodeID`, gates that peer
against the tenant's fresh cluster-peer envelope (registry state can
outlive a revocation), and arranges an origin-pull from the recording node
via `federation.ArrangeOriginPull` — the viewer's edge then pulls the
rolling recording over DTSC. No advertised recording or a failed
arrangement → `offline:not_recorded`. Signed DVR authority includes both the
parent stream ID and its Mist routing name. A source-promoted remote DVR can
therefore build the same arrange request without requiring a local artifact row
or returning to Commodore.

#### DVR chapter replay

DVR archive playback does not use whole-artifact `PrepareArtifact`. A DVR can
run for months; replay is sliced into finalized chapter VOD artifacts:

1. Gateway calls Commodore `RetrieveDVRChapter` / `ListDVRChapters`.
2. Commodore validates tenant ownership, routes to the DVR's
   `origin_cluster_id`, and returns the chapter metadata. Each chapter
   carries a Commodore-minted, externally shareable `playbackId` (in
   `commodore.dvr_chapter_playback`) once the chapter has been dispatched
   for finalization. Active-but-unfinalized chapters carry no `playbackId`;
   the rolling DVR's own `playbackId` (`dvr+<dvr_internal_name>` surface)
   serves the in-flight portion.
3. Chapter playback flows through the standard artifact playback path —
   the chapter `.mkv` is a `chapter` artifact with VOD-shaped byte layout (with
   `origin_type='dvr_chapter'`, `library_visible=false`). Edges resolve
   the chapter playback_id through Commodore exactly the way they resolve
   any VOD playback_id, and serve it via the relay/block-cache path. The ID is
   a locator, not an allow: the chapter snapshots its parent DVR's public/JWT/
   webhook policy at registration and every normal playback gate enforces it.

Federation `PrepareArtifact` rejects DVR. Chapter replay uses the chapter
API + normal artifact playback; cross-cluster requests for the chapter use the
VOD relay/delete key shape. A federated chapter row is only a local routing
pointer: primary-media retention and byte-purge queries exclude every `federated_pointer`, so
a consuming cell cannot instruct the origin to destroy owned bytes when it
removes its pointer. Its local metadata row is eventually purged either after a
signed tombstone is durable, or after the pointer passes its cache-age threshold with no
unexpired active signed authority. A tombstone remains the stronger
resurrection fence; without one, a later valid federation response may recreate
the cache row. Cell-local thumbnail derivatives are fenced and swept before the
pointer row is removed. The control rows and pointer are finalized atomically;
live node copies or either of the chapter ledger's artifact references block the purge.

### Cross-Cluster Artifact Command Routing

When Commodore needs to delete/stop an artifact, it routes to the cluster that
owns it (push model). If the command arrives at the wrong Foghorn (stale cache,
race condition), Foghorn forwards it via ForwardArtifactCommand (safety net).

#### Push Model (Commodore → Foghorn)

1. Foghorn sends `cluster_id` in `ValidateStreamKey` during ingest
2. Commodore records `active_ingest_cluster_id` on the stream
3. On CreateClip, Commodore routes to the ingest cluster (not primary)
4. Clip/DVR/VOD DB records store `origin_cluster_id`
5. On DeleteClip/StopDVR/DeleteDVR/DeleteVodAsset:
   - Query `origin_cluster_id` from business registry
   - Resolve Foghorn address via `GetClusterRouting` peer list
   - Call the correct cluster directly

#### Forward Model (Foghorn → Federation Peer)

If Foghorn receives an artifact command for an artifact not in its local DB:

1. Try local handler (existing logic)
2. If `ErrNoRows` → iterate known federation peers
3. Call `ForwardArtifactCommand(command, hash, tenant_id)` on each peer
4. First peer that returns `handled=true` wins
5. If no peer handles → return NotFound to caller

#### Tenant Operations Fan-Out

`TerminateTenantStreams` and the legacy `InvalidateTenantCache` compatibility
signal fan out to all clusters the tenant has access to, not just the primary
cluster. Signed tenant/object replacements use a separate durable per-cell
delivery ledger that also targets historical recipient cells. An invalidation
cannot delete or replace signed authority; partial compatibility fan-out failure
therefore does not turn still-valid authority into unknown state.

Origin-pull destination identity comes from the authenticated destination
node's locally registered virtual cluster. Foghorn's process `CLUSTER_ID` is not
a substitute because one control cell serves multiple media clusters. The
arrangement rejects an unavailable or mismatching node-cluster binding before
notifying the origin.

### Artifact Migration

```
Tenant moves preferred cluster from B to A:

1. Foghorn A: MigrateArtifactMetadata(tenant_id, source_cluster=B)
2. Foghorn A → Foghorn B: ListTenantArtifacts(tenant_id)
3. Foghorn B: returns all artifact metadata records
4. Foghorn A: INSERT ... ON CONFLICT DO NOTHING with origin_cluster_id = B
5. Playback requests for migrated artifacts use PrepareArtifact to fetch from B's S3
```

## HA Model

In multi-replica Foghorn deployments:

- **Unary RPCs** (QueryStream, NotifyOriginPull, PrepareArtifact): LB round-robin. Any instance handles them via shared Redis state.
- **PeerChannel**: Leader-only. Redis-based leader election (SET NX, 15s TTL, renewed every 5s on telemetry tick). Leader opens and maintains all PeerChannel connections. If leader dies, lease expires, another instance acquires and reconnects.
- **Non-leader replicas**: Read remote edge data from Redis (written by leader's PeerChannel). GetPeerAddr populated from Redis sync (syncPeerAddressesToRedis/loadPeerAddressesFromRedis).

```
Peer B ──PeerChannel──→ [LB] ──→ Leader Instance ──writes──→ Redis
                                                               ↑ reads
                                  Replica Instance ──reads────┘
```

## Federation Telemetry & Geo Enrichment

Federation events are emitted by Foghorn for every cross-cluster operation (peering, replication, artifact access, redirect) and ingested into ClickHouse (`periscope.federation_events`) via the standard analytics pipeline.

### Self-Geo Resolution

Each Foghorn resolves its own geographic coordinates at bootstrap:

1. Foghorn reads `NODE_ID` from env (set by CLI provisioning)
2. Sends `NodeId` in `BootstrapServiceRequest` to Quartermaster
3. Quartermaster JOINs `infrastructure_nodes`, returns full `InfrastructureNode` in response
4. Foghorn reads `ExternalIp` → GeoIP lookup → caches lat/lon/location in `handlers.SetSelfGeo()`

If `NODE_ID` is unset or the node has no `external_ip`, self-geo stays zero (graceful degradation).

### Geo Exchange via PeerHeartbeat

PeerHeartbeat messages (10s interval) carry `foghorn_lat`, `foghorn_lon`, and `foghorn_location`. Each peer caches the remote foghorn's geo in `peerState`. This enables:

- Geo-aware federation topology visualization in the UI
- Per-flow distance calculation for cross-cluster routing analytics
- `GetPeerGeo(clusterID)` for enriching outbound federation events

### Auto-Enrichment

`emitFederationEvent()` in federation handlers automatically sets `local_lat`, `local_lon` from self-geo and `remote_lat`, `remote_lon` from peer geo cache before emitting. All call sites (peering, replication, artifact, redirect events) get geo enrichment without per-site changes.

### ClickHouse Columns

Federation events carry `local_lat`, `local_lon`, `remote_lat`, `remote_lon` (all `Float64`). Periscope Ingest writes these in `processFederationEvent()`.

## Key Files

- `pkg/proto` - Service definition (11 RPCs, 8 PeerMessage payload types)
- `api_balancing/internal/federation` - FederationServer: all RPC handlers (incl. PrepareArtifact/MintStorageURLs/DeleteStorageObjects + canLocallyMintFor ownership gate)
- `api_balancing/internal/control/cross_cluster_artifact.go` - shared resolve→authorize→adopt path (`ResolveAndAdoptRemoteArtifact`, `adoptRemoteArtifactRow`)
- `api_balancing/internal/control/relay_resolve.go` - RelayResolve: byte-serve-time resolution for Helmsman's artifact relay
- `api_balancing/internal/federation` - FederationClient: pool wrapper for outbound RPCs
- `api_balancing/internal/federation` - PeerManager: lifecycle, discovery, telemetry, leader election
- `api_balancing/internal/federation` - RemoteEdgeCache: TTL-bound telemetry and leader/authority leases plus revision-fenced stream-peer membership (non-expiring while active; DB-coordinated cleanup after end); stream identity, playback index, and active replication live in control.StreamRegistry
- `api_balancing/internal/control` - StreamRegistry: unified per-stream identity + per-peer Locations + admission state; consumes StreamAdvertisement ingest via UpsertFederatedSource; records dest-side pulls via MarkReplicating and source-side outbound pulls via RecordOutboundPull
- `api_balancing/cmd/foghorn` - Wiring: FederationServer, FederationClient, PeerManager, RemoteEdgeCache, StreamRegistry (with NewRedisRegistryStore + StartSweeper)

## Gotchas

- **Leader-only PeerChannel**: Only one Foghorn instance per cluster runs persistent PeerChannel connections. Loss of leadership triggers `disconnectAllPeers`; peers reconnect to the new leader via LB. Non-leaders still serve unary RPCs.
- **Demand-driven discovery is the fast path**: Peers are usually discovered from stream validation responses (sub-second), not from 5-min polling. The complete peer hints are committed with the admission obligation; only the federation leader imports them and atomically CAS-replaces the generation's complete membership before broadcasting live state. Conflicting captured endpoints contribute no address authority; current leased Quartermaster discovery must resolve the conflict.
- **StreamAdvertisement eliminates control plane in steady state**: Once PeerChannel is open, peers build a local stream directory (playback_id reverse-index) from StreamAdvertisement messages. Viewer routing can skip Commodore resolve entirely. The directory lives in `control.StreamRegistry` as per-peer `Locations[peer_cluster]` entries; `withdrawFederatedSource` (IsLive=false in the next ad) drops the peer's Location and, if no Locations remain, the whole entry plus its playback_id reverse-index. `SweepStaleLocations` provides a 5-min fallback expiry for peers that stop advertising without a clean withdrawal. Ads also keep the registry's stream identities warm during a Commodore outage (peer-fed entries refresh `cached`, so the stale-serve fallback in `docs/architecture/foghorn-ha.md` rarely engages for federated streams).
- **Ad-fed edges pre-warm cold viewer resolution**: `PeerStreamEdge` carries per-edge scoring data including `ram_used`/`ram_max`; the receiver stores it as `Location.EdgeCandidates`, and both viewer-resolution surfaces — HTTP /play (`internal/handlers`) and the gRPC `ViewerControlService` (`internal/grpc`) — consume them via the shared `control.FederatedRemoteEdges` (20s freshness gate ≈ 4 missed 5s pushes) before paying the cold QueryStream fan-out. The fan-out itself is single-flighted + memoized (5s) per (tenant, stream) via `balancer.SharedFanOut`, and runs **detached from the triggering request's cancellation** (`context.WithoutCancel` + own timeout) — the result is shared and memoized for everyone, so an abandoned first viewer must not poison the window with an empty candidate set. Edges from peers predating the RAM fields are dropped (remote scoring rejects `ram_max==0`) and the request falls through to the fan-out. The warm `EdgeSummary` cache remains the primary source.
- **Tenant filtering in shared-lb**: `QueryStream` filters EdgeCandidates by `tenant_id` so tenants on shared clusters only see their own edges.
- **CapacitySummary is a proto shell**: Received but not stored yet. Reserved for dCDN marketplace capacity trading.
