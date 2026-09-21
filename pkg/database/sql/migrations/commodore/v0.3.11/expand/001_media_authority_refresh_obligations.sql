-- One refresh obligation per (target, lane). Any number of change events for a
-- target fold into its single row, so refresh work is bounded by the number of
-- authorities rather than by event volume or by how long a target has been
-- failing. target_key leads the primary key so rows hash by target.
CREATE TABLE IF NOT EXISTS commodore.media_authority_refresh_obligations (
    target_key VARCHAR(320) NOT NULL,
    lane VARCHAR(20) NOT NULL,
    tenant_id UUID NOT NULL,
    target_kind VARCHAR(24) NOT NULL,
    revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    bound_version BIGINT CHECK (bound_version IS NULL OR bound_version > 0),
    status VARCHAR(20) NOT NULL DEFAULT 'pending',
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    lease_expires_at TIMESTAMPTZ,
    -- Names the claim that holds the lease. Settling requires it, so a worker
    -- whose lease lapsed cannot settle the attempt that replaced it.
    claim_token UUID,
    pending_since TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- Renewal lanes only: when the version this renewal is bound to stops being
    -- valid. A live renewal past it is an authority in use that expired.
    expires_at TIMESTAMPTZ,
    park_reason VARCHAR(64),
    last_reason VARCHAR(255) NOT NULL,
    last_source_service VARCHAR(64) NOT NULL,
    last_source_event_id VARCHAR(255) NOT NULL,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (target_key, lane),
    CONSTRAINT chk_media_authority_obligation_lane
        CHECK (lane IN ('event', 'bulk', 'object_deadline', 'tenant_deadline')),
    CONSTRAINT chk_media_authority_obligation_kind
        CHECK (target_kind IN ('tenant', 'live_stream', 'artifact', 'tenant_media_objects')),
    CONSTRAINT chk_media_authority_obligation_status
        CHECK (status IN ('pending', 'processing', 'completed', 'parked', 'dormant')),
    CONSTRAINT chk_media_authority_obligation_parked
        CHECK ((status = 'parked') = (park_reason IS NOT NULL)),
    CONSTRAINT chk_media_authority_obligation_reason
        CHECK (btrim(last_reason) <> '')
);

CREATE INDEX IF NOT EXISTS idx_media_authority_refresh_obligations_due
    ON commodore.media_authority_refresh_obligations(lane ASC, next_attempt_at ASC, pending_since ASC)
    WHERE status IN ('pending', 'processing');

CREATE INDEX IF NOT EXISTS idx_media_authority_refresh_obligations_tenant
    ON commodore.media_authority_refresh_obligations(tenant_id, status);

-- Which lane is compiling a target. Every claim writes this row, which is what
-- keeps two lanes from compiling the same target at once: a lease check on
-- another lane's obligation reads a snapshot and cannot. Deleted when the
-- compile settles; lease_expires_at covers a worker that never settles.
CREATE TABLE IF NOT EXISTS commodore.media_authority_target_claims (
    target_key VARCHAR(320) PRIMARY KEY,
    lane VARCHAR(20) NOT NULL,
    lease_expires_at TIMESTAMPTZ NOT NULL,
    claim_token UUID NOT NULL
);

-- Parked targets are few and are counted on a timer; the table is not read to
-- find them.
CREATE INDEX IF NOT EXISTS idx_media_authority_refresh_obligations_parked
    ON commodore.media_authority_refresh_obligations(target_kind)
    WHERE status = 'parked';

-- Counts authorities in use that ran past their validity without scanning the
-- catalog: a dormant renewal belongs to an object nobody uses and is expected
-- to expire.
CREATE INDEX IF NOT EXISTS idx_media_authority_refresh_obligations_expiry
    ON commodore.media_authority_refresh_obligations(expires_at ASC)
    WHERE lane IN ('object_deadline', 'tenant_deadline') AND status IN ('pending', 'processing', 'parked');

-- The only writer of refresh obligations. A fold bumps the revision so a worker
-- that claimed the previous revision cannot complete away the newer change; it
-- keeps an active lease as a short serialization fence and re-arms a parked
-- target, because new source state may now compile. A dormant renewal (its
-- object went unused) is revived the same way by the next publication.
CREATE OR REPLACE FUNCTION commodore.enqueue_media_authority_obligation(
    p_lane TEXT,
    p_target_key TEXT,
    p_target_kind TEXT,
    p_tenant_id UUID,
    p_reason TEXT,
    p_source_service TEXT,
    p_source_event_id TEXT,
    p_next_attempt_at TIMESTAMPTZ,
    p_bound_version BIGINT
) RETURNS VOID
LANGUAGE plpgsql
AS $$
DECLARE
    bound_expires_at TIMESTAMPTZ;
