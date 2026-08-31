ALTER TABLE purser.billing_event_outbox
    ADD COLUMN IF NOT EXISTS lease_token UUID;
