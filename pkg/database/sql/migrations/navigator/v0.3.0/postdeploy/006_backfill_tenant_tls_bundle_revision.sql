-- The revision is an opaque content identity, not a security primitive. MD5 is
-- used only for deterministic backfill portability across PostgreSQL/Yugabyte;
-- all subsequent writes use the service's SHA-256 revision. Expand installs an
-- empty non-null compatibility value, so both old NULL rows from an interrupted
-- earlier rollout and newly expanded empty rows are backfilled here.
UPDATE navigator.tls_bundles
SET version = md5(cert_pem)
WHERE version IS NULL OR version = '';
