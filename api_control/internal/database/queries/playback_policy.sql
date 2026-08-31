-- name: GetStreamWebhookSecret :one
SELECT playback_webhook_secret_enc
FROM commodore.streams
WHERE id::text = sqlc.arg(target_id)
  AND tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: GetVODWebhookSecret :one
SELECT playback_webhook_secret_enc
FROM commodore.vod_assets
WHERE (id::text = sqlc.arg(target_id) OR vod_hash = sqlc.arg(target_id))
  AND tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: GetClipWebhookSecret :one
SELECT playback_webhook_secret_enc
FROM commodore.clips
WHERE (id::text = sqlc.arg(target_id) OR clip_hash = sqlc.arg(target_id))
  AND tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: SetStreamPlaybackPolicy :one
WITH updated_stream AS (
    UPDATE commodore.streams AS stream_row
    SET requires_auth = sqlc.arg(requires_auth),
        playback_policy = sqlc.arg(playback_policy)::text::jsonb,
        playback_webhook_secret_enc = sqlc.narg(webhook_secret),
        updated_at = NOW()
    WHERE stream_row.id::text = sqlc.arg(target_id)
      AND stream_row.tenant_id = sqlc.arg(tenant_id)::uuid
    RETURNING stream_row.id, stream_row.tenant_id, stream_row.requires_auth,
              stream_row.playback_policy, stream_row.playback_webhook_secret_enc
), updated_dvrs AS (
    UPDATE commodore.dvr_recordings AS dvr
    SET requires_auth = stream.requires_auth,
        playback_policy = stream.playback_policy,
        playback_webhook_secret_enc = stream.playback_webhook_secret_enc,
        playback_authority_ready = TRUE
    FROM updated_stream AS stream
    WHERE dvr.tenant_id = stream.tenant_id
      AND dvr.stream_id = stream.id
    RETURNING dvr.tenant_id, dvr.dvr_hash, dvr.requires_auth,
              dvr.playback_policy, dvr.playback_webhook_secret_enc
), chapter_authority AS (
	SELECT chapter_asset.id,
	       CASE WHEN parent_dvr.dvr_hash IS NOT NULL THEN parent_dvr.requires_auth
	            WHEN chapter_identity.dvr_hash IS NULL THEN stream.requires_auth
	            ELSE TRUE END AS requires_auth,
	       CASE WHEN parent_dvr.dvr_hash IS NOT NULL THEN parent_dvr.playback_policy
	            WHEN chapter_identity.dvr_hash IS NULL THEN stream.playback_policy
	            ELSE NULL END AS playback_policy,
	       CASE WHEN parent_dvr.dvr_hash IS NOT NULL THEN parent_dvr.playback_webhook_secret_enc
	            WHEN chapter_identity.dvr_hash IS NULL THEN stream.playback_webhook_secret_enc
	            ELSE NULL END AS playback_webhook_secret_enc
	FROM commodore.vod_assets AS chapter_asset
	JOIN updated_stream AS stream ON stream.tenant_id = chapter_asset.tenant_id
	LEFT JOIN commodore.dvr_chapter_playback AS chapter_identity
	  ON chapter_identity.tenant_id = chapter_asset.tenant_id
	 AND chapter_identity.artifact_hash = chapter_asset.vod_hash
	LEFT JOIN commodore.dvr_recordings AS recorded_parent
	  ON recorded_parent.tenant_id = chapter_identity.tenant_id
	 AND recorded_parent.dvr_hash = chapter_identity.dvr_hash
	LEFT JOIN updated_dvrs AS parent_dvr
	  ON parent_dvr.tenant_id = chapter_asset.tenant_id
	 AND parent_dvr.dvr_hash = chapter_identity.dvr_hash
	WHERE chapter_asset.origin_type = 'dvr_chapter'
	  AND (
	      parent_dvr.dvr_hash IS NOT NULL
	      OR (
	          chapter_asset.stream_id = stream.id
	          AND (chapter_identity.dvr_hash IS NULL OR recorded_parent.dvr_hash IS NULL)
	      )
	  )
	  AND NOT EXISTS (
	      SELECT 1 FROM commodore.artifact_catalog_tombstones AS tombstone
	      WHERE tombstone.tenant_id = chapter_asset.tenant_id
	        AND tombstone.kind = 'vod'
	        AND tombstone.artifact_hash = chapter_asset.vod_hash
	  )
), updated_chapters AS (
    UPDATE commodore.vod_assets AS chapter_asset
	SET requires_auth = authority.requires_auth,
		playback_policy = authority.playback_policy,
		playback_webhook_secret_enc = authority.playback_webhook_secret_enc,
		updated_at = NOW()
	FROM chapter_authority AS authority
	WHERE chapter_asset.id = authority.id
	RETURNING chapter_asset.id
)
SELECT id::text FROM updated_stream;

