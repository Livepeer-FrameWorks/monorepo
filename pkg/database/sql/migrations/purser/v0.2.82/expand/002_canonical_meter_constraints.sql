-- Switch the tier_pricing_rules / subscription_pricing_overrides meter
-- CHECK from a fixed enum to a data-driven regex. Keeps marketplace and
-- future advanced-processing meter names addable without a schema change
-- while still rejecting garbage values.
--
-- ADD CONSTRAINT runs NOT VALID so the swap is non-blocking: new writes
-- are checked, existing rows are not scanned. v0.2.82/postdeploy/001
-- runs VALIDATE CONSTRAINT to enforce on existing data once the old
-- enum-aware code paths are out of service.
--
-- Idempotent under repeated runs.

ALTER TABLE purser.tier_pricing_rules
    DROP CONSTRAINT IF EXISTS chk_tier_pricing_meter;
ALTER TABLE purser.tier_pricing_rules
    ADD CONSTRAINT chk_tier_pricing_meter
    CHECK (meter ~ '^[a-z][a-z0-9_]{0,63}$') NOT VALID;

ALTER TABLE purser.subscription_pricing_overrides
    DROP CONSTRAINT IF EXISTS chk_subscription_pricing_meter;
ALTER TABLE purser.subscription_pricing_overrides
    ADD CONSTRAINT chk_subscription_pricing_meter
    CHECK (meter ~ '^[a-z][a-z0-9_]{0,63}$') NOT VALID;
