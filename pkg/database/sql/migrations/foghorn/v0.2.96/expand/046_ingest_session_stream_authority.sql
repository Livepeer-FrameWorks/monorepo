-- HA-safe ingest admission: make the DATABASE the single authority for "who is publishing this
-- stream", not the process-local StreamRegistry. Foghorn runs multiple replicas against one
-- per-cell database; a node's triggers land on whichever replica it is connected to, so two
-- replicas can otherwise both admit the same stream on different nodes. Admission is now serialized
-- cross-replica by a (tenant, stream) advisory lock in CreateIngestSession; these indexes are the
-- durable backstop that makes a duplicate active publisher impossible even under a lock-hash
-- collision or an application bug.
--
-- Schema source of truth: pkg/database/sql/schema/foghorn.sql — same objects in the baseline.

-- At most one ACTIVE session per (tenant, stream), across ALL nodes. The stream-scoped admission
-- decision inserts under the (tenant, stream) advisory lock, having found no active session for the
-- stream; this partial unique makes a second concurrent insert for the same stream fail closed
-- rather than admit a duplicate publisher. Plain (non-concurrent) create, matching the sibling
-- active-pid index: ingest_sessions is recent and small, and this runs in the expand phase.
CREATE UNIQUE INDEX IF NOT EXISTS uq_foghorn_ingest_sessions_active_per_stream
    ON foghorn.ingest_sessions(tenant_id, stream_internal_name)
    WHERE ended_at IS NULL;

-- A Mist trigger UUID (X-Trigger-UUID) identifies one trigger EXECUTION — stable across that
-- execution's blocking-trigger retries (so a re-fired admission is idempotent), but a distinct value
-- for a later reconnect's PUSH_REWRITE. So (tenant, node, start_trigger_uuid) identifies exactly one
-- session; a second row carrying the same UUID — e.g. the same trigger minting on another stream — is
-- an anomaly rejected at write time (full unique, ended or not: a UUID is never legitimately reused).
CREATE UNIQUE INDEX IF NOT EXISTS uq_foghorn_ingest_sessions_trigger_uuid
    ON foghorn.ingest_sessions(tenant_id, node_id, start_trigger_uuid);
