-- name: InsertAuthorizationCode :exec
INSERT INTO commodore.auth_authorization_codes (
    tenant_id, user_id, client_id, code_hash, code_challenge,
    code_challenge_method, redirect_uri, scope, state, expires_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);

-- name: LockAuthorizationCode :one
SELECT
    id, user_id, tenant_id, code_challenge, code_challenge_method,
    client_id, redirect_uri, consumed_at
FROM commodore.auth_authorization_codes
WHERE code_hash = $1 AND expires_at > NOW()
FOR UPDATE;

-- name: ConsumeAuthorizationCode :exec
UPDATE commodore.auth_authorization_codes
SET consumed_at = NOW()
WHERE id = $1;

-- name: InsertDeviceAuthorization :exec
INSERT INTO commodore.auth_device_codes (
    client_id, device_code_hash, user_code, scope, status,
    poll_interval_seconds, expires_at
)
VALUES ($1, $2, $3, $4, 'pending', $5, $6);

-- name: LockDeviceAuthorizationByHash :one
SELECT
    id, client_id, status, user_id, tenant_id, expires_at,
    last_polled_at, poll_interval_seconds
FROM commodore.auth_device_codes
WHERE device_code_hash = $1
FOR UPDATE;

-- name: ExpireDeviceAuthorization :exec
UPDATE commodore.auth_device_codes
SET status = 'expired'
WHERE id = $1;

-- name: TouchDeviceAuthorizationPoll :exec
UPDATE commodore.auth_device_codes
SET last_polled_at = NOW()
WHERE id = $1;

-- name: DeleteDeviceAuthorization :exec
DELETE FROM commodore.auth_device_codes
WHERE id = $1;

-- name: LockDeviceAuthorizationByUserCode :one
SELECT id, client_id, scope, status, expires_at
FROM commodore.auth_device_codes
WHERE user_code = $1
FOR UPDATE;

-- name: ApproveDeviceAuthorization :exec
UPDATE commodore.auth_device_codes
SET user_id = $1, tenant_id = $2, status = 'approved', approved_at = NOW()
WHERE id = $3;
