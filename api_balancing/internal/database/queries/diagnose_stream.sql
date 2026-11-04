-- name: GetStreamRuntimeDiagnosis :one
SELECT jsonb_build_object(
 'tenant_id', sqlc.arg(tenant_id)::text, 'stream_id', sqlc.arg(stream_id)::text,
 'session_count', (SELECT count(*) FROM foghorn.ingest_sessions i WHERE i.tenant_id = sqlc.arg(tenant_id)::uuid AND i.stream_internal_name = sqlc.arg(internal_name)::text),
 'sessions', COALESCE((SELECT jsonb_agg(page.item ORDER BY page.started_at DESC, page.id DESC) FROM (
   SELECT i.id, i.started_at, jsonb_build_object('id', i.id, 'node_id', i.node_id,
     'cluster_id', COALESCE(i.ingest_cluster_id, ''), 'claim_id', i.start_trigger_uuid,
     'projection_state', i.projection_state, 'started_at', i.started_at::text,
     'ended_at', COALESCE(i.ended_at::text, ''), 'ended_reason', COALESCE(i.ended_reason, '')) AS item
   FROM foghorn.ingest_sessions i
   WHERE i.tenant_id = sqlc.arg(tenant_id)::uuid AND i.stream_internal_name = sqlc.arg(internal_name)::text
   ORDER BY i.started_at DESC, i.id DESC LIMIT 20) page), '[]'::jsonb),
 'authorities', COALESCE((SELECT jsonb_agg(jsonb_build_object(
   'kind', a.authority_kind, 'authority_id', a.authority_id, 'version', a.authority_version,
   'issued_at', a.issued_at::text, 'refresh_after', a.refresh_after::text, 'valid_until', a.valid_until::text,
   'lifecycle', COALESCE(tenant.lifecycle, object.lifecycle, ''),
   'billing_decision', COALESCE(tenant.billing_decision, ''),
   'decision_reason', COALESCE(tenant.decision_reason, ''))
   ORDER BY a.authority_kind)
   FROM foghorn.media_authorities a
   LEFT JOIN foghorn.tenant_authority_projection tenant
     ON a.authority_kind = 'tenant' AND tenant.tenant_id::text = a.authority_id
    AND tenant.tenant_id = sqlc.arg(tenant_id)::uuid
   LEFT JOIN foghorn.media_object_authority_projection object
     ON a.authority_kind = 'media_object' AND object.authority_id = a.authority_id
    AND object.tenant_id = sqlc.arg(tenant_id)::uuid
   WHERE (a.authority_kind = 'tenant' AND a.authority_id = sqlc.arg(tenant_id)::text)
      OR (a.authority_kind = 'media_object' AND a.authority_id = 'live_stream:' || sqlc.arg(stream_id)::text
        AND EXISTS (SELECT 1 FROM foghorn.media_object_authority_projection p WHERE p.authority_id = a.authority_id AND p.tenant_id = sqlc.arg(tenant_id)::uuid))), '[]'::jsonb)
)::jsonb AS diagnosis;
