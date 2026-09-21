-- name: AllocateMediaAuthorityVersion :one
INSERT INTO commodore.media_authority_counters (
    authority_kind, authority_id, last_version, updated_at
) VALUES (
    sqlc.arg(authority_kind), sqlc.arg(authority_id), 1, NOW()
)
ON CONFLICT (authority_kind, authority_id) DO UPDATE
SET last_version = commodore.media_authority_counters.last_version + 1,
    updated_at = NOW()
RETURNING last_version;

-- name: BeginMediaAuthorityCompile :one
INSERT INTO commodore.media_authority_compile_fences (scope_key, generation, updated_at)
VALUES (sqlc.arg(scope_key), 1, NOW())
ON CONFLICT (scope_key) DO UPDATE
SET generation = commodore.media_authority_compile_fences.generation + 1,
    updated_at = NOW()
RETURNING generation;

-- name: LockMediaAuthorityCompile :one
SELECT generation
FROM commodore.media_authority_compile_fences
WHERE scope_key = sqlc.arg(scope_key)
FOR UPDATE;

-- name: InsertMediaAuthorityVersion :exec
INSERT INTO commodore.media_authority_versions (
    authority_kind, authority_id, authority_version, payload_schema_version,
    payload, payload_sha256, content_digest, dependents_digest, tombstone, source_revisions,
    issued_at, refresh_after, valid_until
) VALUES (
    sqlc.arg(authority_kind), sqlc.arg(authority_id), sqlc.arg(authority_version),
    sqlc.arg(payload_schema_version), sqlc.arg(payload), sqlc.arg(payload_sha256),
    sqlc.arg(content_digest), sqlc.narg(dependents_digest), sqlc.arg(tombstone),
    sqlc.arg(source_revisions), sqlc.arg(issued_at), sqlc.arg(refresh_after), sqlc.arg(valid_until)
);

-- name: GetCurrentMediaAuthorityPublication :one
SELECT versions.authority_version, versions.content_digest, versions.dependents_digest,
       versions.issued_at, versions.refresh_after, versions.valid_until
FROM commodore.media_authority_current AS current
JOIN commodore.media_authority_versions AS versions
  ON versions.authority_kind = current.authority_kind
 AND versions.authority_id = current.authority_id
 AND versions.authority_version = current.authority_version
WHERE current.authority_kind = sqlc.arg(authority_kind)
  AND current.authority_id = sqlc.arg(authority_id);

-- name: UpsertCurrentMediaAuthority :execrows
INSERT INTO commodore.media_authority_current (
    authority_kind, authority_id, authority_version, updated_at
) VALUES (
    sqlc.arg(authority_kind), sqlc.arg(authority_id), sqlc.arg(authority_version), NOW()
)
ON CONFLICT (authority_kind, authority_id) DO UPDATE
SET authority_version = EXCLUDED.authority_version,
    updated_at = NOW()
WHERE commodore.media_authority_current.authority_version < EXCLUDED.authority_version;

-- name: EnqueueMediaAuthorityDelivery :execrows
INSERT INTO commodore.media_authority_deliveries (
    authority_kind, authority_id, authority_version, cell_id, signed_envelope, short_lease, correction_until
) VALUES (
    sqlc.arg(authority_kind), sqlc.arg(authority_id), sqlc.arg(authority_version),
    sqlc.arg(cell_id), sqlc.arg(signed_envelope), sqlc.arg(short_lease), sqlc.narg(correction_until)
)
ON CONFLICT (authority_kind, authority_id, authority_version, cell_id) DO NOTHING;

-- name: UpsertMediaAuthorityTarget :exec
INSERT INTO commodore.media_authority_targets (
    authority_kind, authority_id, cell_id, highest_targeted_version,
    first_targeted_at, last_targeted_at
) VALUES (
    sqlc.arg(authority_kind), sqlc.arg(authority_id), sqlc.arg(cell_id),
    sqlc.arg(authority_version), NOW(), NOW()
)
ON CONFLICT (authority_kind, authority_id, cell_id) DO UPDATE
SET highest_targeted_version = GREATEST(
        commodore.media_authority_targets.highest_targeted_version,
        EXCLUDED.highest_targeted_version
    ),
    last_targeted_at = NOW();

-- name: SupersedeOlderMediaAuthorityDeliveries :execrows
UPDATE commodore.media_authority_deliveries
SET status = 'superseded', lease_expires_at = NULL,
    last_error = 'superseded by a newer authority version', updated_at = NOW()
WHERE authority_kind = sqlc.arg(authority_kind)
  AND authority_id = sqlc.arg(authority_id)
  AND authority_version < sqlc.arg(authority_version)
  AND (
      status = 'pending'
      OR (status = 'delivering' AND (lease_expires_at IS NULL OR lease_expires_at <= NOW()))
  );

-- name: ListMediaAuthorityDeliveryCells :many
-- The cells that have an ordinary delivery waiting, found by stepping through
-- the per-cell index from one cell to the next instead of reading the queue.
WITH RECURSIVE cells AS (
    (SELECT queued.cell_id FROM commodore.media_authority_deliveries AS queued
     WHERE NOT queued.short_lease AND queued.status IN ('pending', 'delivering')
     ORDER BY queued.cell_id LIMIT 1)
    UNION ALL
    SELECT (SELECT queued.cell_id FROM commodore.media_authority_deliveries AS queued
            WHERE NOT queued.short_lease AND queued.status IN ('pending', 'delivering')
              AND queued.cell_id > cells.cell_id
            ORDER BY queued.cell_id LIMIT 1)
    FROM cells WHERE cells.cell_id IS NOT NULL
)
SELECT cell_id::text AS cell_id FROM cells WHERE cell_id IS NOT NULL;

-- name: ClaimMediaAuthorityDeliveries :many
-- Deliveries are claimed one cell at a time, each cell with its own workers, so
-- a cell that is slow or down cannot take the workers another cell needs. Within
-- a cell, a delivery the cell asked to have repeated waits behind fresh ones: a
-- cell catching up after losing its database must not hold back a change or a
-- revocation. A version past its validity is never sent; the cell would refuse
-- it, and it has nothing left to say.
WITH candidates AS (
    SELECT queued.authority_kind, queued.authority_id, queued.authority_version, queued.cell_id
    FROM commodore.media_authority_deliveries AS queued
    JOIN commodore.media_authority_current AS current
      ON current.authority_kind = queued.authority_kind
     AND current.authority_id = queued.authority_id
     AND current.authority_version = queued.authority_version
    JOIN commodore.media_authority_versions AS versions
      ON versions.authority_kind = queued.authority_kind
     AND versions.authority_id = queued.authority_id
     AND versions.authority_version = queued.authority_version
    WHERE queued.cell_id = sqlc.arg(cell_id)
      AND LEAST(versions.valid_until, queued.correction_until) > NOW()
      AND queued.status IN ('pending', 'delivering')
      AND NOT queued.short_lease
      AND queued.next_attempt_at <= NOW()
      AND (queued.lease_expires_at IS NULL OR queued.lease_expires_at <= NOW())
      AND NOT EXISTS (
          SELECT 1
          FROM commodore.media_authority_deliveries AS inflight
          WHERE inflight.authority_kind = queued.authority_kind
            AND inflight.authority_id = queued.authority_id
            AND inflight.cell_id = queued.cell_id
            AND inflight.status = 'delivering'
            AND inflight.lease_expires_at > NOW()
      )
    ORDER BY queued.replay, queued.next_attempt_at, queued.created_at
    LIMIT sqlc.arg(batch_size)
    FOR UPDATE OF queued SKIP LOCKED
)
UPDATE commodore.media_authority_deliveries AS delivery
SET status = 'delivering',
    attempts = delivery.attempts + 1,
    lease_expires_at = NOW() + sqlc.arg(lease_ms)::bigint * INTERVAL '1 millisecond',
    updated_at = NOW()
FROM candidates
WHERE delivery.authority_kind = candidates.authority_kind
  AND delivery.authority_id = candidates.authority_id
  AND delivery.authority_version = candidates.authority_version
  AND delivery.cell_id = candidates.cell_id
RETURNING delivery.authority_kind, delivery.authority_id, delivery.authority_version,
          delivery.cell_id, delivery.signed_envelope, delivery.attempts;

-- name: ClaimMediaAuthorityDeadlineDelivery :many
-- short_lease is recorded on the delivery when it is enqueued. Deriving it here
-- from the version's validity would join the queue to the whole version
-- history on an expression no index can serve, and this claim runs every
-- second inside a one-second budget.
WITH heads AS (
    SELECT DISTINCT ON (queued.cell_id)
           queued.authority_kind, queued.authority_id, queued.authority_version,
           queued.cell_id, queued.next_attempt_at, queued.created_at
    FROM commodore.media_authority_deliveries AS queued
    JOIN commodore.media_authority_current AS current
      ON current.authority_kind = queued.authority_kind
     AND current.authority_id = queued.authority_id
     AND current.authority_version = queued.authority_version
    WHERE queued.short_lease
      AND (queued.correction_until IS NULL OR queued.correction_until > NOW())
      AND queued.status IN ('pending', 'delivering')
      AND queued.next_attempt_at <= NOW()
      AND (queued.lease_expires_at IS NULL OR queued.lease_expires_at <= NOW())
      AND NOT EXISTS (
          SELECT 1 FROM commodore.media_authority_deliveries AS inflight
          WHERE inflight.short_lease
            AND inflight.cell_id = queued.cell_id
            AND inflight.status = 'delivering' AND inflight.lease_expires_at > NOW()
      )
    ORDER BY queued.cell_id, queued.next_attempt_at, queued.created_at,
             queued.authority_kind, queued.authority_id, queued.authority_version
), candidates AS (
    SELECT queued.authority_kind, queued.authority_id, queued.authority_version, queued.cell_id
    FROM commodore.media_authority_deliveries AS queued
    JOIN heads ON heads.authority_kind = queued.authority_kind
              AND heads.authority_id = queued.authority_id
              AND heads.authority_version = queued.authority_version
              AND heads.cell_id = queued.cell_id
    WHERE queued.status IN ('pending', 'delivering')
      AND queued.next_attempt_at <= NOW()
      AND (queued.lease_expires_at IS NULL OR queued.lease_expires_at <= NOW())
    ORDER BY heads.next_attempt_at, heads.created_at, heads.cell_id
    LIMIT sqlc.arg(batch_size)
    FOR UPDATE OF queued SKIP LOCKED
)
UPDATE commodore.media_authority_deliveries AS delivery
SET status = 'delivering', attempts = delivery.attempts + 1,
    lease_expires_at = NOW() + sqlc.arg(lease_ms)::bigint * INTERVAL '1 millisecond',
    updated_at = NOW()
