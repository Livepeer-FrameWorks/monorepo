-- Durable delivery of account emails (email verification). Register and
-- resend write one row in the same transaction as the user/token change; a
-- worker mints the link token at send time and retries with backoff, so an
-- SMTP outage no longer strands an account without a verification email.
CREATE TABLE IF NOT EXISTS commodore.account_email_outbox (
    user_id         UUID NOT NULL,
    tenant_id       UUID NOT NULL,
    purpose         TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending',
    attempts        INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMP NOT NULL DEFAULT NOW(),
    last_error      TEXT,
    lease_token     TEXT,
    created_at      TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMP NOT NULL DEFAULT NOW(),
    completed_at    TIMESTAMP,
    PRIMARY KEY (user_id, purpose),
    CONSTRAINT chk_commodore_account_email_outbox_purpose CHECK (purpose IN ('verification')),
    CONSTRAINT chk_commodore_account_email_outbox_status CHECK (status IN ('pending', 'completed', 'abandoned'))
);

CREATE INDEX IF NOT EXISTS idx_commodore_account_email_outbox_pending
    ON commodore.account_email_outbox(next_attempt_at)
    WHERE status = 'pending';
