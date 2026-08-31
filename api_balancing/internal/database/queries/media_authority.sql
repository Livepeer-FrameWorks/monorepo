-- name: SetLocalMediaAuthorityLockTimeout :exec
SELECT set_config('lock_timeout', sqlc.arg(lock_timeout), true);

-- name: LockMediaAuthority :exec
SELECT pg_advisory_xact_lock(
    sqlc.arg(lock_namespace)::integer,
    hashtext(sqlc.arg(authority_kind)::text || ':' || sqlc.arg(authority_id)::text)
);

-- name: GetMediaAuthorityForUpdate :one
SELECT authority_version, payload_sha256, payload
FROM foghorn.media_authorities
WHERE authority_kind = sqlc.arg(authority_kind)
  AND authority_id = sqlc.arg(authority_id)
FOR UPDATE;

-- name: UpsertMediaAuthority :exec
INSERT INTO foghorn.media_authorities (
    authority_kind, authority_id, authority_version, signer_key_id,
    audience_cell_id, issued_at, refresh_after, valid_until, payload_sha256,
    signed_envelope, payload, source_revisions, applied_at
) VALUES (
    sqlc.arg(authority_kind), sqlc.arg(authority_id), sqlc.arg(authority_version),
    sqlc.arg(signer_key_id), sqlc.arg(audience_cell_id), sqlc.arg(issued_at),
    sqlc.arg(refresh_after), sqlc.arg(valid_until), sqlc.arg(payload_sha256),
    sqlc.arg(signed_envelope), sqlc.arg(payload), sqlc.arg(source_revisions), NOW()
)
ON CONFLICT (authority_kind, authority_id) DO UPDATE
SET authority_version = EXCLUDED.authority_version,
    signer_key_id = EXCLUDED.signer_key_id,
    audience_cell_id = EXCLUDED.audience_cell_id,
    issued_at = EXCLUDED.issued_at,
    refresh_after = EXCLUDED.refresh_after,
    valid_until = EXCLUDED.valid_until,
    payload_sha256 = EXCLUDED.payload_sha256,
    signed_envelope = EXCLUDED.signed_envelope,
    payload = EXCLUDED.payload,
    source_revisions = EXCLUDED.source_revisions,
    applied_at = NOW();

-- name: UpsertTenantAuthorityProjection :exec
INSERT INTO foghorn.tenant_authority_projection (
    tenant_id, authority_version, lifecycle, billing_decision, billing_model,
    official_cluster_id, allow_platform_shared_playback, max_streams, max_viewers,
    allowances, decision_reason, local_read_ready, local_ingest_ready, local_source_ready,
    valid_until, updated_at
) VALUES (
    sqlc.arg(tenant_id)::uuid, sqlc.arg(authority_version), sqlc.arg(lifecycle),
    sqlc.arg(billing_decision), sqlc.arg(billing_model), NULLIF(sqlc.arg(official_cluster_id)::text, ''),
    sqlc.arg(allow_platform_shared_playback), sqlc.arg(max_streams), sqlc.arg(max_viewers),
    sqlc.arg(allowances), sqlc.arg(decision_reason), FALSE, FALSE, FALSE,
    sqlc.arg(valid_until), NOW()
)
ON CONFLICT (tenant_id) DO UPDATE
SET authority_version = EXCLUDED.authority_version,
    lifecycle = EXCLUDED.lifecycle,
    billing_decision = EXCLUDED.billing_decision,
    billing_model = EXCLUDED.billing_model,
    official_cluster_id = EXCLUDED.official_cluster_id,
    allow_platform_shared_playback = EXCLUDED.allow_platform_shared_playback,
    max_streams = EXCLUDED.max_streams,
    max_viewers = EXCLUDED.max_viewers,
    allowances = EXCLUDED.allowances,
    decision_reason = EXCLUDED.decision_reason,
    local_read_ready = CASE
        WHEN sqlc.arg(preserve_local_read_ready)::boolean
        THEN foghorn.tenant_authority_projection.local_read_ready
        ELSE FALSE
    END,
    local_ingest_ready = CASE
        WHEN sqlc.arg(preserve_local_ingest_ready)::boolean
        THEN foghorn.tenant_authority_projection.local_ingest_ready
        ELSE FALSE
    END,
    local_source_ready = CASE
        WHEN sqlc.arg(preserve_local_source_ready)::boolean
        THEN foghorn.tenant_authority_projection.local_source_ready
        ELSE FALSE
    END,
    valid_until = EXCLUDED.valid_until,
    updated_at = NOW();