FROM candidates
WHERE delivery.authority_kind = candidates.authority_kind
  AND delivery.authority_id = candidates.authority_id
  AND delivery.authority_version = candidates.authority_version
  AND delivery.cell_id = candidates.cell_id
RETURNING delivery.authority_kind, delivery.authority_id, delivery.authority_version,
          delivery.cell_id, delivery.signed_envelope, delivery.attempts;

-- name: MarkMediaAuthorityDeliveryAcknowledged :execrows
UPDATE commodore.media_authority_deliveries
SET status = 'acknowledged', acknowledged_at = NOW(), lease_expires_at = NULL,
    last_error = NULL, updated_at = NOW()
WHERE authority_kind = sqlc.arg(authority_kind)
  AND authority_id = sqlc.arg(authority_id)
  AND authority_version = sqlc.arg(authority_version)
  AND cell_id = sqlc.arg(cell_id)
  AND status = 'delivering';

-- name: RecordMediaAuthorityDeliveryFailure :execrows
-- A cell that refuses the current version on a precondition (it holds a newer
-- version, a conflicting digest, or a terminal tombstone) will refuse every
-- retry of the same envelope, so that delivery settles as rejected instead of
-- returning to the queue. Cell replay re-opens it.
UPDATE commodore.media_authority_deliveries AS delivery
SET status = CASE
        WHEN delivery.authority_version < current.authority_version THEN 'superseded'
        WHEN sqlc.arg(rejected)::boolean THEN 'rejected'
        ELSE 'pending'
    END,
    next_attempt_at = sqlc.arg(next_attempt_at),
    lease_expires_at = NULL,
    last_error = CASE
        WHEN delivery.authority_version < current.authority_version
        THEN 'superseded by a newer authority version after delivery failure'
        ELSE sqlc.arg(last_error)
    END,
    updated_at = NOW()
FROM commodore.media_authority_current AS current
WHERE delivery.authority_kind = sqlc.arg(authority_kind)
  AND delivery.authority_id = sqlc.arg(authority_id)
  AND delivery.authority_version = sqlc.arg(authority_version)
  AND delivery.cell_id = sqlc.arg(cell_id)
  AND delivery.status = 'delivering'
  AND current.authority_kind = delivery.authority_kind
  AND current.authority_id = delivery.authority_id;

-- name: SupersedeExpiredObsoleteMediaAuthorityDeliveries :execrows
WITH candidates AS MATERIALIZED (
    SELECT delivery.authority_kind, delivery.authority_id,
           delivery.authority_version, delivery.cell_id
    FROM commodore.media_authority_deliveries AS delivery
    JOIN commodore.media_authority_current AS current
      ON current.authority_kind = delivery.authority_kind
     AND current.authority_id = delivery.authority_id
     AND current.authority_version > delivery.authority_version
    WHERE (
        delivery.status = 'pending'
        OR (delivery.status = 'delivering' AND (delivery.lease_expires_at IS NULL OR delivery.lease_expires_at <= NOW()))
    )
    ORDER BY delivery.updated_at, delivery.authority_kind,
             delivery.authority_id, delivery.authority_version, delivery.cell_id
    LIMIT sqlc.arg(batch_size)
    FOR UPDATE OF delivery SKIP LOCKED
)
UPDATE commodore.media_authority_deliveries AS delivery
SET status = 'superseded', lease_expires_at = NULL,
    last_error = 'superseded by a newer authority version', updated_at = NOW()
FROM candidates
WHERE delivery.authority_kind = candidates.authority_kind
  AND delivery.authority_id = candidates.authority_id
  AND delivery.authority_version = candidates.authority_version
  AND delivery.cell_id = candidates.cell_id;

-- name: UpsertMediaAuthorityDistribution :exec
INSERT INTO commodore.media_authority_distribution (
    authority_kind, authority_id, cell_id, highest_acknowledged_version,
    first_acknowledged_at, last_acknowledged_at
) VALUES (
    sqlc.arg(authority_kind), sqlc.arg(authority_id), sqlc.arg(cell_id),
    sqlc.arg(authority_version), NOW(), NOW()
)
ON CONFLICT (authority_kind, authority_id, cell_id) DO UPDATE
SET highest_acknowledged_version = GREATEST(
        commodore.media_authority_distribution.highest_acknowledged_version,
        EXCLUDED.highest_acknowledged_version
    ),
    last_acknowledged_at = NOW();

-- name: ListMediaAuthorityPriorCells :many
SELECT cell_id
FROM commodore.media_authority_targets
WHERE authority_kind = sqlc.arg(authority_kind)
  AND authority_id = sqlc.arg(authority_id)
  AND (correction_until IS NULL OR correction_until > NOW())
ORDER BY cell_id;

-- name: RetireMediaAuthorityTargets :execrows
-- Freeze the horizon on departure, including unacknowledged and restored
-- copies. Subsequent corrections cannot extend this cell's retention.
UPDATE commodore.media_authority_targets AS target
SET correction_until = CASE WHEN target.cell_id = ANY(sqlc.arg(active_cells)::text[]) THEN NULL
    ELSE COALESCE(target.correction_until, (
        SELECT MAX(LEAST(versions.valid_until, delivery.correction_until))
        FROM commodore.media_authority_deliveries AS delivery
        JOIN commodore.media_authority_versions AS versions
          USING (authority_kind, authority_id, authority_version)
        WHERE delivery.authority_kind = target.authority_kind
          AND delivery.authority_id = target.authority_id
          AND delivery.cell_id = target.cell_id
          AND delivery.authority_version <= target.highest_targeted_version
    ), NOW()) END
WHERE target.authority_kind = sqlc.arg(authority_kind)
  AND target.authority_id = sqlc.arg(authority_id)
  AND ((target.correction_until IS NULL AND NOT target.cell_id = ANY(sqlc.arg(active_cells)::text[]))
    OR (target.correction_until IS NOT NULL AND target.cell_id = ANY(sqlc.arg(active_cells)::text[])));

-- name: LockMediaAuthorityTargetHorizons :many
-- Publication keeps these rows locked through enqueue. The pruning sweep skips
-- them, so a correction recipient cannot disappear and be reinserted as active.
SELECT cell_id, correction_until
FROM commodore.media_authority_targets
WHERE authority_kind = sqlc.arg(authority_kind)
  AND authority_id = sqlc.arg(authority_id)
ORDER BY cell_id
FOR UPDATE;

-- name: DeleteRetiredMediaAuthorityTargets :execrows
WITH candidates AS MATERIALIZED (
    SELECT retired.authority_kind, retired.authority_id, retired.cell_id
    FROM commodore.media_authority_targets AS retired
    WHERE retired.correction_until <= sqlc.arg(expired_before)
    ORDER BY retired.correction_until, retired.authority_kind, retired.authority_id, retired.cell_id
    LIMIT sqlc.arg(batch_size)
    FOR UPDATE SKIP LOCKED
)
DELETE FROM commodore.media_authority_targets AS target
USING candidates
WHERE target.authority_kind = candidates.authority_kind
  AND target.authority_id = candidates.authority_id
  AND target.cell_id = candidates.cell_id;

-- name: ListActiveMediaAuthorityCells :many
SELECT cell_id FROM commodore.media_authority_targets
WHERE authority_kind = sqlc.arg(authority_kind)
  AND authority_id = sqlc.arg(authority_id)
  AND correction_until IS NULL
ORDER BY cell_id;

-- name: ListMediaAuthorityHoldingCells :many
-- Cells that may still hold a valid copy: some version between the last one
-- they acknowledged and the last one they were sent has not expired (see
-- GetMediaAuthorityValidHorizon). A change must reach them even when they are
-- no longer targets, and a cell whose every copy has expired has nothing left
-- to correct.
SELECT target.cell_id
FROM commodore.media_authority_targets AS target
LEFT JOIN commodore.media_authority_distribution AS distribution
  ON distribution.authority_kind = target.authority_kind
 AND distribution.authority_id = target.authority_id
 AND distribution.cell_id = target.cell_id
LEFT JOIN commodore.media_authority_cell_ack_resets AS reset
  ON reset.cell_id = target.cell_id
WHERE target.authority_kind = sqlc.arg(authority_kind)
  AND target.authority_id = sqlc.arg(authority_id)
  AND (target.correction_until IS NULL OR target.correction_until > NOW())
  AND EXISTS (
      SELECT 1 FROM commodore.media_authority_deliveries AS delivery
      JOIN commodore.media_authority_versions AS versions
        USING (authority_kind, authority_id, authority_version)
      WHERE delivery.authority_kind = target.authority_kind
        AND delivery.authority_id = target.authority_id
        AND delivery.cell_id = target.cell_id
        AND delivery.authority_version <= target.highest_targeted_version
        AND delivery.authority_version >= CASE
            WHEN distribution.last_acknowledged_at > COALESCE(reset.reset_at, '-infinity'::timestamptz)
            THEN distribution.highest_acknowledged_version ELSE 0 END
        AND LEAST(versions.valid_until, delivery.correction_until) > NOW()
  )
