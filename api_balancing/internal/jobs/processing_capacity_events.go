package jobs

import (
	"context"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/database/foghorndb"
	"frameworks/api_balancing/internal/state"
)

func (d *ProcessingDispatcher) capacityChanged(nodeID string) {
	select {
	case d.capacityCh <- nodeID:
	default:
	}
}

func (d *ProcessingDispatcher) wakeCapacity(nodeID string) {
	node := state.DefaultManager().GetNodeState(nodeID)
	if node == nil || node.ClusterID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := foghorndb.New(d.db).WakeProcessingClusterJobs(ctx, node.ClusterID); err != nil {
		d.logger.WithError(err).WithField("node_id", nodeID).Warn("Failed to wake waiting processing jobs")
		return
	}
	NotifyProcessingJobQueued()
}

func (d *ProcessingDispatcher) observeWaitingJobs(ctx context.Context) {
	if !control.ProcessingWaitingMetricsEnabled() {
		return
	}
	rows, err := foghorndb.New(d.db).CountWaitingProcessingJobs(ctx)
	if err != nil {
		d.logger.WithError(err).Warn("Failed to observe processing queue reasons")
		return
	}
	counts := make(map[string]int64, len(rows))
	for _, row := range rows {
		counts[row.WaitReason.String] = row.Jobs
	}
	control.ObserveProcessingWaiting(counts)
}
