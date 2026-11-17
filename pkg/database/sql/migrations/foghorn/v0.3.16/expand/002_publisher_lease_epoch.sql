ALTER TABLE foghorn.ingest_sessions ADD COLUMN IF NOT EXISTS publisher_lease_epoch BIGINT NOT NULL DEFAULT 0;
ALTER TABLE foghorn.ingest_sessions DROP CONSTRAINT IF EXISTS chk_ingest_publisher_lease_epoch;
ALTER TABLE foghorn.ingest_sessions ADD CONSTRAINT chk_ingest_publisher_lease_epoch CHECK (publisher_lease_epoch >= 0) NOT VALID;

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
    current_row foghorn.ingest_sessions%ROWTYPE;
    incumbent foghorn.ingest_sessions%ROWTYPE;
    generation uuid;
    revision bigint;
    stop_claims jsonb := '[]'::jsonb;
    stale_reason text;
    result_outcome integer := 1;
BEGIN
    IF tid IS NULL OR node IS NULL OR btrim(node) = '' OR stream IS NULL OR btrim(stream) = ''
       OR execution IS NULL OR btrim(execution) = '' OR pid <= 0 OR fired <= 0 OR cap < 0 OR lease_epoch < 0 THEN
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
          tenant_authority_version,processes_json,capacity_max_streams,publisher_admission_version,publisher_lease_strict_owner,publisher_lease_epoch)
        VALUES(generation,tid,node,stream,pid,execution,fired,NULLIF(p->'dvr_intent','null'::jsonb),NULLIF(p->>'ingest_cluster_id',''),sid,
          NULLIF(p->'authority'->>'MediaAuthorityID',''),NULLIF((p->'authority'->>'MediaAuthorityVersion')::bigint,0),
          NULLIF((p->'authority'->>'TenantAuthorityVersion')::bigint,0),COALESCE(p->'authority'->>'ProcessesJSON',''),cap,1,COALESCE((p->>'strict_lease_owner')::boolean,false),lease_epoch);
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
      'publisher_lease_epoch',current_row.publisher_lease_epoch,'resumed',current_row.projection_state='active','stop_claims',stop_claims);
END;
$$;

