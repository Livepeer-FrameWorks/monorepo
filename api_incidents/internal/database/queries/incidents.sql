-- name: GetOpenIncidentByGroupKeyForUpdate :one
-- The partial unique index on group_key is the open-incident authority, so
-- this lookup is keyed by group only; the row's scope and tenant can change
-- afterwards through RescopeIncident.
SELECT * FROM lookout.incidents
WHERE group_key = sqlc.arg(group_key)
  AND status <> 'resolved'
FOR UPDATE;

-- name: RescopeIncident :one
-- Moves an open incident to the verified owner of its cluster. The current
-- tenant is part of the predicate, so a concurrent rescope cannot be overwritten.
UPDATE lookout.incidents
SET scope = sqlc.arg(scope),
    tenant_id = sqlc.narg(new_tenant_id)::uuid,
    updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND tenant_id IS NOT DISTINCT FROM sqlc.narg(tenant_id)::uuid
  AND status <> 'resolved'
RETURNING *;

-- name: ListOpenIncidentClusterIDs :many
-- Ownership reconciliation across all tenants: the clusters whose open
-- incidents must follow Quartermaster's current owner. It runs without a caller
-- and returns only cluster IDs.
SELECT DISTINCT cluster_id
FROM lookout.incidents
WHERE status <> 'resolved'
  AND cluster_id <> ''
ORDER BY cluster_id;

-- name: LockOpenClusterIncidents :many
-- Ownership reconciliation across all tenants: locks the open incidents of one
-- cluster. The caller holds the cluster's scope row exclusively, so no
-- ingestion can open or move an incident of the cluster meanwhile.
SELECT *
FROM lookout.incidents
WHERE cluster_id = sqlc.arg(cluster_id)
  AND status <> 'resolved'
ORDER BY created_at, id
FOR UPDATE;

-- name: GetIncidentOpeningEventID :one
SELECT e.id
FROM lookout.incident_events e
JOIN lookout.incidents i ON i.id = e.incident_id
WHERE e.incident_id = sqlc.arg(incident_id)
  AND i.tenant_id IS NOT DISTINCT FROM sqlc.narg(tenant_id)::uuid
  AND e.kind = 'alert_firing'
ORDER BY e.created_at, e.id
LIMIT 1;

-- name: InsertIncident :one
INSERT INTO lookout.incidents (
    scope, tenant_id, cluster_id, region, group_key, alertname, severity,
    title, summary, started_at, last_alert_at
) VALUES (
    sqlc.arg(scope), sqlc.narg(tenant_id)::uuid, sqlc.arg(cluster_id), sqlc.arg(region),
    sqlc.arg(group_key), sqlc.arg(alertname), sqlc.arg(severity), sqlc.arg(title),
    sqlc.arg(summary), sqlc.arg(started_at), sqlc.arg(last_alert_at)
)
ON CONFLICT (group_key) WHERE status <> 'resolved' DO NOTHING
RETURNING *;

-- name: ResolvedIncidentAlertRepeatExists :one
SELECT EXISTS (
    SELECT 1
    FROM lookout.incident_alerts a
    JOIN lookout.incidents i ON i.id = a.incident_id
    WHERE i.group_key = sqlc.arg(group_key)
      AND i.tenant_id IS NOT DISTINCT FROM sqlc.narg(tenant_id)::uuid
      AND i.status = 'resolved'
      AND a.fingerprint = sqlc.arg(fingerprint)
      AND a.starts_at = sqlc.arg(starts_at)
)::boolean AS repeated;

-- name: GetIncident :one
SELECT * FROM lookout.incidents
WHERE id = sqlc.arg(id);

-- name: GetTenantIncident :one
SELECT * FROM lookout.incidents
WHERE id = sqlc.arg(id)
  AND tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: LockIncident :one
SELECT * FROM lookout.incidents
WHERE id = sqlc.arg(id)
  AND tenant_id IS NOT DISTINCT FROM sqlc.narg(tenant_id)::uuid
FOR UPDATE;

-- name: UpdateIncidentAlerting :one
UPDATE lookout.incidents
SET last_alert_at = GREATEST(last_alert_at, sqlc.arg(last_alert_at)::timestamptz),
    severity = sqlc.arg(severity),
    region = COALESCE(NULLIF(sqlc.arg(region)::text, ''), region),
    title = sqlc.arg(title),
    summary = sqlc.arg(summary),
    updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND tenant_id IS NOT DISTINCT FROM sqlc.narg(tenant_id)::uuid
