ALTER TABLE purser.provider_webhook_inbox
    ADD COLUMN IF NOT EXISTS wake_requested BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE purser.payment_provider_intents
    ADD COLUMN IF NOT EXISTS activated_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS funding_intent_id UUID,
    ADD COLUMN IF NOT EXISTS provider_mandate_id TEXT;
