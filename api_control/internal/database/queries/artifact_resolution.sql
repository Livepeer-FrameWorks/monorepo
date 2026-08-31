-- name: ResolveClipByPlaybackID :one
SELECT c.clip_hash, c.internal_name, c.tenant_id, c.user_id, c.stream_id::text AS stream_id, c.origin_cluster_id, c.requires_auth,
       COALESCE(parent.internal_name, '')::text AS parent_stream_internal_name
FROM commodore.clips AS c
LEFT JOIN commodore.streams AS parent ON parent.id = c.stream_id AND parent.tenant_id = c.tenant_id
WHERE lower(c.playback_id::text) = lower($1::text);

-- name: ResolveDVRByPlaybackID :one
SELECT d.dvr_hash, d.internal_name, d.tenant_id, d.user_id, d.stream_id::text AS stream_id,
       d.origin_cluster_id,
       (CASE WHEN d.playback_authority_ready THEN d.requires_auth ELSE COALESCE(s.requires_auth, TRUE) END)::boolean AS requires_auth,
       COALESCE(d.stream_internal_name, s.internal_name, '')::text AS parent_stream_internal_name
FROM commodore.dvr_recordings d
LEFT JOIN commodore.streams s ON s.id = d.stream_id AND s.tenant_id = d.tenant_id
WHERE lower(d.playback_id::text) = lower($1::text);

-- name: ResolveVODByPlaybackID :one
SELECT v.vod_hash, v.internal_name, v.tenant_id, v.user_id, COALESCE(v.stream_id::text, '')::text AS stream_id,
       v.origin_cluster_id, v.requires_auth,
       CASE WHEN v.origin_type = 'dvr_chapter' THEN 'chapter'::text ELSE 'vod'::text END AS content_type,
       COALESCE(parent_dvr.stream_internal_name, parent_stream.internal_name, '')::text AS parent_stream_internal_name
FROM commodore.vod_assets AS v
LEFT JOIN commodore.dvr_chapter_playback AS chapter
  ON chapter.tenant_id = v.tenant_id AND chapter.artifact_hash = v.vod_hash
LEFT JOIN commodore.dvr_recordings AS parent_dvr
  ON parent_dvr.tenant_id = chapter.tenant_id AND parent_dvr.dvr_hash = chapter.dvr_hash
LEFT JOIN commodore.streams AS parent_stream ON parent_stream.id = v.stream_id AND parent_stream.tenant_id = v.tenant_id
WHERE lower(v.playback_id::text) = lower($1::text);

-- name: ResolveClipByInternalName :one
SELECT c.clip_hash, c.internal_name, c.tenant_id, c.user_id, c.stream_id::text AS stream_id, c.origin_cluster_id, c.requires_auth,
       COALESCE(parent.internal_name, '')::text AS parent_stream_internal_name
FROM commodore.clips AS c
LEFT JOIN commodore.streams AS parent ON parent.id = c.stream_id AND parent.tenant_id = c.tenant_id
WHERE c.internal_name = $1;

-- name: ResolveDVRByInternalName :one
SELECT d.dvr_hash, d.internal_name, d.tenant_id, d.user_id, d.stream_id::text AS stream_id,
       d.origin_cluster_id,
       (CASE WHEN d.playback_authority_ready THEN d.requires_auth ELSE COALESCE(s.requires_auth, TRUE) END)::boolean AS requires_auth,
       COALESCE(d.stream_internal_name, s.internal_name, '')::text AS parent_stream_internal_name
FROM commodore.dvr_recordings d
LEFT JOIN commodore.streams s ON s.id = d.stream_id AND s.tenant_id = d.tenant_id
WHERE d.internal_name = $1;

-- name: ResolveVODByInternalName :one
SELECT v.vod_hash, v.internal_name, v.tenant_id, v.user_id, COALESCE(v.stream_id::text, '')::text AS stream_id,
       v.origin_cluster_id, v.requires_auth,
       CASE WHEN v.origin_type = 'dvr_chapter' THEN 'chapter'::text ELSE 'vod'::text END AS content_type,
       COALESCE(parent_dvr.stream_internal_name, parent_stream.internal_name, '')::text AS parent_stream_internal_name
FROM commodore.vod_assets AS v
LEFT JOIN commodore.dvr_chapter_playback AS chapter
  ON chapter.tenant_id = v.tenant_id AND chapter.artifact_hash = v.vod_hash
LEFT JOIN commodore.dvr_recordings AS parent_dvr
  ON parent_dvr.tenant_id = chapter.tenant_id AND parent_dvr.dvr_hash = chapter.dvr_hash
LEFT JOIN commodore.streams AS parent_stream ON parent_stream.id = v.stream_id AND parent_stream.tenant_id = v.tenant_id
WHERE v.internal_name = $1;
