-- Backfill active_object_key for rows synced before version-addressing existed (expand/019 adds the column
-- NULL). active_object_key is the authoritative read/delete pointer; a NULL legacy value forces readers onto
-- the s3_url fallback and, critically, makes a republication unable to enqueue the superseded object (the
-- completion reads previousActiveKey='' and skips cleanup) — so a re-uploaded VOD would leak its old object.
--
-- vod_metadata.s3_key is the EXACT raw object key (the same value GeneratePresignedGET/Delete consume, no
-- bucket/prefix), so it backfills active_object_key precisely. This covers uploaded + processed VOD, which is
-- the only artifact type with a republication path (clips are immutable once frozen; DVR has no single
-- object). Rows without a vod_metadata row (e.g. legacy clips) keep active_object_key NULL and are handled at
-- their next write by the completion's prefix-aware s3_url recovery in control.processSyncComplete.
--
-- active_dtsh_key is deliberately NOT backfilled: a NULL value already routes readers to the correct legacy
-- co-located <active_object_key>.dtsh derivation (relay_resolve.generateDtshPresignedGET).
--
-- Runs in POSTDEPLOY (a bulk UPDATE is not expand-phase safe); idempotent (guarded on active_object_key IS
-- NULL), so re-running is a no-op.
UPDATE foghorn.artifacts a
   SET active_object_key = vm.s3_key
  FROM foghorn.vod_metadata vm
 WHERE vm.artifact_hash = a.artifact_hash
   AND a.sync_status = 'synced'
   AND a.active_object_key IS NULL
   AND vm.s3_key IS NOT NULL
   AND vm.s3_key <> '';
