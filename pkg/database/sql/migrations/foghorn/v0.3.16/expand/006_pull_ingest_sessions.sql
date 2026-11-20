-- Each direct upstream buffer owns one row; relay destinations do not admit pull ingest.
CREATE TABLE IF NOT EXISTS foghorn.pull_ingest_sessions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    cell_session_id UUID NOT NULL DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    stream_id UUID NOT NULL,
    stream_internal_name VARCHAR(255) NOT NULL,
    node_id VARCHAR(100) NOT NULL,
    ingest_cluster_id VARCHAR(100) NOT NULL,
    admission_state VARCHAR(16) NOT NULL DEFAULT 'pending',
    buffer_pid BIGINT,
    start_event_id TEXT,
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at_unix_millis BIGINT NOT NULL DEFAULT 0,
    playable_at TIMESTAMPTZ,
    playable_at_unix_millis BIGINT,
    ended_at TIMESTAMPTZ,
    ended_at_unix_millis BIGINT,
    ended_reason VARCHAR(100),
    CONSTRAINT ck_foghorn_pull_ingest_admission_state CHECK (admission_state IN ('pending','active')),
    CONSTRAINT ck_foghorn_pull_ingest_buffer CHECK (buffer_pid IS NULL OR buffer_pid > 0),
    CONSTRAINT ck_foghorn_pull_ingest_active_buffer CHECK (admission_state <> 'active' OR
        (buffer_pid IS NOT NULL AND start_event_id IS NOT NULL AND playable_at IS NOT NULL AND playable_at_unix_millis > 0))
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_foghorn_pull_ingest_open_buffer
    ON foghorn.pull_ingest_sessions(tenant_id,node_id,buffer_pid)
    WHERE ended_at IS NULL AND buffer_pid IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_foghorn_pull_ingest_open_node_stream
    ON foghorn.pull_ingest_sessions(tenant_id,stream_internal_name,node_id)
    WHERE ended_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_foghorn_pull_ingest_stream_history
    ON foghorn.pull_ingest_sessions(tenant_id,stream_id,started_at DESC,id);
CREATE INDEX IF NOT EXISTS idx_foghorn_pull_ingest_open_stream
    ON foghorn.pull_ingest_sessions(tenant_id,stream_internal_name)
    WHERE ended_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_foghorn_pull_ingest_node_buffer
    ON foghorn.pull_ingest_sessions(tenant_id,node_id,buffer_pid);
CREATE INDEX IF NOT EXISTS idx_foghorn_pull_ingest_node_open
    ON foghorn.pull_ingest_sessions(node_id,started_at ASC)
    WHERE ended_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_foghorn_pull_ingest_node_end
    ON foghorn.pull_ingest_sessions(tenant_id,stream_internal_name,node_id,ended_at_unix_millis DESC)
    WHERE ended_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_foghorn_pull_ingest_pending_range
    ON foghorn.pull_ingest_sessions(started_at ASC,id)
    WHERE ended_at IS NULL AND admission_state='pending';
CREATE INDEX IF NOT EXISTS idx_foghorn_pull_ingest_ended_range
    ON foghorn.pull_ingest_sessions(ended_at ASC,id)
    WHERE ended_at IS NOT NULL;

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
    INSERT INTO foghorn.pull_ingest_sessions(tenant_id,stream_id,stream_internal_name,node_id,ingest_cluster_id)
      VALUES(p_tenant_id,p_stream_id,p_internal_name,p_node_id,p_cluster_id) RETURNING id INTO existing;
    RETURN jsonb_build_object('admitted',true,'session_id',existing);
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
    lease_epoch bigint := COALESCE((p->>'publisher_lease_epoch')::bigint, 0);
    layout_version smallint := COALESCE((p->>'runtime_layout_version')::smallint,0);
    bound_runtime text := COALESCE(p->>'runtime_name','');
    current_row foghorn.ingest_sessions%ROWTYPE;
    incumbent foghorn.ingest_sessions%ROWTYPE;
    generation uuid;
    revision bigint;
    stop_claims jsonb := '[]'::jsonb;
    stale_reason text;
    result_outcome integer := 1;
BEGIN
    IF tid IS NULL OR node IS NULL OR btrim(node) = '' OR stream IS NULL OR btrim(stream) = ''
       OR execution IS NULL OR btrim(execution) = '' OR pid <= 0 OR fired <= 0 OR cap < 0 OR lease_epoch < 0 OR layout_version NOT IN (0,1) THEN
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
        IF current_row.runtime_layout_version=1 AND layout_version<>1 THEN
            RETURN jsonb_build_object('outcome',6,'generation',current_row.id);
        END IF;
        IF current_row.publisher_lease_epoch > 0 AND current_row.publisher_lease_epoch <> lease_epoch THEN
            RETURN jsonb_build_object('outcome',6,'generation',current_row.id);
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
        ELSIF cap > 0 AND ((SELECT count(*) FROM foghorn.ingest_sessions s WHERE s.tenant_id=tid AND s.ended_at IS NULL)
            + (SELECT count(DISTINCT s.stream_internal_name) FROM foghorn.pull_ingest_sessions s WHERE s.tenant_id=tid AND s.ended_at IS NULL))
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
        IF layout_version=1 AND (lease_epoch<=0 OR NOT COALESCE((p->>'strict_lease_owner')::boolean,false)
          OR bound_runtime<>'live+'||stream||'_fw1p_'||replace(generation::text,'-','') OR length(bound_runtime)>100) THEN
            RAISE EXCEPTION 'invalid isolated publisher runtime' USING ERRCODE = '22023';
        END IF;
        INSERT INTO foghorn.ingest_sessions(id,tenant_id,node_id,stream_internal_name,connector_pid,start_trigger_uuid,
          started_at_unix_millis,dvr_intent,ingest_cluster_id,stream_id,media_authority_id,media_authority_version,
          tenant_authority_version,processes_json,capacity_max_streams,publisher_admission_version,publisher_lease_strict_owner,publisher_lease_epoch,runtime_layout_version,runtime_name)
        VALUES(generation,tid,node,stream,pid,execution,fired,NULLIF(p->'dvr_intent','null'::jsonb),NULLIF(p->>'ingest_cluster_id',''),sid,
          NULLIF(p->'authority'->>'MediaAuthorityID',''),NULLIF((p->'authority'->>'MediaAuthorityVersion')::bigint,0),
          NULLIF((p->'authority'->>'TenantAuthorityVersion')::bigint,0),COALESCE(p->'authority'->>'ProcessesJSON',''),cap,1,COALESCE((p->>'strict_lease_owner')::boolean,false),lease_epoch,layout_version,bound_runtime);
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
      'publisher_lease_epoch',current_row.publisher_lease_epoch,
      'runtime_layout_version',current_row.runtime_layout_version,'runtime_name',COALESCE(NULLIF(current_row.runtime_name,''),'live+'||stream),'resumed',current_row.projection_state='active','stop_claims',stop_claims);
END;
$$;
