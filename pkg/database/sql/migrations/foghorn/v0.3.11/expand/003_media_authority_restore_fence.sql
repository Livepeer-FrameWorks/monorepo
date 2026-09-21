-- Present while the cell may hold media authority older than what it last
-- acknowledged: its database was restored, or the control plane found it holding
-- something other than what it acknowledged. While it is present an authority
-- is decided on only after a fresh fetch confirms it; the row goes
-- when the cell's summary of what it holds matches the control plane's record.
CREATE TABLE IF NOT EXISTS foghorn.media_authority_restore_fence (
    singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    fenced_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Set when a tenant authority brings back a tenant the cell held only past its
-- validity, or not at all. Object copies without a fetch begun after it are
-- withheld until the control plane confirms them: they were not corrected
-- while the tenant was out, and nothing orders their corrections before the
-- tenant's return.
ALTER TABLE foghorn.tenant_authority_projection
    ADD COLUMN IF NOT EXISTS objects_trusted_from TIMESTAMPTZ;

ALTER TABLE foghorn.media_authorities
    ADD COLUMN IF NOT EXISTS confirmed_at TIMESTAMPTZ NOT NULL DEFAULT 'epoch';
