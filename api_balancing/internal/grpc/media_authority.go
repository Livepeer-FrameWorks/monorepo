package grpc

import (
	"context"
	"errors"
	"fmt"

	localauthority "frameworks/api_balancing/internal/mediaauthority"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// ApplyMediaAuthority durably installs one signed cell-bound authority. A
// successful duplicate is acknowledged; every rejection stays an RPC error so
// Commodore keeps that cell's delivery pending.
func (s *FoghornGRPCServer) ApplyMediaAuthority(ctx context.Context, req *foghornpb.ApplyMediaAuthorityRequest) (*foghornpb.ApplyMediaAuthorityResponse, error) {
	if req == nil || req.GetAuthority() == nil || req.GetAuthority().GetEnvelope() == nil {
		return nil, status.Error(codes.InvalidArgument, "signed media authority is required")
	}
	if s.mediaAuthorityStore == nil {
		return nil, status.Error(codes.Unavailable, "media authority store is not configured")
	}
	encoded, err := proto.MarshalOptions{Deterministic: true}.Marshal(req.GetAuthority())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "encode signed media authority: %v", err)
	}
	result, err := s.mediaAuthorityStore.Apply(ctx, encoded)
	if err != nil {
		return nil, mediaAuthorityStatus(err)
	}
	outcome := foghornpb.MediaAuthorityApplyOutcome_MEDIA_AUTHORITY_APPLY_OUTCOME_APPLIED
	if result.Status == localauthority.ApplyStatusDuplicate {
		outcome = foghornpb.MediaAuthorityApplyOutcome_MEDIA_AUTHORITY_APPLY_OUTCOME_DUPLICATE
	}
	return &foghornpb.ApplyMediaAuthorityResponse{
		Outcome:          outcome,
		AuthorityKind:    req.GetAuthority().GetEnvelope().GetKind(),
		AuthorityId:      result.ID,
		AuthorityVersion: result.Version,
		RefreshDue:       result.Refreshed,
	}, nil
}

func mediaAuthorityStatus(err error) error {
	switch {
	case errors.Is(err, sharedauthority.ErrWrongAudience),
		errors.Is(err, sharedauthority.ErrUnknownSigner),
		errors.Is(err, sharedauthority.ErrInvalidSignature):
		return status.Error(codes.PermissionDenied, err.Error())
	case errors.Is(err, localauthority.ErrRollback), errors.Is(err, localauthority.ErrVersionConflict):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, sharedauthority.ErrMalformed),
		errors.Is(err, sharedauthority.ErrUnknownSchema),
		errors.Is(err, sharedauthority.ErrPayloadDigest),
		errors.Is(err, sharedauthority.ErrExpired),
		errors.Is(err, sharedauthority.ErrNotYetValid),
		errors.Is(err, sharedauthority.ErrNonCanonical):
		return status.Error(codes.InvalidArgument, err.Error())
	default:
		return status.Error(codes.Unavailable, fmt.Sprintf("persist media authority: %v", err))
	}
}

var _ foghornpb.MediaAuthorityControlServiceServer = (*FoghornGRPCServer)(nil)
