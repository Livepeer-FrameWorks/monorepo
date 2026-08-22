-- name: ResolveIdentifierCatalog :one
WITH candidates AS (
    SELECT 0 AS priority, s.tenant_id::text AS tenant_id, s.user_id::text AS user_id,
           s.internal_name, 'stream_id'::text AS identifier_type,
           COALESCE(s.is_recording_enabled, false)::boolean AS is_recording_enabled,
           s.id::text AS stream_id, s.requires_auth
    FROM commodore.streams s
    WHERE sqlc.arg(include_ids)::boolean AND s.id::text = sqlc.arg(identifier)::text
    UNION ALL
    SELECT 1, v.tenant_id::text, v.user_id::text, ''::text, 'vod_id'::text,
           false, ''::text, v.requires_auth
    FROM commodore.vod_assets v
    WHERE sqlc.arg(include_ids)::boolean AND v.id::text = sqlc.arg(identifier)::text
    UNION ALL
    SELECT 2, s.tenant_id::text, s.user_id::text, s.internal_name, 'stream'::text,
           COALESCE(s.is_recording_enabled, false)::boolean, s.id::text, s.requires_auth
    FROM commodore.streams s
    WHERE s.internal_name = sqlc.arg(identifier)::text AND s.deleted_at IS NULL
    UNION ALL
    SELECT 3, s.tenant_id::text, s.user_id::text, s.internal_name, 'playback_id'::text,
           COALESCE(s.is_recording_enabled, false)::boolean, s.id::text, s.requires_auth
    FROM commodore.streams s
    WHERE lower(s.playback_id::text) = lower(sqlc.arg(identifier)::text) AND s.deleted_at IS NULL
    UNION ALL
    SELECT 4, c.tenant_id::text, c.user_id::text, COALESCE(s.internal_name, '')::text,
           'clip_playback_id'::text, false, c.stream_id::text, c.requires_auth
    FROM commodore.clips c LEFT JOIN commodore.streams s ON s.id = c.stream_id
    WHERE lower(c.playback_id::text) = lower(sqlc.arg(identifier)::text)
    UNION ALL
    SELECT 5, d.tenant_id::text, d.user_id::text, d.internal_name, 'dvr_playback_id'::text,
           false, COALESCE(d.stream_id::text, '')::text, COALESCE(s.requires_auth, true)::boolean
    FROM commodore.dvr_recordings d LEFT JOIN commodore.streams s ON s.id = d.stream_id
    WHERE lower(d.playback_id::text) = lower(sqlc.arg(identifier)::text)
    UNION ALL
    SELECT 6, v.tenant_id::text, v.user_id::text, ''::text, 'vod_playback_id'::text,
           false, ''::text, v.requires_auth
    FROM commodore.vod_assets v
    WHERE lower(v.playback_id::text) = lower(sqlc.arg(identifier)::text)
    UNION ALL
    SELECT 7, c.tenant_id::text, c.user_id::text, COALESCE(s.internal_name, '')::text,
           'clip_internal_name'::text, false, c.stream_id::text, c.requires_auth
    FROM commodore.clips c LEFT JOIN commodore.streams s ON s.id = c.stream_id
    WHERE c.internal_name = sqlc.arg(identifier)::text
    UNION ALL
    SELECT 8, d.tenant_id::text, d.user_id::text, d.internal_name, 'dvr_internal_name'::text,
           false, COALESCE(d.stream_id::text, '')::text, COALESCE(s.requires_auth, true)::boolean
    FROM commodore.dvr_recordings d LEFT JOIN commodore.streams s ON s.id = d.stream_id
    WHERE d.internal_name = sqlc.arg(identifier)::text
    UNION ALL
    SELECT 9, v.tenant_id::text, v.user_id::text, ''::text, 'vod_internal_name'::text,
           false, ''::text, v.requires_auth
    FROM commodore.vod_assets v
    WHERE v.internal_name = sqlc.arg(identifier)::text
    UNION ALL
    SELECT 10, c.tenant_id::text, c.user_id::text, COALESCE(s.internal_name, '')::text,
           'clip'::text, false, c.stream_id::text, c.requires_auth
    FROM commodore.clips c LEFT JOIN commodore.streams s ON s.id = c.stream_id
    WHERE c.clip_hash = sqlc.arg(identifier)::text
    UNION ALL
    SELECT 11, d.tenant_id::text, d.user_id::text, d.internal_name, 'dvr'::text,
           false, COALESCE(d.stream_id::text, '')::text, COALESCE(s.requires_auth, true)::boolean
    FROM commodore.dvr_recordings d LEFT JOIN commodore.streams s ON s.id = d.stream_id
    WHERE d.dvr_hash = sqlc.arg(identifier)::text
    UNION ALL
    SELECT 12, v.tenant_id::text, v.user_id::text, ''::text, 'vod'::text,
           false, ''::text, v.requires_auth
    FROM commodore.vod_assets v
    WHERE v.vod_hash = sqlc.arg(identifier)::text
)
SELECT tenant_id, user_id, internal_name, identifier_type,
       is_recording_enabled, stream_id, requires_auth
FROM candidates
ORDER BY priority
LIMIT 1;