ORDER BY target.cell_id;

-- name: MarkMediaAuthorityCellAcknowledgementsUntrusted :exec
-- A cell reported holding something other than what it acknowledged. Until it
-- acknowledges again, it may hold any version it was ever sent.
INSERT INTO commodore.media_authority_cell_ack_resets (cell_id, reset_at)
VALUES (sqlc.arg(cell_id), NOW())
ON CONFLICT (cell_id) DO UPDATE SET reset_at = NOW();

-- name: EnqueueMediaAuthorityCorrectionsForCell :one
-- For a cell that does not hold what it acknowledged: every authority whose
-- current version has run out while the cell may still hold a valid older one.
-- Replay cannot resend an expired version, so these are compiled again, which
-- re-issues them (see decideMediaAuthorityPublication, correcting). Current
-- versions still valid are replayed as they are. The renewal rows index every
-- published authority; their status does not matter.
SELECT COUNT(commodore.enqueue_media_authority_obligation(
    'bulk', renewal.target_key, renewal.target_kind, renewal.tenant_id,
    'cell_holds_unacknowledged', 'commodore', 'cell-reset:' || sqlc.arg(cell_id)::text, NULL, NULL
))::bigint AS enqueued
FROM commodore.media_authority_targets AS target
JOIN commodore.media_authority_refresh_obligations AS renewal
  ON renewal.target_key = target.authority_kind || ':' || target.authority_id
 AND renewal.lane IN ('object_deadline', 'tenant_deadline')
JOIN commodore.media_authority_current AS current
  ON current.authority_kind = target.authority_kind
 AND current.authority_id = target.authority_id
JOIN commodore.media_authority_versions AS current_version
  ON current_version.authority_kind = current.authority_kind
 AND current_version.authority_id = current.authority_id
 AND current_version.authority_version = current.authority_version
WHERE target.cell_id = sqlc.arg(cell_id)::text
  AND (target.correction_until IS NULL OR target.correction_until > NOW())
  AND current_version.valid_until <= NOW()
  AND EXISTS (
      SELECT 1 FROM commodore.media_authority_versions AS held
      WHERE held.authority_kind = target.authority_kind
        AND held.authority_id = target.authority_id
        AND held.authority_version <= target.highest_targeted_version
        AND held.valid_until > NOW()
  );

-- name: RequeueCurrentMediaAuthoritiesForCell :one
-- Repeats what the cell was sent, for a cell whose database was restored or
-- rebuilt. The rows are marked as a replay, which the claim serves after fresh
-- deliveries, and a version past its validity is left alone: the cell would
-- refuse it.
WITH requeued AS (
    UPDATE commodore.media_authority_deliveries AS delivery
    SET status = 'pending', replay = TRUE, next_attempt_at = NOW(), lease_expires_at = NULL,
        last_error = NULL, updated_at = NOW()
    FROM commodore.media_authority_current AS current
    JOIN commodore.media_authority_versions AS versions
      ON versions.authority_kind = current.authority_kind
     AND versions.authority_id = current.authority_id
     AND versions.authority_version = current.authority_version
    WHERE delivery.authority_kind = current.authority_kind
      AND delivery.authority_id = current.authority_id
      AND delivery.authority_version = current.authority_version
      AND delivery.cell_id = sqlc.arg(cell_id)
      AND delivery.status IN ('acknowledged', 'rejected')
      AND LEAST(versions.valid_until, delivery.correction_until) > NOW()
    RETURNING 1
)
SELECT COUNT(*)::bigint AS requeued_count FROM requeued;

-- name: ListAcknowledgedMediaAuthoritiesForCell :many
-- Publishing a successor does not change what a cell acknowledged holding.
SELECT distribution.authority_kind, distribution.authority_id,
       distribution.highest_acknowledged_version AS authority_version
FROM commodore.media_authority_distribution AS distribution
JOIN commodore.media_authority_versions AS versions
  ON versions.authority_kind = distribution.authority_kind
 AND versions.authority_id = distribution.authority_id
 AND versions.authority_version = distribution.highest_acknowledged_version
JOIN commodore.media_authority_deliveries AS delivery
  ON delivery.authority_kind = distribution.authority_kind
 AND delivery.authority_id = distribution.authority_id
 AND delivery.authority_version = distribution.highest_acknowledged_version
 AND delivery.cell_id = distribution.cell_id
WHERE distribution.cell_id = sqlc.arg(cell_id)
  AND LEAST(versions.valid_until, delivery.correction_until) > sqlc.arg(as_of)::timestamptz
  AND (distribution.authority_kind COLLATE "C", distribution.authority_id COLLATE "C") >
      (sqlc.arg(after_kind)::text COLLATE "C", sqlc.arg(after_id)::text COLLATE "C")
ORDER BY distribution.authority_kind COLLATE "C", distribution.authority_id COLLATE "C"
LIMIT sqlc.arg(page_size);

-- name: ReconcileMediaAuthorityPage :many
-- Compare each identity with its acknowledged floor. An unrelated delivery
-- cannot hide a regression; an apply ahead of its acknowledgement is normal.
WITH held AS MATERIALIZED (
    SELECT (item.value->>'authority_kind')::text AS authority_kind,
           (item.value->>'authority_id')::text AS authority_id,
           (item.value->>'authority_version')::bigint AS authority_version
    FROM jsonb_array_elements(sqlc.arg(held)::jsonb) AS item(value)
), expected AS MATERIALIZED (
    SELECT distribution.authority_kind, distribution.authority_id,
           distribution.highest_acknowledged_version AS authority_version,
           distribution.last_acknowledged_at,
           distribution.last_acknowledged_at > COALESCE(reset.reset_at, '-infinity'::timestamptz) AS acknowledgement_trusted
    FROM commodore.media_authority_distribution AS distribution
    LEFT JOIN commodore.media_authority_cell_ack_resets AS reset ON reset.cell_id = distribution.cell_id
    JOIN commodore.media_authority_versions AS versions
      ON versions.authority_kind = distribution.authority_kind
     AND versions.authority_id = distribution.authority_id
     AND versions.authority_version = distribution.highest_acknowledged_version
    JOIN commodore.media_authority_deliveries AS acknowledged
      ON acknowledged.authority_kind = distribution.authority_kind
     AND acknowledged.authority_id = distribution.authority_id
     AND acknowledged.authority_version = distribution.highest_acknowledged_version
     AND acknowledged.cell_id = distribution.cell_id
    WHERE distribution.cell_id = sqlc.arg(cell_id)
      AND (LEAST(versions.valid_until, acknowledged.correction_until) > sqlc.arg(as_of)::timestamptz OR EXISTS (
          SELECT 1 FROM held
          WHERE held.authority_kind = distribution.authority_kind
            AND held.authority_id = distribution.authority_id
      ))
      -- A missing identity may have applied a newer, shorter-lived version
      -- whose ACK was lost. Its expired copy is absent from live inventory.
      -- An explicitly held older version is still evidence of regression.
      AND (EXISTS (
          SELECT 1 FROM held
          WHERE held.authority_kind = distribution.authority_kind
            AND held.authority_id = distribution.authority_id
      ) OR NOT EXISTS (
          SELECT 1 FROM commodore.media_authority_deliveries AS successor
          JOIN commodore.media_authority_versions AS expired
            ON expired.authority_kind = successor.authority_kind
           AND expired.authority_id = successor.authority_id
           AND expired.authority_version = successor.authority_version
          WHERE successor.cell_id = distribution.cell_id
            AND successor.authority_kind = distribution.authority_kind
            AND successor.authority_id = distribution.authority_id
            AND successor.authority_version > distribution.highest_acknowledged_version
            AND LEAST(expired.valid_until, successor.correction_until) <= sqlc.arg(as_of)::timestamptz
      ))
      AND (distribution.authority_kind COLLATE "C", distribution.authority_id COLLATE "C") >
          (sqlc.arg(after_kind)::text COLLATE "C", sqlc.arg(after_id)::text COLLATE "C")
      AND (sqlc.arg(final_page)::boolean OR
          (distribution.authority_kind COLLATE "C", distribution.authority_id COLLATE "C") <=
          (sqlc.arg(last_kind)::text COLLATE "C", sqlc.arg(last_id)::text COLLATE "C"))
), identities AS (
    SELECT authority_kind, authority_id FROM held
    UNION
    SELECT authority_kind, authority_id FROM expected
), compared AS (
SELECT identity.authority_kind, identity.authority_id,
       COALESCE(held.authority_version, 0)::bigint AS held_version,
       COALESCE(expected.authority_version > COALESCE(held.authority_version, 0)
           AND expected.acknowledgement_trusted
           AND expected.last_acknowledged_at < sqlc.arg(acknowledged_before)::timestamptz, FALSE)::boolean AS regressed,
       COALESCE(expected.authority_version > COALESCE(held.authority_version, 0)
           AND (NOT expected.acknowledgement_trusted OR expected.last_acknowledged_at >= sqlc.arg(acknowledged_before)::timestamptz), FALSE)::boolean AS raced,
       (held.authority_id IS NULL OR delivered.authority_id IS NOT NULL)::boolean AS known,
       COALESCE(current.authority_version = held.authority_version
           AND LEAST(versions.valid_until, delivered.correction_until) > NOW() AND delivered.authority_id IS NOT NULL, FALSE)::boolean AS confirmed
FROM identities AS identity
LEFT JOIN held USING (authority_kind, authority_id)
LEFT JOIN expected USING (authority_kind, authority_id)
LEFT JOIN commodore.media_authority_current AS current USING (authority_kind, authority_id)
LEFT JOIN commodore.media_authority_versions AS versions
  ON versions.authority_kind = identity.authority_kind
 AND versions.authority_id = identity.authority_id
 AND versions.authority_version = current.authority_version
LEFT JOIN commodore.media_authority_deliveries AS delivered
  ON delivered.authority_kind = identity.authority_kind
 AND delivered.authority_id = identity.authority_id
 AND delivered.authority_version = held.authority_version
 AND delivered.cell_id = sqlc.arg(cell_id)
)
SELECT authority_kind, authority_id, held_version, regressed, raced, known, confirmed
FROM compared WHERE held_version > 0
UNION ALL
SELECT ''::text, ''::text, 0::bigint,
       COALESCE(bool_or(regressed), FALSE)::boolean,
       COALESCE(bool_or(raced), FALSE)::boolean,
       TRUE::boolean, FALSE::boolean
