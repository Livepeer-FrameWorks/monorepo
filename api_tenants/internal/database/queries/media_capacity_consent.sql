-- name: GetOwnedMediaCapacityConsent :one
SELECT id::text AS cluster_record_id, media_consent_revision AS revision, media_allow_ingest AS allow_ingest,
       media_allow_serve AS allow_serve, media_allow_external_source AS allow_external_source
FROM quartermaster.infrastructure_clusters
WHERE owner_tenant_id = sqlc.arg(tenant_id)::uuid AND cluster_id = sqlc.arg(cluster_id);

-- name: LockOwnedMediaCapacityConsent :one
SELECT id::text AS cluster_record_id, media_consent_revision AS revision, media_allow_ingest AS allow_ingest,
       media_allow_serve AS allow_serve, media_allow_external_source AS allow_external_source
FROM quartermaster.infrastructure_clusters
WHERE owner_tenant_id = sqlc.arg(tenant_id)::uuid AND cluster_id = sqlc.arg(cluster_id)
FOR UPDATE;

-- name: UpdateOwnedMediaCapacityConsent :execrows
UPDATE quartermaster.infrastructure_clusters
SET media_consent_revision = media_consent_revision + 1,
    media_allow_ingest = sqlc.arg(allow_ingest), media_allow_serve = sqlc.arg(allow_serve),
    media_allow_external_source = sqlc.arg(allow_external_source), updated_at = NOW()
WHERE owner_tenant_id = sqlc.arg(tenant_id)::uuid AND cluster_id = sqlc.arg(cluster_id)
  AND media_consent_revision = sqlc.arg(expected_revision)::bigint;

-- name: GetOwnedMediaCapacityConsentChange :one
SELECT change.* FROM quartermaster.media_capacity_consent_changes AS change
JOIN quartermaster.infrastructure_clusters AS cluster ON cluster.id = change.cluster_record_id AND cluster.cluster_id = change.cluster_id
WHERE change.tenant_id = sqlc.arg(tenant_id)::uuid
  AND cluster.owner_tenant_id = sqlc.arg(tenant_id)::uuid
  AND change.cluster_id = sqlc.arg(cluster_id)
  AND change.idempotency_key = sqlc.arg(idempotency_key);

-- name: InsertMediaCapacityConsentChange :one
INSERT INTO quartermaster.media_capacity_consent_changes (
    tenant_id, cluster_record_id, cluster_id, idempotency_key, request_sha256, revision, consent_digest, review_digest,
    previous_allow_ingest, previous_allow_serve, previous_allow_external_source,
    allow_ingest, allow_serve, allow_external_source, actor_id
) VALUES (
    sqlc.arg(tenant_id)::uuid, sqlc.arg(cluster_record_id)::uuid, sqlc.arg(cluster_id), sqlc.arg(idempotency_key), sqlc.arg(request_sha256),
    sqlc.arg(revision), sqlc.arg(consent_digest), sqlc.arg(review_digest),
    sqlc.arg(previous_allow_ingest), sqlc.arg(previous_allow_serve), sqlc.arg(previous_allow_external_source),
    sqlc.arg(allow_ingest), sqlc.arg(allow_serve), sqlc.arg(allow_external_source), sqlc.arg(actor_id)
) RETURNING *;
