-- Durable stream tombstone + thumbnail-cleanup obligation. A LIVE stream's thumbnails are keyed by
-- asset_key = stream_id, which has NO foghorn.artifacts row — so the purge job never reaches it, version GC
-- never reclaims its final version, and the terminal-status fences (which read foghorn.artifacts) never fire.
-- On stream deletion Foghorn records ONE row here inside a guarded transaction; its EXISTENCE is the durable
-- tombstone that fences claim/publish and stops any new projection for the (rowless) live stream, and its drain
-- state (status + lease/backoff) is the fenced queue a background worker (StreamCleanupJob) works until the S3
-- bytes are confirmed gone and the control rows dropped, then marks it 'cleaned' (the row persists as the tombstone).
--
-- One Foghorn database belongs to ONE cell and ONE immutable S3 backend (cell_storage_identity), so an obligation
-- has a SINGLE sweep target: this cell's local store. backend_id snapshots the recorded fingerprint of that store; the
-- sweep fails closed if it no longer matches the cell's current store (a forbidden repoint). Cross-cell / multi-backend
-- placement is out of scope: obligations never target another cell's store.
-- asset_key/stream_id are globally-unique and never reused, so the tombstone may persist indefinitely without
-- colliding a future asset. Schema source of truth: pkg/database/sql/schema/foghorn.sql.
CREATE TABLE IF NOT EXISTS foghorn.stream_cleanup_obligation (
    asset_key            TEXT PRIMARY KEY,                         -- deleted asset's globally-unique key (live stream_id). Existence = tombstone.
    tenant_id            TEXT NOT NULL,                            -- ownership attribution: the tenant the deletion was authorized for
    backend_id           TEXT,                                     -- recorded fingerprint of the cell's immutable store the thumbnails live on; the sweep deletes ONLY on an exact match and fails closed otherwise (empty/legacy or mismatch → retained, never a guessed-store delete). Recorded at record time; legacy rows adopted once at boot
    status               TEXT NOT NULL DEFAULT 'pending'
                         CHECK (status IN ('pending','cleaned')),  -- pending = bytes not yet swept; cleaned = bytes gone + control rows dropped. Row PERSISTS as the tombstone either way.
    enqueued_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),       -- window anchor: the delayed second sweep fires at enqueued_at + DeterministicCopyWindow
    first_swept_at       TIMESTAMPTZ,                              -- set on the first sweep; arms the delayed resurrection second sweep
    next_attempt_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),       -- due time for the drainer; bumped with backoff on a failed sweep, and to enqueued_at + window after the first sweep
    leased_until         TIMESTAMPTZ,                              -- in-flight lease; a worker claims a pending, due, unleased row so HA replicas do not double-sweep
    lease_token          TEXT,                                     -- fences the lease: settlement matches asset_key AND this token, so a worker whose lease expired cannot settle another worker's row
    attempts             INTEGER NOT NULL DEFAULT 0,               -- capped-linear backoff counter on failing sweeps
    last_error           TEXT,
    cleaned_at           TIMESTAMPTZ                               -- set when the sweep confirmed the bytes gone + control rows dropped
);
-- Drainer scan: only pending rows are due for a sweep (cleaned rows persist purely as the tombstone).
CREATE INDEX IF NOT EXISTS idx_foghorn_stream_cleanup_due ON foghorn.stream_cleanup_obligation(next_attempt_at) WHERE status = 'pending';