-- name: DeleteTenantAuthorityGrants :exec
DELETE FROM foghorn.tenant_authority_grants
WHERE tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: InsertTenantAuthorityGrant :exec
INSERT INTO foghorn.tenant_authority_grants (
    tenant_id, cluster_id, authority_version, access_source, access_level,
    subscription_status, cluster_class, cluster_type, deployment_model, owner_tenant_id, expires_at
) VALUES (
    sqlc.arg(tenant_id)::uuid, sqlc.arg(cluster_id), sqlc.arg(authority_version),
    sqlc.arg(access_source), sqlc.arg(access_level), sqlc.arg(subscription_status),
    sqlc.arg(cluster_class), sqlc.arg(cluster_type), sqlc.arg(deployment_model),
    NULLIF(sqlc.arg(owner_tenant_id)::text, '')::uuid, sqlc.narg(expires_at)
);

-- name: UpsertMediaObjectAuthorityProjection :exec
WITH upserted AS (
INSERT INTO foghorn.media_object_authority_projection (
    authority_id, authority_version, object_kind, tenant_id, user_id, internal_name,
    playback_id, lifecycle, origin_cluster_id, playback_policy_kind, playback_policy,
    stream_id, ingest_mode, artifact_id, artifact_hash, artifact_kind,
    local_read_ready, local_ingest_ready, local_source_ready, publishing_credential_sha256,
    valid_until, updated_at
) VALUES (
    sqlc.arg(authority_id), sqlc.arg(authority_version), sqlc.arg(object_kind),
    sqlc.arg(tenant_id)::uuid, NULLIF(sqlc.arg(user_id)::text, '')::uuid,
    sqlc.arg(internal_name), sqlc.arg(playback_id), sqlc.arg(lifecycle),
    NULLIF(sqlc.arg(origin_cluster_id)::text, ''), sqlc.arg(playback_policy_kind),
    sqlc.arg(playback_policy), NULLIF(sqlc.arg(stream_id)::text, '')::uuid,
    NULLIF(sqlc.arg(ingest_mode)::text, ''), NULLIF(sqlc.arg(artifact_id)::text, ''),
    NULLIF(sqlc.arg(artifact_hash)::text, ''), NULLIF(sqlc.arg(artifact_kind)::text, ''),
    FALSE, FALSE, FALSE, sqlc.narg(publishing_credential_sha256), sqlc.arg(valid_until), NOW()
)
ON CONFLICT (authority_id) DO UPDATE
SET authority_version = EXCLUDED.authority_version,
    object_kind = EXCLUDED.object_kind,
    tenant_id = EXCLUDED.tenant_id,
    user_id = EXCLUDED.user_id,
    internal_name = EXCLUDED.internal_name,
    playback_id = EXCLUDED.playback_id,
    lifecycle = EXCLUDED.lifecycle,
    origin_cluster_id = EXCLUDED.origin_cluster_id,
    playback_policy_kind = EXCLUDED.playback_policy_kind,
    playback_policy = EXCLUDED.playback_policy,
    stream_id = EXCLUDED.stream_id,
    ingest_mode = EXCLUDED.ingest_mode,
    artifact_id = EXCLUDED.artifact_id,
    artifact_hash = EXCLUDED.artifact_hash,
    artifact_kind = EXCLUDED.artifact_kind,
    local_read_ready = CASE
        WHEN sqlc.arg(preserve_local_read_ready)::boolean
        THEN foghorn.media_object_authority_projection.local_read_ready
        ELSE FALSE
    END,
    local_ingest_ready = CASE
        WHEN sqlc.arg(preserve_local_ingest_ready)::boolean
        THEN foghorn.media_object_authority_projection.local_ingest_ready
        ELSE FALSE
    END,
    local_source_ready = CASE
        WHEN sqlc.arg(preserve_local_source_ready)::boolean
        THEN foghorn.media_object_authority_projection.local_source_ready
        ELSE FALSE
    END,
    publishing_credential_sha256 = EXCLUDED.publishing_credential_sha256,
    valid_until = EXCLUDED.valid_until,
    updated_at = NOW()
RETURNING authority_id, tenant_id, artifact_hash, lifecycle, valid_until
)
UPDATE foghorn.artifacts AS artifact
SET status = 'ready'
FROM upserted
WHERE upserted.lifecycle = 'active'
  AND upserted.valid_until > NOW()
  AND upserted.artifact_hash IS NOT NULL
  AND artifact.artifact_hash = upserted.artifact_hash
  AND artifact.tenant_id = upserted.tenant_id
  AND artifact.federated_pointer = true
  AND artifact.status = 'deleted'
  -- A non-NULL token means a purge worker may already be deleting bytes
  -- outside this transaction. Settlement, not authority apply, owns restore.
  AND artifact.federated_purge_token IS NULL
  -- A newly delivered active authority cancels an interrupted expiry-only
  -- purge fence. A signed tombstone under any other authority identity stays
  -- terminal; the row being replaced is read through the CTE's RETURNING data.
  AND NOT EXISTS (
      SELECT 1
      FROM foghorn.media_object_authority_projection AS tombstone
      WHERE tombstone.tenant_id = upserted.tenant_id
        AND tombstone.artifact_hash = upserted.artifact_hash
        AND tombstone.lifecycle = 'tombstone'
        AND tombstone.authority_id <> upserted.authority_id
  );

