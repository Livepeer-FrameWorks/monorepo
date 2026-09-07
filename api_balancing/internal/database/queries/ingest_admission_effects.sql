-- name: EnqueueAdmissionEffect :exec
INSERT INTO foghorn.ingest_admission_effects
    (tenant_id, stream_internal_name, node_id, source_generation, source_revision,
     prior_owner_node_id, prior_owner_source_generation, push_targets, target_revision, broadcast_live, decklog_trigger, peer_clusters,
     drain_done, activation_done, broadcast_done, decklog_done, state)
VALUES (sqlc.arg(tenant_id)::text::uuid, sqlc.arg(stream_internal_name), sqlc.arg(node_id),
        sqlc.arg(source_generation)::text::uuid, sqlc.arg(source_revision), sqlc.arg(prior_owner_node_id),
        NULLIF(sqlc.arg(prior_owner_source_generation)::text, '')::uuid, sqlc.arg(push_targets), sqlc.arg(target_revision),
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
              a.push_targets, a.target_revision, a.capacity_pending, a.broadcast_live, a.decklog_trigger, COALESCE(a.peer_clusters, '[]'::text) AS peer_clusters,
              a.drain_done, a.activation_done, a.broadcast_done, a.decklog_done, a.state,
              a.lease_token::text AS lease_token
)
SELECT * FROM leased ORDER BY id;

-- name: ReadAdmissionLegsLocked :one
SELECT drain_done, activation_done, broadcast_done, decklog_done, capacity_pending
FROM foghorn.ingest_admission_effects
WHERE id = sqlc.arg(effect_id) AND state IN ('pending', 'pending_v2')
  AND lease_token = sqlc.arg(lease_token)::text::uuid
FOR UPDATE;

-- name: SettleAdmissionLegs :execrows
UPDATE foghorn.ingest_admission_effects
SET drain_done = sqlc.arg(drain_done), activation_done = sqlc.arg(activation_done),
    broadcast_done = sqlc.arg(broadcast_done), decklog_done = sqlc.arg(decklog_done),
    capacity_pending = sqlc.arg(capacity_pending),
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

-- name: MarkAdmissionActivationDone :execrows
UPDATE foghorn.ingest_admission_effects AS effect
SET activation_done = TRUE,
    activation_connection_fence = sqlc.arg(connection_fence),
    updated_at = NOW()
WHERE effect.state IN ('pending', 'pending_v2') AND effect.source_generation = sqlc.arg(source_generation)::text::uuid
  AND effect.node_id = sqlc.arg(node_id)
  AND effect.target_revision = sqlc.arg(target_revision)
  AND sqlc.arg(connection_fence) >= effect.activation_connection_fence
  AND (
      NULLIF(sqlc.arg(activation_attempt)::text, '') IS NULL
      OR EXISTS (
          SELECT 1
          FROM foghorn.admission_push_target_revisions AS history
          WHERE history.source_generation = effect.source_generation
            AND history.target_revision = effect.target_revision
            AND history.activation_attempt = NULLIF(sqlc.arg(activation_attempt)::text, '')::uuid
      )
  );

-- name: AdmissionPushTargetAttemptCurrent :one
SELECT EXISTS (
    SELECT 1
    FROM foghorn.admission_push_target_revisions AS history
    WHERE history.source_generation = sqlc.arg(source_generation)::text::uuid
      AND history.node_id = sqlc.arg(node_id)
      AND history.target_revision = sqlc.arg(target_revision)
      AND history.activation_attempt = NULLIF(sqlc.arg(activation_attempt)::text, '')::uuid
) AS current;

-- name: ListActivePushTargetActivationsForNodeRequeue :many
SELECT effect.id, effect.tenant_id::text AS tenant_id, effect.stream_internal_name,
       effect.source_generation::text AS source_generation, effect.target_revision,
       effect.push_targets
