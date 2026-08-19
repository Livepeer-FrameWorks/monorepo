-- Durable, leased application of source-offline state and its stream-wide effects.

CREATE TABLE IF NOT EXISTS foghorn.ingest_offline_effects (
    id                  BIGSERIAL PRIMARY KEY,
    tenant_id           UUID NOT NULL,
    stream_internal_name VARCHAR(255) NOT NULL,
    source_node_id      VARCHAR(100) NOT NULL,
    source_generation   UUID,
    source_revision     BIGINT NOT NULL CHECK (source_revision > 0),
    set_node_offline    BOOLEAN NOT NULL DEFAULT FALSE,
    teardown_stream     BOOLEAN NOT NULL DEFAULT FALSE,
    broadcast_offline   BOOLEAN NOT NULL DEFAULT FALSE,
    decklog_trigger     BYTEA,
    state               VARCHAR(16) NOT NULL DEFAULT 'pending'
                            CHECK (state IN ('pending', 'applied', 'superseded')),
    attempts            INTEGER NOT NULL DEFAULT 0,
    next_attempt_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    leased_until        TIMESTAMPTZ,
    lease_token         UUID,
    -- Durable authority routing: the instance that OWNS this row's outstanding authority-bound
    -- work (the publishing node's connection owner for activation, the federation leader for the
    -- broadcast), recorded when a non-authoritative claimant hands the row back. The claim query
    -- admits only that instance until the affinity goes stale (10s since it was set), so wrong
    -- replicas cannot win the SKIP LOCKED race away from the authority, while a dead/moved
    -- authority never strands the row. Cleared on every claim.
    claim_affinity      VARCHAR(100),
    last_error          TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    applied_at          TIMESTAMPTZ,
    UNIQUE (tenant_id, stream_internal_name, source_revision)
);

CREATE INDEX IF NOT EXISTS idx_foghorn_ingest_offline_effects_pending
    ON foghorn.ingest_offline_effects(next_attempt_at, id)
    WHERE state = 'pending';

CREATE INDEX IF NOT EXISTS idx_foghorn_ingest_offline_effects_terminal
    ON foghorn.ingest_offline_effects(updated_at)
    WHERE state IN ('applied', 'superseded');
