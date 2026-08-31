-- name: AllocateMediaAuthorityVersion :one
INSERT INTO commodore.media_authority_counters (
    authority_kind, authority_id, last_version, updated_at
) VALUES (
    sqlc.arg(authority_kind), sqlc.arg(authority_id), 1, NOW()
)
ON CONFLICT (authority_kind, authority_id) DO UPDATE
SET last_version = commodore.media_authority_counters.last_version + 1,
    updated_at = NOW()
RETURNING last_version;

-- name: BeginMediaAuthorityCompile :one
INSERT INTO commodore.media_authority_compile_fences (scope_key, generation, updated_at)
VALUES (sqlc.arg(scope_key), 1, NOW())
ON CONFLICT (scope_key) DO UPDATE
SET generation = commodore.media_authority_compile_fences.generation + 1,
    updated_at = NOW()
RETURNING generation;

-- name: LockMediaAuthorityCompile :one
SELECT generation
FROM commodore.media_authority_compile_fences
WHERE scope_key = sqlc.arg(scope_key)
FOR UPDATE;

-- name: InsertMediaAuthorityVersion :exec
INSERT INTO commodore.media_authority_versions (
    authority_kind, authority_id, authority_version, payload_schema_version,
    payload, payload_sha256, source_revisions, issued_at, refresh_after, valid_until
) VALUES (
    sqlc.arg(authority_kind), sqlc.arg(authority_id), sqlc.arg(authority_version),
    sqlc.arg(payload_schema_version), sqlc.arg(payload), sqlc.arg(payload_sha256),
    sqlc.arg(source_revisions), sqlc.arg(issued_at), sqlc.arg(refresh_after), sqlc.arg(valid_until)
);

-- name: UpsertCurrentMediaAuthority :execrows
INSERT INTO commodore.media_authority_current (
    authority_kind, authority_id, authority_version, updated_at
) VALUES (
    sqlc.arg(authority_kind), sqlc.arg(authority_id), sqlc.arg(authority_version), NOW()
)
ON CONFLICT (authority_kind, authority_id) DO UPDATE
SET authority_version = EXCLUDED.authority_version,
    updated_at = NOW()
WHERE commodore.media_authority_current.authority_version < EXCLUDED.authority_version;

-- name: EnqueueMediaAuthorityDelivery :execrows
INSERT INTO commodore.media_authority_deliveries (
    authority_kind, authority_id, authority_version, cell_id, signed_envelope
) VALUES (
    sqlc.arg(authority_kind), sqlc.arg(authority_id), sqlc.arg(authority_version),
    sqlc.arg(cell_id), sqlc.arg(signed_envelope)
)
ON CONFLICT (authority_kind, authority_id, authority_version, cell_id) DO NOTHING;

-- name: UpsertMediaAuthorityTarget :exec
INSERT INTO commodore.media_authority_targets (
    authority_kind, authority_id, cell_id, highest_targeted_version,
    first_targeted_at, last_targeted_at
) VALUES (
    sqlc.arg(authority_kind), sqlc.arg(authority_id), sqlc.arg(cell_id),
    sqlc.arg(authority_version), NOW(), NOW()
)
ON CONFLICT (authority_kind, authority_id, cell_id) DO UPDATE
SET highest_targeted_version = GREATEST(
        commodore.media_authority_targets.highest_targeted_version,
        EXCLUDED.highest_targeted_version
    ),
    last_targeted_at = NOW();

-- name: SupersedeOlderMediaAuthorityDeliveries :execrows
UPDATE commodore.media_authority_deliveries
SET status = 'superseded', lease_expires_at = NULL,
    last_error = 'superseded by a newer authority version', updated_at = NOW()
WHERE authority_kind = sqlc.arg(authority_kind)
  AND authority_id = sqlc.arg(authority_id)
  AND authority_version < sqlc.arg(authority_version)
  AND status IN ('pending', 'delivering');

-- name: ClaimMediaAuthorityDeliveries :many
WITH candidates AS (
    SELECT authority_kind, authority_id, authority_version, cell_id
    FROM commodore.media_authority_deliveries
    WHERE status IN ('pending', 'delivering')
      AND next_attempt_at <= NOW()
      AND (lease_expires_at IS NULL OR lease_expires_at <= NOW())
    ORDER BY next_attempt_at, created_at
    LIMIT sqlc.arg(batch_size)
    FOR UPDATE SKIP LOCKED
)
UPDATE commodore.media_authority_deliveries AS delivery
SET status = 'delivering',
    attempts = delivery.attempts + 1,
    lease_expires_at = NOW() + sqlc.arg(lease_ms)::bigint * INTERVAL '1 millisecond',
    updated_at = NOW()
