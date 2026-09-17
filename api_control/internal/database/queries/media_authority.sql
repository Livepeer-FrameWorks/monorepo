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
    FROM commodore.media_authority_deliveries AS queued
    WHERE status IN ('pending', 'delivering')
      AND NOT EXISTS (
          SELECT 1 FROM commodore.media_authority_versions AS version
          WHERE version.authority_kind = queued.authority_kind
            AND version.authority_id = queued.authority_id
            AND version.authority_version = queued.authority_version
            AND version.payload_schema_version = 2
            AND version.valid_until <= version.issued_at + INTERVAL '1 minute'
      )
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

-- name: ClaimMediaAuthorityDeadlineDelivery :many
WITH heads AS (
    SELECT DISTINCT ON (queued.cell_id)
           queued.authority_kind, queued.authority_id, queued.authority_version,
           queued.cell_id, queued.next_attempt_at, queued.created_at
    FROM commodore.media_authority_deliveries AS queued
    JOIN commodore.media_authority_versions AS version
      ON version.authority_kind = queued.authority_kind
     AND version.authority_id = queued.authority_id
     AND version.authority_version = queued.authority_version
    WHERE queued.status IN ('pending', 'delivering')
      AND queued.next_attempt_at <= NOW()
      AND (queued.lease_expires_at IS NULL OR queued.lease_expires_at <= NOW())
      AND version.payload_schema_version = 2
      AND version.valid_until <= version.issued_at + INTERVAL '1 minute'
      AND NOT EXISTS (
          SELECT 1 FROM commodore.media_authority_deliveries AS inflight
          JOIN commodore.media_authority_versions AS active_version
            ON active_version.authority_kind = inflight.authority_kind
           AND active_version.authority_id = inflight.authority_id
           AND active_version.authority_version = inflight.authority_version
          WHERE inflight.cell_id = queued.cell_id
            AND inflight.status = 'delivering' AND inflight.lease_expires_at > NOW()
            AND active_version.payload_schema_version = 2
            AND active_version.valid_until <= active_version.issued_at + INTERVAL '1 minute'
      )
    ORDER BY queued.cell_id, queued.next_attempt_at, queued.created_at,
             queued.authority_kind, queued.authority_id, queued.authority_version
), candidates AS (
    SELECT queued.authority_kind, queued.authority_id, queued.authority_version, queued.cell_id
    FROM commodore.media_authority_deliveries AS queued
    JOIN heads ON heads.authority_kind = queued.authority_kind
              AND heads.authority_id = queued.authority_id
              AND heads.authority_version = queued.authority_version
              AND heads.cell_id = queued.cell_id
    WHERE queued.status IN ('pending', 'delivering')
      AND queued.next_attempt_at <= NOW()
      AND (queued.lease_expires_at IS NULL OR queued.lease_expires_at <= NOW())
    ORDER BY heads.next_attempt_at, heads.created_at, heads.cell_id
    LIMIT sqlc.arg(batch_size)
    FOR UPDATE OF queued SKIP LOCKED
)
UPDATE commodore.media_authority_deliveries AS delivery
SET status = 'delivering', attempts = delivery.attempts + 1,
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
WITH current_deliveries AS MATERIALIZED (
    SELECT current.authority_kind, current.authority_id, current.authority_version,
           delivery.cell_id, delivery.status, delivery.created_at
    FROM commodore.media_authority_current AS current
    JOIN LATERAL (
        SELECT cell_id, status, created_at
        FROM commodore.media_authority_deliveries
        WHERE authority_kind = current.authority_kind
          AND authority_id = current.authority_id
          AND authority_version = current.authority_version
        OFFSET 0
    ) AS delivery ON TRUE
)
SELECT current.authority_kind,
       COUNT(*) FILTER (WHERE current.status IN ('pending', 'delivering'))::bigint AS pending_count,
       COALESCE(MAX(
           current.authority_version - COALESCE(distribution.highest_acknowledged_version, 0)
       ), 0)::bigint AS max_version_lag,
       COALESCE(MAX(
           CASE WHEN current.status IN ('pending', 'delivering')
                THEN EXTRACT(EPOCH FROM (NOW() - current.created_at))
                ELSE 0 END
       ), 0)::double precision AS oldest_pending_seconds
