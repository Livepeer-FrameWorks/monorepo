package grpc

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func (s *CommodoreServer) AdminRecordDiagnosticRead(ctx context.Context, req *commodorepb.AdminRecordDiagnosticReadRequest) (*emptypb.Empty, error) {
	if err := middleware.RequirePlatformOperatorJWT(ctx); err != nil {
		return nil, err
	}
	switch req.GetKind() {
	case "billing", "webhooks", "stream", "node", "dvr":
	default:
		return nil, status.Error(codes.InvalidArgument, "unknown diagnostic kind")
	}
	target, service := strings.TrimSpace(req.GetTargetId()), strings.TrimSpace(req.GetService())
	if target == "" || len(target) > 256 || len(service) > 128 || strings.ContainsFunc(target+service, unicode.IsControl) {
		return nil, status.Error(codes.InvalidArgument, "invalid diagnostic target or service")
	}
	tenant, err := uuid.Parse(ctxkeys.GetTenantID(ctx))
	if err != nil {
		return nil, status.Error(codes.PermissionDenied, "operator account tenant is required")
	}
	if req.GetKind() == "billing" || req.GetKind() == "webhooks" {
		if _, targetErr := uuid.Parse(target); targetErr != nil {
			return nil, status.Error(codes.InvalidArgument, "target must be a tenant UUID")
		}
	}
	err = commodoredb.New(s.db).RecordOperatorAccountAudit(ctx, commodoredb.RecordOperatorAccountAuditParams{
		TenantID: tenant.String(), ActorUserID: ctxkeys.GetUserID(ctx), TargetID: target,
		Action: "diagnose." + req.GetKind(), Reason: fmt.Sprintf("CLI diagnostic access requested; service=%q", service),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "record diagnostic access: %v", err)
	}
	return &emptypb.Empty{}, nil
}
