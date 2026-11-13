CREATE TABLE IF NOT EXISTS purser.provider_webhook_retry_audit (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    webhook_id UUID NOT NULL,
    provider VARCHAR(50) NOT NULL,
    provider_payment_id TEXT NOT NULL,
    previous_error_code VARCHAR(64) NOT NULL,
    expected_state TEXT NOT NULL,
    actor_id UUID NOT NULL,
    reason TEXT NOT NULL CHECK (length(btrim(reason)) BETWEEN 1 AND 1024),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (tenant_id, webhook_id, expected_state)
);
CREATE INDEX IF NOT EXISTS idx_provider_webhook_retry_audit_tenant
    ON purser.provider_webhook_retry_audit(tenant_id, created_at, id);

CREATE OR REPLACE FUNCTION purser.reject_provider_webhook_retry_audit_mutation()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'provider webhook retry audit is append-only' USING ERRCODE = '23514';
END;
$$;
DROP TRIGGER IF EXISTS provider_webhook_retry_audit_append_only ON purser.provider_webhook_retry_audit;
CREATE TRIGGER provider_webhook_retry_audit_append_only
    BEFORE UPDATE OR DELETE ON purser.provider_webhook_retry_audit
    FOR EACH ROW EXECUTE FUNCTION purser.reject_provider_webhook_retry_audit_mutation();
