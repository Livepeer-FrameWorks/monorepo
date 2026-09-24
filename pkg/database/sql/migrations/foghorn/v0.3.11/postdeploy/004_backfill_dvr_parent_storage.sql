-- A recording's parent row reflects where its segments live. Recordings with an
-- uploaded segment move from pending to this cell's S3: running ones to
-- 'in_progress', finished ones to 'synced' with the recording prefix as s3_url,
-- matching what segment upload and finalization now record. Segments upload
-- through the cell's immutable store, so its recorded identity supplies the
-- backend and the bucket/prefix of the URL. A cell without a store records none.
UPDATE foghorn.artifacts AS a
SET storage_location = 's3',
    sync_status = CASE WHEN a.status IN ('completed', 'completed_partial') THEN 'synced' ELSE 'in_progress' END,
    durable_backend_local = true,
    backend_id = COALESCE(a.backend_id, ident.backend_id),
    s3_url = CASE
        WHEN a.status IN ('completed', 'completed_partial') AND COALESCE(a.s3_url, '') = ''
            THEN 's3://' || ident.bucket || '/'
                 || CASE WHEN ident.prefix = '' THEN '' ELSE rtrim(ident.prefix, '/') || '/' END
                 || 'dvr/' || a.tenant_id::text || '/' || COALESCE(a.stream_internal_name, '') || '/' || a.artifact_hash
        ELSE a.s3_url END,
    updated_at = NOW()
FROM foghorn.cell_storage_identity AS ident
WHERE a.artifact_type = 'dvr'
  AND COALESCE(a.sync_status, 'pending') = 'pending'
  AND a.tenant_id IS NOT NULL
  AND a.status IN ('requested', 'starting', 'recording', 'stopping', 'finalizing', 'completed', 'completed_partial')
  AND EXISTS (
      SELECT 1 FROM foghorn.dvr_segments AS s
      WHERE s.artifact_hash = a.artifact_hash AND s.status IN ('uploaded', 'deleted_local')
  );
