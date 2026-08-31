package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"frameworks/api_dns/internal/database/navigatordb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
)

func tenantAliasFromDB(row navigatordb.NavigatorTenantAlias) TenantAlias {
	return TenantAlias{
		TenantID: row.TenantID, Subdomain: row.Subdomain, Status: row.Status,
		AuthorityVersion: row.AuthorityVersion,
		CertIssuedAt:     row.CertIssuedAt, LastError: row.LastError, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func tenantEdgeApplyStateFromDB(
	tenantID, clusterID, nodeID, bundleID, bundleVersion, currentBundleVersion, state string,
	currentBundlePresent bool,
	lastSeedVersion sql.NullInt64,
	lastDeliverySequence int64,
	lastAckAt, inDNSAt sql.NullTime,
	updatedAt time.Time,
) TenantEdgeApplyState {
	return TenantEdgeApplyState{
		TenantID: tenantID, ClusterID: clusterID, NodeID: nodeID, BundleID: bundleID,
		BundleVersion: bundleVersion, CurrentBundleVersion: currentBundleVersion, CurrentBundlePresent: currentBundlePresent,
		State: state, LastSeedVersion: lastSeedVersion, LastDeliverySequence: lastDeliverySequence, LastAckAt: lastAckAt,
		InDNSAt: inDNSAt, UpdatedAt: updatedAt,
	}
}

// EnsureTenantAlias inserts or updates the alias intent row for a
// tenant. On conflict (same tenant_id), updates subdomain + bumps
// updated_at. A new row, a changed label, or reactivation during teardown
// resets the cert lifecycle. Re-ensuring the same active label leaves the
// worker-driven status untouched.
func (s *Store) EnsureTenantAlias(ctx context.Context, tenantID, subdomain string) (*TenantAlias, error) {
	var row navigatordb.EnsureTenantAliasRow
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		row, queryErr = s.q.EnsureTenantAlias(ctx, navigatordb.EnsureTenantAliasParams{TenantID: tenantID, Subdomain: subdomain})
		return queryErr
	})
	if err != nil {
		return nil, err
	}
	a := TenantAlias{
		TenantID: row.TenantID, Subdomain: row.Subdomain, Status: row.Status,
		AuthorityVersion: row.AuthorityVersion,
		CertIssuedAt:     row.CertIssuedAt, LastError: row.LastError, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	return &a, nil
}

// GetTenantAlias retrieves a single alias row by tenant_id.
func (s *Store) GetTenantAlias(ctx context.Context, tenantID string) (*TenantAlias, error) {
	var row navigatordb.NavigatorTenantAlias
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		row, queryErr = s.q.GetTenantAlias(ctx, tenantID)
		return queryErr
	})
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
	var rows []navigatordb.NavigatorTenantAlias
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		rows, queryErr = s.q.ListTenantAliasesByStatus(ctx, statuses)
		return queryErr
	})
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
	var rows []navigatordb.NavigatorTenantAlias
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		rows, queryErr = s.q.ListPendingTenantAliases(ctx)
		return queryErr
	})
	if err != nil {
		return nil, err
	}
	var out []TenantAlias
	for _, row := range rows {
		out = append(out, tenantAliasFromDB(row))
	}
	return out, nil
}

// SetTenantAliasStatus transitions alias lifecycle. Teardown is an intent
// writer; issuance completion is fenced by the subdomain and an active issue
// state so late ACME work cannot cross a rename or teardown.
func (s *Store) SetTenantAliasStatus(ctx context.Context, tenantID, expectedSubdomain string, expectedAuthorityVersion int64, status, errMsg string) (bool, error) {
	var rows int64
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		rows, queryErr = s.q.SetTenantAliasStatus(ctx, navigatordb.SetTenantAliasStatusParams{
			TenantID: tenantID, ExpectedSubdomain: expectedSubdomain, ExpectedAuthorityVersion: expectedAuthorityVersion, Status: status, ErrMsg: errMsg,
		})
		return queryErr
	})
	return rows == 1, err
}

