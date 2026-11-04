-- Existing markers precede node-bound publisher leases. New markers await
-- the global owner's acknowledgement before ordinary retention removes them.
ALTER TABLE foghorn.ingest_admission_abandonments ADD COLUMN IF NOT EXISTS publisher_lease_settled_at TIMESTAMPTZ DEFAULT NOW();
ALTER TABLE foghorn.ingest_admission_abandonments ALTER COLUMN publisher_lease_settled_at DROP DEFAULT;
CREATE INDEX IF NOT EXISTS idx_publisher_abandonments_unsettled ON foghorn.ingest_admission_abandonments(created_at ASC)
    WHERE publisher_lease_settled_at IS NULL;

ALTER TABLE foghorn.ingest_sessions ADD COLUMN IF NOT EXISTS publisher_lease_strict_owner BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE foghorn.ingest_sessions ADD COLUMN IF NOT EXISTS publisher_admission_version SMALLINT NOT NULL DEFAULT 0;
ALTER TABLE foghorn.ingest_sessions ADD COLUMN IF NOT EXISTS publisher_lease_released_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS idx_publisher_lease_release_pending ON foghorn.ingest_sessions(ended_at ASC, tenant_id, id)
    WHERE publisher_admission_version=1 AND ended_at IS NOT NULL AND publisher_lease_released_at IS NULL;

-- These operations keep publisher transaction branching beside the cell's data.
-- Redis projection is deliberately outside both transactions.
CREATE OR REPLACE FUNCTION foghorn.enqueue_publisher_event_v1(p_tenant uuid, p_stream uuid, p_event jsonb)
RETURNS void LANGUAGE plpgsql VOLATILE SET search_path = pg_catalog, foghorn AS $$
BEGIN
    IF p_stream IS NULL THEN RETURN; END IF;
    IF p_event IS NULL OR p_event->>'TenantID' IS DISTINCT FROM p_tenant::text
       OR p_event->>'AggregateID' IS DISTINCT FROM p_stream::text
       OR p_event->>'Source' IS DISTINCT FROM 'foghorn'
       OR p_event->>'Type' NOT IN ('stream.connected', 'stream.idle') THEN
        RAISE EXCEPTION 'publisher event scope mismatch' USING ERRCODE = '22023';
    END IF;
    INSERT INTO foghorn.domain_event_outbox
        (event_id, event_type, source, aggregate_type, aggregate_id, aggregate_version,
         scope, tenant_id, actor_auth_type, actor_user_id, actor_token_hash, occurred_at, payload)
    VALUES ((p_event->>'ID')::uuid, p_event->>'Type', 'foghorn', 'streams', p_stream::text,
            (p_event->>'AggregateVersion')::bigint, 'tenant', p_tenant,
            COALESCE(p_event->'Actor'->>'AuthType', ''), COALESCE(p_event->'Actor'->>'UserID', ''),
            CASE WHEN p_event->'Actor'->>'TokenHash' = '0' THEN '' ELSE COALESCE(p_event->'Actor'->>'TokenHash', '') END,
            (p_event->>'Time')::timestamptz, decode(p_event->>'Data', 'base64'));
END;
$$;

CREATE OR REPLACE FUNCTION foghorn.prepare_publisher_admission_v1(p jsonb)
RETURNS jsonb LANGUAGE plpgsql VOLATILE SET search_path = pg_catalog, foghorn AS $$
DECLARE
    tid uuid := (p->>'tenant_id')::uuid;
    sid uuid := NULLIF(p->>'stream_id', '')::uuid;
    node text := p->>'node_id';
    stream text := p->>'internal_name';
    execution text := p->>'trigger_uuid';
    pid bigint := (p->>'connector_pid')::bigint;
    fired bigint := (p->>'started_at_millis')::bigint;
    cap integer := COALESCE((p->'authority'->>'CapacityMaxStreams')::integer, 0);
    current_row foghorn.ingest_sessions%ROWTYPE;
    incumbent foghorn.ingest_sessions%ROWTYPE;
    generation uuid;
    revision bigint;
    stop_claims jsonb := '[]'::jsonb;
    stale_reason text;
    result_outcome integer := 1;
