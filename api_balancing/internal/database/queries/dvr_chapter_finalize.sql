-- name: LockChapterFinalizeArtifact :one
SELECT c.state, COALESCE(c.playback_artifact_hash, '')::text AS playback_artifact_hash,
       a.tenant_id::text AS tenant_id, a.status AS artifact_status,
       COALESCE(p.status, '')::text AS parent_status,
       COALESCE(c.finalize_node_id, '')::text AS finalize_node_id
FROM foghorn.dvr_chapters c
JOIN foghorn.artifacts a ON a.artifact_hash = c.playback_artifact_hash
LEFT JOIN foghorn.artifacts p ON p.artifact_hash = c.artifact_hash AND p.artifact_type = 'dvr'
WHERE c.chapter_id = $1
FOR UPDATE OF c, a;

-- name: FinalizeChapterPlaybackArtifact :execrows
UPDATE foghorn.artifacts
SET status = 'ready', format = 'mkv',
    size_bytes = NULLIF(sqlc.arg(size_bytes)::bigint, 0),
    tracks = CASE WHEN sqlc.arg(tracks_present)::boolean THEN sqlc.arg(tracks_json)::text::jsonb ELSE tracks END,
    duration_ms = CASE WHEN sqlc.arg(duration_ms)::bigint > 0 THEN sqlc.arg(duration_ms)::bigint ELSE duration_ms END,
    duration_seconds = CASE WHEN sqlc.arg(duration_ms)::bigint > 0 THEN (sqlc.arg(duration_ms)::bigint / 1000)::int ELSE duration_seconds END,
    sync_status = 'pending', storage_location = 'local', updated_at = NOW()
WHERE artifact_hash = sqlc.arg(artifact_hash) AND status = 'finalizing';

-- name: GetChapterArtifactLifecycleIdentity :one
SELECT c.playback_artifact_hash, a.tenant_id::text AS tenant_id
FROM foghorn.dvr_chapters c
JOIN foghorn.artifacts a ON a.artifact_hash = c.playback_artifact_hash
WHERE c.chapter_id = $1;

-- name: UpsertChapterVodMetadata :exec
INSERT INTO foghorn.vod_metadata (
    artifact_hash, duration_ms, resolution, video_codec, audio_codec,
    bitrate_kbps, width, height, fps, audio_channels, audio_sample_rate, updated_at
) VALUES (
    sqlc.arg(artifact_hash),
    sqlc.narg(duration_ms)::text::integer, sqlc.narg(resolution)::text,
    sqlc.narg(video_codec)::text, sqlc.narg(audio_codec)::text,
    sqlc.narg(bitrate_kbps)::text::integer, sqlc.narg(width)::text::integer,
    sqlc.narg(height)::text::integer, sqlc.narg(fps)::text::real,
    sqlc.narg(audio_channels)::text::integer, sqlc.narg(audio_sample_rate)::text::integer, NOW()
)
ON CONFLICT (artifact_hash) DO UPDATE SET
    duration_ms = COALESCE(EXCLUDED.duration_ms, foghorn.vod_metadata.duration_ms),
    resolution = COALESCE(EXCLUDED.resolution, foghorn.vod_metadata.resolution),
    video_codec = COALESCE(EXCLUDED.video_codec, foghorn.vod_metadata.video_codec),
    audio_codec = COALESCE(EXCLUDED.audio_codec, foghorn.vod_metadata.audio_codec),
    bitrate_kbps = COALESCE(EXCLUDED.bitrate_kbps, foghorn.vod_metadata.bitrate_kbps),
    width = COALESCE(EXCLUDED.width, foghorn.vod_metadata.width),
    height = COALESCE(EXCLUDED.height, foghorn.vod_metadata.height),
    fps = COALESCE(EXCLUDED.fps, foghorn.vod_metadata.fps),
    audio_channels = COALESCE(EXCLUDED.audio_channels, foghorn.vod_metadata.audio_channels),
    audio_sample_rate = COALESCE(EXCLUDED.audio_sample_rate, foghorn.vod_metadata.audio_sample_rate),
    updated_at = NOW();

-- name: GetChapterArtifactResolution :one
SELECT origin_type, origin_id, tenant_id::text AS tenant_id,
       COALESCE(internal_name, '')::text AS internal_name,
       (status NOT IN ('deleted', 'failed'))::boolean AS playable
FROM foghorn.artifacts
WHERE artifact_hash = $1;

-- name: GetPlayableChapterArtifactResolution :one
SELECT origin_type, origin_id, tenant_id::text AS tenant_id,
       COALESCE(internal_name, '')::text AS internal_name
FROM foghorn.artifacts
WHERE artifact_hash = $1 AND status NOT IN ('deleted', 'failed');

-- name: GetChapterArtifactRouting :one
SELECT origin_type, origin_id, tenant_id::text AS tenant_id,
       COALESCE(origin_cluster_id, '')::text AS origin_cluster_id
FROM foghorn.artifacts
WHERE artifact_hash = $1 AND status NOT IN ('deleted', 'failed');
