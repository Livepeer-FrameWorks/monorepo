ALTER TABLE quartermaster.tenants
    ALTER COLUMN billing_entitlements_observed_at SET DEFAULT NOW();
