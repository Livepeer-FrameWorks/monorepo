-- name: ListIncidentAlerts :many
SELECT a.incident_id, a.fingerprint, a.status, a.alertname, a.labels, a.annotations,
       a.starts_at, a.ends_at, a.generator_url, a.created_at, a.updated_at
FROM lookout.incident_alerts a
JOIN lookout.incidents i ON i.id = a.incident_id
WHERE a.incident_id = sqlc.arg(incident_id)
  AND i.tenant_id IS NOT DISTINCT FROM sqlc.narg(tenant_id)::uuid
ORDER BY a.starts_at, a.fingerprint;

-- name: UpsertIncidentAlert :exec
INSERT INTO lookout.incident_alerts (
    incident_id, fingerprint, status, alertname, labels, annotations,
    starts_at, ends_at, generator_url
) VALUES (
    sqlc.arg(incident_id), sqlc.arg(fingerprint), sqlc.arg(status), sqlc.arg(alertname),
    sqlc.arg(labels), sqlc.arg(annotations), sqlc.arg(starts_at), sqlc.narg(ends_at),
    sqlc.arg(generator_url)
)
ON CONFLICT (incident_id, fingerprint) DO UPDATE
SET status = EXCLUDED.status,
    alertname = EXCLUDED.alertname,
    labels = EXCLUDED.labels,
    annotations = EXCLUDED.annotations,
    starts_at = EXCLUDED.starts_at,
    ends_at = EXCLUDED.ends_at,
    generator_url = EXCLUDED.generator_url,
    updated_at = NOW();

-- name: CountFiringIncidentAlerts :one
SELECT COUNT(*)::integer AS firing
FROM lookout.incident_alerts a
JOIN lookout.incidents i ON i.id = a.incident_id
WHERE a.incident_id = sqlc.arg(incident_id)
  AND i.tenant_id IS NOT DISTINCT FROM sqlc.narg(tenant_id)::uuid
  AND a.status = 'firing';

-- name: CountFiringAlertsForIncidents :many
-- Firing alert counts for one page of incidents. Tenant-restricted callers pass
-- their tenant; unrestricted callers (platform operators, internal services)
-- pass NULL. Incidents without firing alerts have no row.
SELECT a.incident_id, COUNT(*)::integer AS firing
FROM lookout.incident_alerts a
JOIN lookout.incidents i ON i.id = a.incident_id
WHERE a.incident_id = ANY(sqlc.arg(incident_ids)::uuid[])
  AND (sqlc.narg(tenant_id)::uuid IS NULL OR i.tenant_id = sqlc.narg(tenant_id)::uuid)
  AND a.status = 'firing'
GROUP BY a.incident_id;

-- name: InsertIncidentEvent :one
INSERT INTO lookout.incident_events (incident_id, kind, actor_user_id, body)
VALUES (sqlc.arg(incident_id), sqlc.arg(kind), sqlc.narg(actor_user_id)::uuid, sqlc.arg(body))
RETURNING id, created_at;

-- name: InsertInvestigationAttachedEvent :execrows
-- A repeated (incident, report) pair hits uq_incident_events_investigation_report
-- and inserts nothing.
INSERT INTO lookout.incident_events (incident_id, kind, body)
VALUES (sqlc.arg(incident_id), 'investigation_attached', sqlc.arg(body))
ON CONFLICT DO NOTHING;

-- name: ListIncidentEvents :many
SELECT e.id, e.incident_id, e.kind, e.actor_user_id, e.body, e.created_at
FROM lookout.incident_events e
JOIN lookout.incidents i ON i.id = e.incident_id
WHERE e.incident_id = sqlc.arg(incident_id)
  AND i.tenant_id IS NOT DISTINCT FROM sqlc.narg(tenant_id)::uuid
ORDER BY e.created_at, e.id;
