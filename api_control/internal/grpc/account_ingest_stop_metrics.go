package grpc

import (
	"context"
	"fmt"
	"strings"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
)

// ingestStopUnattributedCell labels pending stops whose last error names none
// of their cells: never attempted yet, or failed before contacting one.
const ingestStopUnattributedCell = "unknown"

// ingestStopMetricWindow bounds the pending stops one observation reads, oldest
// first. A tenant owes one closure stop plus one per deactivated publisher, so
// this many pending at once is already an incident on its own.
const ingestStopMetricWindow = 1000

// ingestStopCellError is the form every per-cell delivery failure takes in a
// stop's last error; ingestStopFailingCells reads the cell back from it.
func ingestStopCellError(action, cell string, err error) error {
	return fmt.Errorf("%s cell %s: %w", action, cell, err)
}

// ingestStopFailingCells returns the cells of a pending stop that its last
// attempt failed on. A stop is owed to all its cells, but only these hold it.
func ingestStopFailingCells(stop commodoredb.PendingIngestStopRow) []string {
	var failing []string
	for _, cell := range stop.Cells {
		if strings.Contains(stop.LastError, " cell "+cell+": ") {
			failing = append(failing, cell)
		}
	}
	return failing
}

// ingestStopOldestPendingByCell is the age of the oldest pending stop per cell
// that holds it, measured from its first enqueue.
func ingestStopOldestPendingByCell(stops []commodoredb.PendingIngestStopRow, now time.Time) map[string]float64 {
	oldest := map[string]float64{}
	for _, stop := range stops {
		age := now.Sub(stop.PendingSince).Seconds()
		if age < 0 {
			age = 0
		}
		cells := ingestStopFailingCells(stop)
		if len(cells) == 0 {
			cells = []string{ingestStopUnattributedCell}
		}
		for _, cell := range cells {
			if prev, seen := oldest[cell]; !seen || age > prev {
				oldest[cell] = age
			}
		}
	}
	return oldest
}

// observeAccountIngestStops publishes the oldest pending session stop per
// cell. Cells that no longer hold one lose their series, so an alert on one
// cell resolves while another keeps firing.
func (s *CommodoreServer) observeAccountIngestStops(ctx context.Context) {
	if s.metrics == nil || s.metrics.AccountIngestStopOldestPendingSeconds == nil {
		return
	}
	stops, err := authorityMetricQuery(ctx, func(queryCtx context.Context) ([]commodoredb.PendingIngestStopRow, error) {
		return commodoredb.New(s.db).ListPendingIngestStops(queryCtx, "", ingestStopMetricWindow)
	})
	if err != nil {
		s.logger.WithError(err).Warn("Failed to observe pending session stops per cell")
		return
	}
	oldest := ingestStopOldestPendingByCell(stops, time.Now())
	for cell := range s.ingestStopObservedCells {
		if _, still := oldest[cell]; !still {
			s.metrics.AccountIngestStopOldestPendingSeconds.DeleteLabelValues(cell)
		}
	}
	observed := make(map[string]struct{}, len(oldest))
	for cell, age := range oldest {
		s.metrics.AccountIngestStopOldestPendingSeconds.WithLabelValues(cell).Set(age)
		observed[cell] = struct{}{}
	}
	s.ingestStopObservedCells = observed
}
