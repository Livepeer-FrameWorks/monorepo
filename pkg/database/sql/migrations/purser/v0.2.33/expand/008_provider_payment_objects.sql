-- Durable mapping from provider object ids (customer/subscription/invoice/
-- payment_intent/charge/refund/dispute on Stripe; customer/mandate/
-- subscription/payment/refund/chargeback on Mollie) to local rows. Reads
-- by webhook handlers translate "we just received event X for object Y"
-- into "which tenant, which invoice, which intent, which payment". Inserts
-- are idempotent on (provider, object_type, provider_object_id).
-- Schema source of truth: pkg/database/sql/schema/purser.sql

CREATE TABLE IF NOT EXISTS purser.provider_payment_objects (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    provider VARCHAR(20) NOT NULL,
    object_type VARCHAR(40) NOT NULL,
    provider_object_id VARCHAR(255) NOT NULL,
    tenant_id UUID,
    local_reference_type VARCHAR(40),
    local_reference_id UUID,
    intent_id UUID REFERENCES purser.payment_provider_intents(id),
    metadata JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_provider_payment_object_provider CHECK (provider IN ('stripe', 'mollie')),
    CONSTRAINT chk_provider_payment_object_type CHECK (object_type IN (
        'customer', 'subscription', 'invoice',
        'payment_intent', 'charge', 'refund', 'dispute',
        'payment', 'mandate', 'chargeback'
    ))
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_provider_payment_objects
    ON purser.provider_payment_objects(provider, object_type, provider_object_id);

CREATE INDEX IF NOT EXISTS idx_provider_payment_objects_tenant
    ON purser.provider_payment_objects(tenant_id, object_type)
    WHERE tenant_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_provider_payment_objects_local_ref
    ON purser.provider_payment_objects(local_reference_type, local_reference_id)
    WHERE local_reference_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_provider_payment_objects_intent
    ON purser.provider_payment_objects(intent_id)
    WHERE intent_id IS NOT NULL;
