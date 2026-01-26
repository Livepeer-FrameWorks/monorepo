package graph

import (
	"context"

	"frameworks/pkg/globalid"
	pb "frameworks/pkg/proto"
)

func (r *Resolver) resolveStreamByID(ctx context.Context, streamID string) (*pb.Stream, error) {
	if streamID == "" {
		return nil, nil
	}
	rawID, err := globalid.DecodeExpected(streamID, globalid.TypeStream)
	if err != nil {
		return nil, err
	}
	return r.DoGetStream(ctx, rawID)
}

func (r *Resolver) resolveStreamByIDPtr(ctx context.Context, streamID *string) (*pb.Stream, error) {
	if streamID == nil || *streamID == "" {
		return nil, nil
	}
	return r.resolveStreamByID(ctx, *streamID)
}