// DeleteTenantAlias atomically removes credentials and intent only while the
// row still carries teardown authority. A concurrent reactivation wins by
// changing the status, causing this operation to affect zero rows.
func (s *Store) DeleteTenantAlias(ctx context.Context, tenantID string) (bool, error) {
	var rows int64
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		rows, queryErr = s.q.DeleteTenantAlias(ctx, tenantID)
		return queryErr
	})
	return rows == 1, err
}

// MarkTenantEdgeInDNS promotes the exact ACK snapshot the DNS worker published.
// A false result means an ACK advanced the row while external reconciliation was
// in flight; the worker must re-read rather than replaying stale ACK columns.
func (s *Store) MarkTenantEdgeInDNS(ctx context.Context, st *TenantEdgeApplyState) (bool, error) {
	var rows int64
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		rows, queryErr = s.q.MarkTenantEdgeInDNS(ctx, navigatordb.MarkTenantEdgeInDNSParams{
			TenantID: st.TenantID, NodeID: st.NodeID, BundleID: st.BundleID,
			SnapshotBundleVersion: st.BundleVersion, SnapshotSeedVersion: st.LastSeedVersion,
			SnapshotDeliverySequence: st.LastDeliverySequence,
		})
		return queryErr
	})
	return rows == 1, err
}

// MarkTenantEdgeNotInDNS removes only the exact published snapshot from DNS.
func (s *Store) MarkTenantEdgeNotInDNS(ctx context.Context, st *TenantEdgeApplyState) (bool, error) {
	var rows int64
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		rows, queryErr = s.q.MarkTenantEdgeNotInDNS(ctx, navigatordb.MarkTenantEdgeNotInDNSParams{
			TenantID: st.TenantID, NodeID: st.NodeID, BundleID: st.BundleID,
			SnapshotBundleVersion: st.BundleVersion, SnapshotSeedVersion: st.LastSeedVersion,
			SnapshotDeliverySequence: st.LastDeliverySequence,
		})
		return queryErr
	})
	return rows == 1, err
}

type TenantEdgeApplyAckOutcome string

const (
	TenantEdgeApplyAckAccepted TenantEdgeApplyAckOutcome = "accepted"
	TenantEdgeApplyAckStale    TenantEdgeApplyAckOutcome = "stale"
	// TenantEdgeApplyAckRevoked distinguishes an ACK rejected because the
	// tenant/cluster authority carries a revocation tombstone from an
	// ordinary fence rejection, so a revocation race stays observable.
	TenantEdgeApplyAckRevoked       TenantEdgeApplyAckOutcome = "revoked"
	TenantEdgeApplyAckMissingParent TenantEdgeApplyAckOutcome = "missing_parent"
)

