-- Settled funding enables canonical prepaid usage unless the customer opts out.
-- Existing funded Free accounts follow the same rule at rollout.
ALTER TABLE purser.tenant_subscriptions
    ADD COLUMN IF NOT EXISTS automatic_prepaid_usage BOOLEAN NOT NULL DEFAULT TRUE;