FROM candidates
WHERE delivery.authority_kind = candidates.authority_kind
  AND delivery.authority_id = candidates.authority_id
  AND delivery.authority_version = candidates.authority_version
  AND delivery.cell_id = candidates.cell_id
RETURNING delivery.authority_kind, delivery.authority_id, delivery.authority_version,
          delivery.cell_id, delivery.signed_envelope, delivery.attempts;

-- name: MarkMediaAuthorityDeliveryAcknowledged :execrows
UPDATE commodore.media_authority_deliveries
SET status = 'acknowledged', acknowledged_at = NOW(), lease_expires_at = NULL,
    last_error = NULL, updated_at = NOW()
WHERE authority_kind = sqlc.arg(authority_kind)
  AND authority_id = sqlc.arg(authority_id)
  AND authority_version = sqlc.arg(authority_version)
  AND cell_id = sqlc.arg(cell_id)
  AND status = 'delivering';

-- name: RecordMediaAuthorityDeliveryFailure :execrows
UPDATE commodore.media_authority_deliveries
SET status = 'pending', next_attempt_at = sqlc.arg(next_attempt_at),
    lease_expires_at = NULL, last_error = sqlc.arg(last_error), updated_at = NOW()
WHERE authority_kind = sqlc.arg(authority_kind)
  AND authority_id = sqlc.arg(authority_id)
  AND authority_version = sqlc.arg(authority_version)
  AND cell_id = sqlc.arg(cell_id)
  AND status = 'delivering';

-- name: UpsertMediaAuthorityDistribution :exec
INSERT INTO commodore.media_authority_distribution (
    authority_kind, authority_id, cell_id, highest_acknowledged_version,
    first_acknowledged_at, last_acknowledged_at
) VALUES (
    sqlc.arg(authority_kind), sqlc.arg(authority_id), sqlc.arg(cell_id),
    sqlc.arg(authority_version), NOW(), NOW()
)
ON CONFLICT (authority_kind, authority_id, cell_id) DO UPDATE
SET highest_acknowledged_version = GREATEST(
        commodore.media_authority_distribution.highest_acknowledged_version,
        EXCLUDED.highest_acknowledged_version
    ),
    last_acknowledged_at = NOW();

-- name: ListMediaAuthorityPriorCells :many
SELECT cell_id
FROM commodore.media_authority_targets
WHERE authority_kind = sqlc.arg(authority_kind)
  AND authority_id = sqlc.arg(authority_id)
ORDER BY cell_id;

-- name: RequeueCurrentMediaAuthoritiesForCell :one
WITH requeued AS (
    UPDATE commodore.media_authority_deliveries AS delivery
    SET status = 'pending', next_attempt_at = NOW(), lease_expires_at = NULL,
        last_error = NULL, updated_at = NOW()
    FROM commodore.media_authority_current AS current
    WHERE delivery.authority_kind = current.authority_kind
      AND delivery.authority_id = current.authority_id
      AND delivery.authority_version = current.authority_version
      AND delivery.cell_id = sqlc.arg(cell_id)
      AND delivery.status = 'acknowledged'
    RETURNING 1
)
SELECT COUNT(*)::bigint AS requeued_count FROM requeued;

-- name: ListMediaAuthorityDeliveryStats :many
SELECT current.authority_kind,
       COUNT(*) FILTER (WHERE delivery.status IN ('pending', 'delivering'))::bigint AS pending_count,
       COALESCE(MAX(
           current.authority_version - COALESCE(distribution.highest_acknowledged_version, 0)
       ), 0)::bigint AS max_version_lag,
       COALESCE(MAX(
           CASE WHEN delivery.status IN ('pending', 'delivering')
                THEN EXTRACT(EPOCH FROM (NOW() - delivery.created_at))
                ELSE 0 END
       ), 0)::double precision AS oldest_pending_seconds
FROM commodore.media_authority_current AS current
JOIN commodore.media_authority_deliveries AS delivery
  ON delivery.authority_kind = current.authority_kind
 AND delivery.authority_id = current.authority_id
 AND delivery.authority_version = current.authority_version
LEFT JOIN commodore.media_authority_distribution AS distribution
  ON distribution.authority_kind = current.authority_kind
 AND distribution.authority_id = current.authority_id
 AND distribution.cell_id = delivery.cell_id
GROUP BY current.authority_kind
ORDER BY current.authority_kind;

-- name: InsertMediaAuthorityRefreshInbox :execrows
INSERT INTO commodore.media_authority_refresh_inbox (
    source_service, source_event_id, tenant_id, reason
) VALUES (
    sqlc.arg(source_service), sqlc.arg(source_event_id), sqlc.arg(tenant_id)::uuid,
    sqlc.arg(reason)
)
ON CONFLICT (source_service, source_event_id) DO NOTHING;