-- name: SetVODPlaybackPolicy :one
UPDATE commodore.vod_assets AS v
SET requires_auth = sqlc.arg(requires_auth),
    playback_policy = sqlc.arg(playback_policy)::text::jsonb,
    playback_webhook_secret_enc = sqlc.narg(webhook_secret),
    updated_at = NOW()
WHERE (v.id::text = sqlc.arg(target_id) OR v.vod_hash = sqlc.arg(target_id))
  AND v.tenant_id = sqlc.arg(tenant_id)::uuid
  AND COALESCE(v.origin_type, '') <> 'dvr_chapter'
  AND NOT EXISTS (
      SELECT 1 FROM commodore.artifact_catalog_tombstones t
      WHERE t.tenant_id = v.tenant_id AND t.kind = 'vod' AND t.artifact_hash = v.vod_hash
  )
RETURNING v.vod_hash;

-- name: IsDVRChapterPlaybackTarget :one
SELECT EXISTS (
    SELECT 1
    FROM commodore.vod_assets v
    WHERE (v.id::text = sqlc.arg(target_id) OR v.vod_hash = sqlc.arg(target_id))
      AND v.tenant_id = sqlc.arg(tenant_id)::uuid
      AND v.origin_type = 'dvr_chapter'
);

-- name: SetClipPlaybackPolicy :one
UPDATE commodore.clips AS c
SET requires_auth = sqlc.arg(requires_auth),
    playback_policy = sqlc.arg(playback_policy)::text::jsonb,
    playback_webhook_secret_enc = sqlc.narg(webhook_secret),
    updated_at = NOW()
WHERE (c.id::text = sqlc.arg(target_id) OR c.clip_hash = sqlc.arg(target_id))
  AND c.tenant_id = sqlc.arg(tenant_id)::uuid
  AND NOT EXISTS (
      SELECT 1 FROM commodore.artifact_catalog_tombstones t
      WHERE t.tenant_id = c.tenant_id AND t.kind = 'clip' AND t.artifact_hash = c.clip_hash
  )
RETURNING c.clip_hash;

-- name: LookupStreamPolicyByPlaybackID :one
SELECT COALESCE(playback_policy::text, ''::text)::text AS playback_policy, playback_webhook_secret_enc,
       tenant_id::text AS tenant_id
FROM commodore.streams
WHERE lower(playback_id::text) = lower(sqlc.arg(playback_id)::text)
  AND deleted_at IS NULL;

-- name: LookupVODPolicyByPlaybackID :one
SELECT COALESCE(playback_policy::text, ''::text)::text AS playback_policy, playback_webhook_secret_enc,
       tenant_id::text AS tenant_id
FROM commodore.vod_assets
WHERE lower(playback_id::text) = lower(sqlc.arg(playback_id)::text);

-- name: LookupClipPolicyByPlaybackID :one
SELECT COALESCE(playback_policy::text, ''::text)::text AS playback_policy, playback_webhook_secret_enc,
       tenant_id::text AS tenant_id
FROM commodore.clips
WHERE lower(playback_id::text) = lower(sqlc.arg(playback_id)::text);

-- name: LookupDVRPolicyByPlaybackID :one
SELECT COALESCE((CASE WHEN d.playback_authority_ready THEN d.playback_policy ELSE parent.playback_policy END)::text, ''::text)::text AS playback_policy,
       COALESCE(CASE WHEN d.playback_authority_ready THEN d.playback_webhook_secret_enc ELSE parent.playback_webhook_secret_enc END, '')::text AS playback_webhook_secret_enc,
       d.tenant_id::text AS tenant_id
FROM commodore.dvr_recordings d
LEFT JOIN commodore.streams parent ON parent.id = d.stream_id AND parent.tenant_id = d.tenant_id
WHERE lower(d.playback_id::text) = lower(sqlc.arg(playback_id)::text)
  AND (d.playback_authority_ready OR parent.id IS NOT NULL);

