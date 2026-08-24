-- name: ReleaseOfflineEffectNotOwner :exec
UPDATE foghorn.ingest_offline_effects
SET leased_until = NULL, lease_token = NULL, attempts = GREATEST(attempts - 1, 0),
    claim_affinity = NULLIF(sqlc.arg(authority_instance)::text, ''), next_attempt_at = NOW(), updated_at = NOW()
WHERE id = sqlc.arg(effect_id) AND state = 'pending'
  AND lease_token = sqlc.arg(lease_token)::text::uuid;

-- name: EnqueueOfflineEffect :exec
INSERT INTO foghorn.ingest_offline_effects
    (tenant_id, stream_internal_name, source_node_id, source_generation, source_revision,
     set_node_offline, teardown_stream, broadcast_offline, decklog_trigger)
VALUES (sqlc.arg(tenant_id)::text::uuid, sqlc.arg(stream_internal_name), sqlc.arg(source_node_id),
        NULLIF(sqlc.arg(source_generation)::text, '')::uuid, sqlc.arg(source_revision),
        sqlc.arg(set_node_offline), sqlc.arg(teardown_stream), sqlc.arg(broadcast_offline), sqlc.arg(decklog_trigger))
ON CONFLICT (tenant_id, stream_internal_name, source_revision) DO NOTHING;

-- name: ClaimOfflineEffects :many
WITH candidates AS (
    SELECT e.id FROM foghorn.ingest_offline_effects e
    WHERE e.state = 'pending' AND e.next_attempt_at <= NOW()
      AND (e.leased_until IS NULL OR e.leased_until < NOW())
      AND (e.claim_affinity IS NULL OR e.claim_affinity = sqlc.arg(instance_id)
           OR e.updated_at <= NOW() - INTERVAL '10 seconds')
    ORDER BY e.next_attempt_at, e.id
    FOR UPDATE SKIP LOCKED
    LIMIT sqlc.arg(row_limit)
), leased AS (
    UPDATE foghorn.ingest_offline_effects o
    SET lease_token = gen_random_uuid(),
        leased_until = NOW() + (sqlc.arg(lease_ms)::bigint * INTERVAL '1 millisecond'),
        attempts = attempts + 1, claim_affinity = NULL, updated_at = NOW()
    FROM candidates c WHERE o.id = c.id
    RETURNING o.id, o.tenant_id::text AS tenant_id, o.stream_internal_name, o.source_node_id,
              COALESCE(o.source_generation::text, '')::text AS source_generation, o.source_revision,
              o.set_node_offline, o.teardown_stream, o.broadcast_offline,
              o.decklog_trigger, o.lease_token::text AS lease_token
)
SELECT * FROM leased ORDER BY id;

-- name: LockOfflineEffectLease :one
SELECT true FROM foghorn.ingest_offline_effects
WHERE id = sqlc.arg(effect_id) AND state = 'pending'
  AND lease_token = sqlc.arg(lease_token)::text::uuid
FOR UPDATE;

-- name: HasActiveIngestSession :one
SELECT EXISTS (
    SELECT 1 FROM foghorn.ingest_sessions
    WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
      AND stream_internal_name = sqlc.arg(stream_internal_name) AND ended_at IS NULL
);

-- name: SupersedeOfflineEffect :execrows
UPDATE foghorn.ingest_offline_effects
SET state = 'superseded', applied_at = NOW(), updated_at = NOW(), leased_until = NULL, lease_token = NULL
WHERE id = sqlc.arg(effect_id) AND state = 'pending'
  AND lease_token = sqlc.arg(lease_token)::text::uuid;

-- name: CompleteOfflineEffect :execrows
UPDATE foghorn.ingest_offline_effects
SET state = 'applied', applied_at = NOW(), updated_at = NOW(), leased_until = NULL, lease_token = NULL, last_error = NULL
WHERE id = sqlc.arg(effect_id) AND state = 'pending'
  AND lease_token = sqlc.arg(lease_token)::text::uuid;

-- name: FailOfflineEffect :exec
UPDATE foghorn.ingest_offline_effects
SET leased_until = NULL, lease_token = NULL, last_error = sqlc.arg(error_message)::text, updated_at = NOW(),
    next_attempt_at = NOW() + LEAST(INTERVAL '5 minutes', INTERVAL '1 second' * power(2, LEAST(attempts, 8)))
WHERE id = sqlc.arg(effect_id) AND state = 'pending'
  AND lease_token = sqlc.arg(lease_token)::text::uuid;

-- name: PurgeTerminalOfflineEffects :execrows
DELETE FROM foghorn.ingest_offline_effects
WHERE id IN (
    SELECT id FROM foghorn.ingest_offline_effects
    WHERE state IN ('applied', 'superseded')
      AND updated_at < NOW() - (sqlc.arg(older_than_ms)::bigint * INTERVAL '1 millisecond')
    ORDER BY updated_at LIMIT 1000
);