// UpsertTenantEdgeApplyAck applies a newer seed or advances the equal-version
// delivery fence. A successful replay preserves Navigator's in_dns state; an
// equal-version failure demotes it and a later recovery restores applied. Its
// result distinguishes an obsolete ACK from one whose alias authority no
// longer exists.
func (s *Store) UpsertTenantEdgeApplyAck(ctx context.Context, st *TenantEdgeApplyState) (TenantEdgeApplyAckOutcome, error) {
	if st.State != "applied" && st.State != "pending_apply" {
		return "", fmt.Errorf("unsupported tenant edge apply ACK state %q", st.State)
	}
	var outcome string
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		outcome, queryErr = s.q.UpsertTenantEdgeApplyAck(ctx, navigatordb.UpsertTenantEdgeApplyAckParams{
			TenantID: st.TenantID, ClusterID: st.ClusterID, NodeID: st.NodeID, BundleID: st.BundleID,
			BundleVersion: st.BundleVersion,
			State:         st.State, LastSeedVersion: st.LastSeedVersion, LastDeliverySequence: st.LastDeliverySequence, LastAckAt: st.LastAckAt,
		})
		return queryErr
	})
	return TenantEdgeApplyAckOutcome(outcome), err
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
	var out []TenantEdgeApplyState
	if stateFilter != "" {
		var rows []navigatordb.ListTenantEdgeApplyStateByStateRow
		err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
			var queryErr error
			rows, queryErr = s.q.ListTenantEdgeApplyStateByState(ctx, navigatordb.ListTenantEdgeApplyStateByStateParams{
				TenantID: tenantID, State: stateFilter,
			})
			return queryErr
		})
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			out = append(out, tenantEdgeApplyStateFromDB(
				row.TenantID, row.ClusterID, row.NodeID, row.BundleID, row.BundleVersion, row.CurrentBundleVersion, row.State, row.CurrentBundlePresent,
				row.LastSeedVersion, row.LastDeliverySequence, row.LastAckAt, row.InDnsAt, row.UpdatedAt,
			))
		}
	} else {
		var rows []navigatordb.ListTenantEdgeApplyStateRow
		err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
			var queryErr error
			rows, queryErr = s.q.ListTenantEdgeApplyState(ctx, tenantID)
			return queryErr
		})
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			out = append(out, tenantEdgeApplyStateFromDB(
				row.TenantID, row.ClusterID, row.NodeID, row.BundleID, row.BundleVersion, row.CurrentBundleVersion, row.State, row.CurrentBundlePresent,
				row.LastSeedVersion, row.LastDeliverySequence, row.LastAckAt, row.InDnsAt, row.UpdatedAt,
			))
		}
	}
	return out, nil
}

// TenantAliasClusterAuthorityState returns active, revoked, or an empty string
// when the local Quartermaster projection has never observed this pair.
func (s *Store) TenantAliasClusterAuthorityState(ctx context.Context, tenantID, clusterID string) (string, error) {
	var state string
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		state, queryErr = s.q.TenantAliasClusterAuthorityState(ctx, navigatordb.TenantAliasClusterAuthorityStateParams{
			TenantID: tenantID, ClusterID: clusterID,
		})
		return queryErr
	})
	return state, err
}

// GrantTenantAliasClusterAuthority applies an ordered positive authority
// decision. A sequence-zero compatibility grant cannot cross an existing
// revocation because revocation wins equal-sequence conflicts in SQL.
func (s *Store) GrantTenantAliasClusterAuthority(ctx context.Context, tenantID, clusterID string, sequence int64) (bool, error) {
	var applied bool
	err := retryAuthorityUpsertDuplicateKey(func() error {
		return database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
			var queryErr error
			applied, queryErr = s.q.GrantTenantAliasClusterAuthority(ctx, navigatordb.GrantTenantAliasClusterAuthorityParams{
				TenantID: tenantID, ClusterID: clusterID, AuthoritySequence: sequence,
			})
			if errors.Is(queryErr, sql.ErrNoRows) {
				applied = false
				return nil
			}
			return queryErr
		})
	})
	return applied, err
}

// retryAuthorityUpsertDuplicateKey re-runs an authority upsert when a
// concurrent grant/tombstone insert race on the same (tenant, cluster) key
// surfaces as duplicate-key 23505. PostgreSQL resolves this race internally
// (speculative insertion, then the ON CONFLICT arm), but some Yugabyte
// versions raise 23505 instead of a retryable 40001 when the conflicting row
// is invisible to the transaction's read time. The retry is scoped to these
// two writers rather than added to the global classifier, where a duplicate
// key is usually a genuine bug. On the re-run the row exists, so the ON
// CONFLICT arm applies the sequence fence normally.
func retryAuthorityUpsertDuplicateKey(fn func() error) error {
	var err error
	for attempt := 0; attempt < database.DefaultRetryAttempts; attempt++ {
		err = fn()
		if database.SQLState(err) != "23505" {
			return err
		}
	}
	return err
}