BEGIN
    IF tid IS NULL OR node IS NULL OR btrim(node) = '' OR stream IS NULL OR btrim(stream) = ''
       OR execution IS NULL OR btrim(execution) = '' OR pid <= 0 OR fired <= 0 OR cap < 0 THEN
        RAISE EXCEPTION 'publisher admission identity missing' USING ERRCODE = '22023';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtext('push-admission:' || octet_length(node) || ':' || node || ':' || octet_length(execution) || ':' || execution)::bigint);
    IF EXISTS (SELECT 1 FROM foghorn.ingest_admission_abandonments a WHERE a.node_id=node AND a.start_trigger_uuid=execution) THEN
        RETURN jsonb_build_object('outcome', 2);
    END IF;
    PERFORM pg_advisory_xact_lock(hashtext('tenant-capacity:' || octet_length(tid::text) || ':' || tid::text)::bigint);
    PERFORM pg_advisory_xact_lock(hashtext(octet_length(tid::text) || ':' || tid::text || ':' || octet_length(stream) || ':' || stream)::bigint);
    SELECT * INTO current_row FROM foghorn.ingest_sessions s
      WHERE s.tenant_id=tid AND s.node_id=node AND s.start_trigger_uuid=execution FOR UPDATE;
    IF FOUND THEN
        IF current_row.stream_internal_name <> stream OR current_row.connector_pid <> pid THEN
            RETURN jsonb_build_object('outcome', 3);
        END IF;
        IF current_row.ended_at IS NOT NULL THEN
            RETURN jsonb_build_object('outcome', 2, 'generation', current_row.id);
        END IF;
        IF current_row.projection_state='pending' AND (
             current_row.media_authority_version IS DISTINCT FROM NULLIF((p->'authority'->>'MediaAuthorityVersion')::bigint,0)
             OR current_row.tenant_authority_version IS DISTINCT FROM NULLIF((p->'authority'->>'TenantAuthorityVersion')::bigint,0)
             OR current_row.processes_json IS DISTINCT FROM COALESCE(p->'authority'->>'ProcessesJSON','')
             OR current_row.dvr_intent IS DISTINCT FROM NULLIF(p->'dvr_intent','null'::jsonb)) THEN
            RETURN jsonb_build_object('outcome',6,'generation',current_row.id);
        END IF;
        generation := current_row.id;
        UPDATE foghorn.ingest_sessions SET publisher_admission_version=1,
          publisher_lease_strict_owner=publisher_lease_strict_owner OR COALESCE((p->>'strict_lease_owner')::boolean,false)
          WHERE tenant_id=tid AND id=generation;
    ELSE
        SELECT * INTO incumbent FROM foghorn.ingest_sessions s
          WHERE s.tenant_id=tid AND s.stream_internal_name=stream AND s.ended_at IS NULL FOR UPDATE;
        IF FOUND THEN
            IF incumbent.node_id <> node THEN
                IF COALESCE(p->>'takeover_generation', '') <> incumbent.id::text THEN
                    RETURN jsonb_build_object('outcome', 5, 'incumbent_generation', incumbent.id, 'incumbent_node', incumbent.node_id);
                END IF;
                stale_reason := 'superseded_by_new_node';
            ELSIF incumbent.connector_pid=pid AND fired > incumbent.started_at_unix_millis THEN
                stale_reason := 'superseded_pid_reuse';
            ELSIF incumbent.connector_pid <> pid AND incumbent.projection_state='pending'
                  AND incumbent.started_at < NOW() - ((p->>'admission_window_ms')::bigint * INTERVAL '1 millisecond') THEN
                stale_reason := 'admission_lapsed';
            ELSE
                RETURN jsonb_build_object('outcome', 3);
            END IF;
            -- A takeover cannot destroy its incumbent unless tombstone/capacity checks admit its successor.
            IF stale_reason <> 'superseded_by_new_node' THEN
                UPDATE foghorn.ingest_sessions SET ended_at=NOW(), ended_reason=stale_reason,
                  ended_at_unix_millis=CASE WHEN stale_reason='superseded_pid_reuse' THEN fired ELSE (EXTRACT(EPOCH FROM NOW())*1000)::bigint END
                  WHERE id=incumbent.id AND tenant_id=tid;
            END IF;
        END IF;
        IF EXISTS (SELECT 1 FROM foghorn.ingest_close_tombstones t WHERE t.tenant_id=tid AND t.node_id=node
                   AND t.connector_pid=pid AND t.stream_internal_name=stream AND t.close_unix_millis >= fired) THEN
            result_outcome := 2;
        ELSIF cap > 0 AND (SELECT count(*) FROM foghorn.ingest_sessions s WHERE s.tenant_id=tid AND s.ended_at IS NULL)
              - (CASE WHEN stale_reason='superseded_by_new_node' THEN 1 ELSE 0 END) >= cap THEN
            result_outcome := 4;
        END IF;
        IF stale_reason='superseded_by_new_node' AND result_outcome <> 1 THEN
            RETURN jsonb_build_object('outcome', result_outcome);
        END IF;
        IF stale_reason IS NOT NULL THEN
            IF stale_reason='superseded_by_new_node' THEN
                UPDATE foghorn.ingest_sessions SET ended_at=NOW(), ended_at_unix_millis=fired, ended_reason=stale_reason
                  WHERE id=incumbent.id AND tenant_id=tid;
            END IF;
            PERFORM foghorn.enqueue_publisher_event_v1(tid, sid, p->'idle_event');
            WITH stopped AS (
                UPDATE foghorn.artifacts SET dvr_start_dispatch=jsonb_set(COALESCE(dvr_start_dispatch,'{}'::jsonb),'{state}','"stop_pending"'::jsonb),
                    status='stopping', updated_at=NOW()
                WHERE tenant_id=tid AND ingest_generation=incumbent.id AND artifact_type='dvr'
                  AND status IN ('requested','starting','recording')
                RETURNING artifact_hash, COALESCE(dvr_start_dispatch->>'node_id','') AS storage_node_id
            ) SELECT COALESCE(jsonb_agg(jsonb_build_object('DVRHash',artifact_hash,'StorageNodeID',storage_node_id)),'[]'::jsonb) INTO stop_claims FROM stopped;
            IF stale_reason <> 'superseded_by_new_node' AND result_outcome <> 1 THEN
                INSERT INTO foghorn.source_projection_revision_counter(tenant_id,stream_internal_name,value)
                  VALUES(tid,stream,4503599627370497)
                  ON CONFLICT(tenant_id,stream_internal_name) DO UPDATE SET value=foghorn.source_projection_revision_counter.value+1
                  RETURNING value INTO revision;
                INSERT INTO foghorn.ingest_offline_effects(tenant_id,stream_internal_name,source_node_id,source_generation,source_revision,
                    set_node_offline,set_node_offline_done,teardown_stream,teardown_done,broadcast_offline,broadcast_offline_done,decklog_done)
                  VALUES(tid,stream,incumbent.node_id,incumbent.id,revision,
                    stale_reason='superseded_pid_reuse',stale_reason<>'superseded_pid_reuse',
                    stale_reason='superseded_pid_reuse',stale_reason<>'superseded_pid_reuse',
                    stale_reason='superseded_pid_reuse',stale_reason<>'superseded_pid_reuse',true);
            END IF;
        END IF;
        IF result_outcome <> 1 THEN
            RETURN jsonb_build_object('outcome',result_outcome,'stop_claims',stop_claims);
        END IF;
        generation := (p->>'generation')::uuid;
        INSERT INTO foghorn.ingest_sessions(id,tenant_id,node_id,stream_internal_name,connector_pid,start_trigger_uuid,
          started_at_unix_millis,dvr_intent,ingest_cluster_id,stream_id,media_authority_id,media_authority_version,
          tenant_authority_version,processes_json,capacity_max_streams,publisher_admission_version,publisher_lease_strict_owner)
        VALUES(generation,tid,node,stream,pid,execution,fired,NULLIF(p->'dvr_intent','null'::jsonb),NULLIF(p->>'ingest_cluster_id',''),sid,
          NULLIF(p->'authority'->>'MediaAuthorityID',''),NULLIF((p->'authority'->>'MediaAuthorityVersion')::bigint,0),
          NULLIF((p->'authority'->>'TenantAuthorityVersion')::bigint,0),COALESCE(p->'authority'->>'ProcessesJSON',''),cap,1,COALESCE((p->>'strict_lease_owner')::boolean,false));
        PERFORM foghorn.enqueue_publisher_event_v1(tid,sid,p->'connected_event');
        SELECT * INTO current_row FROM foghorn.ingest_sessions s WHERE s.id=generation AND s.tenant_id=tid;
    END IF;
    revision := current_row.source_revision;
    IF revision IS NULL THEN
        INSERT INTO foghorn.source_projection_revision_counter(tenant_id,stream_internal_name,value)
          VALUES(tid,stream,4503599627370497)
          ON CONFLICT(tenant_id,stream_internal_name) DO UPDATE SET value=foghorn.source_projection_revision_counter.value+1
          RETURNING value INTO revision;
        UPDATE foghorn.ingest_sessions SET source_revision=revision WHERE id=generation AND tenant_id=tid;
    END IF;
    IF p ? 'decision_expires_at' AND (p->>'decision_expires_at')::timestamptz <= clock_timestamp() THEN
        RAISE EXCEPTION 'publisher placement decision expired' USING ERRCODE='22023';
    END IF;
    RETURN jsonb_build_object('outcome',1,'generation',generation,'source_revision',revision,
      'resumed',current_row.projection_state='active','stop_claims',stop_claims);
