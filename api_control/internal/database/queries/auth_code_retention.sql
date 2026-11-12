-- name: ListExpiredAuthorizationCodeKeys :many
-- Global read-only discovery; each delete is scoped to the returned owner/key.
SELECT id, tenant_id FROM commodore.auth_authorization_codes
WHERE expires_at < ((NOW() AT TIME ZONE 'UTC') - INTERVAL '24 hours')
ORDER BY expires_at LIMIT sqlc.arg(batch_size);

-- name: ListConsumedAuthorizationCodeKeys :many
-- A consumed code is terminal even if its original expiry has not passed.
SELECT id, tenant_id FROM commodore.auth_authorization_codes
WHERE consumed_at < ((NOW() AT TIME ZONE 'UTC') - INTERVAL '24 hours')
ORDER BY consumed_at LIMIT sqlc.arg(batch_size);

-- name: ListExpiredDeviceCodeKeys :many
-- NULL tenant_id identifies a device code not yet approved by a user.
SELECT id, tenant_id FROM commodore.auth_device_codes
WHERE expires_at < ((NOW() AT TIME ZONE 'UTC') - INTERVAL '24 hours')
ORDER BY expires_at LIMIT sqlc.arg(batch_size);

-- name: DeleteRetainedAuthorizationCode :execrows
DELETE FROM commodore.auth_authorization_codes
WHERE id = sqlc.arg(id)::uuid AND tenant_id = sqlc.arg(tenant_id)::uuid
  AND (expires_at < ((NOW() AT TIME ZONE 'UTC') - INTERVAL '24 hours')
       OR consumed_at < ((NOW() AT TIME ZONE 'UTC') - INTERVAL '24 hours'));

-- name: DeleteRetainedDeviceCode :execrows
DELETE FROM commodore.auth_device_codes
WHERE id = sqlc.arg(id)::uuid
  AND tenant_id IS NOT DISTINCT FROM sqlc.narg(tenant_id)::uuid
  AND expires_at < ((NOW() AT TIME ZONE 'UTC') - INTERVAL '24 hours');
