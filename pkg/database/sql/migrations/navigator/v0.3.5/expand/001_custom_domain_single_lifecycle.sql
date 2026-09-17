-- v0.3.5: custom domains are served only through the tenant bundle. Renewal
-- failures are recorded separately from issuance status, and a failed
-- issuance carries its own retry time.

ALTER TABLE navigator.tenant_custom_domains
    ADD COLUMN IF NOT EXISTS last_renewal_error TEXT,
    ADD COLUMN IF NOT EXISTS last_renewal_error_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS next_attempt_at TIMESTAMPTZ;