END;
$$;

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
    target_revision bigint := COALESCE((p->>'target_revision')::bigint,0);
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
    VALUES(tid,stream,node,generation,revision,COALESCE(p->>'prior_node',''),NULLIF(p->>'prior_generation','')::uuid,targets,target_revision,
      (p->>'broadcast_live')::boolean,decklog,NULLIF(p->>'peer_clusters',''),
      COALESCE(p->>'prior_node','') IN ('',node) OR COALESCE(p->>'prior_generation','')='',
      COALESCE(octet_length(targets),0)=0,NOT (p->>'broadcast_live')::boolean,COALESCE(octet_length(decklog),0)=0,
      CASE WHEN COALESCE(octet_length(targets),0)=0 THEN 'pending' ELSE 'pending_v2' END)
    ON CONFLICT(source_generation) DO NOTHING;
    IF COALESCE(octet_length(targets),0)>0 AND target_revision>0 THEN
        INSERT INTO foghorn.admission_push_target_revisions(tenant_id,stream_internal_name,node_id,source_generation,target_revision,activation_attempt,push_targets)
        VALUES(tid,stream,node,generation,target_revision,(p->>'activation_attempt')::uuid,targets)
        ON CONFLICT(source_generation,target_revision) DO NOTHING;
    END IF;
    IF p ? 'decision_expires_at' AND (p->>'decision_expires_at')::timestamptz <= clock_timestamp() THEN
        RAISE EXCEPTION 'publisher placement decision expired' USING ERRCODE='22023';
    END IF;
    RETURN true;
