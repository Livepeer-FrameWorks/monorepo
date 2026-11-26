-- name: ListTenantMediaOwnerCells :many
-- Business creation intents precede physical media. Include historical owners
-- and tombstones so a routing change or delayed delete cannot hide paid storage.
SELECT DISTINCT COALESCE(origin_cluster_id,'')::text AS cell_id FROM (
 SELECT origin_cluster_id FROM commodore.vod_assets WHERE tenant_id=sqlc.arg(tenant_id)::uuid
 UNION ALL SELECT origin_cluster_id FROM commodore.clips WHERE tenant_id=sqlc.arg(tenant_id)::uuid
 UNION ALL SELECT origin_cluster_id FROM commodore.dvr_recordings WHERE tenant_id=sqlc.arg(tenant_id)::uuid
 UNION ALL SELECT origin_cluster_id FROM commodore.artifact_catalog_tombstones WHERE tenant_id=sqlc.arg(tenant_id)::uuid
 UNION ALL SELECT origin_cluster_id FROM commodore.artifact_creation_intents WHERE tenant_id=sqlc.arg(tenant_id)::uuid AND status='pending'
) owners ORDER BY cell_id;

-- name: HasPendingTenantMediaCreation :one
SELECT EXISTS(SELECT 1 FROM commodore.artifact_creation_intents
 WHERE tenant_id=sqlc.arg(tenant_id)::uuid AND status='pending')::boolean AS pending;
