-- A delivery the target cell refuses on a precondition settles as rejected
-- rather than returning to the retry queue.
ALTER TABLE commodore.media_authority_deliveries
    DROP CONSTRAINT IF EXISTS chk_media_authority_delivery_status;
ALTER TABLE commodore.media_authority_deliveries
    ADD CONSTRAINT chk_media_authority_delivery_status
    CHECK (status IN ('pending', 'delivering', 'acknowledged', 'superseded', 'rejected')) NOT VALID;