END;
$$;

CREATE OR REPLACE FUNCTION foghorn.settle_publisher_admission_v1(p jsonb)
RETURNS boolean LANGUAGE plpgsql VOLATILE SET search_path = pg_catalog, foghorn AS $$
DECLARE
    tid uuid := (p->>'tenant_id')::uuid;
    generation uuid := (p->>'generation')::uuid;
    stream text := p->>'internal_name';
    node text := p->>'node_id';
    execution text := p->>'trigger_uuid';
    reason text := p->>'reason';
    current_row foghorn.ingest_sessions%ROWTYPE;
    revision bigint;
    active boolean;
BEGIN
    IF tid IS NULL OR generation IS NULL OR node IS NULL OR execution IS NULL OR stream IS NULL
       OR reason NOT IN ('admission_abandoned','admission_lapsed') THEN
        RAISE EXCEPTION 'publisher settlement identity or reason missing' USING ERRCODE='22023';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtext('push-admission:' || octet_length(node) || ':' || node || ':' || octet_length(execution) || ':' || execution)::bigint);
    PERFORM pg_advisory_xact_lock(hashtext(octet_length(tid::text) || ':' || tid::text || ':' || octet_length(stream) || ':' || stream)::bigint);
    SELECT * INTO current_row FROM foghorn.ingest_sessions s
      WHERE s.tenant_id=tid AND s.id=generation AND s.node_id=node
        AND s.start_trigger_uuid=execution AND s.stream_internal_name=stream AND s.ended_at IS NULL FOR UPDATE;
    IF NOT FOUND THEN RETURN false; END IF;
    IF reason='admission_lapsed' AND (current_row.projection_state<>'pending'
       OR current_row.started_at >= NOW() - ((p->>'admission_window_ms')::bigint * INTERVAL '1 millisecond')) THEN RETURN false; END IF;
    IF reason='admission_abandoned' AND NOT EXISTS(SELECT 1 FROM foghorn.ingest_admission_abandonments a WHERE a.node_id=node AND a.start_trigger_uuid=execution) THEN RETURN false; END IF;
    active := current_row.projection_state='active';
    UPDATE foghorn.ingest_sessions SET ended_at=NOW(), ended_at_unix_millis=(EXTRACT(EPOCH FROM NOW())*1000)::bigint, ended_reason=reason
      WHERE tenant_id=tid AND id=generation;
    PERFORM foghorn.enqueue_publisher_event_v1(tid,current_row.stream_id,p->'idle_event');
    UPDATE foghorn.artifacts SET dvr_start_dispatch=jsonb_set(COALESCE(dvr_start_dispatch,'{}'::jsonb),'{state}','"stop_pending"'::jsonb), status='stopping', updated_at=NOW()
      WHERE tenant_id=tid AND ingest_generation=generation AND artifact_type='dvr' AND status IN ('requested','starting','recording');
    INSERT INTO foghorn.source_projection_revision_counter(tenant_id,stream_internal_name,value)
      VALUES(tid,stream,4503599627370497)
      ON CONFLICT(tenant_id,stream_internal_name) DO UPDATE SET value=foghorn.source_projection_revision_counter.value+1
      RETURNING value INTO revision;
    INSERT INTO foghorn.ingest_offline_effects(tenant_id,stream_internal_name,source_node_id,source_generation,source_revision,
      set_node_offline,set_node_offline_done,teardown_stream,teardown_done,broadcast_offline,broadcast_offline_done,decklog_done)
      VALUES(tid,stream,node,generation,revision,active,NOT active,active,NOT active,active,NOT active,true);
    RETURN true;
END;
$$;
