-- name: ListAuthSessionCapOwners :many
-- Platform-wide owner discovery for the migration. Each cap read and mutation
-- rechecks the discovered tenant and user before changing session state.
SELECT id AS user_id, tenant_id FROM commodore.users
WHERE sqlc.narg(after_user_id)::uuid IS NULL OR id > sqlc.narg(after_user_id)::uuid
ORDER BY id LIMIT sqlc.arg(batch_size);
