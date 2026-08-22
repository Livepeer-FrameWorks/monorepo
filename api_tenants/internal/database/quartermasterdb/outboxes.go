package quartermasterdb

import "context"

const failNavigatorTenantAliasOutbox = `
UPDATE quartermaster.navigator_tenant_alias_outbox
SET attempts = attempts + 1,
    last_error = $2,
    claimed_at = NULL,
    next_retry_at = NOW() + $3::interval
WHERE id = $1::uuid
`

type FailNavigatorTenantAliasOutboxParams struct {
	ID            string
	LastError     string
	RetryInterval string
}

// FailNavigatorTenantAliasOutbox preserves the textual interval accepted by
// PostgreSQL; sqlc maps interval parameters to integer duration values.
func (q *Queries) FailNavigatorTenantAliasOutbox(ctx context.Context, arg FailNavigatorTenantAliasOutboxParams) error {
	_, err := q.db.ExecContext(ctx, failNavigatorTenantAliasOutbox, arg.ID, arg.LastError, arg.RetryInterval)
	return err
}