-- name: ClaimMediaAuthorityRefreshInbox :many
WITH candidates AS (
    SELECT source_service, source_event_id
    FROM commodore.media_authority_refresh_inbox
    WHERE status <> 'completed'
      AND next_attempt_at <= NOW()
      AND (lease_expires_at IS NULL OR lease_expires_at <= NOW())
    ORDER BY next_attempt_at, created_at
    LIMIT sqlc.arg(batch_size)
    FOR UPDATE SKIP LOCKED
)
UPDATE commodore.media_authority_refresh_inbox AS inbox
SET status = 'processing', attempts = inbox.attempts + 1,
    lease_expires_at = NOW() + sqlc.arg(lease_ms)::bigint * INTERVAL '1 millisecond',
    updated_at = NOW()
FROM candidates
WHERE inbox.source_service = candidates.source_service
  AND inbox.source_event_id = candidates.source_event_id
RETURNING inbox.source_service, inbox.source_event_id, inbox.tenant_id::text AS tenant_id,
          inbox.reason, inbox.attempts;

-- name: CompleteMediaAuthorityRefreshInbox :execrows
UPDATE commodore.media_authority_refresh_inbox
SET status = 'completed', completed_at = NOW(), lease_expires_at = NULL,
    last_error = NULL, updated_at = NOW()
WHERE source_service = sqlc.arg(source_service)
  AND source_event_id = sqlc.arg(source_event_id)
  AND status = 'processing';

-- name: FailMediaAuthorityRefreshInbox :execrows
UPDATE commodore.media_authority_refresh_inbox
SET status = 'pending', next_attempt_at = sqlc.arg(next_attempt_at),
    lease_expires_at = NULL, last_error = sqlc.arg(last_error), updated_at = NOW()
WHERE source_service = sqlc.arg(source_service)
  AND source_event_id = sqlc.arg(source_event_id)
  AND status = 'processing';

-- name: GetCurrentMediaAuthorityPayload :one
SELECT versions.payload, versions.valid_until
FROM commodore.media_authority_current AS current
JOIN commodore.media_authority_versions AS versions
  ON versions.authority_kind = current.authority_kind
 AND versions.authority_id = current.authority_id
 AND versions.authority_version = current.authority_version
WHERE current.authority_kind = sqlc.arg(authority_kind)
  AND current.authority_id = sqlc.arg(authority_id);

-- name: ListCurrentTenantAuthorityIDs :many
SELECT authority_id
FROM commodore.media_authority_current
WHERE authority_kind = 'tenant'
ORDER BY authority_id;

-- name: ListCurrentMediaAuthorityDeliveryCells :many
SELECT delivery.cell_id
FROM commodore.media_authority_current AS current
JOIN commodore.media_authority_deliveries AS delivery
  ON delivery.authority_kind = current.authority_kind
 AND delivery.authority_id = current.authority_id
 AND delivery.authority_version = current.authority_version
WHERE current.authority_kind = sqlc.arg(authority_kind)
  AND current.authority_id = sqlc.arg(authority_id)
ORDER BY delivery.cell_id;

-- name: GetLiveStreamMediaAuthoritySource :one
SELECT id::text AS stream_id, tenant_id::text AS tenant_id, user_id::text AS user_id,
       internal_name, playback_id::text AS playback_id, stream_key::text AS stream_key,
       ingest_mode, requires_auth, COALESCE(is_recording_enabled, FALSE)::boolean AS is_recording_enabled,
       COALESCE(playback_policy::text, '')::text AS playback_policy,
       COALESCE(playback_webhook_secret_enc, '')::text AS playback_webhook_secret_enc,
       COALESCE(active_ingest_cluster_id, '')::text AS active_ingest_cluster_id,
       deleted_at
FROM commodore.streams
WHERE id = sqlc.arg(stream_id)::uuid;

-- name: ListLiveStreamMediaAuthoritySources :many
SELECT id::text AS stream_id, tenant_id::text AS tenant_id
FROM commodore.streams
ORDER BY id;

-- name: ListTenantLiveStreamMediaAuthoritySources :many
SELECT id::text AS stream_id, tenant_id::text AS tenant_id
FROM commodore.streams
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
ORDER BY id;

-- name: GetPullMediaAuthoritySecret :one
SELECT source_uri_enc, enabled, COALESCE(allowed_cluster_ids, '{}') AS allowed_cluster_ids
FROM commodore.stream_pull_sources
WHERE stream_id = sqlc.arg(stream_id)::uuid;

-- name: GetNativeMediaAuthoritySecret :one
SELECT native.source_spec, native.source_kind, native.placement_count,
       COALESCE(native.allowed_cluster_ids, '{}') AS allowed_cluster_ids,
       stream.always_on
FROM commodore.stream_mist_sources AS native
JOIN commodore.streams AS stream ON stream.id = native.stream_id
WHERE native.stream_id = sqlc.arg(stream_id)::uuid;

