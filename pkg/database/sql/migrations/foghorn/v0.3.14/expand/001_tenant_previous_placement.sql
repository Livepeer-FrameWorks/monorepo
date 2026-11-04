-- The tenant placement policy set a cell held before the current tenant
-- authority raised its placement revision, and when that happened. Objects
-- still compiled against that previous revision are evaluated against it for a
-- bounded window instead of failing resolution.
ALTER TABLE foghorn.tenant_authority_projection
    ADD COLUMN IF NOT EXISTS previous_placement_policy BYTEA;
ALTER TABLE foghorn.tenant_authority_projection
    ADD COLUMN IF NOT EXISTS placement_superseded_at TIMESTAMPTZ;
