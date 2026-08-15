-- Durable ingest-session identity for DVR lifecycle. An ingest session is one
-- publisher connection, correlated by the node, MistServer connector PID, and
-- the start trigger's UUID/time. Foghorn mints a row on the accepted
-- PUSH_REWRITE (the connector PID arrives in the X-PID header, captured by Helmsman)
-- BEFORE launching async DVR work, ends it on the durable PUSH_INPUT_CLOSE carrying
-- the same PID, and binds each DVR recording to a session id — so a same-node
-- reconnect mints a genuinely new session/recording, and a close
-- finalizes exactly that session's DVR without depending on aggregate STREAM_END.
--
-- Schema source of truth: pkg/database/sql/schema/foghorn.sql — same objects in the
-- baseline so a fresh init and an upgrade converge.
CREATE TABLE IF NOT EXISTS foghorn.ingest_sessions (
    id                     UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id              UUID NOT NULL,
    node_id                VARCHAR(100) NOT NULL,
    stream_internal_name   VARCHAR(255) NOT NULL,
    -- The MistServer connector PID (X-PID) correlates the connector's lifecycle triggers.
    -- PID reuse is fenced by trigger identity and event time. A live PID is positive;
    -- zero/negative fails closed rather than minting an unidentifiable session.
    connector_pid          BIGINT NOT NULL CHECK (connector_pid > 0),
    -- Mist trigger identity (X-Trigger-UUID / X-Trigger-UnixMillis) — the event-time
    -- fence for a delayed close or PID reuse: a close older than the session start, or
    -- a start whose PID matches an already-ended interval, is not applied to this row.
    -- Both are REQUIRED (non-empty / positive): every Mist HTTP trigger carries them, so
    -- an absent value marks a malformed/non-Mist request the identity must reject.
    start_trigger_uuid     VARCHAR(64) NOT NULL CHECK (start_trigger_uuid <> ''),
    started_at_unix_millis BIGINT NOT NULL CHECK (started_at_unix_millis > 0),
    started_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ended_at               TIMESTAMPTZ,
    ended_at_unix_millis   BIGINT,
    ended_reason           VARCHAR(64),
    -- Durable DVR start intent, set synchronously at admission for a record:true stream
    -- BEFORE the push is approved. It is the crash-safe seed: if Foghorn dies before the
    -- async StartDVR inserts the artifact, DVRIntentRecovery replays the start from this
    -- blob (StartDVR inputs: user_id, cluster_id, dvr_policy, processes_json). NULL for a
    -- non-recording session. A record:true mint that cannot persist this denies the push.
    dvr_intent             JSONB,
    -- DVRIntentRecovery bookkeeping. dvr_intent_attempts counts leased replay attempts.
    -- dvr_intent_lease_until leases a row to ONE replica per attempt (HA-safe claim) and
    -- doubles as retry backoff — a claimed row is not re-selected until the lease expires.
    -- dvr_intent_error is the EXPLICIT terminal state (operator-visible): set ONLY for a
    -- permanently-undecodable (structurally invalid) payload. There is NO attempt cap — a transient
    -- StartDVR failure retries under the lease for as long as the session is active, so a recoverable
    -- outage never permanently abandons a recording. A row with a non-NULL error is never re-claimed.
    dvr_intent_attempts    INT NOT NULL DEFAULT 0,
    dvr_intent_lease_until TIMESTAMPTZ,
    dvr_intent_error       TEXT,
    -- Admission remains pending until the source revision wins the shared Redis CAS.
    -- A pending row still owns stream admission and is retired if projection never confirms.
    projection_state       VARCHAR(16) NOT NULL DEFAULT 'pending',
    source_revision        BIGINT,
    projected_at           TIMESTAMPTZ,
    -- Composite-unique target for the artifacts.ingest_generation FK below: lets the FK
    -- enforce that a DVR's (generation, tenant, stream) triple references a REAL session of
    -- the SAME tenant AND the SAME stream, so a malformed internal request cannot bind a
    -- recording to another tenant's OR another stream's session.
    UNIQUE (id, tenant_id, stream_internal_name),
    CONSTRAINT ck_foghorn_ingest_sessions_projection_state
        CHECK (projection_state IN ('pending', 'active')),
    CONSTRAINT ck_foghorn_ingest_sessions_source_revision
        CHECK (source_revision IS NULL OR source_revision > 0)
);

-- At most one ACTIVE session per (tenant, node, connector PID). A new PUSH_REWRITE
-- reusing a PID only mints a row once the prior session is ended. Trigger UUID and
-- event time distinguish publisher generations across PID reuse.
CREATE UNIQUE INDEX IF NOT EXISTS uq_foghorn_ingest_sessions_active_pid
    ON foghorn.ingest_sessions(tenant_id, node_id, connector_pid)
    WHERE ended_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_foghorn_ingest_sessions_stream
    ON foghorn.ingest_sessions(tenant_id, stream_internal_name);

CREATE INDEX IF NOT EXISTS idx_foghorn_ingest_sessions_pending_projection
    ON foghorn.ingest_sessions(started_at)
    WHERE ended_at IS NULL AND projection_state = 'pending';

-- Bind each DVR recording to its ingest session (not a JSON predicate): a same-node
-- reconnect is a new session id → a new recording, and PUSH_INPUT_CLOSE finalizes the
-- recording for exactly the closing session. A dedicated indexed column (not only the
-- dvr_start_dispatch descriptor) is the lookup path for the close-side stop.
ALTER TABLE foghorn.artifacts
    ADD COLUMN IF NOT EXISTS ingest_generation UUID;

CREATE INDEX IF NOT EXISTS idx_foghorn_artifacts_ingest_generation
    ON foghorn.artifacts(ingest_generation) WHERE ingest_generation IS NOT NULL;

-- Enforce the generation binding in schema, not just application code. The composite FK
-- (MATCH SIMPLE: skipped only when a key column is NULL, i.e. an unbound manual DVR)
-- requires a bound recording to reference an existing session of the SAME tenant AND the
-- SAME stream — a nonexistent, cross-tenant, OR cross-stream generation is rejected at
-- write time, so the wrong close can never stop it.
ALTER TABLE foghorn.artifacts
    DROP CONSTRAINT IF EXISTS fk_foghorn_artifacts_ingest_generation;
-- Add the constraint without scanning the artifacts table under the initial lock, then validate it.
-- ingest_generation is introduced nullable, so pre-existing artifacts are unbound.
ALTER TABLE foghorn.artifacts
    ADD CONSTRAINT fk_foghorn_artifacts_ingest_generation
    FOREIGN KEY (ingest_generation, tenant_id, stream_internal_name)
    REFERENCES foghorn.ingest_sessions(id, tenant_id, stream_internal_name) NOT VALID;
ALTER TABLE foghorn.artifacts
    VALIDATE CONSTRAINT fk_foghorn_artifacts_ingest_generation;

-- One ACTIVE DVR per ingest generation. This is the durable backstop behind startDVR's
-- generation-scoped duplicate re-check: even under a lost race it is impossible to
-- register two live recordings for the same publisher session.
CREATE UNIQUE INDEX IF NOT EXISTS uq_foghorn_artifacts_active_dvr_per_generation
    ON foghorn.artifacts(ingest_generation)
    WHERE ingest_generation IS NOT NULL
          AND artifact_type = 'dvr'
          AND status IN ('requested', 'starting', 'recording');
