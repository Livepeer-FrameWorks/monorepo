-- name: EnqueueAdmissionEffect :exec
INSERT INTO foghorn.ingest_admission_effects
    (tenant_id, stream_internal_name, node_id, source_generation, source_revision,
     prior_owner_node_id, prior_owner_source_generation, push_targets, broadcast_live, decklog_trigger, peer_clusters,
     drain_done, activation_done, broadcast_done, decklog_done, state)
VALUES (sqlc.arg(tenant_id)::text::uuid, sqlc.arg(stream_internal_name), sqlc.arg(node_id),
        sqlc.arg(source_generation)::text::uuid, sqlc.arg(source_revision), sqlc.arg(prior_owner_node_id),
        NULLIF(sqlc.arg(prior_owner_source_generation)::text, '')::uuid, sqlc.arg(push_targets),
        sqlc.arg(broadcast_live), sqlc.arg(decklog_trigger), sqlc.narg(peer_clusters),
        sqlc.arg(drain_done), sqlc.arg(activation_done), sqlc.arg(broadcast_done), sqlc.arg(decklog_done),
        CASE WHEN COALESCE(octet_length(sqlc.arg(push_targets)::bytea), 0) = 0
             THEN 'pending' ELSE 'pending_v2' END)
ON CONFLICT (source_generation) DO NOTHING;

-- name: ClaimAdmissionEffects :many
WITH candidates AS (
    SELECT e.id FROM foghorn.ingest_admission_effects e
    WHERE e.state IN ('pending', 'pending_v2') AND e.next_attempt_at <= NOW()
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
              a.drain_done, a.activation_done, a.broadcast_done, a.decklog_done, a.state,
              a.lease_token::text AS lease_token
)
SELECT * FROM leased ORDER BY id;

-- name: ReadAdmissionLegsLocked :one
SELECT drain_done, activation_done, broadcast_done, decklog_done
FROM foghorn.ingest_admission_effects
WHERE id = sqlc.arg(effect_id) AND state IN ('pending', 'pending_v2')
  AND lease_token = sqlc.arg(lease_token)::text::uuid
FOR UPDATE;

-- name: SettleAdmissionLegs :execrows
UPDATE foghorn.ingest_admission_effects
SET drain_done = sqlc.arg(drain_done), activation_done = sqlc.arg(activation_done),
    broadcast_done = sqlc.arg(broadcast_done), decklog_done = sqlc.arg(decklog_done),
    state = sqlc.arg(new_state)::text, updated_at = NOW(),
    last_error = COALESCE(NULLIF(sqlc.arg(poison_note)::text, ''), last_error),
    applied_at = CASE WHEN sqlc.arg(new_state)::text NOT IN ('pending', 'pending_v2') THEN NOW() ELSE applied_at END,
    leased_until = CASE WHEN sqlc.arg(new_state)::text NOT IN ('pending', 'pending_v2') THEN NULL ELSE leased_until END,
    lease_token = CASE WHEN sqlc.arg(new_state)::text NOT IN ('pending', 'pending_v2') THEN NULL ELSE lease_token END,
    push_targets = CASE WHEN sqlc.arg(clear_push_targets)::boolean THEN NULL ELSE push_targets END,
    decklog_trigger = CASE WHEN sqlc.arg(new_state)::text NOT IN ('pending', 'pending_v2') THEN NULL ELSE decklog_trigger END
WHERE id = sqlc.arg(effect_id) AND state IN ('pending', 'pending_v2')
  AND lease_token = sqlc.arg(lease_token)::text::uuid;

-- name: AdmissionGenerationActive :one
SELECT EXISTS (
    SELECT 1 FROM foghorn.ingest_sessions
    WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
      AND stream_internal_name = sqlc.arg(stream_internal_name)
      AND id = sqlc.arg(source_generation)::text::uuid AND ended_at IS NULL
);

-- name: GetAdmissionEffectSourceRevision :one
SELECT source_revision
FROM foghorn.ingest_admission_effects
WHERE source_generation = sqlc.arg(generation)::text::uuid
  AND tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND stream_internal_name = sqlc.arg(stream_internal_name);

-- name: MarkAdmissionDrainDone :exec
UPDATE foghorn.ingest_admission_effects
SET drain_done = TRUE, updated_at = NOW()
WHERE state IN ('pending', 'pending_v2') AND source_generation = sqlc.arg(source_generation)::text::uuid
  AND prior_owner_node_id = sqlc.arg(node_id);

-- name: MarkAdmissionActivationDone :exec
UPDATE foghorn.ingest_admission_effects
SET activation_done = TRUE,
    activation_connection_fence = sqlc.arg(connection_fence),
    updated_at = NOW()
