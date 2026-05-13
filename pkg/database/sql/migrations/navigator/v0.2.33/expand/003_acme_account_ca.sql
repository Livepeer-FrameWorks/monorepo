-- Per-CA ACME account scoping.
--
-- ACME registrations are CA-specific (the registration JWS binds an
-- account key to one CA's directory URL). When Navigator falls back
-- from Let's Encrypt to Google Trust Services on rate-limit errors,
-- it must establish (and persist) a separate registration per CA.
-- Renewals must reuse the account that originally issued the cert
-- (tracked by certificates.issuer_ca added in 001).

ALTER TABLE navigator.acme_accounts
    ADD COLUMN IF NOT EXISTS ca TEXT NOT NULL DEFAULT 'letsencrypt';

CREATE UNIQUE INDEX IF NOT EXISTS idx_acme_accounts_platform_email_ca
    ON navigator.acme_accounts(email, ca)
    WHERE tenant_id IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_acme_accounts_tenant_email_ca
    ON navigator.acme_accounts(tenant_id, email, ca);
