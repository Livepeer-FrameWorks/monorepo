-- name: ReleaseOfflineEffectNotOwner :exec
UPDATE foghorn.ingest_offline_effects
SET leased_until = NULL, lease_token = NULL, attempts = GREATEST(attempts - 1, 0),
    claim_affinity = NULLIF(sqlc.arg(authority_instance)::text, ''), next_attempt_at = NOW(), updated_at = NOW()
WHERE id = sqlc.arg(effect_id) AND state = 'pending'
  AND lease_token = sqlc.arg(lease_token)::text::uuid;

-- name: EnqueueOfflineEffect :exec
INSERT INTO foghorn.ingest_offline_effects
    (tenant_id, stream_internal_name, source_node_id, source_generation, source_revision,
     set_node_offline, set_node_offline_done, teardown_stream, teardown_done,
     broadcast_offline, broadcast_offline_done, decklog_trigger, decklog_done)
VALUES (sqlc.arg(tenant_id)::text::uuid, sqlc.arg(stream_internal_name), sqlc.arg(source_node_id),
        NULLIF(sqlc.arg(source_generation)::text, '')::uuid, sqlc.arg(source_revision),
        sqlc.arg(set_node_offline), NOT sqlc.arg(set_node_offline),
        sqlc.arg(teardown_stream), NOT sqlc.arg(teardown_stream),
        sqlc.arg(broadcast_offline), NOT sqlc.arg(broadcast_offline),
        sqlc.arg(decklog_trigger), COALESCE(octet_length(sqlc.arg(decklog_trigger)::bytea), 0) = 0)
ON CONFLICT (tenant_id, stream_internal_name, source_revision) DO NOTHING;

-- name: FailExhaustedOfflineEffects :many
UPDATE foghorn.ingest_offline_effects
SET state = 'failed', applied_at = NOW(), leased_until = NULL, lease_token = NULL,
    last_error = COALESCE(NULLIF(last_error, ''), 'offline obligation exhausted retry budget'),
    updated_at = NOW()
WHERE state = 'pending' AND (attempts >= 12 OR ack_wait_attempts >= 12)
  AND (leased_until IS NULL OR leased_until < NOW())
RETURNING id, tenant_id::text AS tenant_id, stream_internal_name, source_revision, last_error;

-- name: ClaimOfflineEffects :many
WITH candidates AS (
    SELECT e.id FROM foghorn.ingest_offline_effects e
    WHERE e.state = 'pending' AND e.next_attempt_at <= NOW()
      AND e.attempts < 12 AND e.ack_wait_attempts < 12
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
              o.set_node_offline, o.set_node_offline_done, o.teardown_stream, o.teardown_done,
              o.broadcast_offline, o.broadcast_offline_done, o.decklog_trigger, o.decklog_done,
              o.lease_token::text AS lease_token
)
SELECT * FROM leased ORDER BY id;

-- name: ReadOfflineEffectLegsLocked :one
SELECT set_node_offline_done, teardown_done, broadcast_offline_done, decklog_done
FROM foghorn.ingest_offline_effects
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

