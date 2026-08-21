package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"frameworks/api_dns/internal/database/navigatordb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
)

func tenantAliasFromDB(row navigatordb.NavigatorTenantAlias) TenantAlias {
	return TenantAlias{
		TenantID: row.TenantID, Subdomain: row.Subdomain, Status: row.Status,
		CertIssuedAt: row.CertIssuedAt, LastError: row.LastError, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func tenantEdgeApplyStateFromDB(row navigatordb.NavigatorTenantEdgeApplyState) TenantEdgeApplyState {
	return TenantEdgeApplyState{
		TenantID: row.TenantID, ClusterID: row.ClusterID, NodeID: row.NodeID, BundleID: row.BundleID,
		State: row.State, LastSeedVersion: row.LastSeedVersion, LastAckAt: row.LastAckAt,
		InDNSAt: row.InDnsAt, UpdatedAt: row.UpdatedAt,
	}
}

// EnsureTenantAlias inserts or updates the alias intent row for a
// tenant. On conflict (same tenant_id), updates subdomain + bumps
// updated_at. A new row, or one whose label actually changed, resets the
// cert lifecycle (status=cert_issuing, cert_issued_at/last_error cleared):
// the new label has no cert yet, so preserving cert_issued across a rename
// would falsely report readiness. Re-ensuring the same label leaves the
// worker-driven status untouched.
func (s *Store) EnsureTenantAlias(ctx context.Context, tenantID, subdomain string) (*TenantAlias, error) {
	var row navigatordb.NavigatorTenantAlias
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		row, queryErr = s.q.EnsureTenantAlias(ctx, navigatordb.EnsureTenantAliasParams{TenantID: tenantID, Subdomain: subdomain})
		return queryErr
	})
	if err != nil {
		return nil, err
	}
	a := tenantAliasFromDB(row)
	return &a, nil
}

// GetTenantAlias retrieves a single alias row by tenant_id.
func (s *Store) GetTenantAlias(ctx context.Context, tenantID string) (*TenantAlias, error) {
	row, err := s.q.GetTenantAlias(ctx, tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	a := tenantAliasFromDB(row)
	return &a, nil
}

// ListTenantAliasesByStatus returns alias rows in any of the supplied
// statuses, ordered oldest-updated-first so callers process them in a
// stable order across worker ticks.
func (s *Store) ListTenantAliasesByStatus(ctx context.Context, statuses []string) ([]TenantAlias, error) {
	if len(statuses) == 0 {
		return nil, nil
	}
	rows, err := s.q.ListTenantAliasesByStatus(ctx, statuses)
	if err != nil {
		return nil, err
	}
	var out []TenantAlias
	for _, row := range rows {
		out = append(out, tenantAliasFromDB(row))
	}
	return out, nil
}

// ListPendingTenantAliases returns rows with status cert_issuing or
// cert_failed. The intent reconciler worker processes these.
func (s *Store) ListPendingTenantAliases(ctx context.Context) ([]TenantAlias, error) {
	rows, err := s.q.ListPendingTenantAliases(ctx)
	if err != nil {
		return nil, err
	}
	var out []TenantAlias
	for _, row := range rows {
		out = append(out, tenantAliasFromDB(row))
	}
	return out, nil
}

// SetTenantAliasStatus transitions alias lifecycle. Successful cert
// issuance records cert_issued_at; failures record last_error.
func (s *Store) SetTenantAliasStatus(ctx context.Context, tenantID, status, errMsg string) error {
	return s.q.SetTenantAliasStatus(ctx, navigatordb.SetTenantAliasStatusParams{TenantID: tenantID, Status: status, ErrMsg: errMsg})
}

// DeleteTenantAlias removes the alias intent row. Called on tenant
// downgrade/cancellation after Navigator has torn down DNS + cert.
func (s *Store) DeleteTenantAlias(ctx context.Context, tenantID string) error {
	return s.q.DeleteTenantAlias(ctx, tenantID)
}

// UpsertTenantEdgeApplyState writes per-edge bundle apply state. Foghorn
// reports Helmsman ACKs into this table; DNS reconciliation transitions
// applied rows to in_dns after Bunny publishes them.
func (s *Store) UpsertTenantEdgeApplyState(ctx context.Context, st *TenantEdgeApplyState) error {
	return database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		return s.q.UpsertTenantEdgeApplyState(ctx, navigatordb.UpsertTenantEdgeApplyStateParams{
			TenantID: st.TenantID, ClusterID: st.ClusterID, NodeID: st.NodeID, BundleID: st.BundleID,
			State: st.State, LastSeedVersion: st.LastSeedVersion, LastAckAt: st.LastAckAt, InDnsAt: st.InDNSAt,
		})
	})
}