-- name: GetArtifactMediaAuthoritySource :one
SELECT c.id::text AS authority_id, 'clip'::text AS artifact_kind, c.clip_hash AS artifact_hash,
       c.tenant_id::text AS tenant_id, c.user_id::text AS user_id, c.stream_id::text AS stream_id,
       c.internal_name, c.playback_id::text AS playback_id,
       COALESCE(c.origin_cluster_id, '')::text AS origin_cluster_id,
       c.requires_auth, COALESCE(c.playback_policy::text, '')::text AS playback_policy,
       COALESCE(c.playback_webhook_secret_enc, '')::text AS playback_webhook_secret_enc,
       TRUE AS parent_stream_exists, COALESCE(parent.internal_name, '')::text AS parent_stream_internal_name
FROM commodore.clips AS c
LEFT JOIN commodore.streams AS parent ON parent.id = c.stream_id
WHERE c.id = sqlc.arg(authority_id)::uuid
UNION ALL
SELECT d.id::text, 'dvr'::text, d.dvr_hash, d.tenant_id::text, d.user_id::text,
       COALESCE(d.stream_id::text, '')::text, d.internal_name, d.playback_id::text,
       COALESCE(d.origin_cluster_id, '')::text,
       CASE WHEN d.playback_authority_ready THEN d.requires_auth ELSE COALESCE(parent.requires_auth, TRUE) END,
       COALESCE((CASE WHEN d.playback_authority_ready THEN d.playback_policy ELSE parent.playback_policy END)::text, '')::text,
       COALESCE(CASE WHEN d.playback_authority_ready THEN d.playback_webhook_secret_enc ELSE parent.playback_webhook_secret_enc END, '')::text,
       EXISTS (SELECT 1 FROM commodore.streams AS parent WHERE parent.id = d.stream_id) AS parent_stream_exists,
       COALESCE(d.stream_internal_name, '')::text AS parent_stream_internal_name
FROM commodore.dvr_recordings AS d
LEFT JOIN commodore.streams AS parent ON parent.id = d.stream_id AND parent.tenant_id = d.tenant_id
WHERE d.id = sqlc.arg(authority_id)::uuid
UNION ALL
SELECT v.id::text,
       CASE WHEN v.origin_type = 'dvr_chapter' THEN 'chapter'::text ELSE 'vod'::text END,
       v.vod_hash, v.tenant_id::text, v.user_id::text, COALESCE(v.stream_id::text, '')::text,
       v.internal_name, v.playback_id::text, COALESCE(v.origin_cluster_id, '')::text,
       v.requires_auth, COALESCE(v.playback_policy::text, '')::text,
       COALESCE(v.playback_webhook_secret_enc, '')::text,
       TRUE AS parent_stream_exists,
       COALESCE(parent_dvr.stream_internal_name, parent_stream.internal_name, '')::text AS parent_stream_internal_name
FROM commodore.vod_assets AS v
LEFT JOIN commodore.dvr_chapter_playback AS chapter
  ON chapter.tenant_id = v.tenant_id AND chapter.artifact_hash = v.vod_hash
LEFT JOIN commodore.dvr_recordings AS parent_dvr
  ON parent_dvr.tenant_id = chapter.tenant_id AND parent_dvr.dvr_hash = chapter.dvr_hash
LEFT JOIN commodore.streams AS parent_stream ON parent_stream.id = v.stream_id
WHERE v.id = sqlc.arg(authority_id)::uuid;

-- name: ListArtifactMediaAuthoritySources :many
SELECT id::text AS authority_id, tenant_id::text AS tenant_id, 'clip'::text AS artifact_kind
FROM commodore.clips
UNION ALL
SELECT id::text, tenant_id::text, 'dvr'::text FROM commodore.dvr_recordings
UNION ALL
SELECT id::text, tenant_id::text,
       CASE WHEN origin_type = 'dvr_chapter' THEN 'chapter'::text ELSE 'vod'::text END
FROM commodore.vod_assets
ORDER BY authority_id;

-- name: ListTenantArtifactMediaAuthoritySources :many
SELECT id::text AS authority_id, tenant_id::text AS tenant_id, 'clip'::text AS artifact_kind
FROM commodore.clips WHERE tenant_id = sqlc.arg(tenant_id)::uuid
UNION ALL
SELECT id::text, tenant_id::text, 'dvr'::text FROM commodore.dvr_recordings
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
UNION ALL
SELECT id::text, tenant_id::text,
       CASE WHEN origin_type = 'dvr_chapter' THEN 'chapter'::text ELSE 'vod'::text END
FROM commodore.vod_assets WHERE tenant_id = sqlc.arg(tenant_id)::uuid
ORDER BY authority_id;
