-- An artifact's origin cluster produced it and owns its durable storage; media
-- authority is never compiled with a tenant-level cluster in its place. Rows
-- missing the origin take it from their creation intent, which records the
-- cluster that accepted the create. An empty string becomes NULL so the origin
-- Foghorn's catalog projection can still record it (the projection only fills
-- a NULL origin). Each change re-enqueues the artifact's authority compile.
UPDATE commodore.clips AS c
SET origin_cluster_id = i.origin_cluster_id, updated_at = NOW()
FROM commodore.artifact_creation_intents AS i
WHERE COALESCE(c.origin_cluster_id, '') = ''
  AND i.tenant_id = c.tenant_id AND i.kind = 'clip' AND i.artifact_hash = c.clip_hash
  AND i.status <> 'aborted';

UPDATE commodore.dvr_recordings AS d
SET origin_cluster_id = i.origin_cluster_id, updated_at = NOW()
FROM commodore.artifact_creation_intents AS i
WHERE COALESCE(d.origin_cluster_id, '') = ''
  AND i.tenant_id = d.tenant_id AND i.kind = 'dvr' AND i.artifact_hash = d.dvr_hash
  AND i.status <> 'aborted';

UPDATE commodore.vod_assets AS v
SET origin_cluster_id = i.origin_cluster_id, updated_at = NOW()
FROM commodore.artifact_creation_intents AS i
WHERE COALESCE(v.origin_cluster_id, '') = ''
  AND i.tenant_id = v.tenant_id AND i.kind = 'vod' AND i.artifact_hash = v.vod_hash
  AND i.status <> 'aborted';

UPDATE commodore.clips SET origin_cluster_id = NULL, updated_at = NOW() WHERE origin_cluster_id = '';
UPDATE commodore.dvr_recordings SET origin_cluster_id = NULL, updated_at = NOW() WHERE origin_cluster_id = '';
UPDATE commodore.vod_assets SET origin_cluster_id = NULL, updated_at = NOW() WHERE origin_cluster_id = '';
