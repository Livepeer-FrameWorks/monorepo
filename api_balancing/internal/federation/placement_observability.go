package federation

import (
	"context"
	"errors"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var placementReplicaDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "foghorn_federation_placement_replica_duration_seconds",
	Help:    "Placement observation and preparation duration per remote replica attempt",
	Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2, 5, 10},
}, []string{"phase", "outcome"})

func placementBudgetRemainingMS(ctx context.Context) int64 {
	if deadline, ok := ctx.Deadline(); ok {
		return max(0, time.Until(deadline).Milliseconds())
	}
	return -1
}

func recordPlacementReplica(logger logging.Logger, ctx context.Context, phase string, started time.Time, fields logging.Fields, err error) {
	elapsed := time.Since(started)
	code := status.Code(err)
	if errors.Is(err, context.DeadlineExceeded) {
		code = codes.DeadlineExceeded
	} else if errors.Is(err, context.Canceled) {
		code = codes.Canceled
	}
	placementReplicaDuration.WithLabelValues(phase, code.String()).Observe(elapsed.Seconds())
	if logger == nil {
		return
	}
	fields["phase"] = phase
	fields["duration_ms"] = elapsed.Milliseconds()
	fields["outcome"] = code.String()
	fields["attempt_budget_remaining_ms"] = placementBudgetRemainingMS(ctx)
	entry := logger.WithFields(fields)
	if err != nil {
		entry = entry.WithError(err)
	}
	if err != nil || elapsed >= 250*time.Millisecond {
		entry.Info("Placement replica phase completed")
	} else {
		entry.Debug("Placement replica phase completed")
	}
}