FROM current_deliveries AS current
LEFT JOIN commodore.media_authority_distribution AS distribution
  ON distribution.authority_kind = current.authority_kind
 AND distribution.authority_id = current.authority_id
 AND distribution.cell_id = current.cell_id
GROUP BY current.authority_kind
ORDER BY current.authority_kind;

-- name: DeleteCompletedMediaAuthorityRefreshInbox :execrows
WITH candidates AS (
    SELECT queued.source_service, queued.source_event_id
    FROM commodore.media_authority_refresh_inbox AS queued
    WHERE queued.status = 'completed'
      AND queued.completed_at < sqlc.arg(completed_before)::timestamptz
    ORDER BY queued.completed_at, queued.source_service, queued.source_event_id
    LIMIT sqlc.arg(batch_size)
    FOR UPDATE SKIP LOCKED
)
DELETE FROM commodore.media_authority_refresh_inbox AS inbox
USING candidates
WHERE inbox.source_service = candidates.source_service
  AND inbox.source_event_id = candidates.source_event_id;

-- name: DeleteExpiredMediaAuthorityDeliveries :execrows
WITH candidates AS MATERIALIZED (
    SELECT version.authority_kind, version.authority_id, version.authority_version
    FROM commodore.media_authority_versions AS version
    LEFT JOIN commodore.media_authority_current AS current
      ON current.authority_kind = version.authority_kind
     AND current.authority_id = version.authority_id
     AND current.authority_version = version.authority_version
    WHERE version.valid_until < sqlc.arg(expired_before)
      AND current.authority_id IS NULL
    ORDER BY version.valid_until, version.authority_kind,
             version.authority_id, version.authority_version
    LIMIT sqlc.arg(batch_size)
)
DELETE FROM commodore.media_authority_deliveries AS delivery
USING candidates
WHERE delivery.authority_kind = candidates.authority_kind
  AND delivery.authority_id = candidates.authority_id
  AND delivery.authority_version = candidates.authority_version
  AND delivery.status IN ('acknowledged', 'superseded');

-- name: DeleteOrphanedMediaAuthorityVersions :execrows
WITH candidates AS MATERIALIZED (
    SELECT version.authority_kind, version.authority_id, version.authority_version
    FROM commodore.media_authority_versions AS version
    LEFT JOIN commodore.media_authority_current AS current
      ON current.authority_kind = version.authority_kind
     AND current.authority_id = version.authority_id
     AND current.authority_version = version.authority_version
    WHERE version.valid_until < sqlc.arg(expired_before)
      AND current.authority_id IS NULL
    ORDER BY version.valid_until, version.authority_kind,
             version.authority_id, version.authority_version
    LIMIT sqlc.arg(batch_size)
)
DELETE FROM commodore.media_authority_versions AS version
USING candidates
WHERE version.authority_kind = candidates.authority_kind
  AND version.authority_id = candidates.authority_id
  AND version.authority_version = candidates.authority_version
  AND NOT EXISTS (
      SELECT 1
      FROM commodore.media_authority_deliveries AS delivery
      WHERE delivery.authority_kind = version.authority_kind
        AND delivery.authority_id = version.authority_id
        AND delivery.authority_version = version.authority_version
  );

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
      AND NOT (source_service = 'commodore' AND source_event_id LIKE 'authority-deadline:%')
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

-- name: ScheduleMediaAuthorityRefresh :execrows
INSERT INTO commodore.media_authority_refresh_inbox
    (source_service, source_event_id, tenant_id, reason, next_attempt_at)
VALUES ('commodore', sqlc.arg(source_event_id), sqlc.arg(tenant_id)::uuid,
        sqlc.arg(reason), sqlc.arg(next_attempt_at))
ON CONFLICT (source_service, source_event_id) DO NOTHING;

