package quartermasterdb

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type MembershipPageFilter struct {
	ScopeID    string
	CursorTime *time.Time
	CursorID   string
	Backward   bool
	Limit      int
}
type ClusterInviteListRow struct {
	ID, ClusterID, InvitedTenantID, InviteToken, AccessLevel string
	ResourceLimits                                           sql.NullString
	Status, CreatedBy                                        string
	CreatedAt                                                time.Time
	ExpiresAt, AcceptedAt                                    sql.NullTime
	InvitedTenantName, ClusterName                           sql.NullString
}

func (q *Queries) listClusterInvitesPage(ctx context.Context, filter MembershipPageFilter, received bool) ([]ClusterInviteListRow, int32, error) {
	baseWhere := "WHERE i.cluster_id = $1"
	if received {
		baseWhere = "WHERE i.invited_tenant_id = $1 AND i.status = 'pending' AND (i.expires_at IS NULL OR i.expires_at > NOW())"
	}
	var total int32
	if err := q.db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM quartermaster.cluster_invites i %s", baseWhere), filter.ScopeID).Scan(&total); err != nil {
		return nil, 0, err
	}
	where, args, direction := baseWhere, []any{filter.ScopeID}, "DESC"
	if filter.Backward {
		direction = "ASC"
	}
	if filter.CursorTime != nil {
		op := "<"
		if filter.Backward {
			op = ">"
		}
		where += fmt.Sprintf(" AND (i.created_at, i.id) %s ($2, $3)", op)
		args = append(args, *filter.CursorTime, filter.CursorID)
	}
	selectTail := "t.name as invited_tenant_name, c.cluster_name"
	join := "LEFT JOIN quartermaster.tenants t ON i.invited_tenant_id = t.id LEFT JOIN quartermaster.infrastructure_clusters c ON i.cluster_id = c.cluster_id"
	if received {
		selectTail = "NULL::text as invited_tenant_name, c.cluster_name"
		join = "JOIN quartermaster.infrastructure_clusters c ON i.cluster_id = c.cluster_id"
	}
	query := fmt.Sprintf(`
		SELECT i.id, i.cluster_id, i.invited_tenant_id, i.invite_token, i.access_level,
		       i.resource_limits, i.status, i.created_by, i.created_at, i.expires_at, i.accepted_at, %s
		FROM quartermaster.cluster_invites i %s %s ORDER BY i.created_at %s, i.id %s LIMIT $%d
	`, selectTail, join, where, direction, direction, len(args)+1)
	args = append(args, filter.Limit)
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var result []ClusterInviteListRow
	for rows.Next() {
		var row ClusterInviteListRow
		if err := rows.Scan(&row.ID, &row.ClusterID, &row.InvitedTenantID, &row.InviteToken,
			&row.AccessLevel, &row.ResourceLimits, &row.Status, &row.CreatedBy, &row.CreatedAt, &row.ExpiresAt, &row.AcceptedAt,
			&row.InvitedTenantName, &row.ClusterName); err != nil {
			return nil, 0, err
		}
		result = append(result, row)
	}
	return result, total, rows.Err()
}
func (q *Queries) ListClusterInvitesPage(ctx context.Context, filter MembershipPageFilter) ([]ClusterInviteListRow, int32, error) {
	return q.listClusterInvitesPage(ctx, filter, false)
}
func (q *Queries) ListReceivedClusterInvitesPage(ctx context.Context, filter MembershipPageFilter) ([]ClusterInviteListRow, int32, error) {
	return q.listClusterInvitesPage(ctx, filter, true)
}

type ClusterSubscriptionListRow struct {
	ID, TenantID, ClusterID, AccessLevel, SubscriptionStatus string
	ResourceLimits                                           sql.NullString
	RequestedAt, ApprovedAt                                  sql.NullTime
	ApprovedBy, RejectionReason                              sql.NullString
	ExpiresAt                                                sql.NullTime
	CreatedAt, UpdatedAt                                     time.Time
	ClusterName, TenantName                                  sql.NullString
}

func (q *Queries) ListPendingSubscriptionsPage(ctx context.Context, filter MembershipPageFilter) ([]ClusterSubscriptionListRow, int32, error) {
	baseWhere := "WHERE a.cluster_id = $1 AND a.subscription_status = 'pending_approval'"
	var total int32
	if err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM quartermaster.tenant_cluster_access a WHERE a.cluster_id = $1 AND a.subscription_status = 'pending_approval'`, filter.ScopeID).Scan(&total); err != nil {
		return nil, 0, err
	}
	where, args, direction := baseWhere, []any{filter.ScopeID}, "DESC"
	if filter.Backward {
		direction = "ASC"
	}
	if filter.CursorTime != nil {
		op := "<"
		if filter.Backward {
			op = ">"
		}
		where += fmt.Sprintf(" AND (a.created_at, a.id) %s ($2, $3)", op)
		args = append(args, *filter.CursorTime, filter.CursorID)
	}
	query := fmt.Sprintf(`
		SELECT a.id, a.tenant_id, a.cluster_id, a.access_level, a.subscription_status,
		       a.resource_limits, a.requested_at, a.approved_at, a.approved_by, a.rejection_reason,
		       a.expires_at, a.created_at, a.updated_at, c.cluster_name, t.name as tenant_name
		FROM quartermaster.tenant_cluster_access a
		JOIN quartermaster.infrastructure_clusters c ON a.cluster_id = c.cluster_id
		JOIN quartermaster.tenants t ON a.tenant_id = t.id
		%s ORDER BY a.created_at %s, a.id %s LIMIT $%d
	`, where, direction, direction, len(args)+1)
	args = append(args, filter.Limit)
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var result []ClusterSubscriptionListRow
	for rows.Next() {
		var row ClusterSubscriptionListRow
		if err := rows.Scan(&row.ID, &row.TenantID, &row.ClusterID, &row.AccessLevel,
			&row.SubscriptionStatus, &row.ResourceLimits, &row.RequestedAt, &row.ApprovedAt, &row.ApprovedBy,
			&row.RejectionReason, &row.ExpiresAt, &row.CreatedAt, &row.UpdatedAt, &row.ClusterName, &row.TenantName); err != nil {
			return nil, 0, err
		}
		result = append(result, row)
	}
	return result, total, rows.Err()
}