-- name: InsertMediaAuthorityApplyAudit :exec
INSERT INTO foghorn.media_authority_apply_audit (
    authority_kind, authority_id, authority_version, signer_key_id,
    payload_sha256, outcome, reason
) VALUES (
    NULLIF(sqlc.arg(authority_kind)::text, ''), NULLIF(sqlc.arg(authority_id)::text, ''),
    sqlc.narg(authority_version), NULLIF(sqlc.arg(signer_key_id)::text, ''),
    sqlc.narg(payload_sha256), sqlc.arg(outcome), sqlc.arg(reason)
);

-- name: PruneMediaAuthorityApplyAudit :execrows
WITH expired AS (
    SELECT id
    FROM foghorn.media_authority_apply_audit
    WHERE observed_at < NOW() - sqlc.arg(retention_seconds)::bigint * INTERVAL '1 second'
    ORDER BY observed_at
    LIMIT sqlc.arg(batch_size)
)
DELETE FROM foghorn.media_authority_apply_audit AS audit
USING expired
WHERE audit.id = expired.id;

-- name: GetTenantAuthorityProjection :one
SELECT tenant_id::text AS tenant_id, authority_version, lifecycle, billing_decision,
       billing_model, COALESCE(official_cluster_id, '')::text AS official_cluster_id,
       allow_platform_shared_playback, max_streams, max_viewers, allowances,
       decision_reason, valid_until
FROM foghorn.tenant_authority_projection
WHERE tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: ListTenantAuthorityGrants :many
SELECT tenant_id::text AS tenant_id, cluster_id, authority_version, access_source,
       access_level, subscription_status, cluster_class, deployment_model,
       cluster_type,
       COALESCE(owner_tenant_id::text, '')::text AS owner_tenant_id, expires_at
FROM foghorn.tenant_authority_grants
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
ORDER BY cluster_id;

-- name: GetMediaObjectAuthorityByPlaybackID :one
SELECT *
FROM foghorn.media_object_authority_projection
WHERE lower(playback_id) = lower(sqlc.arg(playback_id));

-- name: GetMediaObjectAuthorityByInternalName :one
SELECT *
FROM foghorn.media_object_authority_projection
WHERE internal_name = sqlc.arg(internal_name);

-- name: GetLocalTenantAuthority :one
SELECT authority.payload, authority.payload_sha256, authority.refresh_after, authority.valid_until,
       projection.authority_version, projection.local_read_ready,
       projection.local_ingest_ready, projection.local_source_ready
FROM foghorn.tenant_authority_projection AS projection
JOIN foghorn.media_authorities AS authority
  ON authority.authority_kind = 'tenant'
 AND authority.authority_id = projection.tenant_id::text
 AND authority.authority_version = projection.authority_version
WHERE projection.tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: GetLocalMediaObjectAuthorityByPlaybackID :one
SELECT authority.payload, authority.payload_sha256, authority.refresh_after, authority.valid_until,
       projection.authority_id, projection.authority_version, projection.local_read_ready
FROM foghorn.media_object_authority_projection AS projection
JOIN foghorn.media_authorities AS authority
  ON authority.authority_kind = 'media_object'
 AND authority.authority_id = projection.authority_id
 AND authority.authority_version = projection.authority_version
WHERE lower(projection.playback_id) = lower(sqlc.arg(playback_id))
ORDER BY (projection.lifecycle = 'active') DESC, projection.authority_version DESC
LIMIT 1;

-- name: GetLocalMediaObjectAuthorityByInternalName :one
SELECT authority.payload, authority.payload_sha256, authority.refresh_after, authority.valid_until,
       projection.authority_id, projection.authority_version, projection.local_read_ready
