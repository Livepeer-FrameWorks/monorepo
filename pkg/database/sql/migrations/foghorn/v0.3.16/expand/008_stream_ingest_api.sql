-- Session event identities and durable buffer-unload deduplication.
ALTER TABLE foghorn.ingest_sessions ADD COLUMN IF NOT EXISTS buffer_ended_at TIMESTAMPTZ;
ALTER TABLE foghorn.pull_ingest_sessions
    ADD COLUMN IF NOT EXISTS buffer_ended_at TIMESTAMPTZ;

CREATE OR REPLACE FUNCTION foghorn.admit_pull_ingest_v1(
    p_tenant_id UUID, p_stream_id UUID, p_internal_name TEXT, p_node_id TEXT,
    p_cluster_id TEXT, p_max_streams INTEGER)
RETURNS JSONB LANGUAGE plpgsql VOLATILE SET search_path=pg_catalog,foghorn AS $$
DECLARE
    existing UUID;
    occupied BIGINT;
BEGIN
    IF p_tenant_id IS NULL OR p_stream_id IS NULL OR btrim(COALESCE(p_internal_name,''))=''
       OR btrim(COALESCE(p_node_id,''))='' OR btrim(COALESCE(p_cluster_id,''))=''
       OR p_max_streams IS NULL OR p_max_streams < 0 THEN
        RAISE EXCEPTION 'pull admission scope missing' USING ERRCODE='22023';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtext('tenant-capacity:'||octet_length(p_tenant_id::text)||':'||p_tenant_id::text)::bigint);
    PERFORM pg_advisory_xact_lock(hashtext(octet_length(p_tenant_id::text)||':'||p_tenant_id::text||':'||octet_length(p_internal_name)||':'||p_internal_name)::bigint);
    SELECT s.id INTO existing FROM foghorn.pull_ingest_sessions s
      WHERE s.tenant_id=p_tenant_id AND s.stream_internal_name=p_internal_name
        AND s.node_id=p_node_id AND s.ended_at IS NULL;
    IF FOUND THEN
        RETURN jsonb_build_object('admitted',true,'session_id',existing);
    END IF;
    IF p_max_streams > 0 AND NOT EXISTS (
        SELECT 1 FROM foghorn.pull_ingest_sessions s WHERE s.tenant_id=p_tenant_id
          AND s.stream_internal_name=p_internal_name AND s.ended_at IS NULL) THEN
        SELECT (SELECT count(*) FROM foghorn.ingest_sessions s WHERE s.tenant_id=p_tenant_id AND s.ended_at IS NULL)
             + (SELECT count(DISTINCT s.stream_internal_name) FROM foghorn.pull_ingest_sessions s
                 WHERE s.tenant_id=p_tenant_id AND s.ended_at IS NULL) INTO occupied;
        IF occupied >= p_max_streams THEN RETURN jsonb_build_object('admitted',false); END IF;
    END IF;
    INSERT INTO foghorn.pull_ingest_sessions(tenant_id,stream_id,stream_internal_name,node_id,ingest_cluster_id,cell_session_id)
      VALUES(p_tenant_id,p_stream_id,p_internal_name,p_node_id,p_cluster_id,
        COALESCE((SELECT s.cell_session_id FROM foghorn.pull_ingest_sessions s
          WHERE s.tenant_id=p_tenant_id AND s.stream_internal_name=p_internal_name AND s.ended_at IS NULL
          ORDER BY s.started_at,s.id LIMIT 1),gen_random_uuid())) RETURNING id INTO existing;
    RETURN jsonb_build_object('admitted',true,'session_id',existing);
END;
$$;

CREATE INDEX IF NOT EXISTS idx_foghorn_ingest_sessions_stream_history
    ON foghorn.ingest_sessions(tenant_id,stream_id,started_at DESC,id) WHERE stream_id IS NOT NULL;