RETURNING *;

-- name: AutoResolveIncident :one
UPDATE lookout.incidents
SET status = 'resolved',
    resolution = 'auto',
    resolved_at = NOW(),
    updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND tenant_id IS NOT DISTINCT FROM sqlc.narg(tenant_id)::uuid
  AND status <> 'resolved'
RETURNING *;

-- name: AcknowledgeIncident :one
UPDATE lookout.incidents
SET status = 'acknowledged',
    acknowledged_at = NOW(),
    acknowledged_by = sqlc.narg(actor_user_id)::uuid,
    updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND tenant_id IS NOT DISTINCT FROM sqlc.narg(tenant_id)::uuid
  AND status = 'firing'
RETURNING *;

-- name: AssignIncident :one
UPDATE lookout.incidents
SET assigned_to = sqlc.narg(assigned_to)::uuid,
    updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND tenant_id IS NOT DISTINCT FROM sqlc.narg(tenant_id)::uuid
  AND status <> 'resolved'
RETURNING *;

-- name: ManuallyResolveIncident :one
UPDATE lookout.incidents
SET status = 'resolved',
    resolution = 'manual',
    resolved_at = NOW(),
    resolved_by = sqlc.narg(actor_user_id)::uuid,
    updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND tenant_id IS NOT DISTINCT FROM sqlc.narg(tenant_id)::uuid
  AND status <> 'resolved'
RETURNING *;

-- name: TouchIncident :exec
UPDATE lookout.incidents
SET updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND tenant_id IS NOT DISTINCT FROM sqlc.narg(tenant_id)::uuid;

-- name: ListIncidents :many
-- Platform-operator listing across scopes; tenant callers use ListTenantIncidents.
SELECT * FROM lookout.incidents
WHERE (sqlc.narg(scope)::text IS NULL OR scope = sqlc.narg(scope)::text)
  AND (sqlc.narg(tenant_id)::uuid IS NULL OR tenant_id = sqlc.narg(tenant_id)::uuid)
  AND (cardinality(sqlc.arg(statuses)::text[]) = 0 OR status = ANY(sqlc.arg(statuses)::text[]))
  AND (sqlc.narg(cluster_id)::text IS NULL OR cluster_id = sqlc.narg(cluster_id)::text)
  AND (sqlc.narg(after_created_at)::timestamptz IS NULL
       OR (created_at, id) < (sqlc.narg(after_created_at)::timestamptz, sqlc.narg(after_id)::uuid))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(row_limit)::integer;

-- name: CountIncidents :one
SELECT COUNT(*)::integer AS total
FROM lookout.incidents
WHERE (sqlc.narg(scope)::text IS NULL OR scope = sqlc.narg(scope)::text)
  AND (sqlc.narg(tenant_id)::uuid IS NULL OR tenant_id = sqlc.narg(tenant_id)::uuid)
  AND (cardinality(sqlc.arg(statuses)::text[]) = 0 OR status = ANY(sqlc.arg(statuses)::text[]))
  AND (sqlc.narg(cluster_id)::text IS NULL OR cluster_id = sqlc.narg(cluster_id)::text);

-- name: ListTenantIncidents :many
SELECT * FROM lookout.incidents
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND scope = 'tenant'
  AND (cardinality(sqlc.arg(statuses)::text[]) = 0 OR status = ANY(sqlc.arg(statuses)::text[]))
  AND (sqlc.narg(cluster_id)::text IS NULL OR cluster_id = sqlc.narg(cluster_id)::text)
  AND (sqlc.narg(after_created_at)::timestamptz IS NULL
       OR (created_at, id) < (sqlc.narg(after_created_at)::timestamptz, sqlc.narg(after_id)::uuid))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(row_limit)::integer;

-- name: CountTenantIncidents :one
SELECT COUNT(*)::integer AS total
FROM lookout.incidents
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND scope = 'tenant'
  AND (cardinality(sqlc.arg(statuses)::text[]) = 0 OR status = ANY(sqlc.arg(statuses)::text[]))
  AND (sqlc.narg(cluster_id)::text IS NULL OR cluster_id = sqlc.narg(cluster_id)::text);
