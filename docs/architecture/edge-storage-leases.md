# Edge Storage Leases - Helmsman Disk Protection

In-process lease subsystem (`api_sidecar/internal/leases`) that protects locally cached artifact files from deletion while MistServer is using them, tracks playback heat for LRU eviction, and gates every destructive storage path on the edge. Edges run hot by design — cleanup frees just enough for the next write, and a file is deletable only when no lease of either kind pins it.

## Architecture

```
Mist triggers                       leases package (process-global singletons)
─────────────                       ────────────────────────────────────────────
STREAM_SOURCE ──► AcquireSource ──► Tracker ── sources: streamName → SourceLease
STREAM_END   ──► ReleaseSource      │          viewers: sessionID  → ViewerLease
USER_NEW     ──► AcquireViewer ─────┤          reverse indexes: path/asset → leases
USER_END     ──► ReleaseViewer      │
                                    ├─► HeatTracker   (per-path AccessCount/LastAccessed)
Mist API polls                      ├─► SourceRegistry (streamName → local path handed to Mist)
──────────────                      └─► DeferredStore  (persisted operator delete intents)
active_streams ─► ReconcileSources
clients        ─► ReconcileViewers        consumers
        │                                 ─────────
        └─► boot-pause state machine ──►  cleanup monitor (LRU eviction)
            (StateBootPaused → Normal)    storage admission (two-tier disk gate)
                                          operator deletes (clip/vod/dvr)
```

## Lease Kinds

| Kind        | Lifetime                                         | Purpose                                                                                                                                                                                                 |
| ----------- | ------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| SourceLease | STREAM_SOURCE (local path returned) → STREAM_END | **Primary disk protection.** Mist re-reads the file across viewers, seeks, and reconnects; the file must stay until the stream itself is gone.                                                          |
| ViewerLease | USER_NEW → USER_END                              | **Heat / accounting only.** Viewer churn must not toggle disk protection — one stream serves many viewers without firing new STREAM_SOURCE events. First acquire per session bumps `HeatTracker.Touch`. |

A file is deletable only when **both** lease types are absent for it. `AssetKey{Type, Hash}` identifies the logical asset; for VOD the hash is the Mist `internal_name` (the `vod+` suffix), **not** the artifact hash — Foghorn resolves `internal_name → artifact_hash` separately. For DVR it is the `dvr_hash`.

## Package Layout

| File                 | Contents                                                                                                                                                               |
| -------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `leases.go`          | `Tracker`: source/viewer lease maps, path/asset reverse indexes, TOCTOU-safe delete helpers, 2-strikes reconciliation, degraded-lease counters                         |
| `global.go`          | Process-global singletons (`Install`/`GlobalTracker`/…) + the boot-pause cleanup state machine (`StateBootPaused` → `StateNormal` via `MarkMistReconcileDone`)         |
| `heat.go`            | `HeatTracker`: per-path `AccessCount`/`LastAccessed`, kept outside the scan-replaced artifact index (which would erase heat on every rebuild)                          |
| `source_registry.go` | `SourceRegistry`: streamName → the local path Mist was actually handed (the authoritative "what is open on disk" anchor) + `IsLocalFilesystemResponse` response filter |
| `streamname.go`      | `vod+`/`dvr+`/`live+`/`processing+`/`pull+` prefix constants and parsers (`ParseVODInternalName`, `ParseDVRRollingPlaybackID`)                                         |
| `dvr_paths.go`       | `DeriveDvrHashFromRollingManifestPath`: dvr_hash from the rolling manifest path `.../dvr/<stream>/<dvr_hash>/<dvr_hash>.m3u8`                                          |
| `paths.go`           | `DeterministicPathsForAsset`: candidate on-disk paths to pin per asset kind (media file, `.partial` fill tmpfile, `.dtsh`/`.gop` sidecars, `.blocks` cache dir)        |
| `relay_url.go`       | Relay-URL recognition (`/internal/artifact/...` responses look like external HTTP but are local-cache-backed and must lease) + AssetKey/ext/stream extraction          |
| `deferred_delete.go` | `DeferredStore`: JSON-persisted operator delete intents (`.pending-deletes.json` under the storage root), drained as leases release                                    |

Wiring lives in `api_sidecar/internal/handlers/leases_init.go` (`InitLeases`, called after `InitStorageManager`).

## Data Flows

### Source Lease Acquisition (STREAM_SOURCE)

Two response shapes produce a lease (`acquireSourceLeaseForStream`, `api_sidecar/internal/handlers/handlers.go`):