BEGIN
    IF p_tenant_id IS NULL OR btrim(COALESCE(p_target_key, '')) = '' THEN
        RETURN;
    END IF;
    -- A renewal's target key is "<authority_kind>:<authority_id>", and it is
    -- scheduled in the transaction that published the version it is bound to.
    IF p_lane IN ('object_deadline', 'tenant_deadline') AND p_bound_version IS NOT NULL THEN
        SELECT versions.valid_until INTO bound_expires_at
        FROM commodore.media_authority_versions AS versions
        WHERE versions.authority_kind = split_part(p_target_key, ':', 1)
          AND versions.authority_id = substr(p_target_key, strpos(p_target_key, ':') + 1)
          AND versions.authority_version = p_bound_version;
    END IF;
    INSERT INTO commodore.media_authority_refresh_obligations AS obligation (
        target_key, lane, tenant_id, target_kind, bound_version, next_attempt_at, expires_at,
        last_reason, last_source_service, last_source_event_id
    ) VALUES (
        p_target_key, p_lane, p_tenant_id, p_target_kind, p_bound_version,
        COALESCE(p_next_attempt_at, NOW()), bound_expires_at, p_reason, p_source_service, p_source_event_id
    )
    ON CONFLICT (target_key, lane) DO UPDATE SET
        revision = obligation.revision + 1,
        status = 'pending',
        attempts = 0,
        tenant_id = EXCLUDED.tenant_id,
        target_kind = EXCLUDED.target_kind,
        bound_version = EXCLUDED.bound_version,
        expires_at = EXCLUDED.expires_at,
        -- A renewal lane's schedule is replaced by each publication. An event
        -- that is already waiting keeps its place: the claim orders by due time,
        -- so taking the new one would send a target that keeps changing to the
        -- back of a backlog every time it changed.
        next_attempt_at = CASE
            WHEN obligation.lane IN ('event', 'bulk') AND obligation.status = 'pending'
            THEN LEAST(obligation.next_attempt_at, EXCLUDED.next_attempt_at)
            ELSE EXCLUDED.next_attempt_at
        END,
        pending_since = CASE
            WHEN obligation.status IN ('pending', 'processing') THEN obligation.pending_since
            ELSE NOW()
        END,
        -- A live lease marks a compile in flight whatever the status reads: an
        -- earlier fold during the same compile has already flipped it to pending.
        lease_expires_at = CASE
            WHEN obligation.lease_expires_at > NOW() THEN obligation.lease_expires_at
            ELSE NULL
        END,
        park_reason = NULL,
        last_error = NULL,
        last_reason = EXCLUDED.last_reason,
        last_source_service = EXCLUDED.last_source_service,
        last_source_event_id = EXCLUDED.last_source_event_id,
        updated_at = NOW();
END;
$$;

CREATE OR REPLACE FUNCTION commodore.enqueue_live_stream_media_authority_refresh(
    p_stream_id UUID,
    p_tenant_id UUID,
    p_reason TEXT
) RETURNS VOID
LANGUAGE plpgsql
AS $$
BEGIN
    IF p_stream_id IS NULL OR p_tenant_id IS NULL THEN
        RETURN;
    END IF;
    PERFORM commodore.enqueue_media_authority_obligation(
        'event', 'media_object:live_stream:' || p_stream_id::text, 'live_stream', p_tenant_id,
        'media_object:live_stream:' || p_stream_id::text || ':' || p_reason,
        'commodore', 'live_stream:' || p_stream_id::text, NOW(), NULL
    );
END;
$$;
CREATE OR REPLACE FUNCTION commodore.signing_key_media_authority_changed()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    affected_tenant UUID;
BEGIN
    affected_tenant := CASE WHEN TG_OP = 'DELETE' THEN OLD.tenant_id ELSE NEW.tenant_id END;
    PERFORM commodore.enqueue_media_authority_obligation(
        'event', 'tenant_media_objects:' || affected_tenant::text, 'tenant_media_objects', affected_tenant,
        'tenant_media_objects:signing_key_changed', 'commodore', 'signing_key:' || affected_tenant::text,
        NOW(), NULL
    );
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END;
$$;

CREATE OR REPLACE FUNCTION commodore.enqueue_artifact_media_authority_refresh(
    p_artifact_id UUID,
    p_tenant_id UUID,
    p_kind TEXT,
    p_reason TEXT
) RETURNS VOID
LANGUAGE plpgsql
AS $$
BEGIN
    IF p_artifact_id IS NULL OR p_tenant_id IS NULL THEN
        RETURN;
    END IF;
    -- The target is the artifact authority, not the kind: a chapter and the VOD
    -- row that backs it name the same authority and must share one obligation.
    PERFORM commodore.enqueue_media_authority_obligation(
        'event', 'media_object:artifact:' || p_artifact_id::text, 'artifact', p_tenant_id,
        'media_object:' || p_kind || ':' || p_artifact_id::text || ':' || p_reason,
        'commodore', p_kind || ':' || p_artifact_id::text, NOW(), NULL
    );
END;
$$;