FROM compared WHERE held_version = 0;

-- name: MediaAuthorityRecoveryTime :one
SELECT clock_timestamp()::timestamptz AS observed_at;

-- name: SettleExpiredMediaAuthorityDeliveries :execrows
-- A delivery whose version ran out before it could be made has nothing left to
-- say and would only be refused. It leaves the queue.
WITH candidates AS MATERIALIZED (
    SELECT delivery.authority_kind, delivery.authority_id, delivery.authority_version, delivery.cell_id
    FROM commodore.media_authority_deliveries AS delivery
    JOIN commodore.media_authority_versions AS versions
      ON versions.authority_kind = delivery.authority_kind
     AND versions.authority_id = delivery.authority_id
     AND versions.authority_version = delivery.authority_version
    WHERE delivery.status IN ('pending', 'delivering')
      AND delivery.next_attempt_at <= NOW()
      AND (delivery.lease_expires_at IS NULL OR delivery.lease_expires_at <= NOW())
      AND LEAST(versions.valid_until, delivery.correction_until) <= NOW()
    ORDER BY delivery.next_attempt_at, delivery.created_at
    LIMIT sqlc.arg(batch_size)
    FOR UPDATE OF delivery SKIP LOCKED
)
UPDATE commodore.media_authority_deliveries AS delivery
SET status = 'superseded', lease_expires_at = NULL,
    last_error = 'expired before it was delivered', updated_at = NOW()
FROM candidates
WHERE delivery.authority_kind = candidates.authority_kind
  AND delivery.authority_id = candidates.authority_id
  AND delivery.authority_version = candidates.authority_version
  AND delivery.cell_id = candidates.cell_id;

-- name: ListMediaAuthorityDeliveryStats :many
-- Reads the deliveries that are not settled, through their partial indexes, and
-- nothing else: a delivery that was acknowledged has no backlog, no age, and no
-- version lag to report, and those are nearly all of them.
WITH current_deliveries AS MATERIALIZED (
    SELECT current.authority_kind, current.authority_id, current.authority_version,
           delivery.cell_id, delivery.status, delivery.created_at
    FROM commodore.media_authority_deliveries AS delivery
    JOIN commodore.media_authority_current AS current
      ON current.authority_kind = delivery.authority_kind
     AND current.authority_id = delivery.authority_id
     AND current.authority_version = delivery.authority_version
    WHERE delivery.status IN ('pending', 'delivering', 'rejected')
      AND (delivery.status <> 'rejected' OR EXISTS (
          SELECT 1 FROM commodore.media_authority_actionable_rejections AS actionable
          WHERE actionable.authority_kind = delivery.authority_kind
            AND actionable.authority_id = delivery.authority_id
            AND actionable.authority_version = delivery.authority_version
            AND actionable.cell_id = delivery.cell_id))
)
SELECT current.authority_kind,
       COUNT(*) FILTER (WHERE current.status IN ('pending', 'delivering'))::bigint AS pending_count,
       COUNT(*) FILTER (WHERE current.status = 'rejected')::bigint AS rejected_count,
       COALESCE(MAX(
           current.authority_version - COALESCE(distribution.highest_acknowledged_version, 0)
       ), 0)::bigint AS max_version_lag,
       COALESCE(MAX(
           CASE WHEN current.status IN ('pending', 'delivering')
                THEN EXTRACT(EPOCH FROM (NOW() - current.created_at))
                ELSE 0 END
       ), 0)::double precision AS oldest_pending_seconds
FROM current_deliveries AS current
LEFT JOIN commodore.media_authority_distribution AS distribution
  ON distribution.authority_kind = current.authority_kind
 AND distribution.authority_id = current.authority_id
 AND distribution.cell_id = current.cell_id
GROUP BY current.authority_kind
ORDER BY current.authority_kind;

-- name: DeleteCompletedMediaAuthorityRefreshInbox :execrows
WITH candidates AS (
    SELECT queued.source_service, queued.source_event_id
    FROM commodore.media_authority_refresh_inbox AS queued
    WHERE queued.status = 'completed'
      AND queued.completed_at < sqlc.arg(completed_before)::timestamptz
    ORDER BY queued.completed_at, queued.source_service, queued.source_event_id
    LIMIT sqlc.arg(batch_size)
    FOR UPDATE SKIP LOCKED
)
DELETE FROM commodore.media_authority_refresh_inbox AS inbox
USING candidates
WHERE inbox.source_service = candidates.source_service
  AND inbox.source_event_id = candidates.source_event_id;

-- name: DeleteExpiredMediaAuthorityDeliveries :execrows
WITH candidates AS MATERIALIZED (
    SELECT version.authority_kind, version.authority_id, version.authority_version
    FROM commodore.media_authority_versions AS version
    LEFT JOIN commodore.media_authority_current AS current
      ON current.authority_kind = version.authority_kind
     AND current.authority_id = version.authority_id
     AND current.authority_version = version.authority_version
    WHERE version.valid_until < sqlc.arg(expired_before)
      AND current.authority_id IS NULL
    ORDER BY version.valid_until, version.authority_kind,
             version.authority_id, version.authority_version
    LIMIT sqlc.arg(batch_size)
)
DELETE FROM commodore.media_authority_deliveries AS delivery
USING candidates
WHERE delivery.authority_kind = candidates.authority_kind
  AND delivery.authority_id = candidates.authority_id
  AND delivery.authority_version = candidates.authority_version
  AND delivery.status IN ('acknowledged', 'superseded', 'rejected');

-- name: DeleteOrphanedMediaAuthorityVersions :execrows
WITH candidates AS MATERIALIZED (
    SELECT version.authority_kind, version.authority_id, version.authority_version
    FROM commodore.media_authority_versions AS version
    LEFT JOIN commodore.media_authority_current AS current
      ON current.authority_kind = version.authority_kind
     AND current.authority_id = version.authority_id
     AND current.authority_version = version.authority_version
    WHERE version.valid_until < sqlc.arg(expired_before)
      AND current.authority_id IS NULL
    ORDER BY version.valid_until, version.authority_kind,
             version.authority_id, version.authority_version
    LIMIT sqlc.arg(batch_size)
)
DELETE FROM commodore.media_authority_versions AS version
USING candidates
WHERE version.authority_kind = candidates.authority_kind
  AND version.authority_id = candidates.authority_id
  AND version.authority_version = candidates.authority_version
  AND NOT EXISTS (
      SELECT 1
      FROM commodore.media_authority_deliveries AS delivery
      WHERE delivery.authority_kind = version.authority_kind
        AND delivery.authority_id = version.authority_id
        AND delivery.authority_version = version.authority_version
  );

-- name: EnqueueMediaAuthorityObligation :exec
-- A NULL next_attempt_at means due now by the database clock, which is the
-- clock the claim compares against. An event stamped with the caller's clock
-- would not be claimable until any skew between the two had passed.
SELECT commodore.enqueue_media_authority_obligation(
    sqlc.arg(lane)::text, sqlc.arg(target_key)::text, sqlc.arg(target_kind)::text,
    sqlc.arg(tenant_id)::uuid, sqlc.arg(reason)::text, sqlc.arg(source_service)::text,
    sqlc.arg(source_event_id)::text, sqlc.narg(next_attempt_at)::timestamptz,
    sqlc.narg(bound_version)::bigint
);

-- name: EnsureMediaAuthorityRenewal :exec
-- Gives a live authority a renewal obligation when it has none or only a
-- settled one. A pending, processing, or parked renewal is left exactly as it
-- is: this is the recovery path, not a way to move a schedule. A dormant
-- renewal belongs to an object nobody uses; it is revived only when the caller
-- found the object in use again.
INSERT INTO commodore.media_authority_refresh_obligations AS obligation (
    target_key, lane, tenant_id, target_kind, bound_version, next_attempt_at, expires_at,
    last_reason, last_source_service, last_source_event_id
) VALUES (
    sqlc.arg(target_key)::text, sqlc.arg(lane), sqlc.arg(tenant_id)::uuid, sqlc.arg(target_kind),
    sqlc.arg(bound_version), sqlc.arg(next_attempt_at), sqlc.arg(expires_at), 'renewal', 'commodore',
    'renewal:' || sqlc.arg(target_key)::text
)
ON CONFLICT (target_key, lane) DO UPDATE SET
    revision = obligation.revision + 1,
    status = 'pending',
    attempts = 0,
    bound_version = EXCLUDED.bound_version,
    next_attempt_at = EXCLUDED.next_attempt_at,
    expires_at = EXCLUDED.expires_at,
    pending_since = NOW(),
    lease_expires_at = NULL,
    park_reason = NULL,
    last_error = NULL,
    updated_at = NOW()
WHERE obligation.status = 'completed'
   OR (obligation.status = 'dormant' AND sqlc.arg(revive_dormant)::boolean);

