package datamigrate

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Acquire takes the lease for one scope. Returns true if the caller now owns
// the lease, false if another live owner holds it. Stale leases (expired) are
// reclaimed by the next caller. attempt_count is incremented on every
// successful acquire.
//
// owner should be a stable identifier of the worker (hostname+pid is a
// reasonable default). ttl should be > the longest expected batch interval;
// keep the lease alive with Heartbeat between batches.
func Acquire(ctx context.Context, db *sql.DB, id string, scope ScopeKey, owner string, ttl time.Duration) (bool, error) {
	if owner == "" {
		return false, fmt.Errorf("Acquire: empty owner")
	}
	if ttl <= 0 {
		return false, fmt.Errorf("Acquire: non-positive ttl")
	}
	res, err := db.ExecContext(ctx, `
		UPDATE _data_migration_runs
		SET lease_owner = $4,
		    lease_expires_at = NOW() + ($5 * INTERVAL '1 second'),
		    attempt_count = attempt_count + 1,
		    status = $6,
		    started_at = COALESCE(started_at, NOW()),
		    updated_at = NOW()
		WHERE id = $1 AND scope_kind = $2 AND scope_value = $3
			  AND (lease_owner IS NULL
			       OR lease_owner = $4
			       OR lease_expires_at < NOW())
			  AND status NOT IN ($7, $8)
			  AND EXISTS (
			      SELECT 1 FROM _data_migrations
			      WHERE id = $1 AND status <> $8
			  )`,
		id, scope.Kind, scope.Value, owner, ttl.Seconds(),
		string(StatusRunning), string(StatusCompleted), string(StatusPaused))
	if err != nil {
		return false, fmt.Errorf("acquire lease %q/%s: %w", id, scope, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// Heartbeat extends the lease for owner. Returns false if the row no longer
// belongs to owner (e.g. someone else reclaimed an expired lease).
func Heartbeat(ctx context.Context, db *sql.DB, id string, scope ScopeKey, owner string, ttl time.Duration) (bool, error) {
	if ttl <= 0 {
		return false, fmt.Errorf("Heartbeat: non-positive ttl")
	}
	res, err := db.ExecContext(ctx, `
		UPDATE _data_migration_runs
		SET lease_expires_at = NOW() + ($4 * INTERVAL '1 second'), updated_at = NOW()
		WHERE id = $1 AND scope_kind = $2 AND scope_value = $3 AND lease_owner = $5`,
		id, scope.Kind, scope.Value, ttl.Seconds(), owner)
	if err != nil {
		return false, fmt.Errorf("heartbeat lease %q/%s: %w", id, scope, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// Release clears the lease for owner. Idempotent — releasing an already-released
// or someone-else-owned lease is a no-op.
func Release(ctx context.Context, db *sql.DB, id string, scope ScopeKey, owner string) error {
	_, err := db.ExecContext(ctx, `
		UPDATE _data_migration_runs
		SET lease_owner = NULL, lease_expires_at = NULL, updated_at = NOW()
		WHERE id = $1 AND scope_kind = $2 AND scope_value = $3 AND lease_owner = $4`,
		id, scope.Kind, scope.Value, owner)
	if err != nil {
		return fmt.Errorf("release lease %q/%s: %w", id, scope, err)
	}
	return nil
}
