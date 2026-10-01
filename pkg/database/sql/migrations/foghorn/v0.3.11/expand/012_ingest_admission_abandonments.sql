-- PUSH_REWRITE executions a node answered to Mist without an accept after forwarding them for
-- admission. A mint of a recorded execution is refused and the sessions it minted before are ended,
-- so an admission that commits after Helmsman stopped waiting cannot hold the stream.
CREATE TABLE IF NOT EXISTS foghorn.ingest_admission_abandonments (
    node_id            VARCHAR(100) NOT NULL,
    start_trigger_uuid VARCHAR(64) NOT NULL CHECK (start_trigger_uuid <> ''),
    connector_pid      BIGINT NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (node_id, start_trigger_uuid)
);
CREATE INDEX IF NOT EXISTS idx_foghorn_ingest_admission_abandonments_created
    ON foghorn.ingest_admission_abandonments(created_at);
