-- name: AllocateConfigSeedVersion :one
INSERT INTO foghorn.node_config_seeds (node_id, version_counter)
VALUES (sqlc.arg(node_id), 1)
ON CONFLICT (node_id) DO UPDATE
SET version_counter = foghorn.node_config_seeds.version_counter + 1,
    updated_at = NOW()
RETURNING version_counter;

-- name: EnsureConfigSeedVersionAtLeast :exec
INSERT INTO foghorn.node_config_seeds (node_id, version_counter)
VALUES (sqlc.arg(node_id), sqlc.arg(version_counter)::bigint)
ON CONFLICT (node_id) DO UPDATE
SET version_counter = GREATEST(
        foghorn.node_config_seeds.version_counter,
        EXCLUDED.version_counter
    ),
    updated_at = NOW();

-- name: PersistConfigSeed :execrows
UPDATE foghorn.node_config_seeds
SET seed_version = sqlc.arg(seed_version)::bigint,
    seed_payload = sqlc.arg(seed_payload),
    updated_at = NOW()
WHERE node_id = sqlc.arg(node_id)
  AND version_counter >= sqlc.arg(seed_version)::bigint
  AND (seed_version IS NULL OR seed_version <= sqlc.arg(seed_version)::bigint);

-- name: GetLastConfigSeed :one
SELECT COALESCE(seed_version, 0)::bigint AS seed_version, seed_payload
FROM foghorn.node_config_seeds
WHERE node_id = sqlc.arg(node_id)
  AND seed_version IS NOT NULL
  AND seed_payload IS NOT NULL;
