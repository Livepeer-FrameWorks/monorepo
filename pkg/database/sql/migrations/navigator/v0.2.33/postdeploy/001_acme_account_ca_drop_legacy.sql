-- Remove pre-CA ACME account uniqueness after the per-CA indexes exist.
-- This is postdeploy, not contract: the Google Trust fallback needs to
-- create a second account for the same tenant/email immediately after
-- this feature rolls out, while old binaries remain protected by the
-- new (tenant_id, email, ca) and platform (email, ca) indexes.

ALTER TABLE navigator.acme_accounts
    DROP CONSTRAINT IF EXISTS idx_acme_accounts_tenant_email;

DROP INDEX IF EXISTS navigator.idx_acme_accounts_platform_email;
