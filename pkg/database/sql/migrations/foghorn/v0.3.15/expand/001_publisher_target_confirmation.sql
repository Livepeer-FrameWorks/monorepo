CREATE OR REPLACE FUNCTION foghorn.confirm_publisher_admission_v1(p jsonb)
RETURNS boolean LANGUAGE plpgsql VOLATILE SET search_path = pg_catalog, foghorn AS $$
DECLARE
    tid uuid := (p->>'tenant_id')::uuid;
    generation uuid := (p->>'generation')::uuid;
    stream text := p->>'internal_name';
    node text := p->>'node_id';
    execution text := p->>'trigger_uuid';
    revision bigint := (p->>'source_revision')::bigint;
    targets bytea := decode(p->>'push_targets','base64');
    decklog bytea := decode(p->>'decklog_trigger','base64');
    v_target_revision bigint := COALESCE((p->>'target_revision')::bigint,0);
BEGIN
    PERFORM pg_advisory_xact_lock(hashtext('push-admission:' || octet_length(node) || ':' || node || ':' || octet_length(execution) || ':' || execution)::bigint);
    PERFORM pg_advisory_xact_lock(hashtext(octet_length(tid::text) || ':' || tid::text || ':' || octet_length(stream) || ':' || stream)::bigint);
    IF EXISTS(SELECT 1 FROM foghorn.ingest_admission_abandonments a WHERE a.node_id=node AND a.start_trigger_uuid=execution) THEN RETURN false; END IF;
    UPDATE foghorn.ingest_sessions SET projection_state='active',projected_at=COALESCE(projected_at,NOW())
      WHERE id=generation AND tenant_id=tid AND stream_internal_name=stream AND node_id=node
        AND start_trigger_uuid=execution AND ended_at IS NULL AND source_revision=revision;
    IF NOT FOUND THEN RETURN false; END IF;
    IF p ? 'decision_expires_at' AND (p->>'decision_expires_at')::timestamptz <= clock_timestamp() THEN
        RAISE EXCEPTION 'publisher placement decision expired' USING ERRCODE='22023';
    END IF;
    IF COALESCE((p->>'resumed')::boolean,false) THEN RETURN true; END IF;
    INSERT INTO foghorn.ingest_admission_effects(tenant_id,stream_internal_name,node_id,source_generation,source_revision,
      prior_owner_node_id,prior_owner_source_generation,push_targets,target_revision,broadcast_live,decklog_trigger,peer_clusters,
      drain_done,activation_done,broadcast_done,decklog_done,state)
    VALUES(tid,stream,node,generation,revision,COALESCE(p->>'prior_node',''),NULLIF(p->>'prior_generation','')::uuid,targets,v_target_revision,
      (p->>'broadcast_live')::boolean,decklog,NULLIF(p->>'peer_clusters',''),
      COALESCE(p->>'prior_node','') IN ('',node) OR COALESCE(p->>'prior_generation','')='',
      COALESCE(octet_length(targets),0)=0,NOT (p->>'broadcast_live')::boolean,COALESCE(octet_length(decklog),0)=0,
      CASE WHEN COALESCE(octet_length(targets),0)=0 THEN 'pending' ELSE 'pending_v2' END)
    ON CONFLICT(source_generation) DO NOTHING;
    IF COALESCE(octet_length(targets),0)>0 AND v_target_revision>0 THEN
        INSERT INTO foghorn.admission_push_target_revisions(tenant_id,stream_internal_name,node_id,source_generation,target_revision,activation_attempt,push_targets)
        VALUES(tid,stream,node,generation,v_target_revision,(p->>'activation_attempt')::uuid,targets)
        ON CONFLICT ON CONSTRAINT uq_foghorn_admission_push_target_revision DO NOTHING;
    END IF;
    IF p ? 'decision_expires_at' AND (p->>'decision_expires_at')::timestamptz <= clock_timestamp() THEN
        RAISE EXCEPTION 'publisher placement decision expired' USING ERRCODE='22023';
    END IF;
    RETURN true;
END;
$$;
