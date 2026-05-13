-- Track which ACME CA signed each TLS bundle so renewals route to the
-- same account/CA combination (matching certificates.issuer_ca added in
-- 001). Without this, EnsureTLSBundle would re-resolve via the current
-- NAVIGATOR_ACME_CA_ORDER on every renewal, which silently migrates a
-- bundle's issuer mid-life and orphans the original ACME account.

ALTER TABLE navigator.tls_bundles
    ADD COLUMN IF NOT EXISTS issuer_ca TEXT NOT NULL DEFAULT 'letsencrypt';
