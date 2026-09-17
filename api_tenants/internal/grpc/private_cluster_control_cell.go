package grpc

import (
	"context"
	"time"

	"frameworks/api_tenants/internal/database/quartermasterdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

const (
	privateClusterControlCellReconcileInterval = 30 * time.Second
	privateClusterControlCellReconcileTimeout  = 20 * time.Second
)

// runPrivateClusterControlCellReconciler keeps every tenant-private cluster
// assigned to each running Foghorn of its control cell, so the cluster keeps a
// serving, ConfigSeed-delivering Foghorn while replicas stop, join, or move
// between cells, and advances control-cell reassignments. Every Quartermaster
// replica may run it: each statement is idempotent or fenced.
func (s *QuartermasterServer) runPrivateClusterControlCellReconciler(ctx context.Context) {
	ticker := time.NewTicker(privateClusterControlCellReconcileInterval)
	defer ticker.Stop()
	s.reconcilePrivateClusterControlCellsOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reconcilePrivateClusterControlCellsOnce(ctx)
		}
	}
}

func (s *QuartermasterServer) reconcilePrivateClusterControlCellsOnce(ctx context.Context) {
	if s.db == nil {
		return
	}
	runCtx, cancel := context.WithTimeout(ctx, privateClusterControlCellReconcileTimeout)
	defer cancel()
	changed, err := quartermasterdb.New(s.db).ReconcilePrivateClusterControlCellFoghorns(runCtx, "")
	if err != nil {
		s.logger.WithError(err).Warn("Private cluster control-cell reconcile failed")
	} else if len(changed) > 0 {
		s.logger.WithFields(logging.Fields{"clusters": changed}).Info("Reconciled private cluster Foghorn assignments")
		s.fireNavigatorSyncForPoolClusters("foghorn", changed)
	}
	s.advanceControlCellReassignmentsOnce(runCtx)
}
