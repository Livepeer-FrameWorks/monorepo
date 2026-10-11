-- name: LoadPlacementMembershipSnapshot :one
SELECT revision, payload, observed_at, confirmed_at
FROM foghorn.placement_membership_snapshot
WHERE cell_id = sqlc.arg(cell_id);

-- name: StorePlacementMembershipSnapshot :execrows
-- Writes only a different revision observed no earlier than the stored one, so
-- an unchanged membership costs no write and a replica that received an older
-- revision later cannot replace a newer one.
INSERT INTO foghorn.placement_membership_snapshot (cell_id, revision, payload, observed_at, confirmed_at)
VALUES (sqlc.arg(cell_id), sqlc.arg(revision), sqlc.arg(payload), sqlc.arg(observed_at), sqlc.arg(confirmed_at))
ON CONFLICT (cell_id) DO UPDATE SET
    revision = EXCLUDED.revision,
    payload = EXCLUDED.payload,
    observed_at = EXCLUDED.observed_at,
    confirmed_at = EXCLUDED.confirmed_at
WHERE foghorn.placement_membership_snapshot.revision <> EXCLUDED.revision
  AND foghorn.placement_membership_snapshot.observed_at <= EXCLUDED.observed_at;
