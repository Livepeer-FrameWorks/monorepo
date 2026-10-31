ALTER TABLE commodore.media_authority_refresh_obligations
 DROP CONSTRAINT IF EXISTS chk_media_authority_obligation_lane;
ALTER TABLE commodore.media_authority_refresh_obligations
 ADD CONSTRAINT chk_media_authority_obligation_lane
 CHECK (lane IN ('event', 'bulk', 'ingest_revocation', 'object_deadline', 'tenant_deadline')) NOT VALID;