-- name: SettleDormantMediaAuthorityObligation :execrows
-- The renewal of an object nobody uses. Dormant is not claimable, is not
-- re-armed by reconciliation, and still counts as the authority's renewal, so
-- nothing schedules another one. Use or the next publication revives it.
UPDATE commodore.media_authority_refresh_obligations
SET status = 'dormant', lease_expires_at = NULL, claim_token = NULL, last_error = NULL, updated_at = NOW()
WHERE target_key = sqlc.arg(target_key)
  AND lane = sqlc.arg(lane)
  AND revision = sqlc.arg(revision)
  AND claim_token::text = sqlc.arg(claim_token)::text
  AND status = 'processing';

-- name: ReviveDormantMediaAuthorityRenewal :execrows
-- Use puts an authority's renewal back in the queue. A dormant renewal is made
-- due now. A renewal being compiled is folded like any other change: its
-- revision moves, so the compile in flight cannot settle it dormant, and its
-- lease is kept so it is not compiled twice at once. The lane doing the compile
-- that recorded the use is left alone, or it would fold itself on every run.
UPDATE commodore.media_authority_refresh_obligations
SET status = 'pending', revision = revision + 1, attempts = 0,
    next_attempt_at = CASE WHEN status IN ('dormant', 'completed') THEN NOW() ELSE next_attempt_at END,
    pending_since = CASE WHEN status IN ('dormant', 'completed') THEN NOW() ELSE pending_since END,
    updated_at = NOW()
WHERE target_key = sqlc.arg(target_key)
  AND lane IN ('object_deadline', 'tenant_deadline')
  AND lane <> sqlc.arg(compiling_lane)::text
  AND status IN ('dormant', 'processing', 'completed')
  AND (status <> 'completed' OR EXISTS (
      SELECT 1 FROM commodore.media_authority_current AS current
      JOIN commodore.media_authority_versions AS versions
        ON versions.authority_kind = current.authority_kind
       AND versions.authority_id = current.authority_id
       AND versions.authority_version = current.authority_version
      WHERE current.authority_kind = split_part(sqlc.arg(target_key)::text, ':', 1)
        AND current.authority_id = substr(sqlc.arg(target_key)::text, strpos(sqlc.arg(target_key)::text, ':') + 1)
        AND NOT versions.tombstone
  ));

-- name: RecordMediaAuthorityUse :execrows
-- Use is kept to the day: a row is written at most once a day per authority
-- however often it is decided on. One row means the day advanced.
INSERT INTO commodore.media_authority_use AS used (authority_kind, authority_id, tenant_id, last_used_at)
VALUES (sqlc.arg(authority_kind), sqlc.arg(authority_id), sqlc.arg(tenant_id)::uuid, NOW())
ON CONFLICT (authority_kind, authority_id) DO UPDATE SET
    last_used_at = NOW(), tenant_id = EXCLUDED.tenant_id
WHERE used.last_used_at < NOW() - INTERVAL '1 day';

-- name: GetMediaAuthorityValidHorizon :one
-- The latest instant a copy some cell may hold is valid until. A replacement can
-- be shorter-lived than what it replaced, and a cell that never received the
-- replacement still holds the longer one, so the current version alone does
-- not decide it. A cell holds at least the version it last acknowledged (its
-- fence refuses anything older) and at most the last one it was sent, so only
-- those versions count; once every cell has acknowledged the current version,
-- the horizon is the current version's own validity. Versions are kept well
-- past their validity, so none that matters is missing.
SELECT COALESCE(MAX(copies.valid_until), 'epoch'::timestamptz)::timestamptz AS valid_until
FROM (
    SELECT versions.valid_until
    FROM commodore.media_authority_current AS current
    JOIN commodore.media_authority_versions AS versions USING (authority_kind, authority_id, authority_version)
    WHERE current.authority_kind = sqlc.arg(authority_kind)
      AND current.authority_id = sqlc.arg(authority_id)
    UNION ALL
    SELECT LEAST(versions.valid_until, delivery.correction_until)
    FROM commodore.media_authority_targets AS target
    JOIN commodore.media_authority_deliveries AS delivery USING (authority_kind, authority_id, cell_id)
    JOIN commodore.media_authority_versions AS versions USING (authority_kind, authority_id, authority_version)
    LEFT JOIN commodore.media_authority_distribution AS distribution
      ON distribution.authority_kind = target.authority_kind
     AND distribution.authority_id = target.authority_id
     AND distribution.cell_id = target.cell_id
    LEFT JOIN commodore.media_authority_cell_ack_resets AS reset ON reset.cell_id = target.cell_id
    WHERE target.authority_kind = sqlc.arg(authority_kind)
      AND target.authority_id = sqlc.arg(authority_id)
      AND delivery.authority_version <= target.highest_targeted_version
      AND delivery.authority_version >= CASE
          WHEN distribution.last_acknowledged_at > COALESCE(reset.reset_at, '-infinity'::timestamptz)
          THEN distribution.highest_acknowledged_version ELSE 0 END
) AS copies;

-- name: GetMediaAuthorityLastUse :one
-- An authority with no use row counts as used when use started being recorded.
SELECT COALESCE(used.last_used_at, epoch.started_at)::timestamptz AS last_used_at
FROM commodore.media_authority_use_epoch AS epoch
LEFT JOIN commodore.media_authority_use AS used
  ON used.authority_kind = sqlc.arg(authority_kind)
 AND used.authority_id = sqlc.arg(authority_id);

-- name: RearmMediaAuthorityObligationsAwaitingTenant :execrows
UPDATE commodore.media_authority_refresh_obligations
SET status = 'pending', park_reason = NULL, attempts = 0, revision = revision + 1,
    next_attempt_at = NOW(), pending_since = NOW(), updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND status = 'parked'
  AND park_reason = 'awaiting_tenant_authority';

-- name: EnqueueTenantMediaObjectRefreshes :one
-- Refreshes the tenant's objects that some cell may still hold a valid copy of:
-- any version of the object is valid, and the current one is not a tombstone.
-- The current version alone does not decide it: a shorter-lived replacement can
-- run out while a cell that never received it still holds the longer original.
-- An object with no valid copy anywhere has nothing to correct and is compiled
-- when it is next used. The renewal rows are the tenant's index into its
-- published objects; their status does not matter.
SELECT COUNT(commodore.enqueue_media_authority_obligation(
    'bulk', renewal.target_key, renewal.target_kind, renewal.tenant_id,
    sqlc.arg(reason)::text, 'commodore', sqlc.arg(source_event_id)::text, NULL, NULL
))::bigint AS enqueued
FROM commodore.media_authority_refresh_obligations AS renewal
JOIN commodore.media_authority_current AS current
  ON current.authority_kind = split_part(renewal.target_key, ':', 1)
 AND current.authority_id = substr(renewal.target_key, strpos(renewal.target_key, ':') + 1)
JOIN commodore.media_authority_versions AS versions
  ON versions.authority_kind = current.authority_kind
 AND versions.authority_id = current.authority_id
 AND versions.authority_version = current.authority_version
WHERE renewal.tenant_id = sqlc.arg(tenant_id)::uuid
  AND renewal.lane = 'object_deadline'
  AND NOT versions.tombstone
  AND EXISTS (
      SELECT 1 FROM commodore.media_authority_versions AS held
      WHERE held.authority_kind = current.authority_kind
        AND held.authority_id = current.authority_id
        AND held.valid_until > NOW()
  );

-- name: ResolveMediaAuthorityIdentityByPlaybackID :one
-- Names the authority a cell asked for by playback id. object_id is the stream
-- or artifact id the compiler takes.
SELECT 'live_stream'::text AS target_kind, s.id::text AS object_id, s.tenant_id::text AS tenant_id
FROM commodore.streams AS s WHERE lower(s.playback_id::text) = lower(sqlc.arg(playback_id)::text)
UNION ALL
SELECT 'artifact'::text, c.id::text, c.tenant_id::text
FROM commodore.clips AS c WHERE lower(c.playback_id::text) = lower(sqlc.arg(playback_id)::text)
UNION ALL
SELECT 'artifact'::text, d.id::text, d.tenant_id::text
FROM commodore.dvr_recordings AS d WHERE lower(d.playback_id::text) = lower(sqlc.arg(playback_id)::text)
UNION ALL
SELECT 'artifact'::text, v.id::text, v.tenant_id::text
FROM commodore.vod_assets AS v WHERE lower(v.playback_id::text) = lower(sqlc.arg(playback_id)::text)
LIMIT 1;

-- name: ResolveMediaAuthorityIdentityByInternalName :one
SELECT 'live_stream'::text AS target_kind, s.id::text AS object_id, s.tenant_id::text AS tenant_id
FROM commodore.streams AS s WHERE s.internal_name = sqlc.arg(internal_name)::text
UNION ALL
SELECT 'artifact'::text, c.id::text, c.tenant_id::text
FROM commodore.clips AS c WHERE c.internal_name = sqlc.arg(internal_name)::text
UNION ALL
SELECT 'artifact'::text, d.id::text, d.tenant_id::text
FROM commodore.dvr_recordings AS d WHERE d.internal_name = sqlc.arg(internal_name)::text
UNION ALL
SELECT 'artifact'::text, v.id::text, v.tenant_id::text
FROM commodore.vod_assets AS v WHERE v.internal_name = sqlc.arg(internal_name)::text
LIMIT 1;

-- name: ListCurrentMediaAuthorityEnvelopesForCell :many
-- The signed envelope of an authority's current version for one cell, tenant
-- before object. An expired version is never handed out.
SELECT delivery.authority_kind, delivery.authority_id, delivery.authority_version, delivery.signed_envelope
FROM commodore.media_authority_current AS current
JOIN commodore.media_authority_versions AS versions
  ON versions.authority_kind = current.authority_kind
 AND versions.authority_id = current.authority_id
 AND versions.authority_version = current.authority_version
