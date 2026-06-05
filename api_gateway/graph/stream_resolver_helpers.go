package graph

import (
	"context"

	"frameworks/api_gateway/internal/loaders"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/globalid"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

func (r *Resolver) resolveStreamByID(ctx context.Context, streamID string) (*commodorepb.Stream, error) {
	if streamID == "" {
		return nil, nil
	}
	rawID, err := globalid.DecodeExpected(streamID, globalid.TypeStream)
	if err != nil {
		return nil, err
	}
	if l := loaders.FromContext(ctx); l != nil && l.Stream != nil {
		tenantID := ctxkeys.GetTenantID(ctx)
		return l.Stream.Load(ctx, tenantID, rawID)
	}
	return r.DoGetStream(ctx, rawID)
}

func (r *Resolver) resolveStreamByIDPtr(ctx context.Context, streamID *string) (*commodorepb.Stream, error) {
	if streamID == nil || *streamID == "" {
		return nil, nil
	}
	return r.resolveStreamByID(ctx, *streamID)
}

func (r *Resolver) resolveNullableStreamByRawID(ctx context.Context, rawID string) (*commodorepb.Stream, error) {
	if rawID == "" {
		return nil, nil
	}
	stream, err := r.resolveStreamByID(ctx, globalid.Encode(globalid.TypeStream, rawID))
	if err != nil {
		return nil, nil //nolint:nilerr // Nullable stream fields omit unresolved references.
	}
	return stream, nil
}
