-- v0.3.11: a custom domain that was already in cert_failed before this
-- release counts as reported, so its retry cycles emit no custom_domain.failed.
-- Rows the new binary moved into cert_failed already carry the timestamp and
-- are left alone.

UPDATE navigator.tenant_custom_domains
SET failure_reported_at = updated_at
WHERE status = 'cert_failed' AND failure_reported_at IS NULL;
