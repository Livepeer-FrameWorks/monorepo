package datamigrate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Status is the lifecycle state of a job or run.
type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusPaused    Status = "paused"
)

// JobState is the per-id row in _data_migrations.
type JobState struct {
	ID             string
	ReleaseVersion string
	Status         Status
	StartedAt      *time.Time
	CompletedAt    *time.Time
	LastError      string
	UpdatedAt      time.Time
}

// RunState is one row in _data_migration_runs.
type RunState struct {
	ID             string
	Scope          ScopeKey
	Status         Status
	Checkpoint     json.RawMessage
	LeaseOwner     string
	LeaseExpiresAt *time.Time
	AttemptCount   int
	Scanned        int64
	Changed        int64
	Skipped        int64
	Errors         int64
	LastError      string
	StartedAt      *time.Time
	UpdatedAt      time.Time
	CompletedAt    *time.Time
}

// NotRegisteredError is returned by LoadJob when an id is unknown to both the
// in-process registry and the persisted state. Callers MUST surface this
// distinctly from "registered but pending" — an empty foundation that
// silently passes is exactly what this error prevents.
type NotRegisteredError struct {
	ID string
}

func (e *NotRegisteredError) Error() string {
	return fmt.Sprintf("data migration %q not registered in this binary", e.ID)
}

// IsNotRegistered reports whether err is a *NotRegisteredError.
func IsNotRegistered(err error) bool {
	var e *NotRegisteredError
	return errors.As(err, &e)
}

// LoadJob returns the job lifecycle row for id. If id is in neither the
// registry nor the state table, returns *NotRegisteredError.
func LoadJob(ctx context.Context, db *sql.DB, id string) (JobState, error) {
	if id == "" {
		return JobState{}, errors.New("LoadJob: empty id")
	}
	row := db.QueryRowContext(ctx, `
		SELECT id, release_version, status, started_at, completed_at, COALESCE(last_error, ''), updated_at
		FROM _data_migrations WHERE id = $1`, id)

	var s JobState
	var startedAt, completedAt sql.NullTime
	err := row.Scan(&s.ID, &s.ReleaseVersion, &s.Status, &startedAt, &completedAt, &s.LastError, &s.UpdatedAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if Lookup(id) == nil {
			return JobState{}, &NotRegisteredError{ID: id}
		}
		// Registered but never started.
		return JobState{ID: id, Status: StatusPending}, nil
	case err != nil:
		return JobState{}, fmt.Errorf("load job %q: %w", id, err)
	}
	if startedAt.Valid {
		s.StartedAt = &startedAt.Time
	}
	if completedAt.Valid {
		s.CompletedAt = &completedAt.Time
	}
	return s, nil
}

// LoadRuns returns every per-scope row for id, ordered by (scope_kind, scope_value).
func LoadRuns(ctx context.Context, db *sql.DB, id string) ([]RunState, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, scope_kind, scope_value, status, checkpoint, lease_owner, lease_expires_at,
		       attempt_count, scanned_count, changed_count, skipped_count, error_count,
		       COALESCE(last_error, ''), started_at, updated_at, completed_at
		FROM _data_migration_runs
		WHERE id = $1
		ORDER BY scope_kind, scope_value`, id)
	if err != nil {
		return nil, fmt.Errorf("load runs %q: %w", id, err)
	}
	defer rows.Close()

	var out []RunState
	for rows.Next() {
		var r RunState
		var leaseOwner sql.NullString
		var leaseExpires, startedAt, completedAt sql.NullTime
		if err := rows.Scan(&r.ID, &r.Scope.Kind, &r.Scope.Value, &r.Status, &r.Checkpoint,
			&leaseOwner, &leaseExpires,
			&r.AttemptCount, &r.Scanned, &r.Changed, &r.Skipped, &r.Errors,
			&r.LastError, &startedAt, &r.UpdatedAt, &completedAt); err != nil {
			return nil, err
		}
		if leaseOwner.Valid {
			r.LeaseOwner = leaseOwner.String
		}
		if leaseExpires.Valid {
			r.LeaseExpiresAt = &leaseExpires.Time
		}
		if startedAt.Valid {
			r.StartedAt = &startedAt.Time
		}
		if completedAt.Valid {
			r.CompletedAt = &completedAt.Time
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LoadRun returns one per-scope row for id.
func LoadRun(ctx context.Context, db *sql.DB, id string, scope ScopeKey) (RunState, error) {
	row := db.QueryRowContext(ctx, `
		SELECT id, scope_kind, scope_value, status, checkpoint, lease_owner, lease_expires_at,
		       attempt_count, scanned_count, changed_count, skipped_count, error_count,
		       COALESCE(last_error, ''), started_at, updated_at, completed_at
		FROM _data_migration_runs
		WHERE id = $1 AND scope_kind = $2 AND scope_value = $3`,
		id, scope.Kind, scope.Value)

	var r RunState
	var leaseOwner sql.NullString
	var leaseExpires, startedAt, completedAt sql.NullTime
	if err := row.Scan(&r.ID, &r.Scope.Kind, &r.Scope.Value, &r.Status, &r.Checkpoint,
		&leaseOwner, &leaseExpires,
		&r.AttemptCount, &r.Scanned, &r.Changed, &r.Skipped, &r.Errors,
		&r.LastError, &startedAt, &r.UpdatedAt, &completedAt); err != nil {
		return RunState{}, fmt.Errorf("load run %q/%s: %w", id, scope, err)
	}
	if leaseOwner.Valid {
		r.LeaseOwner = leaseOwner.String
	}
	if leaseExpires.Valid {
		r.LeaseExpiresAt = &leaseExpires.Time
	}
	if startedAt.Valid {
		r.StartedAt = &startedAt.Time
	}
	if completedAt.Valid {
		r.CompletedAt = &completedAt.Time
	}
	return r, nil
}

// MarkJobRunning upserts the job row to running unless the job is already in a
// terminal or operator-held state.
func MarkJobRunning(ctx context.Context, db *sql.DB, id, releaseVersion string) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO _data_migrations (id, release_version, status, started_at, updated_at)
		VALUES ($1, $2, $3, NOW(), NOW())
		ON CONFLICT (id) DO UPDATE SET
			status = CASE
				WHEN _data_migrations.status IN ($4, $5) THEN _data_migrations.status
				ELSE EXCLUDED.status
			END,
			release_version = EXCLUDED.release_version,
			started_at = COALESCE(_data_migrations.started_at, NOW()),
			updated_at = NOW()`,
		id, releaseVersion, string(StatusRunning), string(StatusPaused), string(StatusCompleted))
	if err != nil {
		return fmt.Errorf("mark job running %q: %w", id, err)
	}
	return nil
}

