-- Records the operator who closed a provider-applied upgrade without complete metering.
ALTER TABLE purser.subscription_tier_upgrades
    ADD COLUMN IF NOT EXISTS metering_waived_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS metering_waiver_actor_id TEXT,
    ADD COLUMN IF NOT EXISTS metering_waiver_reason TEXT;