// RevokeTenantAliasClusterAuthority persists a tombstone and removes all edge
// readiness for the pair in one transaction. The tombstone is the first
// statement, so it creates and locks the serialization row for any pair of an
// existing alias, including one never projected before; with no alias row the
// FK-backed insert selects nothing and the revocation is a durable no-op
// (applied=false) that the Quartermaster backstop re-drives once the alias
// exists. A separately fenced delete then removes edge state.
// On PostgreSQL READ COMMITTED the delete takes a fresh snapshot and sees an
// ACK that committed while the tombstone waited. On Yugabyte correctness comes
// from a 40001 restart replaying the whole transaction with a fresh read time.
// Lock order is authority row (plus the FK's KEY SHARE on the alias row), then
// edge rows; DeleteTenantAlias/EnsureTenantAlias lock the alias row FOR UPDATE
// first, so 40P01 deadlocks are possible and are absorbed by the retry
// wrapper.
func (s *Store) RevokeTenantAliasClusterAuthority(ctx context.Context, tenantID, clusterID string, sequence int64) (bool, error) {
	var applied bool
	err := retryAuthorityUpsertDuplicateKey(func() error {
		return database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
			queries := navigatordb.New(tx)
			applied = false
			if _, tombstoneErr := queries.RevokeTenantAliasClusterAuthority(ctx, navigatordb.RevokeTenantAliasClusterAuthorityParams{
				TenantID: tenantID, ClusterID: clusterID, AuthoritySequence: sequence,
			}); tombstoneErr != nil {
				if errors.Is(tombstoneErr, sql.ErrNoRows) {
					return nil
				}
				return tombstoneErr
			}
			applied = true
			_, deleteErr := queries.DeleteTenantEdgeApplyStateForRevokedCluster(ctx, navigatordb.DeleteTenantEdgeApplyStateForRevokedClusterParams{
				TenantID: tenantID, ClusterID: clusterID,
			})
			return deleteErr
		})
	})
	return applied, err
}

func (s *Store) ListTenantAliasAuthorizedClusters(ctx context.Context, tenantID string) ([]string, error) {
	var clusterIDs []string
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		clusterIDs, queryErr = s.q.ListTenantAliasAuthorizedClusters(ctx, tenantID)
		return queryErr
	})
	return clusterIDs, err
}

// InsertTenantAliasRetirement records intent to clear one retired label's
// Bunny records. Idempotent on (tenant_id, subdomain): a duplicate keeps the
// original requested_at so the staleness comparison stays stable.
func (s *Store) InsertTenantAliasRetirement(ctx context.Context, tenantID, subdomain string) error {
	return database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		return s.q.InsertTenantAliasRetirement(ctx, navigatordb.InsertTenantAliasRetirementParams{
			TenantID: tenantID, Subdomain: subdomain,
		})
	})
}

// ListTenantAliasRetirements returns all pending retirement rows, oldest
// first. The alias worker drains these each tick.
func (s *Store) ListTenantAliasRetirements(ctx context.Context) ([]TenantAliasRetirement, error) {
	var rows []navigatordb.NavigatorTenantAliasRetirement
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		rows, queryErr = s.q.ListTenantAliasRetirements(ctx)
		return queryErr
	})
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
	var labels []string
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		labels, queryErr = s.q.ListTenantAliasRetirementLabels(ctx, tenantID)
		return queryErr
	})
	if err != nil || len(labels) != 0 {
		return labels, err
	}
	return nil, nil
}

// DeleteTenantAliasRetirement removes a retirement row after its records
// are cleared (or when it is dropped as stale).
func (s *Store) DeleteTenantAliasRetirement(ctx context.Context, tenantID, subdomain string) error {
	return database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		return s.q.DeleteTenantAliasRetirement(ctx, navigatordb.DeleteTenantAliasRetirementParams{
			TenantID: tenantID, Subdomain: subdomain,
		})
	})
}

// RecordTenantAliasRetirementFailure bumps attempts and records the error
// when a Bunny clear fails, leaving the row pending for the next tick.
func (s *Store) RecordTenantAliasRetirementFailure(ctx context.Context, tenantID, subdomain, errMsg string) error {
	return database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		return s.q.RecordTenantAliasRetirementFailure(ctx, navigatordb.RecordTenantAliasRetirementFailureParams{
			TenantID: tenantID, Subdomain: subdomain, ErrMsg: errMsg,
		})
	})
}