JOIN commodore.media_authority_deliveries AS delivery
  ON delivery.authority_kind = current.authority_kind
 AND delivery.authority_id = current.authority_id
 AND delivery.authority_version = current.authority_version
WHERE delivery.cell_id = sqlc.arg(cell_id)
  AND LEAST(versions.valid_until, delivery.correction_until) > NOW()
  AND ((current.authority_kind = 'tenant' AND current.authority_id = sqlc.arg(tenant_id)::text)
    OR (current.authority_kind = 'media_object' AND current.authority_id = sqlc.arg(object_authority_id)::text))
ORDER BY CASE current.authority_kind WHEN 'tenant' THEN 0 ELSE 1 END;

-- name: GetMediaAuthorityCompilerFingerprint :one
SELECT fingerprint FROM commodore.media_authority_compiler_state;

-- name: SetMediaAuthorityCompilerFingerprint :exec
INSERT INTO commodore.media_authority_compiler_state (singleton, fingerprint, updated_at)
VALUES (TRUE, sqlc.arg(fingerprint), NOW())
ON CONFLICT (singleton) DO UPDATE SET fingerprint = EXCLUDED.fingerprint, updated_at = NOW();

-- name: ListWarmTenantIDs :many
-- Tenants whose authority is still being renewed. Reconciliation covers these;
-- a tenant nobody uses is compiled when it is next used.
SELECT tenant_id::text AS tenant_id
FROM commodore.media_authority_refresh_obligations
WHERE lane = 'tenant_deadline' AND status IN ('pending', 'processing', 'parked')
ORDER BY tenant_id;

-- name: ClaimMediaAuthorityObligations :many
-- A target is never compiled by two lanes at once: an event compile and a
-- renewal of the same authority would otherwise both run, and the compile fence
-- would discard one after its source reads and signing were already paid for.
--
-- The lease check below only filters: it reads a snapshot, and two lanes can
-- both pass it before either commits. What serializes them is the target's claim
-- row, which every claim has to write. A second lane claiming the same target
-- waits on the first lane's write and then finds a live claim held by another
-- lane, and skips the target (under snapshot isolation it fails instead, and the
-- next pass sees the claim). Settlement deletes the claim row; an abandoned one
-- lapses with the lease it carries.
--
-- Each claim carries a fresh token on the obligation and on the claim row.
-- Settlement and release require it: a worker whose lease lapsed and whose
-- target was claimed again cannot settle or release the attempt that replaced
-- it, even at the same revision.
WITH candidates AS (
    SELECT queued.target_key, queued.lane, gen_random_uuid() AS claim_token
    FROM commodore.media_authority_refresh_obligations AS queued
    WHERE queued.lane = sqlc.arg(lane)
      AND queued.status IN ('pending', 'processing')
      AND queued.next_attempt_at <= NOW()
      AND (queued.lease_expires_at IS NULL OR queued.lease_expires_at <= NOW())
      AND NOT EXISTS (
          SELECT 1 FROM commodore.media_authority_refresh_obligations AS inflight
          -- A live lease is what marks a compile in flight. Status is not: an
          -- event that folds in mid-compile flips the row to pending and keeps
          -- the lease.
          WHERE inflight.target_key = queued.target_key
            AND inflight.lane <> queued.lane
            AND inflight.lease_expires_at > NOW()
      )
    ORDER BY queued.next_attempt_at, queued.pending_since
    LIMIT sqlc.arg(batch_size)
    FOR UPDATE OF queued SKIP LOCKED
),
won AS (
    INSERT INTO commodore.media_authority_target_claims AS claim (target_key, lane, lease_expires_at, claim_token)
    SELECT candidates.target_key, candidates.lane,
           NOW() + sqlc.arg(lease_ms)::bigint * INTERVAL '1 millisecond', candidates.claim_token
    FROM candidates
    -- A fixed order, so two claims that want several of the same targets take
    -- their claim rows in the same order.
    ORDER BY candidates.target_key
    ON CONFLICT (target_key) DO UPDATE
    SET lane = EXCLUDED.lane, lease_expires_at = EXCLUDED.lease_expires_at, claim_token = EXCLUDED.claim_token
    WHERE claim.lane = EXCLUDED.lane OR claim.lease_expires_at <= NOW()
    RETURNING claim.target_key, claim.lane, claim.claim_token
)
UPDATE commodore.media_authority_refresh_obligations AS obligation
SET status = 'processing', attempts = obligation.attempts + 1,
    lease_expires_at = NOW() + sqlc.arg(lease_ms)::bigint * INTERVAL '1 millisecond',
    claim_token = won.claim_token,
    updated_at = NOW()
FROM won
WHERE obligation.target_key = won.target_key
  AND obligation.lane = won.lane
RETURNING obligation.target_key, obligation.lane, obligation.tenant_id::text AS tenant_id,
          obligation.target_kind, obligation.revision, obligation.bound_version,
          obligation.attempts, obligation.last_reason, won.claim_token::text AS claim_token;

-- name: ReleaseMediaAuthorityTargetClaim :exec
-- A lane's compile of a target has settled, whatever its outcome; another lane
-- may claim the target now instead of when the lease would have lapsed. Only
-- the claim that compiled may release it.
DELETE FROM commodore.media_authority_target_claims
WHERE target_key = sqlc.arg(target_key)
  AND lane = sqlc.arg(lane)
  AND claim_token::text = sqlc.arg(claim_token)::text;

-- name: CompleteMediaAuthorityObligation :execrows
UPDATE commodore.media_authority_refresh_obligations
SET status = 'completed', lease_expires_at = NULL, claim_token = NULL, last_error = NULL, updated_at = NOW()
WHERE target_key = sqlc.arg(target_key)
  AND lane = sqlc.arg(lane)
  AND revision = sqlc.arg(revision)
  AND claim_token::text = sqlc.arg(claim_token)::text
  AND status = 'processing';

-- name: FailMediaAuthorityObligation :execrows
UPDATE commodore.media_authority_refresh_obligations
SET status = 'pending', next_attempt_at = sqlc.arg(next_attempt_at),
    lease_expires_at = NULL, claim_token = NULL, last_error = sqlc.arg(last_error), updated_at = NOW()
WHERE target_key = sqlc.arg(target_key)
  AND lane = sqlc.arg(lane)
  AND revision = sqlc.arg(revision)
  AND claim_token::text = sqlc.arg(claim_token)::text
  AND status = 'processing';

-- name: ParkMediaAuthorityObligation :execrows
UPDATE commodore.media_authority_refresh_obligations
SET status = 'parked', park_reason = sqlc.arg(park_reason), lease_expires_at = NULL, claim_token = NULL,
    last_error = sqlc.arg(last_error), updated_at = NOW()
WHERE target_key = sqlc.arg(target_key)
  AND lane = sqlc.arg(lane)
  AND revision = sqlc.arg(revision)
  AND claim_token::text = sqlc.arg(claim_token)::text
  AND status = 'processing';

-- name: ReleaseSupersededMediaAuthorityObligation :execrows
-- A fold during processing already re-armed the row at a newer revision; only
-- the serialization lease the fold preserved is left to clear. The status and
-- token filters matter: once another worker has claimed the newer revision the
-- row is processing again under its token, and that lease belongs to it.
UPDATE commodore.media_authority_refresh_obligations
SET lease_expires_at = NULL, claim_token = NULL, updated_at = NOW()
WHERE target_key = sqlc.arg(target_key)
  AND lane = sqlc.arg(lane)
  AND revision <> sqlc.arg(revision)
  AND claim_token::text = sqlc.arg(claim_token)::text
  AND status = 'pending';

-- name: RearmParkedMediaAuthorityObligations :execrows
UPDATE commodore.media_authority_refresh_obligations
SET status = 'pending', park_reason = NULL, attempts = 0, revision = revision + 1,
    next_attempt_at = NOW(), pending_since = NOW(), updated_at = NOW()
WHERE status = 'parked';

-- name: AdoptLegacyMediaAuthorityRefreshInbox :many
-- Settles unfinished rows of the pre-obligation inbox so their targets can be
-- folded into obligations. Leased rows belong to a replica still draining the
-- inbox and are left alone.
WITH legacy AS (
    SELECT source_service, source_event_id
    FROM commodore.media_authority_refresh_inbox
    WHERE status <> 'completed'
      AND (lease_expires_at IS NULL OR lease_expires_at <= NOW())
    ORDER BY created_at
    LIMIT sqlc.arg(batch_size)
    FOR UPDATE SKIP LOCKED
)
UPDATE commodore.media_authority_refresh_inbox AS inbox
SET status = 'completed', completed_at = NOW(), lease_expires_at = NULL, updated_at = NOW()
FROM legacy
WHERE inbox.source_service = legacy.source_service
  AND inbox.source_event_id = legacy.source_event_id
RETURNING inbox.source_service, inbox.source_event_id, inbox.tenant_id::text AS tenant_id, inbox.reason;

-- name: ListMediaAuthorityObligationStats :many
-- Reads only what is due, through the due index. The table holds a renewal row
-- for every published authority, nearly all of them scheduled for later.
SELECT lane,
       COUNT(*)::bigint AS due_count,
       COALESCE(MAX(EXTRACT(EPOCH FROM (NOW() - GREATEST(pending_since, next_attempt_at)))), 0)::double precision AS oldest_due_seconds
FROM commodore.media_authority_refresh_obligations
WHERE status IN ('pending', 'processing')
  AND lane IN ('event', 'bulk', 'object_deadline', 'tenant_deadline')
  AND next_attempt_at <= NOW()
GROUP BY lane
ORDER BY lane;

