-- name: EnqueueServiceEvent :one
INSERT INTO quartermaster.service_event_outbox (
    event_type, tenant_id, user_id, resource_type, resource_id, payload
) VALUES (
    sqlc.arg(event_type)::text,
    sqlc.arg(tenant_id)::uuid,
    sqlc.arg(user_id)::text,
    sqlc.arg(resource_type)::text,
    sqlc.arg(resource_id)::text,
    sqlc.arg(payload)::text::jsonb
)
RETURNING id::text;

-- name: ClaimServiceEventOutboxBatch :many
SELECT id::text AS id,
       payload::text AS payload,
       attempts,
       created_at
FROM quartermaster.service_event_outbox
WHERE completed_at IS NULL
  AND (claimed_at IS NULL OR claimed_at < NOW() - sqlc.arg(lease_interval)::text::interval)
ORDER BY created_at
FOR UPDATE SKIP LOCKED
LIMIT sqlc.arg(batch_size)::integer;

-- name: MarkServiceEventOutboxClaimed :exec
UPDATE quartermaster.service_event_outbox
SET claimed_at = NOW()
WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: CompleteServiceEventOutbox :exec
UPDATE quartermaster.service_event_outbox
SET completed_at = NOW(),
    last_error = NULL
WHERE id = sqlc.arg(id)::uuid;

-- name: FailServiceEventOutbox :exec
WITH input AS (
    SELECT sqlc.arg(id)::uuid AS id,
           sqlc.arg(attempts)::integer AS attempts,
           sqlc.arg(last_error)::text AS last_error
)
UPDATE quartermaster.service_event_outbox
SET attempts = input.attempts,
    last_error = input.last_error,
    claimed_at = NULL
FROM input
WHERE service_event_outbox.id = input.id;

-- name: EnqueueNavigatorCustomDomain :one
INSERT INTO quartermaster.navigator_custom_domain_outbox (
    tenant_id, domain, action
) VALUES (
    sqlc.arg(tenant_id)::uuid,
    sqlc.arg(domain)::text,
    sqlc.arg(action)::text
)
RETURNING id::text;

-- name: ClaimNavigatorCustomDomainOutboxBatch :many
SELECT id::text AS id,
       tenant_id::text AS tenant_id,
       domain,
       action,
       attempts,
       created_at
FROM quartermaster.navigator_custom_domain_outbox
WHERE completed_at IS NULL
  AND (claimed_at IS NULL OR claimed_at < NOW() - sqlc.arg(lease_interval)::text::interval)
ORDER BY created_at
FOR UPDATE SKIP LOCKED
LIMIT sqlc.arg(batch_size)::integer;

-- name: MarkNavigatorCustomDomainOutboxClaimed :exec
UPDATE quartermaster.navigator_custom_domain_outbox
SET claimed_at = NOW()
WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: CompleteNavigatorCustomDomainOutbox :exec
UPDATE quartermaster.navigator_custom_domain_outbox
SET completed_at = NOW(),
    last_error = NULL
WHERE id = sqlc.arg(id)::uuid;

-- name: FailNavigatorCustomDomainOutbox :exec
WITH input AS (
    SELECT sqlc.arg(id)::uuid AS id,
           sqlc.arg(attempts)::integer AS attempts,
           sqlc.arg(last_error)::text AS last_error
)
UPDATE quartermaster.navigator_custom_domain_outbox
SET attempts = input.attempts,
    last_error = input.last_error,
    claimed_at = NULL
FROM input
WHERE navigator_custom_domain_outbox.id = input.id;

-- name: EnqueueNavigatorTenantAlias :one
INSERT INTO quartermaster.navigator_tenant_alias_outbox (
    tenant_id, subdomain, cluster_id, reason, action
) VALUES (
    sqlc.arg(tenant_id)::uuid,
    NULLIF(sqlc.arg(subdomain)::text, ''),
    NULLIF(sqlc.arg(cluster_id)::text, ''),
    NULLIF(sqlc.arg(reason)::text, ''),
    sqlc.arg(action)::text
)
RETURNING id::text;

-- name: ClaimNavigatorTenantAliasOutboxBatch :many
SELECT o.id::text AS id,
       o.tenant_id::text AS tenant_id,
       COALESCE(o.subdomain, '')::text AS subdomain,
       COALESCE(o.cluster_id, '')::text AS cluster_id,
       COALESCE(o.reason, '')::text AS reason,
       o.action,
       o.attempts
FROM quartermaster.navigator_tenant_alias_outbox o
WHERE o.completed_at IS NULL
  AND (o.claimed_at IS NULL OR o.claimed_at < NOW() - sqlc.arg(lease_interval)::text::interval)
  AND (o.next_retry_at IS NULL OR o.next_retry_at <= NOW())
  AND NOT EXISTS (
      SELECT 1
      FROM quartermaster.navigator_tenant_alias_outbox o2
      WHERE o2.tenant_id = o.tenant_id
        AND o2.completed_at IS NULL
        AND o2.seq < o.seq
  )
ORDER BY o.seq
FOR UPDATE SKIP LOCKED
LIMIT sqlc.arg(batch_size)::integer;

-- name: MarkNavigatorTenantAliasOutboxClaimed :exec
UPDATE quartermaster.navigator_tenant_alias_outbox
SET claimed_at = NOW()
WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: CompleteNavigatorTenantAliasOutbox :exec
UPDATE quartermaster.navigator_tenant_alias_outbox
SET completed_at = NOW(),
    last_error = NULL,
    next_retry_at = NULL
WHERE id = sqlc.arg(id)::uuid;
