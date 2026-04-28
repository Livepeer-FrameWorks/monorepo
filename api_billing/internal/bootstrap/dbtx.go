package bootstrap

import (
	"context"
	"database/sql"
)

// DBTX is the subset of *sql.DB / *sql.Tx every reconciler needs. The cobra
// dispatcher opens the outer transaction; reconcilers MUST NOT call BeginTx,
// Commit, or Rollback themselves. That contract is what makes --dry-run honest:
// the dispatcher rolls back the same tx the apply path would commit.
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}