-- name: ListExpiredWarmMediaAuthorityCounts :many
-- Authorities still being renewed whose bound version has run out: renewal did
-- not reach them in time, and cells are refusing them or asking for them on
-- every decision. A dormant renewal is an authority nobody uses and is expected
-- to run out, unless it was used within the use window after all: that is a
-- renewal whose revival was lost, and it is counted too.
-- Each branch is driven by its own index so neither reads the cold catalog: live
-- renewals through the expiry index, dormant ones from the recently used set
-- through its last_used_at index and the obligation key.
SELECT expired.lane, COUNT(*)::bigint AS expired_count
FROM (
    SELECT obligation.lane
    FROM commodore.media_authority_refresh_obligations AS obligation
    WHERE obligation.lane IN ('object_deadline', 'tenant_deadline')
      AND obligation.status IN ('pending', 'processing', 'parked')
      AND obligation.expires_at < NOW()
    UNION ALL
    SELECT obligation.lane
    FROM commodore.media_authority_use AS used
    JOIN commodore.media_authority_refresh_obligations AS obligation
      ON obligation.target_key = used.authority_kind || ':' || used.authority_id
     AND obligation.lane IN ('object_deadline', 'tenant_deadline')
    WHERE used.last_used_at > NOW() - INTERVAL '30 days'
      AND obligation.status = 'dormant'
      AND obligation.expires_at < NOW()
) AS expired
GROUP BY expired.lane
ORDER BY expired.lane;

-- name: ListParkedMediaAuthorityObligationCounts :many
SELECT target_kind, COUNT(*)::bigint AS parked_count
FROM commodore.media_authority_refresh_obligations
WHERE status = 'parked'
GROUP BY target_kind
ORDER BY target_kind;

-- name: ListCurrentMediaAuthoritiesWithoutRenewal :many
-- Current live authorities with no live renewal obligation. A settled row does
-- not count: only a tombstone may rest on one. Versions published before the
-- tombstone column existed all read as live, so the caller still decodes the
-- payload; newest validity first keeps those aging tombstones from crowding
-- live authorities out of a batch.
SELECT current.authority_kind, current.authority_id, current.authority_version,
       versions.payload, versions.issued_at, versions.refresh_after, versions.valid_until
FROM commodore.media_authority_current AS current
JOIN commodore.media_authority_versions AS versions
  ON versions.authority_kind = current.authority_kind
 AND versions.authority_id = current.authority_id
 AND versions.authority_version = current.authority_version
WHERE NOT versions.tombstone
  AND NOT EXISTS (
    SELECT 1 FROM commodore.media_authority_refresh_obligations AS obligation
    WHERE obligation.lane IN ('object_deadline', 'tenant_deadline')
      AND obligation.target_key = current.authority_kind || ':' || current.authority_id
      AND obligation.status IN ('pending', 'processing', 'parked', 'dormant')
)
ORDER BY versions.valid_until DESC
LIMIT sqlc.arg(batch_size);

-- name: GetCurrentMediaAuthorityPayload :one
SELECT versions.payload, versions.valid_until
FROM commodore.media_authority_current AS current
JOIN commodore.media_authority_versions AS versions
  ON versions.authority_kind = current.authority_kind
 AND versions.authority_id = current.authority_id
 AND versions.authority_version = current.authority_version
WHERE current.authority_kind = sqlc.arg(authority_kind)
  AND current.authority_id = sqlc.arg(authority_id);

-- name: GetCurrentMediaAuthorityPlaybackSourceRevision :one
SELECT COALESCE((SELECT item->>'revision'
    FROM jsonb_array_elements(versions.source_revisions) AS item
    WHERE item->>'service' = 'commodore-playback-access' LIMIT 1), '')::text AS revision
FROM commodore.media_authority_current AS current
JOIN commodore.media_authority_versions AS versions USING (authority_kind, authority_id, authority_version)
WHERE current.authority_kind = 'media_object'
  AND current.authority_id = sqlc.arg(authority_id);

-- name: MarkMediaAuthorityVersionTombstone :execrows
UPDATE commodore.media_authority_versions
SET tombstone = TRUE
WHERE authority_kind = sqlc.arg(authority_kind)
  AND authority_id = sqlc.arg(authority_id)
  AND authority_version = sqlc.arg(authority_version)
  AND NOT tombstone;

-- name: ListCurrentTenantAuthorityIDs :many
SELECT authority_id
FROM commodore.media_authority_current
WHERE authority_kind = 'tenant'
ORDER BY authority_id;

-- name: LockCurrentTenantMediaAuthority :one
SELECT versions.payload, versions.valid_until
FROM commodore.media_authority_current AS current
JOIN commodore.media_authority_versions AS versions
  ON versions.authority_kind = current.authority_kind
 AND versions.authority_id = current.authority_id
 AND versions.authority_version = current.authority_version
WHERE current.authority_kind = 'tenant'
  AND current.authority_id = sqlc.arg(tenant_id)::text
FOR SHARE OF current;

-- name: ListCurrentMediaAuthorityDeliveryCells :many
SELECT delivery.cell_id
FROM commodore.media_authority_current AS current
JOIN commodore.media_authority_deliveries AS delivery
  ON delivery.authority_kind = current.authority_kind
 AND delivery.authority_id = current.authority_id
 AND delivery.authority_version = current.authority_version
WHERE current.authority_kind = sqlc.arg(authority_kind)
  AND current.authority_id = sqlc.arg(authority_id)
ORDER BY delivery.cell_id;

-- name: GetLiveStreamMediaAuthoritySource :one
SELECT id::text AS stream_id, tenant_id::text AS tenant_id, user_id::text AS user_id,
       internal_name, playback_id::text AS playback_id, stream_key::text AS stream_key,
       ingest_mode, requires_auth, COALESCE(is_recording_enabled, FALSE)::boolean AS is_recording_enabled,
       COALESCE(playback_policy::text, '')::text AS playback_policy,
       COALESCE(playback_webhook_secret_enc, '')::text AS playback_webhook_secret_enc,
       COALESCE(active_ingest_cluster_id, '')::text AS active_ingest_cluster_id,
       deleted_at,
       -- Ages are taken on the database clock, in the column's own time zone.
       COALESCE(EXTRACT(EPOCH FROM (NOW()::timestamp - created_at)), 1e12)::bigint AS age_seconds,
       COALESCE(EXTRACT(EPOCH FROM (NOW()::timestamp - active_ingest_cluster_updated_at)), 1e12)::bigint AS ingest_lease_age_seconds
FROM commodore.streams
WHERE id = sqlc.arg(stream_id)::uuid;

-- name: ListLiveStreamMediaAuthoritySources :many
SELECT id::text AS stream_id, tenant_id::text AS tenant_id
FROM commodore.streams
ORDER BY id;

-- name: ListTenantLiveStreamMediaAuthoritySources :many
SELECT id::text AS stream_id, tenant_id::text AS tenant_id
FROM commodore.streams
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
ORDER BY id;

-- name: GetPullMediaAuthoritySecret :one
SELECT source_uri_enc, enabled, COALESCE(allowed_cluster_ids, '{}') AS allowed_cluster_ids
FROM commodore.stream_pull_sources
WHERE stream_id = sqlc.arg(stream_id)::uuid;

-- name: GetNativeMediaAuthoritySecret :one
SELECT native.source_spec, native.source_kind, native.placement_count,
       COALESCE(native.allowed_cluster_ids, '{}') AS allowed_cluster_ids,
       stream.always_on
FROM commodore.stream_mist_sources AS native
JOIN commodore.streams AS stream ON stream.id = native.stream_id
WHERE native.stream_id = sqlc.arg(stream_id)::uuid;

-- name: GetArtifactMediaAuthoritySource :one
SELECT c.id::text AS authority_id, 'clip'::text AS artifact_kind, c.clip_hash AS artifact_hash,
       c.tenant_id::text AS tenant_id, c.user_id::text AS user_id, c.stream_id::text AS stream_id,
       c.internal_name, c.playback_id::text AS playback_id,
       COALESCE(c.origin_cluster_id, '')::text AS origin_cluster_id,
       c.requires_auth, COALESCE(c.playback_policy::text, '')::text AS playback_policy,
       COALESCE(c.playback_webhook_secret_enc, '')::text AS playback_webhook_secret_enc,
       TRUE AS parent_stream_exists, COALESCE(parent.internal_name, '')::text AS parent_stream_internal_name,
       COALESCE(EXTRACT(EPOCH FROM (NOW()::timestamp - c.created_at)), 1e12)::bigint AS age_seconds
FROM commodore.clips AS c
LEFT JOIN commodore.streams AS parent ON parent.id = c.stream_id
WHERE c.id = sqlc.arg(authority_id)::uuid
UNION ALL
SELECT d.id::text, 'dvr'::text, d.dvr_hash, d.tenant_id::text, d.user_id::text,
       COALESCE(d.stream_id::text, '')::text, d.internal_name, d.playback_id::text,
       COALESCE(d.origin_cluster_id, '')::text,
       CASE WHEN d.playback_authority_ready THEN d.requires_auth ELSE COALESCE(parent.requires_auth, TRUE) END,
       COALESCE((CASE WHEN d.playback_authority_ready THEN d.playback_policy ELSE parent.playback_policy END)::text, '')::text,
       COALESCE(CASE WHEN d.playback_authority_ready THEN d.playback_webhook_secret_enc ELSE parent.playback_webhook_secret_enc END, '')::text,
       EXISTS (SELECT 1 FROM commodore.streams AS parent WHERE parent.id = d.stream_id) AS parent_stream_exists,
       COALESCE(d.stream_internal_name, '')::text AS parent_stream_internal_name,
       COALESCE(EXTRACT(EPOCH FROM (NOW()::timestamp - d.created_at)), 1e12)::bigint
