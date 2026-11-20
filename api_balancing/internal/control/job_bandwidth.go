package control

import (
	"context"
	"time"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/capacity"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type jobBandwidthLeg struct{ id, node string }

func reserveJobBandwidth(ctx context.Context, id, stream, source, destination string) ([]jobBandwidthLeg, error) {
	if source != "" && source == destination {
		return nil, nil
	}
	manager := state.DefaultManager()
	stream = mist.ExtractInternalName(stream)
	estimate := max(capacity.DefaultIngestEstimate, manager.EstimateStreamBandwidth(source, stream))
	legs := []jobBandwidthLeg{{id: id + ":ingress", node: destination}}
	if source != "" {
		legs = append(legs, jobBandwidthLeg{id: id + ":egress", node: source})
	}
	for i, leg := range legs {
		direction := state.BandwidthEgress
		if i == 0 {
			direction = state.BandwidthIngress
		}
		allowed, err := manager.ReserveNodeBandwidth(ctx, leg.id, leg.node, stream, direction, estimate)
		if err != nil || !allowed {
			releaseCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			for _, reserved := range legs[:i] {
				if releaseErr := manager.ReleasePendingNodeBandwidth(releaseCtx, reserved.id, reserved.node); releaseErr != nil {
					controlLogger().WithError(releaseErr).Warn("Could not release refused job bandwidth")
				}
			}
			cancel()
			if err != nil {
				return nil, err
			}
			return nil, status.Error(codes.ResourceExhausted, "node bandwidth capacity exhausted")
		}
	}
	return legs, nil
}

func activateJobBandwidth(legs []jobBandwidthLeg) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, leg := range legs {
		if _, err := state.DefaultManager().ActivateNodeBandwidth(ctx, leg.id, leg.node); err != nil {
			controlLogger().WithError(err).Warn("Could not activate dispatched job bandwidth")
		}
	}
}

func releasePendingJobBandwidth(legs []jobBandwidthLeg) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, leg := range legs {
		if err := state.DefaultManager().ReleasePendingNodeBandwidth(ctx, leg.id, leg.node); err != nil {
			controlLogger().WithError(err).Warn("Could not release unsent job bandwidth")
		}
	}
}
