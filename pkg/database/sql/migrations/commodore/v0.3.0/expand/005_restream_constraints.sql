ALTER TABLE commodore.push_targets
    ALTER COLUMN is_enabled SET DEFAULT TRUE,
    ALTER COLUMN status SET DEFAULT 'idle';

-- PostgreSQL implements varchar-to-text as a metadata-only widening. It belongs
-- in expand so both the old 512-byte writer and the new encrypted-envelope
-- writer can run before the field-encryption data migration is allowed to pass.
-- The trigger names target_uri in its UPDATE OF list, so PostgreSQL requires it
-- to be recreated around the metadata change. The migration is transactional.
DROP TRIGGER IF EXISTS trg_push_target_media_authority ON commodore.push_targets;
ALTER TABLE commodore.push_targets
    ALTER COLUMN target_uri TYPE TEXT;
CREATE OR REPLACE TRIGGER trg_push_target_media_authority
AFTER INSERT OR DELETE OR UPDATE OF tenant_id, stream_id, platform, name, target_uri, is_enabled
ON commodore.push_targets
FOR EACH ROW EXECUTE FUNCTION commodore.live_stream_child_media_authority_changed('push_target_changed');
