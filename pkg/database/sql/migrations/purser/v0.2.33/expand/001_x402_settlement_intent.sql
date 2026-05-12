-- Durable pre-submit intent for x402 settlements. The 'submitting' state holds
-- the signed payload before broadcast so the reconciler can resubmit while
-- authorizationState is still unused or flag manual reconciliation when the
-- authorization was consumed on-chain without a recorded tx_hash.
-- Schema source of truth: pkg/database/sql/schema/purser.sql

ALTER TABLE purser.x402_nonces
    ALTER COLUMN tx_hash DROP NOT NULL,
    ADD COLUMN IF NOT EXISTS auth_payload JSONB,
    ADD COLUMN IF NOT EXISTS submitted_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS last_submit_attempt_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_x402_nonces_submitting
    ON purser.x402_nonces(settled_at)
    WHERE status = 'submitting';
