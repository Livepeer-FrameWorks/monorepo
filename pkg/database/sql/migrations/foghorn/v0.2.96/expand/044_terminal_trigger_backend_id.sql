-- Carry the artifact's recorded backend_id onto the staging-cleanup keys the terminal trigger enqueues. Those staging
-- objects live on the artifact's OWN store, so the strict cleanup worker must resolve that recorded backend rather than
-- guessing the current store. CREATE OR REPLACE updates the function on preserved databases (the baseline foghorn.sql
-- carries this identical form for fresh installs); the trigger references it by name, so no trigger re-create is needed.
CREATE OR REPLACE FUNCTION foghorn.clear_sync_identity_on_terminal() RETURNS trigger AS $$
BEGIN
    -- Before clearing the attempt identity, durably enqueue the abandoned attempt's STAGING objects AND its
    -- published CANDIDATE (+ co-located .dtsh) for cleanup: once the request ids are NULL, purge can no longer
    -- DERIVE these keys, so a PUT/promote that landed for the abandoned attempt would leak. The MAIN attempt
    -- enqueues all FOUR keys it can produce (a main upload can BUNDLE a .dtsh, staged at <k>.dtsh.staging.<req>
    -- and promoted to <k>.dtsh.att-<req>), mirroring control.applySyncCompletionFailure's enqueue set:
    -- staging <k>.staging.<req>; .dtsh staging <k>.dtsh.staging.<req>; media candidate <k>.att-<req>;
    -- .dtsh candidate <k>.dtsh.att-<req>.
    -- backend_id carries the artifact's recorded owner (OLD.backend_id) onto each queued key: these staging objects
    -- live on the artifact's OWN store, so the strict cleanup worker resolves that recorded backend rather than the
    -- current store. NULL only for a legacy artifact not yet adopted (the worker then retains the row, never guesses).
    IF OLD.sync_object_key IS NOT NULL AND OLD.sync_object_key <> '' THEN
        IF OLD.sync_request_id IS NOT NULL AND OLD.sync_request_id <> '' THEN
            INSERT INTO foghorn.staging_cleanup_queue (object_key, backend_id) VALUES
                (OLD.sync_object_key || '.staging.' || OLD.sync_request_id, OLD.backend_id),
                (OLD.sync_object_key || '.dtsh.staging.' || OLD.sync_request_id, OLD.backend_id),
                (OLD.sync_object_key || '.att-' || OLD.sync_request_id, OLD.backend_id),
                (OLD.sync_object_key || '.dtsh.att-' || OLD.sync_request_id, OLD.backend_id)
            ON CONFLICT (object_key) DO NOTHING;
        END IF;
        IF OLD.dtsh_sync_request_id IS NOT NULL AND OLD.dtsh_sync_request_id <> '' THEN
            INSERT INTO foghorn.staging_cleanup_queue (object_key, backend_id) VALUES
                (OLD.sync_object_key || '.dtsh.staging.' || OLD.dtsh_sync_request_id, OLD.backend_id),
                (OLD.sync_object_key || '.dtsh.att-' || OLD.dtsh_sync_request_id, OLD.backend_id)
            ON CONFLICT (object_key) DO NOTHING;
        END IF;
    END IF;
    NEW.sync_request_id := NULL;
    NEW.sync_node_id := NULL;
    NEW.dtsh_sync_request_id := NULL;
    NEW.dtsh_sync_node_id := NULL;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
