package grpc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"frameworks/api_tenants/internal/database/quartermasterdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	defaultControlCellReassignmentTimeout = 30 * time.Minute
	minControlCellReassignmentTimeout     = time.Minute
	maxControlCellReassignmentTimeout     = 24 * time.Hour
	// controlCellObservationWindow bounds how long a node's newest observation
	// counts as live. Foghorn republishes every known node each minute.
	controlCellObservationWindow = 3 * time.Minute
	maxReportedPendingNodes      = 20
)

var (
	errControlCellReassignmentConflict = errors.New("cluster control cell changed concurrently")
	errControlCellHasNoFoghorn         = errors.New("target control cell has no running Foghorn")
)

// ReassignClusterControlCell moves a tenant-private cluster to another
// platform control cell. In one transaction it switches control_cell_id,
// assigns the target cell's Foghorns and removes the previous cell's rows; the
// previous cell then releases the cluster's edges at its next served-cluster
// refresh and they reconnect through the cluster's Foghorn DNS name.
func (s *QuartermasterServer) ReassignClusterControlCell(ctx context.Context, req *quartermasterpb.ReassignClusterControlCellRequest) (*quartermasterpb.ClusterControlCellReassignment, error) {
	if err := requireServiceOrPlatformOperator(ctx, "ReassignClusterControlCell"); err != nil {
		return nil, err
	}
	clusterID := strings.TrimSpace(req.GetClusterId())
	targetCellID := strings.TrimSpace(req.GetTargetControlCellId())
	if clusterID == "" || targetCellID == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_id and target_control_cell_id required")
	}
	timeout, err := controlCellReassignmentTimeout(req.GetTimeoutSeconds())
	if err != nil {
		return nil, err
	}

	queries := quartermasterdb.New(s.db)
	current, err := queries.GetClusterControlCellReassignment(ctx, clusterID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "active tenant-private cluster not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read cluster control cell: %v", err)
	}
	if current.State == "switching" {
		return nil, status.Errorf(codes.FailedPrecondition, "cluster %q is already moving to control cell %q", clusterID, current.ControlCellID)
	}
	if current.ControlCellID == targetCellID {
		return nil, status.Errorf(codes.FailedPrecondition, "cluster %q is already controlled by cell %q", clusterID, targetCellID)
	}
	hasFoghorn, err := queries.ControlCellHasRunningFoghorn(ctx, targetCellID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "check target control cell: %v", err)
	}
	if !hasFoghorn {
		return nil, status.Errorf(codes.FailedPrecondition, "control cell %q is not an active platform cell with a running Foghorn", targetCellID)
	}

	var started quartermasterdb.ClusterControlCellReassignment
	err = database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		txQueries := quartermasterdb.New(tx)
		row, startErr := txQueries.StartClusterControlCellReassignment(ctx, quartermasterdb.StartClusterControlCellReassignmentParams{
			ClusterID: clusterID, TargetCellID: targetCellID, DeadlineAt: time.Now().Add(timeout),
		})
		if errors.Is(startErr, sql.ErrNoRows) {
			return errControlCellReassignmentConflict
		}
		if startErr != nil {
			return fmt.Errorf("start reassignment: %w", startErr)
		}
		assigned, assignErr := txQueries.AssignControlCellFoghornsToPrivateCluster(ctx, clusterID)
		if assignErr != nil {
			return fmt.Errorf("assign target cell Foghorns: %w", assignErr)
		}
		if assigned == 0 {
			return errControlCellHasNoFoghorn
		}
		if _, reconcileErr := txQueries.ReconcilePrivateClusterControlCellFoghorns(ctx, clusterID); reconcileErr != nil {
			return fmt.Errorf("remove previous cell Foghorns: %w", reconcileErr)
		}
		if emitErr := s.emitClusterEventTx(ctx, tx, eventClusterUpdated, row.OwnerTenantID, ctxkeys.GetUserID(ctx), clusterID, "cluster", clusterID, "", "", ""); emitErr != nil {
			return fmt.Errorf("enqueue cluster_updated: %w", emitErr)
		}
		started = row
		return nil
	})
	switch {
	case errors.Is(err, errControlCellReassignmentConflict):
		return nil, status.Errorf(codes.FailedPrecondition, "cluster %q changed concurrently; read its reassignment and retry", clusterID)
	case errors.Is(err, errControlCellHasNoFoghorn):
		return nil, status.Errorf(codes.FailedPrecondition, "control cell %q has no running Foghorn", targetCellID)
	case err != nil:
		return nil, status.Errorf(codes.Internal, "reassign cluster control cell: %v", err)
	}

	s.fireNavigatorSyncForPoolClusters("foghorn", []string{clusterID})
	s.logger.WithFields(logging.Fields{
		"cluster_id":       clusterID,
		"previous_cell_id": started.PreviousControlCellID,
		"control_cell_id":  started.ControlCellID,
		"deadline_at":      started.DeadlineAt.Time,
	}).Info("Started control-cell reassignment")
	return controlCellReassignmentProto(started, nil), nil
}

