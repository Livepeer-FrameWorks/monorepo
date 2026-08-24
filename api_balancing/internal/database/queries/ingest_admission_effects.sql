-- name: EnqueueAdmissionEffect :exec
INSERT INTO foghorn.ingest_admission_effects
    (tenant_id, stream_internal_name, node_id, source_generation, source_revision,
     prior_owner_node_id, prior_owner_source_generation, push_targets, broadcast_live, decklog_trigger, peer_clusters,
     drain_done, activation_done, broadcast_done, decklog_done)
VALUES (sqlc.arg(tenant_id)::text::uuid, sqlc.arg(stream_internal_name), sqlc.arg(node_id),
        sqlc.arg(source_generation)::text::uuid, sqlc.arg(source_revision), sqlc.arg(prior_owner_node_id),
        NULLIF(sqlc.arg(prior_owner_source_generation)::text, '')::uuid, sqlc.arg(push_targets),
        sqlc.arg(broadcast_live), sqlc.arg(decklog_trigger), sqlc.narg(peer_clusters),
        sqlc.arg(drain_done), sqlc.arg(activation_done), sqlc.arg(broadcast_done), sqlc.arg(decklog_done))
ON CONFLICT (source_generation) DO NOTHING;

-- name: ClaimAdmissionEffects :many
WITH candidates AS (
    SELECT e.id FROM foghorn.ingest_admission_effects e
    WHERE e.state = 'pending' AND e.next_attempt_at <= NOW()
      AND (e.leased_until IS NULL OR e.leased_until < NOW())
      AND (e.claim_affinity IS NULL OR e.claim_affinity = sqlc.arg(instance_id)
           OR e.updated_at <= NOW() - INTERVAL '10 seconds')
    ORDER BY e.next_attempt_at, e.id
    FOR UPDATE SKIP LOCKED
    LIMIT sqlc.arg(row_limit)
), leased AS (
    UPDATE foghorn.ingest_admission_effects a
    SET lease_token = gen_random_uuid(),
        leased_until = NOW() + (sqlc.arg(lease_ms)::bigint * INTERVAL '1 millisecond'),
        attempts = attempts + 1, claim_affinity = NULL, updated_at = NOW()
    FROM candidates c WHERE a.id = c.id
    RETURNING a.id, a.tenant_id::text AS tenant_id, a.stream_internal_name, a.node_id,
              a.source_generation::text AS source_generation, a.source_revision, a.prior_owner_node_id,
              COALESCE(a.prior_owner_source_generation::text, '')::text AS prior_owner_source_generation,
              a.push_targets, a.broadcast_live, a.decklog_trigger, COALESCE(a.peer_clusters, '[]'::text) AS peer_clusters,
              a.drain_done, a.activation_done, a.broadcast_done, a.decklog_done,
              a.lease_token::text AS lease_token
)
SELECT * FROM leased ORDER BY id;

-- name: ReadAdmissionLegsLocked :one
SELECT drain_done, activation_done, broadcast_done, decklog_done
FROM foghorn.ingest_admission_effects
WHERE id = sqlc.arg(effect_id) AND state = 'pending'
  AND lease_token = sqlc.arg(lease_token)::text::uuid
FOR UPDATE;

-- name: SettleAdmissionLegs :execrows
UPDATE foghorn.ingest_admission_effects
SET drain_done = sqlc.arg(drain_done), activation_done = sqlc.arg(activation_done),
    broadcast_done = sqlc.arg(broadcast_done), decklog_done = sqlc.arg(decklog_done),
    state = sqlc.arg(new_state)::text, updated_at = NOW(),
    last_error = COALESCE(NULLIF(sqlc.arg(poison_note)::text, ''), last_error),
    applied_at = CASE WHEN sqlc.arg(new_state)::text <> 'pending' THEN NOW() ELSE applied_at END,
    leased_until = CASE WHEN sqlc.arg(new_state)::text <> 'pending' THEN NULL ELSE leased_until END,
    lease_token = CASE WHEN sqlc.arg(new_state)::text <> 'pending' THEN NULL ELSE lease_token END,
    push_targets = CASE WHEN sqlc.arg(new_state)::text <> 'pending' THEN NULL ELSE push_targets END,
    decklog_trigger = CASE WHEN sqlc.arg(new_state)::text <> 'pending' THEN NULL ELSE decklog_trigger END
WHERE id = sqlc.arg(effect_id) AND state = 'pending'
  AND lease_token = sqlc.arg(lease_token)::text::uuid;

-- name: AdmissionGenerationActive :one
SELECT EXISTS (
    SELECT 1 FROM foghorn.ingest_sessions
    WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
      AND stream_internal_name = sqlc.arg(stream_internal_name)
      AND id = sqlc.arg(source_generation)::text::uuid AND ended_at IS NULL
);

-- name: MarkAdmissionDrainDone :exec
UPDATE foghorn.ingest_admission_effects
SET drain_done = TRUE, updated_at = NOW()
WHERE state = 'pending' AND source_generation = sqlc.arg(source_generation)::text::uuid
  AND prior_owner_node_id = sqlc.arg(node_id);

-- name: MarkAdmissionActivationDone :exec
UPDATE foghorn.ingest_admission_effects
SET activation_done = TRUE, updated_at = NOW()
WHERE state = 'pending' AND source_generation = sqlc.arg(source_generation)::text::uuid
  AND node_id = sqlc.arg(node_id);

-- name: ReleaseAdmissionEffectNotOwner :exec
UPDATE foghorn.ingest_admission_effects
SET leased_until = NULL, lease_token = NULL, attempts = GREATEST(attempts - 1, 0),
    claim_affinity = NULLIF(sqlc.arg(authority_instance)::text, ''), next_attempt_at = NOW(), updated_at = NOW()
WHERE id = sqlc.arg(effect_id) AND state = 'pending'
  AND lease_token = sqlc.arg(lease_token)::text::uuid;

-- name: FailAdmissionEffect :exec
UPDATE foghorn.ingest_admission_effects
SET leased_until = NULL, lease_token = NULL, updated_at = NOW(),
    last_error = CASE WHEN last_error LIKE 'poison:%' THEN last_error || ' | ' || sqlc.arg(error_message)::text ELSE sqlc.arg(error_message)::text END,
    next_attempt_at = NOW() + LEAST(INTERVAL '5 minutes', INTERVAL '1 second' * power(2, LEAST(attempts, 8)))
WHERE id = sqlc.arg(effect_id) AND state = 'pending'
  AND lease_token = sqlc.arg(lease_token)::text::uuid;

-- name: PurgeTerminalAdmissionEffects :execrows
DELETE FROM foghorn.ingest_admission_effects
WHERE id IN (
    SELECT id FROM foghorn.ingest_admission_effects
    WHERE state IN ('applied', 'superseded')
      AND updated_at < NOW() - (sqlc.arg(older_than_ms)::bigint * INTERVAL '1 millisecond')
    ORDER BY updated_at LIMIT 1000
);

-- name: ListPurgeableAdmissionEffectFences :many
WITH candidates AS (
    SELECT value->>'tenant_id' AS tenant_id,
           (value->>'internal_name')::text AS internal_name,
           (value->>'source_revision')::bigint AS source_revision
    FROM jsonb_array_elements(sqlc.arg(fences)::jsonb)
)
SELECT c.internal_name,
       NOT EXISTS (
           SELECT 1 FROM foghorn.ingest_admission_effects e
           WHERE e.tenant_id = c.tenant_id::uuid
             AND e.stream_internal_name = c.internal_name
             AND e.source_revision <= c.source_revision AND e.state = 'pending'
       ) AS purgeable
FROM candidates c;