1. **Local filesystem path** (warm artifact): AssetKey derives from the stream-name prefix; leased paths are the response plus existing `.dtsh`/`.gop` sidecars. Rolling DVR (`dvr+`) pins the manifest path with the dvr_hash derived from the path layout.
2. **Helmsman relay URL** (`http(s)://host/internal/artifact/...`, read-through): AssetKey derives from the URL; leased paths come from `DeterministicPathsForAsset` so the canonical media file, the in-flight `.partial` tmpfile, sidecars, and the `.blocks` block-cache dir are protected **before they exist on disk** — cleanup cannot race the background fill.

External indirections (`balance:`, presigned S3, foreign HTTP) produce no lease — those bytes live elsewhere.

Every acquisition also records a `SourceRegistry` entry so USER_NEW can map sessions back to a local path for heat accounting. For clips the recorded heat path is the nested `clips/<stream>/<hash>.<ext>` the writer produces, not the flat fallback.

### Reconciliation (2-strikes)

`ReconcileSources`/`ReconcileViewers` run against Mist's `active_streams` and `clients` API polls (`poller.go`). A lease missing from **two consecutive successful polls** is released; callers must not reconcile after a failed poll (a Mist API blip must not strip protection). A lease present in the poll resets its strike count and refreshes `LastSeen`.

### Boot Pause and Lease Rebuild

Destructive cleanup starts **paused** (`StateBootPaused`) and unpauses only after both poll classes (`active_streams` and `clients`) complete one successful pass (`MarkMistReconcileDone`). This covers the Helmsman-restart case: Mist sessions survive a sidecar restart, but the in-process lease tracker starts empty — deleting before the first reconciliation could remove files Mist is actively reading.

`rebuildSourceLeasesFromMist` (poller reconciliation) backfills SourceLeases for active Mist streams that lack one:

- VOD resolving via the artifact index → normal lease on the resolved path.
- VOD **not** resolving (no `internal_name → path` mapping, e.g. boot without a Commodore roundtrip) → **degraded** asset-only lease. While any degraded VOD lease exists, `DegradedVodCleanupActive` blocks operator VOD deletes and LRU VOD candidate collection **globally** — path-keyed checks cannot pin an unknown path, so the only safe option is refusing all VOD deletes until reconciliation clears the ambiguity.
- DVR unresolved → degraded DVR lease; `DegradedDvrCleanupActive` pauses DVR destructive cleanup (`DeleteDVRDirIfUnleased` refuses while the count is >0, so an unresolved chapter cannot lose its backing tree).

### TOCTOU-Safe Deletion

