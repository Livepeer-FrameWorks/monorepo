-- name: TryAcquireCertificateIssuanceLease :one
INSERT INTO navigator.certificate_issuance_leases (lease_key, lease_owner, lease_until, updated_at)
VALUES (
    sqlc.arg(lease_key), sqlc.arg(lease_owner),
    NOW() + sqlc.arg(lease_seconds)::bigint * INTERVAL '1 second', NOW()
)
ON CONFLICT (lease_key) DO UPDATE SET
    lease_owner = EXCLUDED.lease_owner,
    lease_until = EXCLUDED.lease_until,
    updated_at = NOW()
WHERE navigator.certificate_issuance_leases.lease_until <= NOW()
   OR navigator.certificate_issuance_leases.lease_owner = EXCLUDED.lease_owner
RETURNING true;

-- name: RenewCertificateIssuanceLease :one
UPDATE navigator.certificate_issuance_leases
SET lease_until = NOW() + sqlc.arg(lease_seconds)::bigint * INTERVAL '1 second',
    updated_at = NOW()
WHERE lease_key = sqlc.arg(lease_key)
  AND lease_owner = sqlc.arg(lease_owner)
  AND lease_until > NOW()
RETURNING true;

-- name: ReleaseCertificateIssuanceLease :exec
DELETE FROM navigator.certificate_issuance_leases
WHERE lease_key = sqlc.arg(lease_key)
  AND lease_owner = sqlc.arg(lease_owner);

-- name: DeleteExpiredCertificateIssuanceLeases :execrows
DELETE FROM navigator.certificate_issuance_leases
WHERE lease_until <= NOW();