-- name: LookupStreamPolicyByInternalName :one
SELECT COALESCE(playback_policy::text, ''::text)::text AS playback_policy, playback_webhook_secret_enc,
       tenant_id::text AS tenant_id
FROM commodore.streams
WHERE internal_name = sqlc.arg(internal_name)
  AND deleted_at IS NULL;

-- name: LookupVODPolicyByInternalName :one
SELECT COALESCE(playback_policy::text, ''::text)::text AS playback_policy, playback_webhook_secret_enc,
       tenant_id::text AS tenant_id
FROM commodore.vod_assets
WHERE internal_name = sqlc.arg(internal_name);

-- name: LookupClipPolicyByInternalName :one
SELECT COALESCE(playback_policy::text, ''::text)::text AS playback_policy, playback_webhook_secret_enc,
       tenant_id::text AS tenant_id
FROM commodore.clips
WHERE internal_name = sqlc.arg(internal_name);

-- name: LookupDVRPolicyByInternalName :one
SELECT COALESCE((CASE WHEN d.playback_authority_ready THEN d.playback_policy ELSE parent.playback_policy END)::text, ''::text)::text AS playback_policy,
       COALESCE(CASE WHEN d.playback_authority_ready THEN d.playback_webhook_secret_enc ELSE parent.playback_webhook_secret_enc END, '')::text AS playback_webhook_secret_enc,
       d.tenant_id::text AS tenant_id
FROM commodore.dvr_recordings d
LEFT JOIN commodore.streams parent ON parent.id = d.stream_id AND parent.tenant_id = d.tenant_id
WHERE d.internal_name = sqlc.arg(internal_name)
  AND (d.playback_authority_ready OR parent.id IS NOT NULL);

-- name: GetStreamPolicyScopeName :one
SELECT internal_name
FROM commodore.streams
WHERE id::text = sqlc.arg(target_id)
  AND tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: GetVODPolicyScopeName :one
SELECT v.internal_name
FROM commodore.vod_assets v
WHERE (v.id::text = sqlc.arg(target_id) OR v.vod_hash = sqlc.arg(target_id))
  AND v.tenant_id = sqlc.arg(tenant_id)::uuid
  AND NOT EXISTS (
      SELECT 1 FROM commodore.artifact_catalog_tombstones t
      WHERE t.tenant_id = v.tenant_id AND t.kind = 'vod' AND t.artifact_hash = v.vod_hash
  );

-- name: GetClipPolicyScopeName :one
SELECT c.internal_name
FROM commodore.clips c
WHERE (c.id::text = sqlc.arg(target_id) OR c.clip_hash = sqlc.arg(target_id))
  AND c.tenant_id = sqlc.arg(tenant_id)::uuid
  AND NOT EXISTS (
      SELECT 1 FROM commodore.artifact_catalog_tombstones t
      WHERE t.tenant_id = c.tenant_id AND t.kind = 'clip' AND t.artifact_hash = c.clip_hash
  );

-- name: GetStreamPolicyForBundle :one
SELECT COALESCE(playback_policy::text, ''::text)::text AS playback_policy,
       internal_name, tenant_id::text AS tenant_id
FROM commodore.streams
WHERE id = sqlc.arg(stream_id)::uuid
  AND deleted_at IS NULL;

-- name: NextPolicyBundleVersion :one
SELECT (COALESCE(MAX(bundle_version), 0::bigint) + 1::bigint)::bigint AS bundle_version
FROM commodore.policy_bundle_versions
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND stream_id = sqlc.arg(stream_id)::uuid;

-- name: LockPolicyBundleStream :exec
SELECT pg_advisory_xact_lock(
    hashtext(sqlc.arg(tenant_id)::text),
    hashtext(sqlc.arg(stream_id)::text)
);

-- name: InsertPolicyBundle :exec
INSERT INTO commodore.policy_bundle_versions
    (tenant_id, stream_id, bundle_version, bundle_jwt, issued_at, expires_at)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(stream_id)::uuid,
        sqlc.arg(bundle_version), sqlc.arg(bundle_jwt),
        sqlc.arg(issued_at), sqlc.arg(expires_at));
