-- tax_assessed_since is NULL for a billing relationship that keeps the untaxed
-- treatment it was sold under. Rows present when this column is added keep NULL;
-- rows created afterwards start assessed tax handling when they are created.
ALTER TABLE purser.tenant_subscriptions
    ADD COLUMN IF NOT EXISTS tax_assessed_since TIMESTAMPTZ;

ALTER TABLE purser.tenant_subscriptions
    ALTER COLUMN tax_assessed_since SET DEFAULT NOW();
