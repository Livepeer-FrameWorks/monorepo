package resolvers

import (
	"context"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/globalid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (r *Resolver) DoDisconnectIngest(ctx context.Context, id string) (model.DisconnectIngestResult, error) {
	if err := middleware.RequirePermission(ctx, "streams:write"); err != nil {
		return &model.AuthError{Message: err.Error(), Code: strPtr("FORBIDDEN")}, nil //nolint:nilerr // Authorization failure is encoded in the GraphQL result union.
	}
	if middleware.IsDemoMode(ctx) {
		return &model.IngestDisconnectSuccess{StreamID: globalid.Encode(globalid.TypeStream, id), StopRequested: true}, nil
	}
	response, err := r.Clients.Commodore.DisconnectIngest(ctx, id)
	if err != nil {
		switch status.Code(err) {
		case codes.InvalidArgument, codes.FailedPrecondition:
			return &model.ValidationError{Message: status.Convert(err).Message(), Code: strPtr("INVALID_ARGUMENT")}, nil
		case codes.NotFound:
			return &model.NotFoundError{Message: "Stream not found", Code: strPtr("NOT_FOUND"), ResourceType: "Stream", ResourceID: globalid.Encode(globalid.TypeStream, id)}, nil
		case codes.PermissionDenied, codes.Unauthenticated:
			return &model.AuthError{Message: status.Convert(err).Message(), Code: strPtr("FORBIDDEN")}, nil
		default:
			return nil, err
		}
	}
	if response == nil {
		return nil, status.Error(codes.Unavailable, "ingest disconnect response unavailable")
	}
	return &model.IngestDisconnectSuccess{StreamID: globalid.Encode(globalid.TypeStream, response.GetStreamId()), StopRequested: response.GetStopRequested()}, nil
}