// GetClusterControlCellReassignment returns a cluster's control cell, its
// open or failed reassignment, and the edges still observed by another cell.
func (s *QuartermasterServer) GetClusterControlCellReassignment(ctx context.Context, req *quartermasterpb.GetClusterControlCellReassignmentRequest) (*quartermasterpb.ClusterControlCellReassignment, error) {
	if err := requireServiceOrPlatformOperator(ctx, "GetClusterControlCellReassignment"); err != nil {
		return nil, err
	}
	clusterID := strings.TrimSpace(req.GetClusterId())
	if clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_id required")
	}
	queries := quartermasterdb.New(s.db)
	row, err := queries.GetClusterControlCellReassignment(ctx, clusterID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "active tenant-private cluster not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read cluster control cell: %v", err)
	}
	var pending []string
	if row.State != "" {
		pending, err = queries.ListControlCellPendingNodes(ctx, clusterID, row.ControlCellID, controlCellObservationWindow)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "list pending edge nodes: %v", err)
		}
	}
	return controlCellReassignmentProto(row, pending), nil
}

// advanceControlCellReassignmentsOnce completes each switching reassignment
// whose cluster has no live edge observed by another cell, and fails the ones
// past their deadline. State lives in the cluster row, so any Quartermaster
// replica resumes after a restart.
func (s *QuartermasterServer) advanceControlCellReassignmentsOnce(ctx context.Context) {
	defer s.recordControlCellReassignmentStates(ctx)
	switching, err := quartermasterdb.New(s.db).ListSwitchingClusterControlCellReassignments(ctx)
	if err != nil {
		s.logger.WithError(err).Warn("List switching control-cell reassignments failed")
		return
	}
	now := time.Now()
	for _, reassignment := range switching {
		if advanceErr := s.advanceControlCellReassignment(ctx, reassignment, now); advanceErr != nil {
			s.logger.WithError(advanceErr).WithField("cluster_id", reassignment.ClusterID).Warn("Advance control-cell reassignment failed")
		}
	}
}

func (s *QuartermasterServer) advanceControlCellReassignment(ctx context.Context, reassignment quartermasterdb.ClusterControlCellReassignment, now time.Time) error {
	if !reassignment.StartedAt.Valid {
		return nil
	}
	queries := quartermasterdb.New(s.db)
	pending, err := queries.ListControlCellPendingNodes(ctx, reassignment.ClusterID, reassignment.ControlCellID, controlCellObservationWindow)
	if err != nil {
		return fmt.Errorf("list pending edge nodes: %w", err)
	}
	fields := logging.Fields{
		"cluster_id":       reassignment.ClusterID,
		"previous_cell_id": reassignment.PreviousControlCellID,
		"control_cell_id":  reassignment.ControlCellID,
	}
	if len(pending) > 0 {
		if !reassignment.DeadlineAt.Valid || now.Before(reassignment.DeadlineAt.Time) {
			return nil
		}
		failed, failErr := queries.FailClusterControlCellReassignment(ctx, reassignment.ClusterID, reassignment.StartedAt.Time, pendingControlCellNodesReason(pending))
		if failErr != nil {
			return fmt.Errorf("fail reassignment: %w", failErr)
		}
		if failed {
			fields["pending_nodes"] = len(pending)
			s.logger.WithFields(fields).Warn("Control-cell reassignment failed at its deadline")
		}
		return nil
	}

	completed := false
	err = database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		done, completeErr := quartermasterdb.New(tx).CompleteClusterControlCellReassignment(ctx, reassignment.ClusterID, reassignment.StartedAt.Time)
		completed = done
		if completeErr != nil || !done {
			return completeErr
		}
		return s.emitClusterEventTx(ctx, tx, eventClusterUpdated, reassignment.OwnerTenantID, "", reassignment.ClusterID, "cluster", reassignment.ClusterID, "", "", "")
	})
	if err != nil {
		return fmt.Errorf("complete reassignment: %w", err)
	}
	if completed {
		s.logger.WithFields(fields).Info("Completed control-cell reassignment")
	}
	return nil
}

func controlCellReassignmentTimeout(seconds int64) (time.Duration, error) {
	if seconds == 0 {
		return defaultControlCellReassignmentTimeout, nil
	}
	if seconds < int64(minControlCellReassignmentTimeout/time.Second) || seconds > int64(maxControlCellReassignmentTimeout/time.Second) {
		return 0, status.Errorf(codes.InvalidArgument, "timeout_seconds must be between %d and %d",
			int64(minControlCellReassignmentTimeout/time.Second), int64(maxControlCellReassignmentTimeout/time.Second))
	}
	return time.Duration(seconds) * time.Second, nil
}

func pendingControlCellNodesReason(pending []string) string {
	shown := pending
	if len(shown) > maxReportedPendingNodes {
		shown = shown[:maxReportedPendingNodes]
	}
	return fmt.Sprintf("%d edge node(s) still observed by another control cell at the deadline: %s", len(pending), strings.Join(shown, ", "))
}

func controlCellReassignmentProto(row quartermasterdb.ClusterControlCellReassignment, pending []string) *quartermasterpb.ClusterControlCellReassignment {
	out := &quartermasterpb.ClusterControlCellReassignment{
		ClusterId:             row.ClusterID,
		ControlCellId:         row.ControlCellID,
		PreviousControlCellId: row.PreviousControlCellID,
		State:                 row.State,
		Error:                 row.Error,
		PendingNodeIds:        pending,
	}
	if row.StartedAt.Valid {
		out.StartedAt = timestamppb.New(row.StartedAt.Time)
	}
	if row.DeadlineAt.Valid {
		out.DeadlineAt = timestamppb.New(row.DeadlineAt.Time)
	}
	return out
}
