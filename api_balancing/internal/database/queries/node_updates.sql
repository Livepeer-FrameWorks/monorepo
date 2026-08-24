-- name: ListNodeComponents :many
SELECT component, COALESCE(current_version, '')::text AS current_version
FROM foghorn.node_components
WHERE node_id = $1;

-- name: GetNodeUpdatePhase :one
SELECT phase
FROM foghorn.node_update_state
WHERE node_id = $1;

-- name: GetNodeUpdatePhaseForRelease :one
SELECT phase
FROM foghorn.node_update_state
WHERE node_id = sqlc.arg(node_id) AND target_release = sqlc.arg(target_release)::text;

-- name: GetNodeUpdateProgress :one
SELECT COALESCE(target_release, '')::text AS target_release,
       phase,
       deadline,
       updated_at,
       COALESCE(expected_components::text, '{}')::text AS expected_components
FROM foghorn.node_update_state
WHERE node_id = $1;

-- name: UpsertNodeUpdateProgress :exec
INSERT INTO foghorn.node_update_state
    (node_id, target_release, phase, last_error, deadline, expected_components, started_at, updated_at)
VALUES (
    sqlc.arg(node_id),
    NULLIF(sqlc.arg(target_release)::text, ''),
    sqlc.arg(phase),
    NULLIF(sqlc.arg(last_error)::text, ''),
    sqlc.narg(deadline),
    COALESCE(NULLIF(sqlc.narg(expected_components)::text, '')::jsonb, '{}'::jsonb),
    NOW(),
    NOW()
)
ON CONFLICT (node_id) DO UPDATE SET
    target_release = EXCLUDED.target_release,
    phase = EXCLUDED.phase,
    started_at = COALESCE(foghorn.node_update_state.started_at, EXCLUDED.started_at),
    deadline = COALESCE(EXCLUDED.deadline, foghorn.node_update_state.deadline),
    expected_components = CASE
        WHEN sqlc.narg(expected_components)::text IS NULL THEN foghorn.node_update_state.expected_components
        ELSE EXCLUDED.expected_components
    END,
    last_error = EXCLUDED.last_error,
    updated_at = NOW();
