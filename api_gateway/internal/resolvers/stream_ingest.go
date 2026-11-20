package resolvers

import (
	"context"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/demo"
	"frameworks/api_gateway/internal/loaders"
	"frameworks/api_gateway/internal/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (r *Resolver) DoStreamIngest(ctx context.Context, stream *commodorepb.Stream) (*model.StreamIngest, error) {
	if err := middleware.RequirePermission(ctx, "streams:read"); err != nil {
		return nil, err
	}
	if stream == nil {
		return nil, nil
	}
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateStreamIngest(stream), nil
	}
	var loader *loaders.StreamIngestLoader
	if request := loaders.FromContext(ctx); request != nil {
		loader = request.StreamIngest
	}
	if loader == nil {
		loader = loaders.NewStreamIngestLoader(r.Clients.Commodore)
	}
	view, err := loader.Load(ctx, ctxkeys.GetTenantID(ctx), stream.GetStreamId())
	if err != nil {
		return nil, err
	}
	return StreamIngestFromProto(view), nil
}

func StreamIngestFromProto(view *pb.StreamIngest) *model.StreamIngest {
	if view == nil {
		return nil
	}
	state := model.StreamIngestStateIdle
	switch view.GetState() {
	case pb.StreamIngestState_STREAM_INGEST_STATE_CONNECTED:
		state = model.StreamIngestStateConnected
	case pb.StreamIngestState_STREAM_INGEST_STATE_LIVE:
		state = model.StreamIngestStateLive
	}
	result := &model.StreamIngest{State: state, IsLive: state == model.StreamIngestStateLive, ConnectedAt: ingestTime(view.GetConnectedAt()), LiveSince: ingestTime(view.GetLiveSince()), EndedAt: ingestTime(view.GetEndedAt()), ResumableUntil: ingestTime(view.GetResumableUntil())}
	if id := view.GetSessionId(); id != "" {
		result.SessionID = &id
	}
	return result
}
func ingestTime(value *timestamppb.Timestamp) *time.Time {
	if value == nil || !value.IsValid() {
		return nil
	}
	t := value.AsTime()
	return &t
}
