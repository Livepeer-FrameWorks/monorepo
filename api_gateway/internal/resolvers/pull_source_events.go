package resolvers

import (
	"context"
	"fmt"

	"frameworks/api_gateway/internal/middleware"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

// DoStreamRecentPullSourceEvents is the field resolver for
// Stream.recentPullSourceEvents. Returns nil for push streams (caller can
// distinguish via Stream.ingestMode); pull streams get the most recent N
// events from commodore.pull_source_events.
func (r *Resolver) DoStreamRecentPullSourceEvents(ctx context.Context, stream *pb.Stream, limit *int) ([]*pb.PullSourceEvent, error) {
	if stream == nil {
		return nil, nil
	}
	if stream.GetIngestMode() != "pull" {
		return nil, nil
	}
	if err := middleware.RequirePermission(ctx, "streams:read"); err != nil {
		return nil, err
	}
	n := int32(50)
	if limit != nil && *limit > 0 {
		n = int32(*limit)
	}
	resp, err := r.Clients.Commodore.ListPullSourceEvents(ctx, &pb.ListPullSourceEventsRequest{
		StreamId: stream.GetStreamId(),
		Limit:    n,
	})
	if err != nil {
		r.Logger.WithError(err).Error("ListPullSourceEvents failed")
		return nil, fmt.Errorf("list pull source events: %w", err)
	}
	return resp.GetEvents(), nil
}
