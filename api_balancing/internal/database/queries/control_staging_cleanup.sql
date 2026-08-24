-- name: EnqueueOwnedStagingCleanup :exec
INSERT INTO foghorn.staging_cleanup_queue (object_key, backend_id)
VALUES (sqlc.arg(object_key), sqlc.arg(backend_id))
ON CONFLICT (object_key) DO UPDATE
SET backend_id = COALESCE(foghorn.staging_cleanup_queue.backend_id, EXCLUDED.backend_id);

-- name: RecordPublicationPair :exec
INSERT INTO foghorn.freeze_publication_ledger
    (object_key, artifact_hash, tenant_id, request_id, guarded, backend_id)
VALUES (sqlc.arg(staging_key), sqlc.arg(artifact_hash), sqlc.arg(tenant_id), sqlc.arg(request_id), false, sqlc.arg(backend_id)),
       (sqlc.arg(candidate_key), sqlc.arg(artifact_hash), sqlc.arg(tenant_id), sqlc.arg(request_id), true, sqlc.arg(backend_id))
ON CONFLICT (object_key) DO UPDATE
SET backend_id = COALESCE(foghorn.freeze_publication_ledger.backend_id, EXCLUDED.backend_id);

-- name: RecordFreezePublicationLedger :exec
INSERT INTO foghorn.freeze_publication_ledger
    (object_key, artifact_hash, tenant_id, request_id, guarded, backend_id)
VALUES (sqlc.arg(staging_key), sqlc.arg(artifact_hash), sqlc.arg(tenant_id), sqlc.arg(request_id), false, sqlc.arg(backend_id)),
       (sqlc.arg(candidate_key), sqlc.arg(artifact_hash), sqlc.arg(tenant_id), sqlc.arg(request_id), true, sqlc.arg(backend_id)),
       (sqlc.arg(dtsh_staging_key), sqlc.arg(artifact_hash), sqlc.arg(tenant_id), sqlc.arg(request_id), false, sqlc.arg(backend_id)),
       (sqlc.arg(dtsh_candidate_key), sqlc.arg(artifact_hash), sqlc.arg(tenant_id), sqlc.arg(request_id), true, sqlc.arg(backend_id))
ON CONFLICT (object_key) DO UPDATE
SET backend_id = COALESCE(foghorn.freeze_publication_ledger.backend_id, EXCLUDED.backend_id);

-- name: DeleteFreezePublicationLedgerKeys :exec
DELETE FROM foghorn.freeze_publication_ledger
WHERE object_key = ANY(sqlc.arg(object_keys)::text[]);
