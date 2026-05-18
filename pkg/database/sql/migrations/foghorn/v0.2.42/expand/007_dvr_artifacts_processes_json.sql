-- Snapshot the tenant's live thumbnail processes_json onto the DVR artifact at
-- recording start so the dvr+<internal_name> STREAM_PROCESS trigger
-- can serve MistProc config without depending on the in-memory cache
-- populated by PUSH_REWRITE. The cache has a short TTL and disappears
-- on Foghorn restart; the snapshot is durable for the artifact's
-- lifetime. Read by Foghorn's handleStreamProcess for the dvr+ token
-- (rolling DVR playback surface).

ALTER TABLE foghorn.artifacts
    ADD COLUMN IF NOT EXISTS dvr_processes_json TEXT;
