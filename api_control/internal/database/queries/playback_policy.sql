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
UPDATE commodore.streams
SET requires_auth = sqlc.arg(requires_auth),
    playback_policy = sqlc.arg(playback_policy)::text::jsonb,
    playback_webhook_secret_enc = sqlc.narg(webhook_secret),
    updated_at = NOW()
WHERE id::text = sqlc.arg(target_id)
  AND tenant_id = sqlc.arg(tenant_id)::uuid
RETURNING id::text;

-- name: SetVODPlaybackPolicy :one
UPDATE commodore.vod_assets AS v
SET requires_auth = sqlc.arg(requires_auth),
    playback_policy = sqlc.arg(playback_policy)::text::jsonb,
    playback_webhook_secret_enc = sqlc.narg(webhook_secret),
    updated_at = NOW()
WHERE (v.id::text = sqlc.arg(target_id) OR v.vod_hash = sqlc.arg(target_id))
  AND v.tenant_id = sqlc.arg(tenant_id)::uuid
  AND NOT EXISTS (
      SELECT 1 FROM commodore.artifact_catalog_tombstones t
      WHERE t.tenant_id = v.tenant_id AND t.kind = 'vod' AND t.artifact_hash = v.vod_hash
  )
RETURNING v.vod_hash;

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
SELECT COALESCE(s.playback_policy::text, ''::text)::text AS playback_policy, s.playback_webhook_secret_enc,
       s.tenant_id::text AS tenant_id
FROM commodore.dvr_recordings d
JOIN commodore.streams s ON s.id = d.stream_id
WHERE lower(d.playback_id::text) = lower(sqlc.arg(playback_id)::text);

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
SELECT COALESCE(s.playback_policy::text, ''::text)::text AS playback_policy, s.playback_webhook_secret_enc,
       s.tenant_id::text AS tenant_id
FROM commodore.dvr_recordings d
JOIN commodore.streams s ON s.id = d.stream_id
WHERE d.internal_name = sqlc.arg(internal_name);

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
