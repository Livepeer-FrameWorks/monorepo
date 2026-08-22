-- name: ResolveClipByPlaybackID :one
SELECT clip_hash, internal_name, tenant_id, user_id, stream_id::text, origin_cluster_id, requires_auth
FROM commodore.clips
WHERE lower(playback_id::text) = lower($1::text);

-- name: ResolveDVRByPlaybackID :one
SELECT d.dvr_hash, d.internal_name, d.tenant_id, d.user_id, d.stream_id::text,
       d.origin_cluster_id, s.requires_auth
FROM commodore.dvr_recordings d
LEFT JOIN commodore.streams s ON s.id = d.stream_id
WHERE lower(d.playback_id::text) = lower($1::text);

-- name: ResolveVODByPlaybackID :one
SELECT vod_hash, internal_name, tenant_id, user_id, origin_cluster_id, requires_auth
FROM commodore.vod_assets
WHERE lower(playback_id::text) = lower($1::text);

-- name: ResolveClipByInternalName :one
SELECT clip_hash, internal_name, tenant_id, user_id, stream_id::text, origin_cluster_id, requires_auth
FROM commodore.clips
WHERE internal_name = $1;

-- name: ResolveDVRByInternalName :one
SELECT d.dvr_hash, d.internal_name, d.tenant_id, d.user_id, d.stream_id::text,
       d.origin_cluster_id, s.requires_auth
FROM commodore.dvr_recordings d
LEFT JOIN commodore.streams s ON s.id = d.stream_id
WHERE d.internal_name = $1;

-- name: ResolveVODByInternalName :one
SELECT vod_hash, internal_name, tenant_id, user_id, origin_cluster_id, requires_auth
FROM commodore.vod_assets
WHERE internal_name = $1;
