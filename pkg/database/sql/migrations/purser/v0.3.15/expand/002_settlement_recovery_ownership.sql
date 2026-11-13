ALTER TABLE purser.provider_webhook_inbox
    ADD COLUMN IF NOT EXISTS tenant_id UUID,
    ADD COLUMN IF NOT EXISTS provider_payment_id TEXT,
    ADD COLUMN IF NOT EXISTS last_error_code VARCHAR(64);
ALTER TABLE purser.provider_webhook_inbox DROP CONSTRAINT IF EXISTS chk_provider_webhook_inbox_status;
ALTER TABLE purser.provider_webhook_inbox ADD CONSTRAINT chk_provider_webhook_inbox_status
    CHECK (status IN ('pending', 'processing', 'failed', 'processed', 'blocked')) NOT VALID;




-- Service and operator diagnostics share the same failure and ownership rules.
CREATE OR REPLACE VIEW purser.provider_webhook_failures AS
SELECT id, COALESCE(tenant_id::text, NULLIF(headers->>'tenant_id', ''), 'unbound') AS tenant_id
FROM (
 SELECT id, tenant_id, headers FROM purser.provider_webhook_inbox
 WHERE status IN ('pending', 'failed', 'processing') AND attempts > 1
 UNION
 SELECT id, tenant_id, headers FROM purser.provider_webhook_inbox
 WHERE status IN ('pending', 'failed', 'processing') AND unresolved_since < NOW() - INTERVAL '14 days'
 UNION
 SELECT id, tenant_id, headers FROM purser.provider_webhook_inbox WHERE status='blocked'
) failures;



CREATE OR REPLACE VIEW purser.provider_settlement_failures AS
 SELECT id,tenant_id::text AS tenant_id FROM purser.provider_settlements WHERE status='blocked'
 UNION
 SELECT id,tenant_id::text AS tenant_id FROM purser.provider_settlements
 WHERE status='recorded' AND settled_at < NOW()-INTERVAL '14 days';

CREATE OR REPLACE VIEW purser.billing_recovery_failures AS
 SELECT 'webhook:'||id::text AS id,tenant_id FROM purser.provider_webhook_failures
 UNION ALL
 SELECT 'settlement:'||id::text AS id,tenant_id FROM purser.provider_settlement_failures;
