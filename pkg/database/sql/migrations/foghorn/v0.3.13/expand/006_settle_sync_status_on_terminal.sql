-- A terminal artifact has no sync running. Settle sync_status='in_progress' (and
-- storage_location='freezing') to 'failed' on every terminal transition. Rows
-- that reached a terminal status before this trigger are settled by the
-- foghorn_terminal_sync_status_v0_3_13 data migration.
CREATE OR REPLACE FUNCTION foghorn.settle_sync_status_on_terminal() RETURNS trigger AS $$
BEGIN
    NEW.sync_status := 'failed';
    NEW.sync_error := COALESCE(NULLIF(NEW.sync_error, ''), 'artifact reached ' || NEW.status || ' before sync completed');
    IF NEW.storage_location = 'freezing' THEN
        NEW.storage_location := 'local';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS artifact_settle_sync_status_on_terminal ON foghorn.artifacts;
CREATE TRIGGER artifact_settle_sync_status_on_terminal
    BEFORE UPDATE ON foghorn.artifacts
    FOR EACH ROW
    WHEN (
        NEW.status IN ('deleted', 'expired', 'aborted')
        AND OLD.status IS DISTINCT FROM NEW.status
        AND NEW.sync_status = 'in_progress'
    )
    EXECUTE FUNCTION foghorn.settle_sync_status_on_terminal();
