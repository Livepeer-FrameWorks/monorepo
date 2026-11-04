package grpc

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

func (s *CommodoreServer) DiagnoseStream(ctx context.Context, req *commodorepb.DiagnoseStreamRequest) (*commodorepb.DiagnoseStreamResponse, error) {
	if err := middleware.RequirePlatformOperatorJWT(ctx); err != nil {
		return nil, err
	}
	identifier := strings.TrimSpace(req.GetIdentifier())
	if identifier == "" || len(identifier) > 256 {
		return nil, status.Error(codes.InvalidArgument, "stream or playback ID is required")
	}
	streamID := ""
	if id, err := uuid.Parse(identifier); err == nil {
		streamID = id.String()
	}
	q := commodoredb.New(s.db)
	tenant, err := q.ResolveDiagnosticStreamTenant(ctx, commodoredb.ResolveDiagnosticStreamTenantParams{StreamID: streamID, PlaybackID: identifier})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "stream not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "resolve stream: %v", err)
	}
	data, err := q.GetStreamDiagnosis(ctx, commodoredb.GetStreamDiagnosisParams{TenantID: tenant, StreamID: streamID, PlaybackID: identifier})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "stream not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read stream diagnosis: %v", err)
	}
	out := &commodorepb.DiagnoseStreamResponse{}
	if err := protojson.Unmarshal(data, out); err != nil {
		return nil, status.Errorf(codes.Internal, "decode stream diagnosis: %v", err)
	}
	return out, nil
}
