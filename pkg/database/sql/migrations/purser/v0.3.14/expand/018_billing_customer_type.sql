ALTER TABLE purser.tenant_subscriptions
    ADD COLUMN IF NOT EXISTS billing_customer_type VARCHAR(16) NOT NULL DEFAULT 'unspecified';

ALTER TABLE purser.tenant_subscriptions
    DROP CONSTRAINT IF EXISTS chk_billing_customer_type;

ALTER TABLE purser.tenant_subscriptions
    ADD CONSTRAINT chk_billing_customer_type
    CHECK (billing_customer_type IN ('unspecified', 'consumer', 'business')) NOT VALID;
