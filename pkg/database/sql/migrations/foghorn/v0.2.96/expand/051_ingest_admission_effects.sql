-- Durable, leased application of the once-only ADMISSION effects (the mirror of the offline
-- teardown outbox in 050). projection_state='active' proves only that the source projection crossed
-- the shared CAS and was confirmed — NOT that the admission effects (push-target activation,
-- prior-owner drain, federation live broadcast) ran: they are external, asynchronous, and can be
-- lost to a crash after the confirmation. The obligation row is inserted in the SAME transaction
-- that confirms the projection, so exactly one obligation exists per admitted generation and a
-- worker proves and settles each obligation under the stream advisory lock, with bounded external
-- I/O between those locked transactions. The admission's Decklog ingest event is a ledgered leg too
-- (decklog_trigger), stamped with the deterministic event_id (the generation) so worker
-- re-deliveries deduplicate downstream.

CREATE TABLE IF NOT EXISTS foghorn.ingest_admission_effects (
    id                  BIGSERIAL PRIMARY KEY,
    tenant_id           UUID NOT NULL,
    stream_internal_name VARCHAR(255) NOT NULL,
    node_id             VARCHAR(100) NOT NULL,
    source_generation   UUID NOT NULL UNIQUE,
    source_revision     BIGINT NOT NULL CHECK (source_revision > 0),
    prior_owner_node_id VARCHAR(100) NOT NULL DEFAULT '',
    -- Exact generation occupying the prior node when the projection CAS replaced it. Helmsman
    -- compares this with its locally admitted generation before invoking name-scoped nuke_stream.
    prior_owner_source_generation UUID,
    -- Serialized ipcpb.ActivatePushTargets (stream name + target specs); NULL when the stream has
    -- no configured push targets.
    push_targets        BYTEA,
    -- JSON array of complete federation peer hints (cluster id, internal Foghorn address, and
    -- lifecycle) resolved by admission. The leader establishes stream tracking from this durable
    -- payload before the broadcast leg can settle.
    peer_clusters       TEXT,
    broadcast_live      BOOLEAN NOT NULL DEFAULT FALSE,
    -- Serialized, enriched ipcpb.MistTrigger for the admission's Decklog ingest event, stamped with
    -- the deterministic event_id (the generation). NULL when the event needs no ledgered delivery.
    decklog_trigger     BYTEA,
    -- Per-leg completion. The legs have DIFFERENT supersession rules: the Decklog event remains
    -- OWED after the admitted generation ends (the admission still happened), while the drain,
    -- activation and live broadcast become moot — a late drain destroys by runtime NAME and could
    -- kill a successor session's buffer. Remote legs (drain, activation) are completed by
    -- Helmsman's generation-correlated acknowledgements (DrainStreamResponse /
    -- ActivatePushTargetsResult), NOT by dispatch: delivery is not completion. Legs not applicable
    -- to an obligation are inserted TRUE.
    drain_done          BOOLEAN NOT NULL DEFAULT FALSE,
    activation_done     BOOLEAN NOT NULL DEFAULT FALSE,
    broadcast_done      BOOLEAN NOT NULL DEFAULT FALSE,
    decklog_done        BOOLEAN NOT NULL DEFAULT FALSE,
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
    applied_at          TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_foghorn_ingest_admission_effects_pending
    ON foghorn.ingest_admission_effects(next_attempt_at, id)
    WHERE state = 'pending';

-- Tombstone cleanup proves that no pending admission callback at or below a membership revision can
-- still restore it. Keep that bounded proof on the pending working set.
CREATE INDEX IF NOT EXISTS idx_foghorn_ingest_admission_effects_pending_fence
    ON foghorn.ingest_admission_effects(tenant_id, stream_internal_name, source_revision)
    WHERE state = 'pending';

-- Terminal rows are retained briefly as diagnostics; this index keeps batched retention cleanup
-- from scanning the pending working set.
CREATE INDEX IF NOT EXISTS idx_foghorn_ingest_admission_effects_terminal
    ON foghorn.ingest_admission_effects(updated_at)
    WHERE state IN ('applied', 'superseded');
