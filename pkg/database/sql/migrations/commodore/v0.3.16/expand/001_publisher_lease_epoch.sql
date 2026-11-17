-- The counter survives release and is serialized by the existing stream row.
ALTER TABLE commodore.streams ADD COLUMN IF NOT EXISTS publisher_lease_epoch BIGINT NOT NULL DEFAULT 0;
ALTER TABLE commodore.streams DROP CONSTRAINT IF EXISTS chk_stream_publisher_lease_epoch;
ALTER TABLE commodore.streams ADD CONSTRAINT chk_stream_publisher_lease_epoch CHECK (publisher_lease_epoch >= 0) NOT VALID;

CREATE OR REPLACE FUNCTION commodore.streams_fence_publisher_lease()
RETURNS TRIGGER LANGUAGE plpgsql SET search_path = pg_catalog, commodore AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        NEW.publisher_lease_epoch := 0;
        IF NULLIF(NEW.active_ingest_cluster_id, '') IS NOT NULL
           AND NULLIF(NEW.active_ingest_claim_id, '') IS NOT NULL THEN
            NEW.publisher_lease_epoch := 1;
        END IF;
    ELSE
        NEW.publisher_lease_epoch := OLD.publisher_lease_epoch;
        IF NULLIF(NEW.active_ingest_cluster_id, '') IS NOT NULL
           AND NULLIF(NEW.active_ingest_claim_id, '') IS NOT NULL
           AND (OLD.publisher_lease_epoch = 0 OR
                ROW(NEW.active_ingest_cluster_id, NEW.active_ingest_claim_id, NEW.active_ingest_node_id, NEW.active_ingest_control_cell_id)
                IS DISTINCT FROM
                ROW(OLD.active_ingest_cluster_id, OLD.active_ingest_claim_id, OLD.active_ingest_node_id, OLD.active_ingest_control_cell_id)) THEN
            NEW.publisher_lease_epoch := OLD.publisher_lease_epoch + 1;
        END IF;
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_streams_publisher_lease_epoch ON commodore.streams;
CREATE TRIGGER trg_streams_publisher_lease_epoch
BEFORE INSERT OR UPDATE OF active_ingest_cluster_id, active_ingest_claim_id,
    active_ingest_node_id, active_ingest_control_cell_id, publisher_lease_epoch
ON commodore.streams FOR EACH ROW EXECUTE FUNCTION commodore.streams_fence_publisher_lease();
