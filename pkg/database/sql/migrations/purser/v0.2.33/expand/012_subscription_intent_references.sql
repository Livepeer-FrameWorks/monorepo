-- Existing subscription/checkout state tables gain a back-reference to the
-- canonical payment_provider_intents row. This keeps the per-row contract
-- (cluster_subscriptions for cluster access, tenant_subscriptions for tier
-- state) as the read model while making the pre-provider intent the
-- write-side source of truth. The intent reference is nullable in expand;
-- post-cutoff inserts must populate it.
-- Schema source of truth: pkg/database/sql/schema/purser.sql

ALTER TABLE purser.tenant_subscriptions
    ADD COLUMN IF NOT EXISTS pending_intent_id UUID REFERENCES purser.payment_provider_intents(id);

ALTER TABLE purser.cluster_subscriptions
    ADD COLUMN IF NOT EXISTS intent_id UUID REFERENCES purser.payment_provider_intents(id);

CREATE INDEX IF NOT EXISTS idx_tenant_subscriptions_pending_intent
    ON purser.tenant_subscriptions(pending_intent_id)
    WHERE pending_intent_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_cluster_subscriptions_intent
    ON purser.cluster_subscriptions(intent_id)
    WHERE intent_id IS NOT NULL;
