DO $$
BEGIN
    IF to_regclass('foghorn.managed_stream_placement_outbox') IS NOT NULL THEN
        INSERT INTO foghorn.managed_stream_active_cluster_outbox (
            stream_id,
            tenant_id,
            cluster_id,
            desired_active,
            revision,
            attempts,
            next_attempt_at,
            last_attempt_at,
            lease_owner,
            lease_until,
            created_at,
            updated_at
        )
        SELECT stream_id,
               tenant_id,
               cluster_id::text,
               desired_active,
               revision,
               attempts,
               next_attempt_at,
               last_attempt_at,
               NULL,
               NULL,
               created_at,
               updated_at
        FROM foghorn.managed_stream_placement_outbox
        ON CONFLICT (stream_id) DO NOTHING;
    END IF;
END
$$;
