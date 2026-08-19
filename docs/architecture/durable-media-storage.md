# Durable Media Storage

How FrameWorks stores durable media (clips, DVR, VOD, freeze artifacts, and their thumbnails) so that placement,
billing, and deletion are governed by **recorded evidence, never current routing**. Side effects converge durably
within the bounds documented below: primary media and durable child artifacts converge fully, while thumbnail
derivatives carry one explicitly accepted exception (the provider-tail residual under [Deletion saga](#deletion-saga)),
and the [Testing posture](#testing-posture) states which surfaces are real-Postgres-covered versus deterministic-fake
or unit-only. The thumbnail _serving_ path (the deterministic `/assets/{id}/{file}` key, Chandler's dumb cache) is a
distinct concern documented in [thumbnails.md](thumbnails.md); this page is the storage/placement/cleanup foundation
underneath it.

Storage backend: **one immutable S3 backend per cell**. Backend repointing is not a supported operation, and
multi-provider, non-S3, and RAID-local backends are out of scope. Placement policy for WHERE durable media lives —
official rated-storage tiers, storage-less/self-hosted private clusters, and the federated thumbnail mint that serves
them — is designed in [placement-policy-engine.md](../rfcs/placement-policy-engine.md), which also records the seams
(serving/storage-cell decoupling) already present in the schema that keep that direction open.

## Invariants

These are the durable-storage contract. Each is enforced in code; the coordination tier is backed by real-Postgres
tests (with deterministic object-store/Foghorn fakes) that exercise the failure interleaving, not just the happy path,
run by `make verify-schema`. The exact coverage boundary — which surfaces are real-Postgres versus deterministic-fake
or unit-only, and the external object-store end-to-end tier that sits outside it — is stated in
[Testing posture](#testing-posture).

- **I1 — Durable writes require a positively-resolved official cluster.** A durable-write placement/mint fails closed
  (`StorageUnavailable`) when the tenant's official cluster is unresolved — no fallback to the caller's cluster,
  `LocalClusterID`, or a dev default. VOD, DVR, freeze, and thumbnail durable mints route through a strict
  official-only resolver; the generic read resolver (with its local candidate) is retained only for read paths.

- **I2 — Backend ownership is recorded at write time, never reconstructed from current routing.** The physical
  backend identity — a deterministic `backend_id` fingerprint over `(kind, bucket, endpoint, region, prefix)`, plus the
  `durable_backend_local` fact — is captured on the artifact / thumbnail assignment / cleanup + ledger rows WHEN storage
  is assigned (a computed fingerprint, no registry table). Billing attribution and cleanup routing read ONLY that
  recorded evidence, so a later routing or advertised-backing change never moves a historical row's attribution or
  misroutes its deletion. **One Foghorn database belongs to one cell and one immutable S3 backend** (enforced at boot by
  `cell_storage_identity` / `EnforceImmutableLocalBackend`; production provisions a per-cell physical database —
  `foghorn_eu` / `foghorn_us` — not a shared one). Cleanup therefore sweeps the local store and compares the recorded
  `backend_id` against the cell's current store, failing closed on a mismatch (a forbidden repoint) rather than deleting
  from the wrong backend. Cross-cell / multi-backend placement is a future storage-placement RFC that routes a cleanup
  obligation to the service owning the selected backend — never assuming workers for different cells share a database.

- **I3 — Deletion and thumbnail cleanup are durably convergent, routed to every owning cell.** The cleanup obligation
  is recorded in the SAME transaction that deletes the stream/artifact (a transactional outbox); a fenced drainer
  delivers and deletes bytes idempotently, clearing the obligation only after confirmed deletion. No one-shot
  best-effort RPC — a failure is retried, not logged-and-lost, so a crash after the delete commit still converges to
  byte deletion. Because a live stream's thumbnails are minted on its INGEST cell (its own per-cell Foghorn database),
  ownership is recorded DURABLY BEFORE the bytes exist: Foghorn calls `RegisterStreamThumbnailServingCell` (fenced by
  `deleted_at IS NULL`) and mints the upload URL only on `registered=true`, so registration serializes with
  `DeleteStream` on the stream row — register-wins → the cell is in the deletion's fan-out; delete-wins → the mint is
  refused (no orphan bytes). The deletion outbox reads the recorded set (`streams.thumbnail_serving_cluster_ids`) and
  fans the cleanup RPC out to EVERY owning cell, aggregating failures so a down cell never starves a healthy sibling,
  and finalizing only after all ack. Owning cells are resolved via Quartermaster SERVICE DISCOVERY (not the tenant
  route), so a cell stays reachable even after the tenant's entitlement changes. An empty set means "no registered
  owner" (nothing to clean) — there is NO tenant-primary fallback, which would sweep the wrong cell and falsely ack.
  Child artifacts (clip/dvr) route to their `origin_cluster_id`, resolved the same tenant-independent way. (Streams
  predating register-before-mint have no reliable serving-cell history — `active_ingest_cluster_id` names only the last
  cell — so ownership is NOT inferred; a one-time clean-cut wipe of the regenerable thumbnail namespace at redeploy
  makes an empty owner set trustworthy. That cutover is a deployment-specific release action, not general behavior.)

- **I4 — A deleted parent cannot claim, publish, or resolve, and stops serving within the documented window.**
  Terminal parents (`deleted/failed/expired/aborted`, one canonical predicate) are fenced at claim, publish, and
  projection. Deletion tombstones the asset (the API stops returning its URL immediately), and the tombstone stops any
  new projection. Because Chandler serves the deterministic key with no control-row lookup, serving-after-delete is
  governed by the cache TTL plus the projection convergence window — with the explicitly accepted, rare provider-tail
  residual risk described in [thumbnails.md](thumbnails.md). See also the delivery-latency caveat under
  [Deletion saga](#deletion-saga).

- **I5 — Serving carries no runtime cross-version dependency.** Chandler resolves nothing at request time, so it has no
  runtime dependency on Foghorn's version — no capability handshake, no request-time coupling. Installation carries one
  deploy-order edge: Chandler's `/ready` reads a sentinel the in-cell Foghorn establishes at boot, so the planner
  deploys Chandler after that Foghorn (a Foghorn is also rejected if its cluster has no Chandler — the cell-topology
  check). Mixed control-protocol versions between Foghorn and third-party sidecars are handled by the capability
  contract below, not by a rollout barrier.

- **I6 — Every recovery item has durable ownership, bounded execution, and fair progress.** Recovery claims are leased
  with a fencing token (keyset/ordered), each item runs under its own deadline, poison rows are backed off rather than
  re-selected at the head, and backlog is observable. The batch is capped so the lease provably covers it
  (`batch ≤ (leaseTTL − margin) / (itemTimeout + settleTimeout)`), so two workers never double-drive and a stuck item
  never starves valid ones.

## Backend identity & descriptor authority

Quartermaster is the SOLE authority for the immutable descriptor tuple `(bucket, endpoint, region, prefix)`. Chandler
reads the full tuple from Quartermaster verbatim (no env-serving fallback). Foghorn commits its descriptor to
`foghorn.cell_storage_identity` on first boot and **refuses to start** if the exact bucket/endpoint/region/prefix later
changes (`EnforceImmutableLocalBackend`; credentials may still rotate). So "one immutable S3 backend per cell" is a
code-enforced invariant, and cleanup routes by an object's recorded `backend_id`, never current placement.

`s3_prefix` is part of the immutable tuple. The prefix is nullable — surfaced through the API as `s3_prefix_present` —
so an incomplete descriptor (NULL prefix) is distinguishable from a known-empty one; Foghorn's first-boot guard fails
closed on the incomplete shape. Desired-state bootstrap establishes the full descriptor before a cell's first boot.

**Billing readiness.** Usage billing over backend attribution stays OFF until ambiguous rows are resolved: inventory
`durable_backend_local = false`/unknown rows, backfill only from defensible evidence (persisted object URLs/backend
ids, provider inventory/HEAD, or an operator-approved mapping with a proven time boundary), and gate usage billing
while ambiguous locally-stored rows remain. Ambiguous rows are unattributed data pending resolution, not lost revenue.

## Deletion saga

Parent-stream deletion is a durable two-phase saga so a caller is never told "deleted" before the serving authority
(Foghorn) holds the tombstone, without holding a DB transaction across the network:

1. `DeleteStream` (Commodore), in one local tx: mark the stream `deleting` (soft-delete, excluded from list/serve) and
   enqueue the cleanup obligation. Commit. No hard-delete yet.
2. Best-effort synchronous delivery to Foghorn's `DeleteStreamThumbnails` (writes the durable tombstone for the
   asset_key — live streams have no artifact row, so the tombstone is what fences them). On a positive ack, finalize:
   hard-delete the stream rows, complete the outbox, return `deleted`. On failure/unreachable, return
   `deletion_pending`.
3. The outbox worker re-delivers and runs the same finalize step, so a delivery outage converges once Foghorn recovers.

**Honest bound.** What is durably convergent (I3) is the DELIVERY of the cleanup obligation: the outbox re-delivers
the tombstone until Foghorn acks, so a prolonged Commodore→Foghorn delivery outage only extends the window until
delivery converges — it is governed by _delivery latency_, not a fixed cache-TTL bound. Physical byte removal is more
qualified. Deleting a durable child artifact's bytes (clip/DVR/VOD) is tombstoned and reconciled, so it converges. But
physical removal of a thumbnail DERIVATIVE is conditional on the accepted provider-tail assumption: a straggler copy
that lands AFTER the ~20-minute deterministic-copy window (upload TTL + provider tail) is not re-swept and can
re-expose the object — the explicitly accepted, rare residual risk documented under [thumbnails](thumbnails.md), never
a loss of primary media. Making the serving authority learn of the deletion synchronously at commit — and closing the
provider-tail gap — are availability-vs-correctness changes tracked outside this document.

## Control-protocol contract

The control plane enforces a single minimum control-protocol version; there is no mixed-version inventory path.

- **Registration minimum.** A sidecar declaring a `control_protocol_version` below `MinControlProtocolVersion` is
  REJECTED at registration with `FailedPrecondition` — it never becomes a node owner. This is the single point that
  makes capability **session-owned**: past registration, every session is staged-capable and inventory-authoritative,
  so no handler ever trusts a payload field to decide authority.
- **Capability seam.** `control.ControlFeatures`, derived once from the negotiated `control_protocol_version`, is where
  protocol behavior lives (no scattered version comparisons). With the minimum enforced, every capability is always
  true for a connected session; the struct is the extension point for a future supported protocol range (real N−1
  support), whose design is tracked outside this document.
- **Inventory.** A connected session is always inventory-authoritative. A versioned whole-node report (monotonic
  `report_seq`) is applied as an authoritative snapshot (an empty slice clears); an incomplete-scan report cordons the
  node; a `report_seq <= 0` report from an authoritative session is a protocol violation and is dropped (never
  applied under weaker semantics).

## Recovery

The thumbnail recovery reconciler (I6) is a load-safe worker over the assignment rows: leased/keyset claims with a
fencing token, per-item deadlines (not one shared context), poison-row backoff, fair ordering across the
publish/fail/GC phases, and backlog/lease metrics. The token fences settlement; `Complete` is a guarded idempotent CAS,
so even a (now-prevented) lease overrun could at worst duplicate work, never corrupt.

## Testing posture

The coordination tier is wired over real Postgres with deterministic object-store/Foghorn fakes, run by
`make verify-schema`: the full lifecycle (publish → deletion saga → drainer), delivery-outage convergence,
committed-child compensation, token-fence, the single-obligation stream-cleanup drainer (durable convergence, delayed
second sweep, atomic finalize under an injected control-cleanup failure), backend-repoint fail-closed, and the
per-cell purge ownership filter. The coverage boundary is deliberate: deterministic fakes exercise our coordination,
not the object store's implementation, so an end-to-end tier over a real external object store (MinIO/testcontainers)
and the Chandler asset-serving HTTP surface (`/assets/{id}/{file}`) sit outside this suite (the Chandler side has its
own `assets_test`).