FROM foghorn.media_object_authority_projection AS projection
JOIN foghorn.media_authorities AS authority
  ON authority.authority_kind = 'media_object'
 AND authority.authority_id = projection.authority_id
 AND authority.authority_version = projection.authority_version
WHERE projection.internal_name = sqlc.arg(internal_name)
ORDER BY (projection.lifecycle = 'active') DESC, projection.authority_version DESC
LIMIT 1;

-- name: GetLocalMediaObjectAuthorityByPublishingCredential :one
SELECT authority.payload, authority.payload_sha256, authority.refresh_after, authority.valid_until,
       projection.authority_id, projection.authority_version,
       projection.local_ingest_ready
FROM foghorn.media_object_authority_projection AS projection
JOIN foghorn.media_authorities AS authority
  ON authority.authority_kind = 'media_object'
 AND authority.authority_id = projection.authority_id
 AND authority.authority_version = projection.authority_version
WHERE projection.publishing_credential_sha256 = sqlc.arg(publishing_credential_sha256)
  AND projection.lifecycle = 'active';

-- name: GetLocalTenantSourceAuthority :one
SELECT authority.payload, authority.payload_sha256, authority.refresh_after, authority.valid_until,
       projection.authority_version, projection.local_source_ready
FROM foghorn.tenant_authority_projection AS projection
JOIN foghorn.media_authorities AS authority
  ON authority.authority_kind = 'tenant'
 AND authority.authority_id = projection.tenant_id::text
 AND authority.authority_version = projection.authority_version
WHERE projection.tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: GetLocalMediaObjectSourceAuthorityByInternalName :one
SELECT authority.payload, authority.payload_sha256, authority.refresh_after, authority.valid_until,
       projection.authority_id, projection.authority_version,
       projection.local_source_ready
FROM foghorn.media_object_authority_projection AS projection
JOIN foghorn.media_authorities AS authority
  ON authority.authority_kind = 'media_object'
 AND authority.authority_id = projection.authority_id
 AND authority.authority_version = projection.authority_version
WHERE projection.internal_name = sqlc.arg(internal_name)
ORDER BY (projection.lifecycle = 'active') DESC, projection.authority_version DESC
LIMIT 1;

-- name: ListLocalManagedStreamAuthorities :many
SELECT authority.authority_id, authority.authority_version, authority.payload, authority.payload_sha256,
       authority.refresh_after, authority.valid_until,
       projection.local_source_ready
FROM foghorn.media_object_authority_projection AS projection
JOIN foghorn.media_authorities AS authority
  ON authority.authority_kind = 'media_object'
 AND authority.authority_id = projection.authority_id
 AND authority.authority_version = projection.authority_version
WHERE projection.object_kind = 'live_stream'
  AND projection.ingest_mode = 'mist_native'
ORDER BY authority.authority_id;

-- name: MarkTenantAuthorityLocalReadReady :execrows
UPDATE foghorn.tenant_authority_projection
SET local_read_ready = TRUE, updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND authority_version = sqlc.arg(authority_version);

-- name: MarkTenantAuthorityLocalIngestReady :execrows
UPDATE foghorn.tenant_authority_projection
SET local_ingest_ready = TRUE, updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND authority_version = sqlc.arg(authority_version);

-- name: MarkMediaObjectAuthorityLocalReadReady :execrows
UPDATE foghorn.media_object_authority_projection
SET local_read_ready = TRUE, updated_at = NOW()
WHERE authority_id = sqlc.arg(authority_id)
  AND authority_version = sqlc.arg(authority_version);

-- name: MarkMediaObjectAuthorityLocalIngestReady :execrows
UPDATE foghorn.media_object_authority_projection
SET local_ingest_ready = TRUE, updated_at = NOW()
WHERE authority_id = sqlc.arg(authority_id)
  AND authority_version = sqlc.arg(authority_version);

-- name: MarkTenantAuthorityLocalSourceReady :execrows
UPDATE foghorn.tenant_authority_projection
SET local_source_ready = TRUE, updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND authority_version = sqlc.arg(authority_version);

-- name: MarkMediaObjectAuthorityLocalSourceReady :execrows
UPDATE foghorn.media_object_authority_projection
SET local_source_ready = TRUE, updated_at = NOW()
WHERE authority_id = sqlc.arg(authority_id)
  AND authority_version = sqlc.arg(authority_version);