-- name: ClaimMediaAuthorityDeadlineRefresh :many
WITH candidates AS (
    SELECT source_service, source_event_id
    FROM commodore.media_authority_refresh_inbox
    WHERE status <> 'completed'
      AND source_service = 'commodore'
      AND source_event_id LIKE 'authority-deadline:%'
      AND reason <> 'tenant_authority:deadline_refresh'
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

-- name: ClaimTenantMediaAuthorityDeadlineRefresh :many
WITH candidates AS (
    SELECT source_service, source_event_id
    FROM commodore.media_authority_refresh_inbox
    WHERE status <> 'completed'
      AND source_service = 'commodore'
      AND source_event_id LIKE 'authority-deadline:%'
      AND reason = 'tenant_authority:deadline_refresh'
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

-- name: GetScheduledMediaAuthorityVersion :one
WITH scoped AS (
    SELECT 'tenant'::text AS kind, sqlc.arg(tenant_id)::text AS id
    UNION ALL
    SELECT 'media_object', 'live_stream:' || id::text FROM commodore.streams
    WHERE tenant_id = sqlc.arg(tenant_id)::uuid
    UNION ALL
    SELECT 'media_object', 'artifact:' || id::text FROM commodore.clips
    WHERE tenant_id = sqlc.arg(tenant_id)::uuid
    UNION ALL
    SELECT 'media_object', 'artifact:' || id::text FROM commodore.dvr_recordings
    WHERE tenant_id = sqlc.arg(tenant_id)::uuid
    UNION ALL
    SELECT 'media_object', 'artifact:' || id::text FROM commodore.vod_assets
    WHERE tenant_id = sqlc.arg(tenant_id)::uuid
)
SELECT current.authority_version
FROM commodore.media_authority_current AS current
JOIN scoped ON scoped.kind = current.authority_kind AND scoped.id = current.authority_id
WHERE current.authority_kind = sqlc.arg(authority_kind)
  AND current.authority_id = sqlc.arg(authority_id);

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

-- name: LockCurrentTenantMediaAuthority :one
SELECT versions.payload, versions.valid_until
FROM commodore.media_authority_current AS current
JOIN commodore.media_authority_versions AS versions
  ON versions.authority_kind = current.authority_kind
 AND versions.authority_id = current.authority_id
 AND versions.authority_version = current.authority_version
WHERE current.authority_kind = 'tenant'
  AND current.authority_id = sqlc.arg(tenant_id)::text
FOR SHARE OF current;

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

-- name: UpsertMediaCellPlacementCapability :one
INSERT INTO commodore.media_cell_placement_capabilities (
    cell_id, max_schema_version, enforcement_ready, live_replicas, first_ready_at, attested_at, updated_at
) VALUES (
    sqlc.arg(cell_id), sqlc.arg(max_schema_version), sqlc.arg(enforcement_ready), sqlc.arg(live_replicas),
    CASE WHEN sqlc.arg(enforcement_ready)::boolean THEN NOW() ELSE NULL END, NOW(), NOW()
)
ON CONFLICT (cell_id) DO UPDATE
SET max_schema_version = EXCLUDED.max_schema_version,
    enforcement_ready = EXCLUDED.enforcement_ready,
    live_replicas = EXCLUDED.live_replicas,
    first_ready_at = CASE
        WHEN EXCLUDED.enforcement_ready AND commodore.media_cell_placement_capabilities.first_ready_at IS NULL THEN NOW()
        WHEN EXCLUDED.enforcement_ready THEN commodore.media_cell_placement_capabilities.first_ready_at
        ELSE NULL END,
    attested_at = NOW(),
    updated_at = NOW()
RETURNING enforcement_ready, first_ready_at;

-- name: GetMediaCellPlacementCapability :one
SELECT cell_id, max_schema_version, enforcement_ready, live_replicas, first_ready_at, attested_at
FROM commodore.media_cell_placement_capabilities
WHERE cell_id = sqlc.arg(cell_id);

-- name: ListMediaCellPlacementCapabilities :many
SELECT cell_id, max_schema_version, enforcement_ready, live_replicas, attested_at
FROM commodore.media_cell_placement_capabilities
WHERE cell_id = ANY(sqlc.arg(cell_ids)::text[])
ORDER BY cell_id;

-- name: ListLegacySchemaTenantsTargetingCell :many
SELECT DISTINCT target.authority_id AS tenant_id
FROM commodore.media_authority_targets AS target
JOIN commodore.media_authority_current AS current
  ON current.authority_kind = target.authority_kind
 AND current.authority_id = target.authority_id
JOIN commodore.media_authority_versions AS version
  ON version.authority_kind = current.authority_kind
 AND version.authority_id = current.authority_id
 AND version.authority_version = current.authority_version
WHERE target.authority_kind = 'tenant'
  AND target.cell_id = sqlc.arg(cell_id)
  AND version.payload_schema_version < sqlc.arg(schema_version)::integer
ORDER BY tenant_id;

-- name: ListCurrentMediaAuthorityRollout :many
SELECT current.authority_version,
       versions.payload, versions.payload_schema_version, versions.valid_until,
       delivery.cell_id, delivery.status AS delivery_status, delivery.acknowledged_at,
       COALESCE(capability.enforcement_ready, FALSE)::boolean AS cell_ready,
       COALESCE(capability.max_schema_version, 0)::integer AS cell_schema_version
FROM commodore.media_authority_current AS current
JOIN commodore.media_authority_versions AS versions
  ON versions.authority_kind = current.authority_kind
 AND versions.authority_id = current.authority_id
 AND versions.authority_version = current.authority_version
JOIN commodore.media_authority_deliveries AS delivery
  ON delivery.authority_kind = current.authority_kind
 AND delivery.authority_id = current.authority_id
 AND delivery.authority_version = current.authority_version
LEFT JOIN commodore.media_cell_placement_capabilities AS capability
  ON capability.cell_id = delivery.cell_id
WHERE current.authority_kind = sqlc.arg(authority_kind)
  AND current.authority_id = sqlc.arg(authority_id)
ORDER BY delivery.cell_id;

-- name: LockMediaAuthorityActivation :exec
-- Serializes activation derivation for one authority. Acknowledgements for the
-- same authority version are delivered concurrently, each in its own READ
-- COMMITTED transaction, so without this every one of them can read the others'
-- pre-acknowledgement state, conclude the rollout is incomplete, and leave a
-- fully delivered change pending forever.
--
-- The key is namespaced: hashtext resolves to the single-bigint overload, which
-- Commodore's artifact-catalog and creation-identity locks also use, and an
-- unnamespaced authority id could collide with one of those.
SELECT pg_advisory_xact_lock(
  hashtext('media_authority_activation:' || sqlc.arg(authority_kind)::text || ':' || sqlc.arg(authority_id)::text)
);

-- name: ListAuthoritiesAwaitingActivation :many
-- One page of placement scopes whose change is still unresolved. Activation is
-- otherwise only ever derived as a side effect of an acknowledgement, so
-- anything that interrupts the acknowledgement that would have activated a
-- change -- a crashed process, a delivery that failed and succeeded on retry --
-- leaves it stranded with no retry path. This is the backstop that converges
-- those.
--
-- Paged by (tenant_id, created_at) rather than filtered by age alone, because a
-- legitimately blocked change never leaves this set: re-writing the same
-- rollout status is a no-op that does not bump updated_at. A fixed ordering plus
-- LIMIT would then let a bounded prefix of permanently blocked scopes hide every
-- scope behind them forever -- starving exactly the stranded change this exists
-- to rescue. The caller walks the cursor and wraps, so every scope is visited.
--
-- The ordering matches idx_media_placement_changes_pending, so paging is an
-- index scan rather than a sort of every pending row once a second.
SELECT changes.tenant_id::text AS tenant_id,
       changes.scope_kind,
       changes.scope_id::text AS scope_id,
       changes.created_at
FROM commodore.media_placement_changes AS changes
WHERE changes.rollout_status IN ('pending', 'blocked')
  AND changes.updated_at < NOW() - sqlc.arg(min_age_ms)::bigint * INTERVAL '1 millisecond'
  AND (changes.tenant_id, changes.created_at) > (sqlc.arg(after_tenant_id)::uuid, sqlc.arg(after_created_at)::timestamptz)
ORDER BY changes.tenant_id, changes.created_at
LIMIT sqlc.arg(max_rows)::integer;
