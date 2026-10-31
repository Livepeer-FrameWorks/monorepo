package commodoredb

import "context"

// BeginBootstrapAccountUser isolates a tenant's prepared bootstrap mutation.
func (q *Queries) BeginBootstrapAccountUser(ctx context.Context) error {
	_, err := q.db.ExecContext(ctx, "SAVEPOINT bootstrap_account_user")
	return err
}

// RollbackBootstrapAccountUser discards a deferred mutation without aborting healthy tenants.
func (q *Queries) RollbackBootstrapAccountUser(ctx context.Context) error {
	_, err := q.db.ExecContext(ctx, "ROLLBACK TO SAVEPOINT bootstrap_account_user")
	return err
}

// ReleaseBootstrapAccountUser closes the tenant's savepoint after reconciliation.
func (q *Queries) ReleaseBootstrapAccountUser(ctx context.Context) error {
	_, err := q.db.ExecContext(ctx, "RELEASE SAVEPOINT bootstrap_account_user")
	return err
}