FROM commodore.dvr_recordings AS d
LEFT JOIN commodore.streams AS parent ON parent.id = d.stream_id AND parent.tenant_id = d.tenant_id
WHERE d.id = sqlc.arg(authority_id)::uuid
UNION ALL
SELECT v.id::text,
       CASE WHEN v.origin_type = 'dvr_chapter' THEN 'chapter'::text ELSE 'vod'::text END,
       v.vod_hash, v.tenant_id::text, v.user_id::text, COALESCE(v.stream_id::text, '')::text,
       v.internal_name, v.playback_id::text, COALESCE(v.origin_cluster_id, '')::text,
       v.requires_auth, COALESCE(v.playback_policy::text, '')::text,
       COALESCE(v.playback_webhook_secret_enc, '')::text,
       TRUE AS parent_stream_exists,
       COALESCE(parent_dvr.stream_internal_name, parent_stream.internal_name, '')::text AS parent_stream_internal_name,
       COALESCE(EXTRACT(EPOCH FROM (NOW()::timestamp - v.created_at)), 1e12)::bigint
FROM commodore.vod_assets AS v
LEFT JOIN commodore.dvr_chapter_playback AS chapter
  ON chapter.tenant_id = v.tenant_id AND chapter.artifact_hash = v.vod_hash
LEFT JOIN commodore.dvr_recordings AS parent_dvr
  ON parent_dvr.tenant_id = chapter.tenant_id AND parent_dvr.dvr_hash = chapter.dvr_hash
LEFT JOIN commodore.streams AS parent_stream ON parent_stream.id = v.stream_id
WHERE v.id = sqlc.arg(authority_id)::uuid;

-- name: ListArtifactMediaAuthoritySources :many
SELECT id::text AS authority_id, tenant_id::text AS tenant_id, 'clip'::text AS artifact_kind
FROM commodore.clips
UNION ALL
SELECT id::text, tenant_id::text, 'dvr'::text FROM commodore.dvr_recordings
UNION ALL
SELECT id::text, tenant_id::text,
       CASE WHEN origin_type = 'dvr_chapter' THEN 'chapter'::text ELSE 'vod'::text END
FROM commodore.vod_assets
ORDER BY authority_id;

-- name: ListTenantArtifactMediaAuthoritySources :many
SELECT id::text AS authority_id, tenant_id::text AS tenant_id, 'clip'::text AS artifact_kind
FROM commodore.clips WHERE tenant_id = sqlc.arg(tenant_id)::uuid
UNION ALL
SELECT id::text, tenant_id::text, 'dvr'::text FROM commodore.dvr_recordings
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
UNION ALL
SELECT id::text, tenant_id::text,
       CASE WHEN origin_type = 'dvr_chapter' THEN 'chapter'::text ELSE 'vod'::text END
FROM commodore.vod_assets WHERE tenant_id = sqlc.arg(tenant_id)::uuid
ORDER BY authority_id;

-- name: UpsertMediaCellPlacementCapability :one
INSERT INTO commodore.media_cell_placement_capabilities (
    cell_id, max_schema_version, enforcement_ready, live_replicas, long_validity_ready, use_reports_ready,
    first_ready_at, attested_at, updated_at
) VALUES (
    sqlc.arg(cell_id), sqlc.arg(max_schema_version), sqlc.arg(enforcement_ready), sqlc.arg(live_replicas),
    sqlc.arg(long_validity_ready), sqlc.arg(use_reports_ready),
    CASE WHEN sqlc.arg(enforcement_ready)::boolean THEN NOW() ELSE NULL END, NOW(), NOW()
)
ON CONFLICT (cell_id) DO UPDATE
SET max_schema_version = EXCLUDED.max_schema_version,
    enforcement_ready = EXCLUDED.enforcement_ready,
    live_replicas = EXCLUDED.live_replicas,
    long_validity_ready = EXCLUDED.long_validity_ready,
    use_reports_ready = EXCLUDED.use_reports_ready,
    activation_schema_version = CASE WHEN EXCLUDED.enforcement_ready
        THEN LEAST(commodore.media_cell_placement_capabilities.activation_schema_version, EXCLUDED.max_schema_version)
        ELSE 0 END,
    first_ready_at = CASE
        WHEN EXCLUDED.enforcement_ready AND commodore.media_cell_placement_capabilities.first_ready_at IS NULL THEN NOW()
        WHEN EXCLUDED.enforcement_ready THEN commodore.media_cell_placement_capabilities.first_ready_at
        ELSE NULL END,
    attested_at = NOW(),
    updated_at = NOW()
RETURNING enforcement_ready, first_ready_at, activation_schema_version;

-- name: MarkMediaCellPlacementActivated :exec
UPDATE commodore.media_cell_placement_capabilities
SET activation_schema_version = sqlc.arg(schema_version)
WHERE cell_id = sqlc.arg(cell_id);

-- name: GetMediaCellPlacementCapability :one
SELECT cell_id, max_schema_version, enforcement_ready, live_replicas, first_ready_at, attested_at
FROM commodore.media_cell_placement_capabilities
WHERE cell_id = sqlc.arg(cell_id);

-- name: ListMediaCellPlacementCapabilities :many
SELECT cell_id, max_schema_version, enforcement_ready, live_replicas, attested_at
FROM commodore.media_cell_placement_capabilities
WHERE cell_id = ANY(sqlc.arg(cell_ids)::text[])
ORDER BY cell_id;

-- name: ListMediaCellAuthorityFeatures :many
-- What each cell's replicas do with media authority, independent of placement.
-- A cell with no row has attested nothing.
SELECT cell_id, long_validity_ready, use_reports_ready
FROM commodore.media_cell_placement_capabilities
WHERE cell_id = ANY(sqlc.arg(cell_ids)::text[])
ORDER BY cell_id;

-- name: ListLegacySchemaTenantsTargetingCell :many
SELECT DISTINCT target.authority_id AS tenant_id
FROM commodore.media_authority_targets AS target
JOIN commodore.media_authority_current AS current
  ON current.authority_kind = target.authority_kind
 AND current.authority_id = target.authority_id
JOIN commodore.media_authority_versions AS version
  ON version.authority_kind = current.authority_kind
 AND version.authority_id = current.authority_id
 AND version.authority_version = current.authority_version
WHERE target.authority_kind = 'tenant'
  AND target.cell_id = sqlc.arg(cell_id)
  AND version.payload_schema_version < sqlc.arg(schema_version)::integer
ORDER BY tenant_id;

-- name: ListCurrentMediaAuthorityRollout :many
SELECT current.authority_version,
       versions.payload, versions.payload_schema_version, versions.valid_until,
       delivery.cell_id, delivery.status AS delivery_status, delivery.acknowledged_at,
       COALESCE(capability.enforcement_ready, FALSE)::boolean AS cell_ready,
       COALESCE(capability.max_schema_version, 0)::integer AS cell_schema_version
FROM commodore.media_authority_current AS current
JOIN commodore.media_authority_versions AS versions
  ON versions.authority_kind = current.authority_kind
 AND versions.authority_id = current.authority_id
 AND versions.authority_version = current.authority_version
JOIN commodore.media_authority_deliveries AS delivery
  ON delivery.authority_kind = current.authority_kind
 AND delivery.authority_id = current.authority_id
 AND delivery.authority_version = current.authority_version
LEFT JOIN commodore.media_cell_placement_capabilities AS capability
  ON capability.cell_id = delivery.cell_id
WHERE current.authority_kind = sqlc.arg(authority_kind)
  AND current.authority_id = sqlc.arg(authority_id)
ORDER BY delivery.cell_id;

-- name: LockMediaAuthorityActivation :exec
-- Serializes activation derivation for one authority. Acknowledgements for the
-- same authority version are delivered concurrently, each in its own READ
-- COMMITTED transaction, so without this every one of them can read the others'
-- pre-acknowledgement state, conclude the rollout is incomplete, and leave a
-- fully delivered change pending forever.
--
-- The key is namespaced: hashtext resolves to the single-bigint overload, which
-- Commodore's artifact-catalog and creation-identity locks also use, and an
-- unnamespaced authority id could collide with one of those.
SELECT pg_advisory_xact_lock(
  hashtext('media_authority_activation:' || sqlc.arg(authority_kind)::text || ':' || sqlc.arg(authority_id)::text)
);

-- name: ListAuthoritiesAwaitingActivation :many
-- One page of placement scopes whose change is still unresolved. Activation is
-- otherwise only ever derived as a side effect of an acknowledgement, so
-- anything that interrupts the acknowledgement that would have activated a
-- change -- a crashed process, a delivery that failed and succeeded on retry --
-- leaves it stranded with no retry path. This is the backstop that converges
-- those.
--
-- Paged by (tenant_id, created_at) rather than filtered by age alone, because a
-- legitimately blocked change never leaves this set: re-writing the same
-- rollout status is a no-op that does not bump updated_at. A fixed ordering plus
-- LIMIT would then let a bounded prefix of permanently blocked scopes hide every
-- scope behind them forever -- starving exactly the stranded change this exists
-- to rescue. The caller walks the cursor and wraps, so every scope is visited.
--
-- The ordering matches idx_media_placement_changes_pending, so paging is an
-- index scan rather than a sort of every pending row once a second.
SELECT changes.tenant_id::text AS tenant_id,
       changes.scope_kind,
       changes.scope_id::text AS scope_id,
       changes.created_at
FROM commodore.media_placement_changes AS changes
WHERE changes.rollout_status IN ('pending', 'blocked')
  AND changes.updated_at < NOW() - sqlc.arg(min_age_ms)::bigint * INTERVAL '1 millisecond'
  AND (changes.tenant_id, changes.created_at) > (sqlc.arg(after_tenant_id)::uuid, sqlc.arg(after_created_at)::timestamptz)
ORDER BY changes.tenant_id, changes.created_at
LIMIT sqlc.arg(max_rows)::integer;