`DeletePathIfUnleased` / `DeleteDVRDirIfUnleased` run the lease check and the `os.Remove`/`os.RemoveAll` under the tracker mutex, so a STREAM_SOURCE arriving between check and remove blocks until the delete returns — it then either leases a still-present file or a gone path (Mist's resolution then lands elsewhere). Trade-off: the mutex is held during the unlink; DVR directory removal can take longer on big trees.

### "Never Delete the Last Copy" (eviction safety)

Lease checks protect _open_ files; the durable-copy check protects _cold_ ones. Before the LRU cleanup monitor evicts any artifact (`cleanupClip`, `api_sidecar/internal/handlers/cleanup.go`), Helmsman asks Foghorn `RequestCanDelete(artifact_hash)` over the control stream. Foghorn (`processCanDeleteRequest`, `api_balancing/internal/control/server.go`) answers safe-to-delete only when:

- the row is `sync_status='synced'` **and** the S3 object is actually verified present (`verifyDurableArtifactCopy` does a live `ListPrefix` against the parsed `s3_url` — a row _marked_ synced with a missing object refuses with `s3_object_missing`), or
- the artifact's authoritative cluster (`COALESCE(storage_cluster_id, origin_cluster_id)`) is a **remote** cluster not served by this Foghorn — the remote cluster's S3 holds the canonical copy, and the local file is by definition a cache. The remote shortcut is only honored when exactly one `foghorn.artifacts` row matches the hash (no tenant_id on the request; multiple rows could bleed a remote disposition across tenants).

Anything else (`sync_pending`, `sync_failed`, `not_synced`, `not_found`, S3 unverifiable, Foghorn disconnected) refuses the eviction; Helmsman skips the candidate and triggers a storage sync instead. Local eviction therefore never removes the sole copy of an artifact — the delete happens only after a durable copy is proven elsewhere.

Successful evictions report back (`StorageLifecycle` EVICTED with warm-duration, plus `ArtifactDeleted`) so Foghorn's placement state stays truthful. Block-cache (`.blocks`) eviction skips the artifact-index purge and `ArtifactDeleted` — it is a derived relay-cache action and the canonical file may still be warm.

### Interaction with Storage Admission

`IsDestructiveCleanupAllowed` (boot pause + tracker presence) gates the two-tier disk-write admission engine (`storage_admission.go`):

- **Tier 2 (blocking)** `ensureRoomForDiskWrite` fails fast with a typed `storage.ErrInsufficientSpace` during boot pause instead of evicting — Foghorn retries the write on another node.
- **Tier 1 (proactive)** background cleanup is a no-op while paused.
- **Playback-cache admission** degrades to `CacheMemoryOnly` rather than evicting when cleanup is paused or fails.

Within cleanup itself, every candidate delete goes through the lease-gated helpers; `ErrLeaseHeld` is a skip-and-retry, not a failure.

### Heat and LRU Ordering

`HeatTracker.Touch` fires on viewer first-acquire (per session, not per idempotent re-acquire) against the source-registry path, and on the relay's warm-block reads with the `.blocks` dir as key. The cleanup monitor's priority formula (`calculateCleanupPriority`) combines age, size, access count, and recency — recently accessed (<24h) files get a 10× retention bonus. Heat lives outside the artifact index because that index is rebuilt by full replace on every scan, which would erase counters written there. `ReapNotOnDisk` GCs entries whose paths vanished.

### Deferred Operator Deletes

An operator/control-plane delete for a leased asset does not fail: the intent persists in `DeferredStore` (`.pending-deletes.json`, written before the enqueue returns — a failed persist is surfaced to the caller because the in-memory entry alone would vanish on restart). A 30s drain loop retries; `ErrLeaseHeld` keeps the entry queued, success forgets it and only then emits `ArtifactDeleted` to Foghorn — control-plane state advances only after bytes are really gone. The drain honors the boot pause.

### DVR Segment Refcounts

DVR SourceLeases with segment lists fan out to a `SegmentIndex` (`control.LocalSegmentIndex`) via `AcquireView`/`ReleaseView` per segment. `control.DropLeaseChecker` (wired in `InitLeases`) lets `DropUnsyncedSegment` refuse disk-pressure drops for any (dvr_hash, segment) an asset-level lease pins, without importing the leases package.

## Key Files

- `api_sidecar/internal/leases` - the package (see layout table above)
- `api_sidecar/internal/handlers/leases_init.go` - singleton wiring, deferred-delete deleter/drain, boot-pause poll tracking
- `api_sidecar/internal/handlers/handlers.go` - trigger integration (`acquireSourceLeaseForStream`, `acquireViewerLeaseForSession`, STREAM_END/USER_END release)
- `api_sidecar/internal/handlers/poller.go` - Mist API reconciliation + `rebuildSourceLeasesFromMist`
- `api_sidecar/internal/handlers/cleanup.go` - LRU cleanup monitor (thresholds, priority, CanDelete gate, lease-gated deletes)
- `api_sidecar/internal/handlers/storage_admission.go` - two-tier disk-write admission consuming `IsDestructiveCleanupAllowed`
- `api_balancing/internal/control/server.go` - `processCanDeleteRequest` / `verifyDurableArtifactCopy` (Foghorn side of the durable-copy check)

## Gotchas

- **VOD AssetKey.Hash is the internal_name, not the artifact hash.** They are different values in Foghorn; conflating them breaks the degraded-lease protection this distinction exists for.
- **Relay URLs must lease.** A STREAM_SOURCE response pointing at Helmsman's own `/internal/artifact/*` relay looks like an external HTTP redirect to a naive filter but is local-cache-backed; `IsRelayArtifactResponse` catches it before the scheme filter rejects it.
- **Candidate paths are speculative on purpose.** `DeterministicPathsForAsset` lists paths that may never materialize (`.partial`, both DVR layout roots); cleanup only checks `IsPathLeased`, so an unused candidate is harmless, and a warm-only artifact stays protected without the caller knowing the layout.
- **Reconciliation only after successful polls.** Calling `ReconcileSources`/`ReconcileViewers` after a failed Mist API call would release every lease in two ticks and expose in-use files to cleanup.
- **Sidecars are not independently leased.** `.dtsh`/`.gop`/`.blocks` are removed only after the primary file's lease-gated delete succeeds — a cache with no canonical file is meaningless.
- **`ViewerLease` without a path still installs.** When the source registry has no entry (non-local source, pre-lease-wiring session), the lease exists so reconciliation can release it, but it cannot bump heat.
- **Uninstalled tracker = leases opt-out.** `IsDestructiveCleanupAllowed` returns true and delete helpers fall through to plain `os.Remove` when `InitLeases` never ran (unit tests); production must call it exactly once.