FROM foghorn.ingest_admission_effects AS effect
WHERE effect.node_id = sqlc.arg(node_id)
  AND effect.state IN ('pending', 'applied', 'pending_v2', 'applied_v2')
  AND effect.activation_done = TRUE
  AND effect.push_targets IS NOT NULL
  AND effect.target_revision > 0
  AND effect.activation_connection_fence < sqlc.arg(connection_fence)
  AND EXISTS (
      SELECT 1
      FROM foghorn.ingest_sessions AS session
      WHERE session.tenant_id = effect.tenant_id
        AND session.stream_internal_name = effect.stream_internal_name
        AND session.id = effect.source_generation
        AND session.ended_at IS NULL
  )
ORDER BY effect.id
LIMIT 100
FOR UPDATE;

-- name: RequeueActivePushTargetActivationByID :execrows
UPDATE foghorn.ingest_admission_effects AS effect
SET state = CASE WHEN effect.state IN ('pending_v2', 'applied_v2') THEN 'pending_v2' ELSE 'pending' END,
    push_targets = sqlc.arg(push_targets),
    activation_done = FALSE,
    activation_connection_fence = GREATEST(effect.activation_connection_fence, sqlc.arg(connection_fence)),
    attempts = 0,
    next_attempt_at = NOW(),
    leased_until = NULL,
    lease_token = NULL,
    claim_affinity = NULLIF(sqlc.arg(instance_id)::text, ''),
    applied_at = NULL,
    updated_at = NOW()
WHERE effect.id = sqlc.arg(effect_id)
  AND effect.activation_done = TRUE
  AND effect.activation_connection_fence < sqlc.arg(connection_fence);

-- name: SkipReconnectPushTargetActivationByID :execrows
UPDATE foghorn.ingest_admission_effects AS effect
SET activation_connection_fence = GREATEST(effect.activation_connection_fence, sqlc.arg(connection_fence)),
    last_error = CONCAT_WS(' | ', NULLIF(effect.last_error, ''), sqlc.arg(error_message)::text),
    updated_at = NOW()
WHERE effect.id = sqlc.arg(effect_id)
  AND effect.activation_done = TRUE
  AND effect.activation_connection_fence < sqlc.arg(connection_fence);

-- name: ListActiveAdmissionPushTargetEffectsForUpdate :many
SELECT effect.id, effect.node_id, effect.source_generation::text AS source_generation,
       effect.push_targets, effect.state, effect.target_revision
FROM foghorn.ingest_admission_effects AS effect
WHERE effect.tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND effect.stream_internal_name = sqlc.arg(stream_internal_name)
  AND effect.state IN ('pending', 'applied', 'pending_v2', 'applied_v2')
  AND EXISTS (
      SELECT 1 FROM foghorn.ingest_sessions AS session
      WHERE session.tenant_id = effect.tenant_id
        AND session.stream_internal_name = effect.stream_internal_name
        AND session.id = effect.source_generation
        AND session.ended_at IS NULL
  )
FOR UPDATE;

-- name: RearmAdmissionPushTargetEffect :execrows
UPDATE foghorn.ingest_admission_effects
SET push_targets = sqlc.arg(push_targets),
    target_revision = sqlc.arg(target_revision),
    capacity_pending = FALSE,
    state = 'pending_v2',
    activation_done = FALSE,
    attempts = 0,
    next_attempt_at = NOW(),
    leased_until = NULL,
    lease_token = NULL,
    claim_affinity = NULL,
    applied_at = NULL,
    last_error = NULL,
    updated_at = NOW()
WHERE id = sqlc.arg(effect_id)
  AND state IN ('pending', 'applied', 'pending_v2', 'applied_v2');

-- name: StoreAdmissionPushTargetRevision :exec
INSERT INTO foghorn.admission_push_target_revisions
    (tenant_id, stream_internal_name, node_id, source_generation, target_revision, activation_attempt, push_targets)
VALUES (sqlc.arg(tenant_id)::text::uuid, sqlc.arg(stream_internal_name), sqlc.arg(node_id),
        sqlc.arg(source_generation)::text::uuid, sqlc.arg(target_revision), sqlc.arg(activation_attempt)::text::uuid, sqlc.arg(push_targets))
ON CONFLICT (source_generation, target_revision) DO NOTHING;

