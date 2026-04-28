package bootstrap

import (
	"context"
	"database/sql"
)

// DBTX is the subset of *sql.DB / *sql.Tx that every reconciler needs. The
// cobra dispatcher opens the outer transaction; reconcilers MUST NOT call
// BeginTx, Commit, or Rollback themselves.
//
// This pivot is what makes --dry-run honest: dry-run is just "run the same
// reconcile path against a tx the dispatcher rolls back instead of committing."
// If a reconciler opened its own tx and committed inside, --dry-run could not
// be a true preview.
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}
