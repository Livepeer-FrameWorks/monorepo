package purserdb

import "context"

// BeginBootstrapGrantPhase isolates a tenant's prepared bootstrap mutation.
func (q *Queries) BeginBootstrapGrantPhase(ctx context.Context) error {
	_, err := q.db.ExecContext(ctx, "SAVEPOINT customer_grant_phase")
	return err
}

// RollbackBootstrapGrantPhase discards a deferred mutation without aborting healthy tenants.
func (q *Queries) RollbackBootstrapGrantPhase(ctx context.Context) error {
	_, err := q.db.ExecContext(ctx, "ROLLBACK TO SAVEPOINT customer_grant_phase")
	return err
}

// ReleaseBootstrapGrantPhase closes the tenant's savepoint after reconciliation.
func (q *Queries) ReleaseBootstrapGrantPhase(ctx context.Context) error {
	_, err := q.db.ExecContext(ctx, "RELEASE SAVEPOINT customer_grant_phase")
	return err
}