-- name: RepairAdmissionPushTargetRevision :execrows
UPDATE foghorn.admission_push_target_revisions
SET push_targets = sqlc.arg(push_targets),
    activation_attempt = sqlc.arg(activation_attempt)::text::uuid,
    mist_push_ids = '{}'::jsonb,
    created_at = NOW()
WHERE source_generation = sqlc.arg(source_generation)::text::uuid
  AND target_revision = sqlc.arg(target_revision);

-- name: BindAdmissionPushTargetMistID :execrows
UPDATE foghorn.admission_push_target_revisions
SET mist_push_ids = jsonb_set(
        mist_push_ids,
        ARRAY[sqlc.arg(target_id)::text],
        to_jsonb(sqlc.arg(mist_push_id)::bigint),
        TRUE
    )
WHERE node_id = sqlc.arg(node_id)
  AND source_generation = sqlc.arg(source_generation)::text::uuid
  AND target_revision = sqlc.arg(target_revision)
  AND activation_attempt = COALESCE(NULLIF(sqlc.arg(activation_attempt), '')::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
  AND sqlc.arg(mist_push_id)::bigint > 0;

-- name: BindAdmissionPushTargetMistIDIfAbsent :execrows
UPDATE foghorn.admission_push_target_revisions
SET mist_push_ids = jsonb_set(
        mist_push_ids,
        ARRAY[sqlc.arg(target_id)::text],
        to_jsonb(sqlc.arg(mist_push_id)::bigint),
        TRUE
    )
WHERE node_id = sqlc.arg(node_id)
  AND source_generation = sqlc.arg(source_generation)::text::uuid
  AND target_revision = sqlc.arg(target_revision)
  AND activation_attempt = COALESCE(NULLIF(sqlc.arg(activation_attempt), '')::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
  AND sqlc.arg(mist_push_id)::bigint > 0
  AND COALESCE(NULLIF(mist_push_ids ->> sqlc.arg(target_id)::text, '')::bigint, 0) <= 0;

-- name: RecordAdmissionPushTargetDispatch :execrows
UPDATE foghorn.admission_push_target_revisions
SET push_targets = sqlc.arg(push_targets),
    created_at = NOW()
WHERE source_generation = sqlc.arg(source_generation)::text::uuid
  AND target_revision = sqlc.arg(target_revision)
  AND activation_attempt = NULLIF(sqlc.arg(activation_attempt)::text, '')::uuid;

-- name: GetAdmissionPushTargetsForStatus :one
SELECT history.tenant_id::text AS tenant_id,
       history.stream_internal_name,
       history.node_id,
       history.push_targets,
       history.mist_push_ids,
       history.target_revision,
       history.activation_attempt::text AS activation_attempt,
       (SELECT COALESCE(max(latest.target_revision), history.target_revision)::bigint
        FROM foghorn.admission_push_target_revisions AS latest
        WHERE latest.source_generation = history.source_generation) AS latest_target_revision,
       EXISTS (
           SELECT 1
           FROM foghorn.ingest_sessions AS session
           WHERE session.id = history.source_generation
             AND session.tenant_id = history.tenant_id
             AND session.ended_at IS NULL
       ) AS source_live
FROM foghorn.admission_push_target_revisions AS history
WHERE history.tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND history.source_generation = sqlc.arg(source_generation)::text::uuid
  AND history.target_revision = sqlc.arg(target_revision)
  AND history.activation_attempt = COALESCE(NULLIF(sqlc.arg(activation_attempt), '')::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
ORDER BY history.id DESC
LIMIT 1;

-- name: GetAdmissionPushTargetsForTeardown :one
SELECT history.tenant_id::text AS tenant_id,
       history.stream_internal_name,
       history.node_id,
       history.push_targets,
       history.mist_push_ids,
       history.target_revision,
       history.activation_attempt::text AS activation_attempt
FROM foghorn.admission_push_target_revisions AS history
WHERE history.source_generation = sqlc.arg(source_generation)::text::uuid
  AND history.node_id = sqlc.arg(node_id)
  AND (sqlc.arg(target_revision)::bigint = 0 OR history.target_revision = sqlc.arg(target_revision)::bigint)
  AND (NULLIF(sqlc.arg(activation_attempt)::text, '') IS NULL
       OR history.activation_attempt = NULLIF(sqlc.arg(activation_attempt)::text, '')::uuid)
ORDER BY history.target_revision DESC, history.id DESC
LIMIT 1;

-- name: GetAdmissionTargetRevision :one
SELECT target_revision
FROM foghorn.admission_push_target_revisions
WHERE source_generation = sqlc.arg(source_generation)::text::uuid
  AND node_id = sqlc.arg(node_id)
ORDER BY target_revision DESC, id DESC
LIMIT 1;

-- name: GetAdmissionPushTargetRuntimeRearmForUpdate :one
SELECT effect.id, effect.push_targets
FROM foghorn.ingest_admission_effects AS effect
WHERE effect.tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND effect.stream_internal_name = sqlc.arg(stream_internal_name)
  AND effect.source_generation = sqlc.arg(source_generation)::text::uuid
  AND effect.target_revision = sqlc.arg(target_revision)
  AND effect.push_targets IS NOT NULL
  AND effect.state IN ('pending_v2', 'applied_v2')
  AND (effect.attempts < 12 OR effect.updated_at <= NOW() - INTERVAL '5 minutes')
  AND EXISTS (
      SELECT 1 FROM foghorn.ingest_sessions AS session
      WHERE session.id = effect.source_generation
        AND session.tenant_id = effect.tenant_id
        AND session.ended_at IS NULL
  )
FOR UPDATE;

-- name: RearmAdmissionPushTargetsAfterRuntimeEnd :execrows
UPDATE foghorn.ingest_admission_effects AS effect
SET state = 'pending_v2', activation_done = FALSE,
	push_targets = sqlc.arg(push_targets),
	attempts = CASE WHEN effect.updated_at <= NOW() - INTERVAL '5 minutes' THEN 0 ELSE effect.attempts END,
	next_attempt_at = NOW() + LEAST(
		INTERVAL '5 minutes',
		INTERVAL '1 second' * power(2, LEAST(CASE WHEN effect.updated_at <= NOW() - INTERVAL '5 minutes' THEN 0 ELSE effect.attempts END, 8))
	),
    leased_until = NULL, lease_token = NULL,
    claim_affinity = NULL, applied_at = NULL, last_error = NULL, updated_at = NOW()
WHERE effect.tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND effect.stream_internal_name = sqlc.arg(stream_internal_name)
  AND effect.source_generation = sqlc.arg(source_generation)::text::uuid
  AND effect.target_revision = sqlc.arg(target_revision)
  AND effect.push_targets IS NOT NULL
  AND effect.state IN ('pending_v2', 'applied_v2')
	AND (effect.attempts < 12 OR effect.updated_at <= NOW() - INTERVAL '5 minutes')
  AND EXISTS (
      SELECT 1 FROM foghorn.ingest_sessions AS session
      WHERE session.id = effect.source_generation
        AND session.tenant_id = effect.tenant_id
        AND session.ended_at IS NULL
	  );

-- name: RotateAdmissionPushTargetRuntimeAttempt :execrows
UPDATE foghorn.admission_push_target_revisions
SET push_targets = sqlc.arg(push_targets),
    activation_attempt = sqlc.arg(activation_attempt)::text::uuid,
    mist_push_ids = '{}'::jsonb,
    created_at = NOW()
WHERE source_generation = sqlc.arg(source_generation)::text::uuid
  AND target_revision = sqlc.arg(target_revision);

-- name: ListCapacityPendingPushTargetEffectsForRearm :many
SELECT effect.id, effect.tenant_id::text AS tenant_id, effect.stream_internal_name,
       effect.source_generation::text AS source_generation, effect.target_revision,
       effect.push_targets
FROM foghorn.ingest_admission_effects AS effect
WHERE effect.capacity_pending
  AND effect.activation_done
  AND effect.push_targets IS NOT NULL
  AND effect.target_revision > 0
  AND effect.next_attempt_at <= NOW()
  AND effect.state IN ('pending', 'applied', 'pending_v2', 'applied_v2')
	AND (effect.attempts < 12 OR effect.updated_at <= NOW() - INTERVAL '5 minutes')
  AND EXISTS (
      SELECT 1 FROM foghorn.ingest_sessions AS session
      WHERE session.id = effect.source_generation
        AND session.tenant_id = effect.tenant_id
        AND session.ended_at IS NULL
  )
ORDER BY effect.next_attempt_at, effect.id
LIMIT 100
FOR UPDATE;

-- name: RearmCapacityPendingPushTargetEffectByID :execrows
UPDATE foghorn.ingest_admission_effects AS effect
SET state = CASE WHEN effect.state IN ('pending_v2', 'applied_v2') THEN 'pending_v2' ELSE 'pending' END,
    push_targets = sqlc.arg(push_targets), activation_done = FALSE,
    attempts = CASE WHEN effect.updated_at <= NOW() - INTERVAL '5 minutes' THEN 0 ELSE effect.attempts END,
    next_attempt_at = NOW() + LEAST(
        INTERVAL '5 minutes',
        INTERVAL '1 second' * power(2, LEAST(CASE WHEN effect.updated_at <= NOW() - INTERVAL '5 minutes' THEN 0 ELSE effect.attempts END, 8))
    ),
    leased_until = NULL, lease_token = NULL, claim_affinity = NULL,
    applied_at = NULL, updated_at = NOW()
WHERE effect.id = sqlc.arg(effect_id)
  AND effect.capacity_pending
  AND effect.activation_done;

-- name: TryAcquireRestreamCapacityRearmLock :one
SELECT pg_try_advisory_xact_lock(hashtext('foghorn_restream_capacity_rearm')) AS acquired;

-- name: ListCooledDownRuntimePushTargetEffectsForRearm :many
SELECT effect.id, effect.tenant_id::text AS tenant_id, effect.stream_internal_name,
       effect.source_generation::text AS source_generation, effect.target_revision,
       effect.push_targets
FROM foghorn.ingest_admission_effects AS effect
WHERE effect.state = 'applied_v2'
  AND effect.activation_done
  AND NOT effect.capacity_pending
  AND effect.push_targets IS NOT NULL
  AND effect.target_revision > 0
  AND effect.attempts >= 12
  AND effect.updated_at <= NOW() - INTERVAL '5 minutes'
  AND EXISTS (
      SELECT 1 FROM foghorn.ingest_sessions AS session
      WHERE session.id = effect.source_generation
        AND session.tenant_id = effect.tenant_id
        AND session.ended_at IS NULL
  )
ORDER BY effect.updated_at, effect.id
LIMIT 100
FOR UPDATE;

-- name: RearmCooledDownRuntimePushTargetEffectByID :execrows
UPDATE foghorn.ingest_admission_effects AS effect
SET state = 'pending_v2', push_targets = sqlc.arg(push_targets), activation_done = FALSE, attempts = 0,
    next_attempt_at = NOW(), leased_until = NULL, lease_token = NULL,
    claim_affinity = NULL, applied_at = NULL, last_error = NULL, updated_at = NOW()
WHERE effect.id = sqlc.arg(effect_id)
  AND effect.state = 'applied_v2'
  AND effect.activation_done
  AND NOT effect.capacity_pending;

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

-- name: PurgeAdmissionPushTargetRevisions :execrows
DELETE FROM foghorn.admission_push_target_revisions
WHERE id IN (
    SELECT history.id
    FROM foghorn.admission_push_target_revisions AS history
    WHERE history.created_at < NOW() - (sqlc.arg(older_than_ms)::bigint * INTERVAL '1 millisecond')
      AND NOT EXISTS (
          SELECT 1 FROM foghorn.ingest_sessions AS session
          WHERE session.id = history.source_generation
            AND session.tenant_id = history.tenant_id
            AND session.ended_at IS NULL
      )
    ORDER BY history.created_at LIMIT 1000
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
