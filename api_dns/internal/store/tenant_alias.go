package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/lib/pq"
)

// EnsureTenantAlias inserts or updates the alias intent row for a
// tenant. On conflict (same tenant_id), updates subdomain + bumps
// updated_at. Status defaults to cert_issuing for new rows; existing
// rows keep their current status (the worker drives status).
func (s *Store) EnsureTenantAlias(ctx context.Context, tenantID, subdomain string) (*TenantAlias, error) {
	const q = `
		INSERT INTO navigator.tenant_aliases (tenant_id, subdomain, status, created_at, updated_at)
		VALUES ($1::uuid, $2, 'cert_issuing', NOW(), NOW())
		ON CONFLICT (tenant_id) DO UPDATE SET
			subdomain = EXCLUDED.subdomain,
			updated_at = NOW()
		RETURNING tenant_id, subdomain, status, cert_issued_at, last_error, created_at, updated_at
	`
	var a TenantAlias
	err := s.db.QueryRowContext(ctx, q, tenantID, subdomain).Scan(
		&a.TenantID, &a.Subdomain, &a.Status, &a.CertIssuedAt, &a.LastError, &a.CreatedAt, &a.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// GetTenantAlias retrieves a single alias row by tenant_id.
func (s *Store) GetTenantAlias(ctx context.Context, tenantID string) (*TenantAlias, error) {
	const q = `
		SELECT tenant_id, subdomain, status, cert_issued_at, last_error, created_at, updated_at
		FROM navigator.tenant_aliases
		WHERE tenant_id = $1::uuid
	`
	var a TenantAlias
	err := s.db.QueryRowContext(ctx, q, tenantID).Scan(
		&a.TenantID, &a.Subdomain, &a.Status, &a.CertIssuedAt, &a.LastError, &a.CreatedAt, &a.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// ListTenantAliasesByStatus returns alias rows in any of the supplied
// statuses, ordered oldest-updated-first so callers process them in a
// stable order across worker ticks.
func (s *Store) ListTenantAliasesByStatus(ctx context.Context, statuses []string) ([]TenantAlias, error) {
	if len(statuses) == 0 {
		return nil, nil
	}
	const q = `
		SELECT tenant_id, subdomain, status, cert_issued_at, last_error, created_at, updated_at
		FROM navigator.tenant_aliases
		WHERE status = ANY($1)
		ORDER BY updated_at ASC
	`
	rows, err := s.db.QueryContext(ctx, q, pq.Array(statuses))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TenantAlias
	for rows.Next() {
		var a TenantAlias
		if err := rows.Scan(&a.TenantID, &a.Subdomain, &a.Status, &a.CertIssuedAt, &a.LastError, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// ListPendingTenantAliases returns rows with status cert_issuing or
// cert_failed. The intent reconciler worker processes these.
func (s *Store) ListPendingTenantAliases(ctx context.Context) ([]TenantAlias, error) {
	const q = `
		SELECT tenant_id, subdomain, status, cert_issued_at, last_error, created_at, updated_at
		FROM navigator.tenant_aliases
		WHERE status IN ('cert_issuing', 'cert_failed')
		ORDER BY updated_at ASC
	`
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TenantAlias
	for rows.Next() {
		var a TenantAlias
		if err := rows.Scan(&a.TenantID, &a.Subdomain, &a.Status, &a.CertIssuedAt, &a.LastError, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// SetTenantAliasStatus transitions alias lifecycle. Successful cert
// issuance records cert_issued_at; failures record last_error.
func (s *Store) SetTenantAliasStatus(ctx context.Context, tenantID, status, errMsg string) error {
	const q = `
		UPDATE navigator.tenant_aliases
		SET status = $2,
		    cert_issued_at = CASE WHEN $2 = 'cert_issued' THEN NOW() ELSE cert_issued_at END,
		    last_error = NULLIF($3, ''),
		    updated_at = NOW()
		WHERE tenant_id = $1::uuid
	`
	_, err := s.db.ExecContext(ctx, q, tenantID, status, errMsg)
	return err
}

// DeleteTenantAlias removes the alias intent row. Called on tenant
// downgrade/cancellation after Navigator has torn down DNS + cert.
func (s *Store) DeleteTenantAlias(ctx context.Context, tenantID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM navigator.tenant_aliases WHERE tenant_id = $1::uuid`, tenantID)
	return err
}

// UpsertTenantEdgeApplyState writes per-edge bundle apply state. Foghorn
// reports Helmsman ACKs into this table; DNS reconciliation transitions
// applied rows to in_dns after Bunny publishes them.
func (s *Store) UpsertTenantEdgeApplyState(ctx context.Context, st *TenantEdgeApplyState) error {
	const q = `
		INSERT INTO navigator.tenant_edge_apply_state (
			tenant_id, cluster_id, node_id, bundle_id,
			state, last_seed_version, last_ack_at, in_dns_at, updated_at
		)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, NOW())
		ON CONFLICT (tenant_id, node_id, bundle_id) DO UPDATE SET
			cluster_id = EXCLUDED.cluster_id,
			state = EXCLUDED.state,
			last_seed_version = EXCLUDED.last_seed_version,
			last_ack_at = EXCLUDED.last_ack_at,
			in_dns_at = EXCLUDED.in_dns_at,
			updated_at = NOW()
	`
	_, err := s.db.ExecContext(ctx, q,
		st.TenantID, st.ClusterID, st.NodeID, st.BundleID,
		st.State, st.LastSeedVersion, st.LastAckAt, st.InDNSAt,
	)
	return err
}

// TenantAliasHasDNS returns true once at least one edge is currently in
// the tenant's DNS pool.
func (s *Store) TenantAliasHasDNS(ctx context.Context, tenantID string) (bool, error) {
	var ok bool
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM navigator.tenant_edge_apply_state
			WHERE tenant_id = $1::uuid AND state = 'in_dns'
		)
	`, tenantID).Scan(&ok)
	return ok, err
}

// ListTenantEdgeApplyState returns rows for a tenant, optionally
// filtered by state. Empty stateFilter returns all states.
func (s *Store) ListTenantEdgeApplyState(ctx context.Context, tenantID, stateFilter string) ([]TenantEdgeApplyState, error) {
	q := `
		SELECT tenant_id, cluster_id, node_id, bundle_id,
		       state, last_seed_version, last_ack_at, in_dns_at, updated_at
		FROM navigator.tenant_edge_apply_state
		WHERE tenant_id = $1::uuid
	`
	args := []interface{}{tenantID}
	if stateFilter != "" {
		q += " AND state = $2"
		args = append(args, stateFilter)
	}
	q += " ORDER BY updated_at DESC"
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TenantEdgeApplyState
	for rows.Next() {
		var st TenantEdgeApplyState
		if err := rows.Scan(
			&st.TenantID, &st.ClusterID, &st.NodeID, &st.BundleID,
			&st.State, &st.LastSeedVersion, &st.LastAckAt, &st.InDNSAt, &st.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, nil
}

// DeleteTenantEdgeApplyState removes all per-edge state for a tenant.
// Called on tenant alias teardown.
func (s *Store) DeleteTenantEdgeApplyState(ctx context.Context, tenantID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM navigator.tenant_edge_apply_state WHERE tenant_id = $1::uuid`, tenantID)
	return err
}

// DeleteTenantEdgeApplyStateForCluster removes DNS eligibility state for
// one tenant/cluster pair. Called when Quartermaster removes that
// subscription; Navigator republish then removes those edges from Bunny
// before Foghorn drops the cert from the edge.
func (s *Store) DeleteTenantEdgeApplyStateForCluster(ctx context.Context, tenantID, clusterID string) error {
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM navigator.tenant_edge_apply_state
		WHERE tenant_id = $1::uuid AND cluster_id = $2
	`, tenantID, clusterID)
	return err
}
