package grpc

import (
	"context"

	"frameworks/api_tenants/internal/database/quartermasterdb"
)

// controlCellReassignmentStates are the reassignment states the gauge always
// reports, so a state that empties drops to zero instead of keeping its last
// value.
var controlCellReassignmentStates = []string{"switching", "failed"}

// recordControlCellReassignmentStates publishes how many tenant-private
// clusters have a switching or failed reassignment. A failed reassignment
// passed its deadline with edges still observed by another cell.
func (s *QuartermasterServer) recordControlCellReassignmentStates(ctx context.Context) {
	if s.metrics == nil || s.metrics.ControlCellReassignments == nil || s.db == nil {
		return
	}
	counts, err := quartermasterdb.New(s.db).CountClusterControlCellReassignmentsByState(ctx)
	if err != nil {
		s.logger.WithError(err).Warn("Count control-cell reassignments failed")
		return
	}
	for _, state := range controlCellReassignmentStates {
		s.metrics.ControlCellReassignments.WithLabelValues(state).Set(float64(counts[state]))
	}
}
