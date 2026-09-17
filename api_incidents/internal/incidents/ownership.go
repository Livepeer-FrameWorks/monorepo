package incidents

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"frameworks/api_incidents/internal/database/lookoutdb"
)

// ErrScopeUnverified means Quartermaster could not confirm a cluster's owner,
// so no open incident was moved.
var ErrScopeUnverified = errors.New("cluster owner could not be verified")

// ReconcileClusterScope asks Quartermaster for the cluster's current owner,
// stores it as the cluster's scope and moves every open incident of the
// cluster to it. Resolved incidents keep the scope they had when they resolved.
// It returns how many incidents moved.
func (s *Service) ReconcileClusterScope(ctx context.Context, clusterID string) (int, error) {
	clusterID = strings.TrimSpace(clusterID)
	if clusterID == "" {
		return 0, nil
	}
	if s.Owners == nil {
		return 0, fmt.Errorf("%w: no owner lookup for cluster %s", ErrScopeUnverified, clusterID)
	}
	owner, err := s.Owners.LookupOwner(ctx, clusterID)
	if err != nil {
		return 0, fmt.Errorf("%w: cluster %s: %w", ErrScopeUnverified, clusterID, err)
	}
	return s.applyClusterOwner(ctx, clusterID, owner)
}

// applyClusterOwner stores a verified owner, then moves the cluster's open
// incidents to the stored scope.
//
// The two steps are separate transactions. Storing the owner writes the scope
// row, so it waits for every ingestion that read the previous scope under its
// share lock, and a later ingestion either reads the new scope or fails with a
// serialization conflict and retries. The move then starts after those
// ingestions committed. In one transaction, a snapshot-isolation database
// (YugabyteDB runs with read committed disabled) would take its snapshot while
// waiting and never see the incidents those ingestions wrote.
func (s *Service) applyClusterOwner(ctx context.Context, clusterID string, owner ClusterOwner) (int, error) {
	tenantID := ""
	if owner.Scope.Kind == ScopeTenant {
		tenantID = owner.Scope.TenantID
	}
	if err := s.inTx(ctx, func(q *lookoutdb.Queries) error {
		return q.StoreVerifiedClusterScope(ctx, lookoutdb.StoreVerifiedClusterScopeParams{
			ClusterID:       clusterID,
			Scope:           owner.Scope.Kind,
			TenantID:        nullString(tenantID),
			SourceUpdatedAt: sql.NullTime{Time: owner.UpdatedAt, Valid: !owner.UpdatedAt.IsZero()},
		})
	}); err != nil {
		return 0, fmt.Errorf("store cluster scope: %w", err)
	}
	return s.moveOpenClusterIncidents(ctx, clusterID)
}

// moveOpenClusterIncidents moves every open incident of the cluster to its
// stored scope and records the scope revision as applied. The scope row stays
// locked exclusively until the moves commit, so no ingestion reads it
// meanwhile. The stored scope may be newer than the owner the caller stored.
func (s *Service) moveOpenClusterIncidents(ctx context.Context, clusterID string) (int, error) {
	var (
		changes []realtimeChange
		moved   int
		scope   Scope
	)
	err := s.inTx(ctx, func(q *lookoutdb.Queries) error {
		changes, moved = nil, 0
		row, err := q.LockClusterScopeForUpdate(ctx, clusterID)
		if err != nil {
			return fmt.Errorf("lock cluster scope: %w", err)
		}
		if !row.Verified {
			return fmt.Errorf("%w: cluster %s", ErrScopeUnverified, clusterID)
		}
		scope = storedScope(row.Scope, row.TenantID)
		open, err := q.LockOpenClusterIncidents(ctx, clusterID)
		if err != nil {
			return fmt.Errorf("lock open incidents of cluster %s: %w", clusterID, err)
		}
		for _, inc := range open {
			if !scopeDiffers(inc, scope) {
				continue
			}
			_, incidentChanges, rescopeErr := s.rescopeOpenIncident(ctx, q, inc, scope)
			if rescopeErr != nil {
				return rescopeErr
			}
			changes = append(changes, incidentChanges...)
			moved++
		}
		if err := q.MarkClusterScopeApplied(ctx, lookoutdb.MarkClusterScopeAppliedParams{ClusterID: clusterID, Revision: row.Revision}); err != nil {
			return fmt.Errorf("mark cluster scope applied: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	for range moved {
		s.Metrics.observeTransition(scope.Kind, changeScopeChanged)
	}
	s.publish(changes)
	return moved, nil
}

// ReconcileOpenIncidentScopes applies ReconcileClusterScope to every cluster
// that has an open incident. A cluster whose owner cannot be verified does not
// stop the others; the joined error reports every failure.
func (s *Service) ReconcileOpenIncidentScopes(ctx context.Context) (int, error) {
	clusterIDs, err := lookoutdb.New(s.DB).ListOpenIncidentClusterIDs(ctx)
	if err != nil {
		return 0, fmt.Errorf("list clusters with open incidents: %w", err)
	}
	moved := 0
	var errs []error
	for _, clusterID := range clusterIDs {
		n, reconcileErr := s.ReconcileClusterScope(ctx, clusterID)
		moved += n
		if reconcileErr != nil {
			errs = append(errs, reconcileErr)
		}
	}
	return moved, errors.Join(errs...)
}
