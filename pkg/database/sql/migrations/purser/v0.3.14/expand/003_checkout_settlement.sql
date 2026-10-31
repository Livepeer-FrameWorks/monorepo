ALTER TABLE purser.payment_provider_intents
    ADD COLUMN IF NOT EXISTS checkout_completed_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS recovery_started_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS recovery_outcome TEXT;

CREATE TABLE IF NOT EXISTS purser.checkout_recovery_audit (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    intent_id UUID NOT NULL REFERENCES purser.payment_provider_intents(id),
    actor_id TEXT NOT NULL,
    reason TEXT NOT NULL,
    outcome TEXT NOT NULL DEFAULT 'requested',
    detail TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_checkout_recovery_audit_tenant
    ON purser.checkout_recovery_audit(tenant_id, created_at DESC);
ALTER TABLE purser.provider_webhook_inbox
    ADD COLUMN IF NOT EXISTS wait_attempts INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS unresolved_since TIMESTAMPTZ;
ALTER TABLE purser.payment_provider_intents
    ADD COLUMN IF NOT EXISTS provider_request BYTEA;
ALTER TABLE purser.payment_provider_intents ADD COLUMN IF NOT EXISTS submission_state VARCHAR(12) NOT NULL DEFAULT 'unknown';
ALTER TABLE purser.payment_provider_intents ADD COLUMN IF NOT EXISTS reconciled_references JSONB;
