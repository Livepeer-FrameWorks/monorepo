package bootstrap

import (
	"context"
	"database/sql"
)

// DBTX is the subset of *sql.DB / *sql.Tx every reconciler needs. The cobra
// dispatcher opens the outer transaction; reconcilers MUST NOT call BeginTx,
// Commit, or Rollback themselves so --dry-run can roll back the same tx the
// apply path would commit.
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// TenantResolver resolves a tenant alias (the bootstrap stable key) into the
// tenant UUID. The cobra dispatcher wires this to a Quartermaster gRPC client
// (ResolveTenantAliases). Tests inject a static map.
//
// Pulling the abstraction out of the reconciler keeps cross-service IO at the
// cobra boundary, where the dispatcher wires both the executor and the resolver
// — and replaces the previous direct read of QM's bootstrap_tenant_aliases.
type TenantResolver interface {
	Resolve(ctx context.Context, alias string) (string, error)
}