// TenantAliasHasDNS returns true once at least one edge is currently in
// the tenant's DNS pool.
func (s *Store) TenantAliasHasDNS(ctx context.Context, tenantID string) (bool, error) {
	var ok bool
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		ok, queryErr = s.q.TenantAliasHasDNS(ctx, tenantID)
		return queryErr
	})
	return ok, err
}

// ListTenantEdgeApplyState returns rows for a tenant, optionally
// filtered by state. Empty stateFilter returns all states.
func (s *Store) ListTenantEdgeApplyState(ctx context.Context, tenantID, stateFilter string) ([]TenantEdgeApplyState, error) {
	var rows []navigatordb.NavigatorTenantEdgeApplyState
	var err error
	if stateFilter != "" {
		rows, err = s.q.ListTenantEdgeApplyStateByState(ctx, navigatordb.ListTenantEdgeApplyStateByStateParams{
			TenantID: tenantID, State: stateFilter,
		})
	} else {
		rows, err = s.q.ListTenantEdgeApplyState(ctx, tenantID)
	}
	if err != nil {
		return nil, err
	}
	var out []TenantEdgeApplyState
	for _, row := range rows {
		out = append(out, tenantEdgeApplyStateFromDB(row))
	}
	return out, nil
}

// DeleteTenantEdgeApplyState removes all per-edge state for a tenant.
// Called on tenant alias teardown.
func (s *Store) DeleteTenantEdgeApplyState(ctx context.Context, tenantID string) error {
	return s.q.DeleteTenantEdgeApplyState(ctx, tenantID)
}

// DeleteTenantEdgeApplyStateForCluster removes DNS eligibility state for
// one tenant/cluster pair. Called when Quartermaster removes that
// subscription; Navigator republish then removes those edges from Bunny
// before Foghorn drops the cert from the edge.
func (s *Store) DeleteTenantEdgeApplyStateForCluster(ctx context.Context, tenantID, clusterID string) error {
	return s.q.DeleteTenantEdgeApplyStateForCluster(ctx, navigatordb.DeleteTenantEdgeApplyStateForClusterParams{
		TenantID: tenantID, ClusterID: clusterID,
	})
}

// InsertTenantAliasRetirement records intent to clear one retired label's
// Bunny records. Idempotent on (tenant_id, subdomain): a duplicate keeps the
// original requested_at so the staleness comparison stays stable.
func (s *Store) InsertTenantAliasRetirement(ctx context.Context, tenantID, subdomain string) error {
	return s.q.InsertTenantAliasRetirement(ctx, navigatordb.InsertTenantAliasRetirementParams{
		TenantID: tenantID, Subdomain: subdomain,
	})
}

// ListTenantAliasRetirements returns all pending retirement rows, oldest
// first. The alias worker drains these each tick.
func (s *Store) ListTenantAliasRetirements(ctx context.Context) ([]TenantAliasRetirement, error) {
	rows, err := s.q.ListTenantAliasRetirements(ctx)
	if err != nil {
		return nil, err
	}
	var out []TenantAliasRetirement
	for _, row := range rows {
		out = append(out, TenantAliasRetirement{
			TenantID: row.TenantID, Subdomain: row.Subdomain, RequestedAt: row.RequestedAt,
			Attempts: int(row.Attempts), LastError: row.LastError,
		})
	}
	return out, nil
}

// ListTenantAliasRetirementLabels returns the pending retirement labels for
// one tenant. The Quartermaster backstop reads this (via GetTenantAliasStatus) to
// avoid re-enqueuing a retire that is already in flight.
func (s *Store) ListTenantAliasRetirementLabels(ctx context.Context, tenantID string) ([]string, error) {
	labels, err := s.q.ListTenantAliasRetirementLabels(ctx, tenantID)
	if err != nil || len(labels) != 0 {
		return labels, err
	}
	return nil, nil
}

// DeleteTenantAliasRetirement removes a retirement row after its records
// are cleared (or when it is dropped as stale).
func (s *Store) DeleteTenantAliasRetirement(ctx context.Context, tenantID, subdomain string) error {
	return s.q.DeleteTenantAliasRetirement(ctx, navigatordb.DeleteTenantAliasRetirementParams{
		TenantID: tenantID, Subdomain: subdomain,
	})
}

// RecordTenantAliasRetirementFailure bumps attempts and records the error
// when a Bunny clear fails, leaving the row pending for the next tick.
func (s *Store) RecordTenantAliasRetirementFailure(ctx context.Context, tenantID, subdomain, errMsg string) error {
	return s.q.RecordTenantAliasRetirementFailure(ctx, navigatordb.RecordTenantAliasRetirementFailureParams{
		TenantID: tenantID, Subdomain: subdomain, ErrMsg: errMsg,
	})
}
