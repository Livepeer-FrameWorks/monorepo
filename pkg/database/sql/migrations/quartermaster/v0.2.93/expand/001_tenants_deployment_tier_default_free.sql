-- deployment_tier is billing-derived (Purser stamps billing_tiers.tier_name);
-- the safe default for rows created before Purser stamps them is 'free', not
-- 'global' — 'global' passed the alias-eligibility gates and leaked tenant
-- *.cdn aliases to non-paying accounts.
ALTER TABLE quartermaster.tenants ALTER COLUMN deployment_tier SET DEFAULT 'free';
