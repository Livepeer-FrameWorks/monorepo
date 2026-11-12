-- name: KnownSessionDevice :one
SELECT EXISTS (SELECT 1 FROM commodore.auth_session_devices
WHERE tenant_id = $1 AND user_id = $2 AND device_key = $3
  AND last_seen_at > clock_timestamp() - INTERVAL '2160 hours');

-- name: RememberSessionDevice :exec
INSERT INTO commodore.auth_session_devices (tenant_id, user_id, device_key, last_seen_at)
VALUES ($1, $2, $3, clock_timestamp())
ON CONFLICT (tenant_id, user_id, device_key) DO UPDATE
SET last_seen_at = GREATEST(commodore.auth_session_devices.last_seen_at, EXCLUDED.last_seen_at);

-- name: EnqueueSecurityEmail :exec
INSERT INTO commodore.account_email_outbox (tenant_id, user_id, purpose, status, payload)
VALUES ($1, $2, $3, 'security_pending', $4)
ON CONFLICT (user_id, purpose) DO NOTHING;

-- name: ListExpiredSessionDeviceKeys :many
-- Platform-wide discovery only; deletion rechecks the full owner and expiry.
SELECT tenant_id, user_id, device_key FROM commodore.auth_session_devices
WHERE last_seen_at < NOW() - INTERVAL '2160 hours'
ORDER BY last_seen_at, tenant_id, user_id, device_key LIMIT $1;

-- name: DeleteExpiredSessionDevice :execrows
DELETE FROM commodore.auth_session_devices
WHERE tenant_id = $1 AND user_id = $2 AND device_key = $3
  AND last_seen_at < NOW() - INTERVAL '2160 hours';

-- name: ListTerminalAccountEmailKeys :many
-- Platform-wide discovery only; settlement metadata is retained for 30 days.
SELECT tenant_id::text AS tenant_id, user_id::text AS user_id, purpose FROM commodore.account_email_outbox
WHERE status IN ('completed', 'abandoned') AND updated_at < NOW() - INTERVAL '720 hours'
ORDER BY updated_at, tenant_id, user_id, purpose LIMIT $1;

-- name: DeleteTerminalAccountEmail :execrows
DELETE FROM commodore.account_email_outbox
WHERE tenant_id = $1 AND user_id = $2 AND purpose = $3
  AND status IN ('completed', 'abandoned') AND updated_at < NOW() - INTERVAL '720 hours';