// MarkJobCompleted moves the job row to completed.
func MarkJobCompleted(ctx context.Context, db *sql.DB, id string) error {
	_, err := db.ExecContext(ctx, `
		UPDATE _data_migrations
		SET status = $2, completed_at = NOW(), updated_at = NOW(), last_error = NULL
		WHERE id = $1`, id, string(StatusCompleted))
	if err != nil {
		return fmt.Errorf("mark job completed %q: %w", id, err)
	}
	return nil
}

// MarkJobCompletedIfAllRunsCompleted completes the job only when every known
// scope row is completed. It returns true when the job moved to completed.
func MarkJobCompletedIfAllRunsCompleted(ctx context.Context, db *sql.DB, id string) (bool, error) {
	res, err := db.ExecContext(ctx, `
		UPDATE _data_migrations
		SET status = $2, completed_at = NOW(), updated_at = NOW(), last_error = NULL
		WHERE id = $1
		  AND EXISTS (SELECT 1 FROM _data_migration_runs WHERE id = $1)
		  AND NOT EXISTS (
		      SELECT 1 FROM _data_migration_runs
		      WHERE id = $1 AND status <> $2
		  )`, id, string(StatusCompleted))
	if err != nil {
		return false, fmt.Errorf("complete job when all runs done %q: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// MarkJobFailed moves the job row to failed and records the error.
func MarkJobFailed(ctx context.Context, db *sql.DB, id string, runErr error) error {
	msg := ""
	if runErr != nil {
		msg = runErr.Error()
	}
	_, err := db.ExecContext(ctx, `
		UPDATE _data_migrations
		SET status = $2, last_error = $3, updated_at = NOW()
		WHERE id = $1`, id, string(StatusFailed), msg)
	if err != nil {
		return fmt.Errorf("mark job failed %q: %w", id, err)
	}
	return nil
}

// UpsertRun seeds a run row in pending status (idempotent on PK).
func UpsertRun(ctx context.Context, db *sql.DB, id string, scope ScopeKey) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO _data_migration_runs (id, scope_kind, scope_value, status, updated_at)
		VALUES ($1, $2, $3, $4, NOW())
		ON CONFLICT (id, scope_kind, scope_value) DO NOTHING`,
		id, scope.Kind, scope.Value, string(StatusPending))
	if err != nil {
		return fmt.Errorf("upsert run %q/%s: %w", id, scope, err)
	}
	return nil
}

// Checkpoint persists the worker's progress for one scope.
func Checkpoint(ctx context.Context, db *sql.DB, id string, scope ScopeKey, p Progress) error {
	checkpoint := p.Checkpoint
	if checkpoint == nil {
		checkpoint = json.RawMessage(`{}`)
	}
	_, err := db.ExecContext(ctx, `
		UPDATE _data_migration_runs
		SET status = $4,
		    checkpoint = $5,
		    scanned_count = $6,
		    changed_count = $7,
		    skipped_count = $8,
		    error_count = $9,
		    updated_at = NOW()
		WHERE id = $1 AND scope_kind = $2 AND scope_value = $3`,
		id, scope.Kind, scope.Value, string(StatusRunning),
		checkpoint, p.Scanned, p.Changed, p.Skipped, p.Errors)
	if err != nil {
		return fmt.Errorf("checkpoint %q/%s: %w", id, scope, err)
	}
	return nil
}

// MarkRunCompleted moves one scope's row to completed.
func MarkRunCompleted(ctx context.Context, db *sql.DB, id string, scope ScopeKey) error {
	_, err := db.ExecContext(ctx, `
		UPDATE _data_migration_runs
		SET status = $4, completed_at = NOW(), updated_at = NOW(), last_error = NULL,
		    lease_owner = NULL, lease_expires_at = NULL
		WHERE id = $1 AND scope_kind = $2 AND scope_value = $3`,
		id, scope.Kind, scope.Value, string(StatusCompleted))
	if err != nil {
		return fmt.Errorf("mark run completed %q/%s: %w", id, scope, err)
	}
	return nil
}

// MarkRunFailed records an error against one scope without releasing the
// row's lease — the lease TTL governs retry by another owner.
func MarkRunFailed(ctx context.Context, db *sql.DB, id string, scope ScopeKey, runErr error) error {
	msg := ""
	if runErr != nil {
		msg = runErr.Error()
	}
	_, err := db.ExecContext(ctx, `
		UPDATE _data_migration_runs
		SET status = $4, last_error = $5, updated_at = NOW()
		WHERE id = $1 AND scope_kind = $2 AND scope_value = $3`,
		id, scope.Kind, scope.Value, string(StatusFailed), msg)
	if err != nil {
		return fmt.Errorf("mark run failed %q/%s: %w", id, scope, err)
	}
	return nil
}

// PauseJob marks a registered, non-completed job and every non-completed scope
// paused. A never-started registered job gets an explicit paused row so future
// runs cannot proceed until resume.
func PauseJob(ctx context.Context, db *sql.DB, id, releaseVersion string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // committed path returns before defer matters

	var status Status
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO _data_migrations (id, release_version, status, updated_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (id) DO UPDATE
		SET status = CASE
				WHEN _data_migrations.status = $4 THEN _data_migrations.status
				ELSE EXCLUDED.status
			END,
			updated_at = NOW()
		RETURNING status`,
		id, releaseVersion, string(StatusPaused), string(StatusCompleted)).Scan(&status); err != nil {
		return fmt.Errorf("pause job %q: %w", id, err)
	}
	if status == StatusCompleted {
		return fmt.Errorf("pause job %q: already completed", id)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE _data_migration_runs
		SET status=$2, lease_owner=NULL, lease_expires_at=NULL, updated_at=NOW()
		WHERE id=$1 AND status <> $3`,
		id, string(StatusPaused), string(StatusCompleted)); err != nil {
		return fmt.Errorf("pause runs %q: %w", id, err)
	}
	return tx.Commit()
}

// ResumeJob returns paused job/run rows to pending. Non-paused and completed
// jobs fail explicitly so operators know whether pause actually took effect.
func ResumeJob(ctx context.Context, db *sql.DB, id string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // committed path returns before defer matters

	res, err := tx.ExecContext(ctx, `UPDATE _data_migrations SET status=$2, updated_at=NOW() WHERE id=$1 AND status=$3`,
		id, string(StatusPending), string(StatusPaused))
	if err != nil {
		return fmt.Errorf("resume job %q: %w", id, err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("resume job %q: check affected rows: %w", id, err)
	}
	if rows == 0 {
		var status Status
		if scanErr := tx.QueryRowContext(ctx, `SELECT status FROM _data_migrations WHERE id=$1`, id).Scan(&status); scanErr != nil {
			if errors.Is(scanErr, sql.ErrNoRows) {
				return fmt.Errorf("resume job %q: not paused", id)
			}
			return fmt.Errorf("resume job %q: %w", id, scanErr)
		}
		if status == StatusCompleted {
			return fmt.Errorf("resume job %q: already completed", id)
		}
		return fmt.Errorf("resume job %q: not paused (status %s)", id, status)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE _data_migration_runs
		SET status=$2, updated_at=NOW()
		WHERE id=$1 AND status=$3`,
		id, string(StatusPending), string(StatusPaused)); err != nil {
		return fmt.Errorf("resume runs %q: %w", id, err)
	}
	return tx.Commit()
}

// String formats a ScopeKey for log/error messages.
func (s ScopeKey) String() string {
	if s.IsZero() {
		return "<whole-job>"
	}
	return s.Kind + "=" + s.Value
}
