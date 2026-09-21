-- When an authority was last decided on in any cell, to the day. An object is
-- kept in cells and renewed only while it is in use; one nobody uses stops being
-- renewed and its copies expire. A tenant row is advanced by every use of one
-- of its objects, so a tenant is never colder than its objects.
CREATE TABLE IF NOT EXISTS commodore.media_authority_use (
    authority_kind VARCHAR(32) NOT NULL,
    authority_id VARCHAR(255) NOT NULL,
    tenant_id UUID NOT NULL,
    last_used_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (authority_kind, authority_id),
    CONSTRAINT chk_media_authority_use_kind
        CHECK (authority_kind IN ('tenant', 'media_object'))
);

CREATE INDEX IF NOT EXISTS idx_media_authority_use_tenant
    ON commodore.media_authority_use(tenant_id);

-- When a cell last reported holding something other than what it acknowledged
-- (its database was restored, or deliveries were lost). Acknowledgements made
-- before then no longer bound which versions the cell may hold.
CREATE TABLE IF NOT EXISTS commodore.media_authority_cell_ack_resets (
    cell_id VARCHAR(255) PRIMARY KEY,
    reset_at TIMESTAMPTZ NOT NULL
);

-- The recently used set, read on a timer to find renewals of authorities in use
-- that were left dormant, without reading the authorities nobody uses.
CREATE INDEX IF NOT EXISTS idx_media_authority_use_recent
    ON commodore.media_authority_use(last_used_at ASC);

-- When use started being recorded. An authority with no use row counts as used
-- at this instant, so an authority that predates the record is not mistaken for
-- one nobody uses.
CREATE TABLE IF NOT EXISTS commodore.media_authority_use_epoch (
    singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO commodore.media_authority_use_epoch (singleton) VALUES (TRUE)
ON CONFLICT (singleton) DO NOTHING;

-- What the running compiler signs with and how it shapes payloads. A change
-- means every published authority has to be issued again, which is otherwise
-- never due: an unchanged compile publishes nothing.
CREATE TABLE IF NOT EXISTS commodore.media_authority_compiler_state (
    singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    fingerprint VARCHAR(512) NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Every live replica of the cell accepts 30-day media-object authorities /
-- reports the authorities it decides on. FALSE until the cell attests it.
ALTER TABLE commodore.media_cell_placement_capabilities
    ADD COLUMN IF NOT EXISTS long_validity_ready BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE commodore.media_cell_placement_capabilities
    ADD COLUMN IF NOT EXISTS use_reports_ready BOOLEAN NOT NULL DEFAULT FALSE;

-- TRUE for a delivery a cell asked to have repeated. Within a cell, replayed
-- rows are served after fresh ones, so a cell that lost its database cannot
-- hold back a change or a revocation behind its own catch-up.
ALTER TABLE commodore.media_authority_deliveries
    ADD COLUMN IF NOT EXISTS replay BOOLEAN NOT NULL DEFAULT FALSE;

-- One obligation for the tenant's objects, not one per stream: the worker that
-- takes it refreshes only the objects cells still hold.
CREATE OR REPLACE FUNCTION commodore.tenant_processing_media_authority_changed()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    affected_tenant UUID;
BEGIN
    affected_tenant := CASE WHEN TG_OP = 'DELETE' THEN OLD.tenant_id ELSE NEW.tenant_id END;
    PERFORM commodore.enqueue_media_authority_obligation(
        'event', 'tenant_media_objects:' || affected_tenant::text, 'tenant_media_objects', affected_tenant,
        'tenant_media_objects:tenant_processing_changed', 'commodore', 'tenant_processing:' || affected_tenant::text,
        NOW(), NULL
    );
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END;
$$;
