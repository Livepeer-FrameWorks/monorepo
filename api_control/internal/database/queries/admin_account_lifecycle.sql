-- name: EnsureTenantAccountState :exec
INSERT INTO commodore.tenant_account_states(tenant_id) VALUES (sqlc.arg(tenant_id)::uuid)
ON CONFLICT (tenant_id) DO NOTHING;

-- name: LockTenantAccountState :one
SELECT closed_at FROM commodore.tenant_account_states
WHERE tenant_id = sqlc.arg(tenant_id)::uuid FOR UPDATE;

-- name: CloseTenantAccount :exec
UPDATE commodore.tenant_account_states
SET closed_at = COALESCE(closed_at, NOW()),
    reason = CASE WHEN closed_at IS NULL THEN sqlc.arg(reason)::text ELSE reason END,
    closed_by = CASE WHEN closed_at IS NULL THEN sqlc.arg(closed_by)::text ELSE closed_by END
WHERE tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: ClaimPendingTenantAccountClosures :many
-- Platform-wide recovery queue. Claim one call with a five-minute lease, so a slow
-- batch cannot outlive leases for tenants it has not attempted.
WITH pending AS (
    SELECT tenant_id FROM commodore.tenant_account_states
    WHERE closed_at IS NOT NULL AND quartermaster_closed_at IS NULL
    AND (closure_retry_after IS NULL OR closure_retry_after <= NOW())
    ORDER BY closure_attempted_at ASC NULLS FIRST, closed_at, tenant_id
    LIMIT 1 FOR UPDATE SKIP LOCKED
)
UPDATE commodore.tenant_account_states account
SET closure_attempted_at = NOW(), closure_retry_after = NOW()+INTERVAL '5 minutes'
FROM pending WHERE account.tenant_id = pending.tenant_id
RETURNING account.tenant_id::text AS tenant_id, account.closure_attempted_at, account.closure_failures;

-- name: RecordTenantAccountClosureFailure :exec
UPDATE commodore.tenant_account_states
SET closure_error = sqlc.arg(failure)::text, closure_failures = closure_failures+1
WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND closure_attempted_at = sqlc.arg(attempted_at)
    AND quartermaster_closed_at IS NULL AND closure_failures = sqlc.arg(expected_failures)::integer;

-- name: CountFailedTenantAccountClosures :one
SELECT COUNT(*) FROM commodore.tenant_account_states
WHERE quartermaster_closed_at IS NULL AND closure_error IS NOT NULL;

-- name: CompleteTenantAccountClosure :exec
UPDATE commodore.tenant_account_states
SET quartermaster_closed_at = NOW(), closure_error = NULL, closure_retry_after = NULL
WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND closed_at IS NOT NULL;

-- name: ListTenantAccountUsers :many
SELECT id::text FROM commodore.users WHERE tenant_id = sqlc.arg(tenant_id)::uuid ORDER BY id;

-- name: SetAccountUserActive :one
UPDATE commodore.users SET is_active = sqlc.arg(active)::boolean, updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
    AND id = sqlc.arg(user_id)::uuid
RETURNING id::text, tenant_id::text, COALESCE(email::text, '')::text AS email,
    role, COALESCE(is_active, false)::boolean AS is_active, COALESCE(verified, false)::boolean AS verified;

-- name: IsTenantAccountClosed :one
SELECT EXISTS(SELECT 1 FROM commodore.tenant_account_states
WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND closed_at IS NOT NULL)::boolean;

-- name: RecordOperatorAccountAudit :exec
-- Private operator audit; never projected into tenant-visible domain events.
INSERT INTO commodore.operator_account_audit(tenant_id, actor_user_id, target_id, action, reason)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(actor_user_id), sqlc.arg(target_id), sqlc.arg(action), sqlc.arg(reason));

-- name: ListOperatorAccountAudit :many
-- Private operator read, fenced by tenant and ordered through the tenant-time index.
SELECT id::text, actor_user_id, target_id, action, reason, created_at
FROM commodore.operator_account_audit
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
    AND (sqlc.narg(before_at)::timestamptz IS NULL OR
    (created_at, id) < (sqlc.narg(before_at)::timestamptz, NULLIF(sqlc.arg(before_id)::text, '')::uuid))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(row_limit)::integer;

-- name: GetAccountUserForUpdate :one
SELECT id::text, tenant_id::text, COALESCE(email::text, '')::text AS email,
    role, COALESCE(is_active, false)::boolean AS is_active, COALESCE(verified, false)::boolean AS verified,
    COALESCE(platform_operator, false)::boolean AS is_platform_operator
FROM commodore.users
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
AND ((sqlc.arg(user_id)::text <> '' AND id = NULLIF(sqlc.arg(user_id)::text, '')::uuid)
OR (sqlc.arg(user_id)::text = '' AND lower(email::text) = lower(sqlc.arg(email)::text)))
FOR UPDATE;

-- name: CountActivePlatformOperators :one
-- Platform operators belong to the system tenant, so its lock serializes this global guard.
SELECT COUNT(*) FROM commodore.users WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND is_active AND platform_operator;

-- name: GetRevokedStreamIngest :one
-- An activation supersedes a pending user stop; a permanently closed tenant does not.
SELECT s.internal_name
FROM commodore.streams s
JOIN commodore.users u ON u.id = s.user_id AND u.tenant_id = s.tenant_id
LEFT JOIN commodore.tenant_account_states account ON account.tenant_id = s.tenant_id
WHERE s.tenant_id = sqlc.arg(tenant_id)::uuid AND s.id = sqlc.arg(stream_id)::uuid
    AND (NOT COALESCE(u.is_active, false) OR account.closed_at IS NOT NULL);

-- name: ListAccountIngestCells :many
-- Include recorded and historical recipients, even after tenant entitlements are removed.
WITH streams AS (
    SELECT s.id, s.active_ingest_cluster_id, s.last_ingest_cluster_id
    FROM commodore.streams s
    WHERE s.tenant_id = sqlc.arg(tenant_id)::uuid
    AND (sqlc.arg(stream_id)::text = '' OR s.id = NULLIF(sqlc.arg(stream_id)::text, '')::uuid)
), cells AS (
    SELECT active_ingest_cluster_id::text AS cell_id FROM streams
    UNION SELECT last_ingest_cluster_id::text FROM streams
    UNION
    SELECT target.cell_id::text
    FROM streams
    JOIN commodore.media_authority_targets target
      ON target.authority_kind = 'media_object'
     AND target.authority_id = 'live_stream:' || streams.id::text
    UNION
    SELECT target.cell_id::text
    FROM commodore.media_authority_targets target
    WHERE sqlc.arg(stream_id)::text = ''
      AND target.authority_kind = 'tenant'
      AND target.authority_id = sqlc.arg(tenant_id)::text
)
SELECT DISTINCT cell_id FROM cells WHERE cell_id IS NOT NULL AND btrim(cell_id)<>'' ORDER BY cell_id;

-- name: CountRepeatedTenantAccountClosureFailures :one
-- Platform-wide recovery metric; a single failed call may recover after backoff.
SELECT COUNT(*) FROM commodore.tenant_account_states
WHERE quartermaster_closed_at IS NULL AND closure_error IS NOT NULL AND closure_failures>1;
