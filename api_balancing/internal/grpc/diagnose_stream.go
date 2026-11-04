package grpc

import (
	"context"
	"strings"

	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

func (s *FoghornGRPCServer) DiagnoseStream(ctx context.Context, req *foghornpb.DiagnoseStreamRequest) (*foghornpb.DiagnoseStreamResponse, error) {
	if err := middleware.RequirePlatformOperatorJWT(ctx); err != nil {
		return nil, err
	}
	tenant, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	stream, err := uuid.Parse(req.GetStreamId())
	if err != nil || strings.TrimSpace(req.GetInternalName()) == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_id and internal_name are required")
	}
	data, err := foghorndb.New(s.db).GetStreamRuntimeDiagnosis(ctx, foghorndb.GetStreamRuntimeDiagnosisParams{TenantID: tenant.String(), StreamID: stream.String(), InternalName: req.GetInternalName()})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read stream diagnosis: %v", err)
	}
	out := &foghornpb.DiagnoseStreamResponse{}
	if err := protojson.Unmarshal(data, out); err != nil {
		return nil, status.Errorf(codes.Internal, "decode stream diagnosis: %v", err)
	}
	return out, nil
}