-- name: SettleOfflineEffect :execrows
UPDATE foghorn.ingest_offline_effects
SET set_node_offline_done = sqlc.arg(set_node_offline_done),
    teardown_done = sqlc.arg(teardown_done),
    broadcast_offline_done = sqlc.arg(broadcast_offline_done),
    decklog_done = sqlc.arg(decklog_done),
	attempts = CASE
		WHEN sqlc.arg(release_lease)::boolean
		 AND NOT ((NOT set_node_offline OR sqlc.arg(set_node_offline_done))
		  AND (NOT teardown_stream OR sqlc.arg(teardown_done))
		  AND (NOT broadcast_offline OR sqlc.arg(broadcast_offline_done))
		  AND (COALESCE(octet_length(decklog_trigger), 0) = 0 OR sqlc.arg(decklog_done)))
		THEN 0 ELSE attempts END,
    ack_wait_attempts = CASE
        WHEN sqlc.arg(release_lease)::boolean
         AND teardown_stream AND NOT sqlc.arg(teardown_done)
         AND NOT ((NOT set_node_offline OR sqlc.arg(set_node_offline_done))
          AND (NOT teardown_stream OR sqlc.arg(teardown_done))
          AND (NOT broadcast_offline OR sqlc.arg(broadcast_offline_done))
          AND (COALESCE(octet_length(decklog_trigger), 0) = 0 OR sqlc.arg(decklog_done)))
        THEN ack_wait_attempts + 1 ELSE ack_wait_attempts END,
    state = CASE
        WHEN (NOT set_node_offline OR sqlc.arg(set_node_offline_done))
         AND (NOT teardown_stream OR sqlc.arg(teardown_done))
         AND (NOT broadcast_offline OR sqlc.arg(broadcast_offline_done))
         AND (COALESCE(octet_length(decklog_trigger), 0) = 0 OR sqlc.arg(decklog_done))
        THEN 'applied'
        WHEN sqlc.arg(release_lease)::boolean
         AND (attempts >= 12 OR (teardown_stream AND NOT sqlc.arg(teardown_done) AND ack_wait_attempts + 1 >= 12))
        THEN 'failed'
        ELSE 'pending'
    END,
    applied_at = CASE
        WHEN (NOT set_node_offline OR sqlc.arg(set_node_offline_done))
         AND (NOT teardown_stream OR sqlc.arg(teardown_done))
         AND (NOT broadcast_offline OR sqlc.arg(broadcast_offline_done))
         AND (COALESCE(octet_length(decklog_trigger), 0) = 0 OR sqlc.arg(decklog_done))
        THEN NOW()
        WHEN sqlc.arg(release_lease)::boolean
         AND (attempts >= 12 OR (teardown_stream AND NOT sqlc.arg(teardown_done) AND ack_wait_attempts + 1 >= 12))
        THEN NOW()
        ELSE applied_at
    END,
    next_attempt_at = CASE WHEN sqlc.arg(release_lease)::boolean
        THEN NOW() + LEAST(
            INTERVAL '5 minutes',
            INTERVAL '1 second' * power(2, LEAST(
                CASE WHEN teardown_stream AND NOT sqlc.arg(teardown_done)
                     THEN ack_wait_attempts + 1 ELSE attempts END,
                8
            ))
        )
        ELSE next_attempt_at END,
    updated_at = NOW(),
    leased_until = CASE WHEN sqlc.arg(release_lease)::boolean THEN NULL ELSE leased_until END,
    lease_token = CASE WHEN sqlc.arg(release_lease)::boolean THEN NULL ELSE lease_token END,
    last_error = CASE
        WHEN sqlc.arg(release_lease)::boolean
         AND (attempts >= 12 OR (teardown_stream AND NOT sqlc.arg(teardown_done) AND ack_wait_attempts + 1 >= 12))
        THEN COALESCE(NULLIF(last_error, ''), 'offline obligation exhausted retry budget')
        WHEN sqlc.arg(release_lease)::boolean THEN NULL
        ELSE last_error END
WHERE id = sqlc.arg(effect_id) AND state = 'pending'
  AND lease_token = sqlc.arg(lease_token)::text::uuid;

-- name: MarkOfflineTeardownDone :execrows
UPDATE foghorn.ingest_offline_effects
SET teardown_done = TRUE,
    next_attempt_at = NOW(), claim_affinity = NULL, updated_at = NOW()
WHERE source_node_id = sqlc.arg(source_node_id)
  AND source_generation = sqlc.arg(source_generation)::text::uuid
  AND state = 'pending' AND teardown_stream AND NOT teardown_done;

-- name: ReviveFailedOfflineTeardownDone :execrows
UPDATE foghorn.ingest_offline_effects
SET teardown_done = TRUE,
    state = 'pending', attempts = 0, ack_wait_attempts = 0, next_attempt_at = NOW(),
    claim_affinity = NULL, applied_at = NULL, leased_until = NULL, lease_token = NULL,
    last_error = NULL, updated_at = NOW()
WHERE source_node_id = sqlc.arg(source_node_id)
  AND source_generation = sqlc.arg(source_generation)::text::uuid
  AND state = 'failed' AND teardown_stream AND NOT teardown_done;

-- name: FailOfflineEffect :exec
UPDATE foghorn.ingest_offline_effects
SET state = CASE WHEN attempts >= 12 OR ack_wait_attempts >= 12 THEN 'failed' ELSE 'pending' END,
    applied_at = CASE WHEN attempts >= 12 OR ack_wait_attempts >= 12 THEN NOW() ELSE applied_at END,
    leased_until = NULL, lease_token = NULL, last_error = sqlc.arg(error_message)::text, updated_at = NOW(),
    next_attempt_at = NOW() + LEAST(INTERVAL '5 minutes', INTERVAL '1 second' * power(2, LEAST(attempts, 8)))
WHERE id = sqlc.arg(effect_id) AND state = 'pending'
  AND lease_token = sqlc.arg(lease_token)::text::uuid;

-- name: PurgeTerminalOfflineEffects :execrows
DELETE FROM foghorn.ingest_offline_effects
WHERE id IN (
    SELECT id FROM foghorn.ingest_offline_effects
	WHERE ((state IN ('applied', 'superseded')
        AND updated_at < NOW() - (sqlc.arg(older_than_ms)::bigint * INTERVAL '1 millisecond'))
       OR (state = 'failed' AND updated_at < NOW() - INTERVAL '30 days'))
    ORDER BY updated_at LIMIT 1000
);