WHERE state IN ('pending', 'pending_v2') AND source_generation = sqlc.arg(source_generation)::text::uuid
  AND node_id = sqlc.arg(node_id)
  AND sqlc.arg(connection_fence) >= activation_connection_fence;

-- name: RequeueActivePushTargetActivationsForNode :execrows
UPDATE foghorn.ingest_admission_effects AS effect
SET state = CASE WHEN effect.state IN ('pending_v2', 'applied_v2') THEN 'pending_v2' ELSE 'pending' END,
    activation_done = FALSE,
    activation_connection_fence = GREATEST(effect.activation_connection_fence, sqlc.arg(connection_fence)),
    attempts = 0,
    next_attempt_at = NOW(),
    leased_until = NULL,
    lease_token = NULL,
    claim_affinity = NULLIF(sqlc.arg(instance_id)::text, ''),
    applied_at = NULL,
    updated_at = NOW()
WHERE effect.node_id = sqlc.arg(node_id)
  AND effect.state IN ('pending', 'applied', 'pending_v2', 'applied_v2')
  AND effect.activation_done = TRUE
  AND effect.push_targets IS NOT NULL
  AND effect.activation_connection_fence < sqlc.arg(connection_fence)
  AND EXISTS (
      SELECT 1
      FROM foghorn.ingest_sessions AS session
      WHERE session.tenant_id = effect.tenant_id
        AND session.stream_internal_name = effect.stream_internal_name
        AND session.id = effect.source_generation
        AND session.ended_at IS NULL
  );

-- name: ReleaseAdmissionEffectNotOwner :exec
UPDATE foghorn.ingest_admission_effects
SET leased_until = NULL, lease_token = NULL, attempts = GREATEST(attempts - 1, 0),
    claim_affinity = NULLIF(sqlc.arg(authority_instance)::text, ''), next_attempt_at = NOW(), updated_at = NOW()
WHERE id = sqlc.arg(effect_id) AND state IN ('pending', 'pending_v2')
  AND lease_token = sqlc.arg(lease_token)::text::uuid;

-- name: FailAdmissionEffect :exec
UPDATE foghorn.ingest_admission_effects
SET leased_until = NULL, lease_token = NULL, updated_at = NOW(),
    last_error = CASE WHEN last_error LIKE 'poison:%' THEN last_error || ' | ' || sqlc.arg(error_message)::text ELSE sqlc.arg(error_message)::text END,
    next_attempt_at = NOW() + LEAST(INTERVAL '5 minutes', INTERVAL '1 second' * power(2, LEAST(attempts, 8)))
WHERE id = sqlc.arg(effect_id) AND state IN ('pending', 'pending_v2')
  AND lease_token = sqlc.arg(lease_token)::text::uuid;

-- name: PurgeTerminalAdmissionEffects :execrows
DELETE FROM foghorn.ingest_admission_effects
WHERE id IN (
    SELECT effect.id FROM foghorn.ingest_admission_effects AS effect
    WHERE effect.state IN ('applied', 'superseded', 'applied_v2', 'superseded_v2')
      AND effect.updated_at < NOW() - (sqlc.arg(older_than_ms)::bigint * INTERVAL '1 millisecond')
      AND (
          effect.push_targets IS NULL
          OR NOT EXISTS (
              SELECT 1
              FROM foghorn.ingest_sessions AS session
              WHERE session.tenant_id = effect.tenant_id
                AND session.stream_internal_name = effect.stream_internal_name
                AND session.id = effect.source_generation
                AND session.ended_at IS NULL
          )
      )
    ORDER BY effect.updated_at LIMIT 1000
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
             AND e.source_revision <= c.source_revision AND e.state IN ('pending', 'pending_v2')
       ) AS purgeable
FROM candidates c;

-- name: ListLegacyAdmissionPushTargetsForEncryption :many
SELECT id, tenant_id::text AS tenant_id, stream_internal_name,
       source_generation::text AS source_generation, push_targets, state
FROM foghorn.ingest_admission_effects
WHERE push_targets IS NOT NULL
  AND state IN ('pending', 'applied', 'superseded')
  AND (leased_until IS NULL OR leased_until < NOW())
  AND lease_token IS NULL
ORDER BY id
LIMIT sqlc.arg(row_limit)
FOR UPDATE SKIP LOCKED;

-- name: UpgradeAdmissionPushTargetsEncryption :execrows
UPDATE foghorn.ingest_admission_effects
SET push_targets = sqlc.arg(push_targets),
    state = CASE state
        WHEN 'pending' THEN 'pending_v2'
        WHEN 'applied' THEN 'applied_v2'
        WHEN 'superseded' THEN 'superseded_v2'
        ELSE state
    END,
    updated_at = NOW()
WHERE id = sqlc.arg(effect_id)
  AND state IN ('pending', 'applied', 'superseded');
