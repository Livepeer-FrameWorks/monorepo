-- name: AcquireMeteringLease :one
INSERT INTO periscope.metering_leases
    (source_id, partition_key, owner_id, fencing_token, lease_until, updated_at)
VALUES ($1, $2, $3, 1, NOW() + (sqlc.arg(lease_seconds)::bigint * INTERVAL '1 second'), NOW())
ON CONFLICT (source_id, partition_key) DO UPDATE SET
    owner_id = EXCLUDED.owner_id,
    fencing_token = periscope.metering_leases.fencing_token + 1,
    lease_until = EXCLUDED.lease_until,
    updated_at = NOW()
WHERE periscope.metering_leases.lease_until <= NOW()
   OR periscope.metering_leases.owner_id = EXCLUDED.owner_id
RETURNING fencing_token;

-- name: ListReservationKeys :many
SELECT tenant_id::text AS tenant_id, cluster_id
FROM periscope.metering_reservation_keys
WHERE source_id = $1;

-- name: DeleteReservationKey :exec
DELETE FROM periscope.metering_reservation_keys
WHERE source_id = $1 AND tenant_id = $2 AND cluster_id = $3;

-- name: UpsertReservationKey :exec
INSERT INTO periscope.metering_reservation_keys
    (source_id, tenant_id, cluster_id, last_sequence, updated_at)
VALUES ($1, $2, $3, $4, NOW())
ON CONFLICT (source_id, tenant_id, cluster_id) DO UPDATE SET
    last_sequence = EXCLUDED.last_sequence,
    updated_at = NOW();

-- name: ListBillingCursorTenants :many
SELECT tenant_id::text AS tenant_id
FROM periscope.billing_cursors
WHERE source_id = $1
  AND tenant_id IS NOT NULL
  AND tenant_id <> '00000000-0000-0000-0000-000000000000'::uuid
ORDER BY tenant_id;

-- name: EnsureMeteringSource :one
INSERT INTO periscope.metering_sources
    (source_id, source_region, activated_at)
VALUES ($1, $2, $3)
ON CONFLICT (source_id) DO UPDATE SET
    source_region = CASE
        WHEN periscope.metering_sources.source_region = '' THEN EXCLUDED.source_region
        ELSE periscope.metering_sources.source_region
    END
RETURNING activated_at, source_region;

-- name: GetBillingCursor :one
SELECT last_processed_at
FROM periscope.billing_cursors
WHERE source_id = $1 AND tenant_id = $2;

-- name: InitializeBillingCursor :exec
INSERT INTO periscope.billing_cursors
    (source_id, tenant_id, last_processed_at, updated_at)
VALUES ($1, $2, $3, NOW())
ON CONFLICT (source_id, tenant_id) DO NOTHING;

-- name: GetMeteringSourceCompletion :one
SELECT completed_through
FROM periscope.metering_sources
WHERE source_id = $1;

-- name: UpdateMeteringSourceCompletion :exec
UPDATE periscope.metering_sources
SET completed_through = sqlc.arg(completed_through)::timestamptz
WHERE source_id = sqlc.arg(source_id);

-- name: AdvanceBillingCursor :exec
UPDATE periscope.billing_cursors
SET last_processed_at = $1, updated_at = NOW()
WHERE source_id = $2 AND tenant_id = $3;
