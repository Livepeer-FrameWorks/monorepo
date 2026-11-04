package grpc

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"frameworks/api_billing/internal/database/purserdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

func (s *PurserServer) DiagnoseBilling(ctx context.Context, req *purserpb.DiagnoseBillingRequest) (*purserpb.DiagnoseBillingResponse, error) {
	if err := middleware.RequirePlatformOperatorJWT(ctx); err != nil {
		return nil, err
	}
	tenant, err := uuid.Parse(strings.TrimSpace(req.GetTenantId()))
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	data, err := purserdb.New(s.db).GetBillingDiagnosis(ctx, tenant)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "tenant subscription not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read billing diagnosis: %v", err)
	}
	out := &purserpb.DiagnoseBillingResponse{}
	if err = protojson.Unmarshal(data, out); err != nil {
		return nil, status.Errorf(codes.Internal, "decode billing diagnosis: %v", err)
	}
	// The existing recovery reader attributes callbacks through durable tenant metadata.
	pending, err := s.AdminListCheckoutRecovery(ctx, &purserpb.AdminListCheckoutRecoveryRequest{TenantId: tenant.String(), Limit: 20})
	if err != nil {
		return nil, err
	}
	out.PendingWebhooks = pending.GetItems()
	for _, item := range out.PendingWebhooks {
		item.LastError = ""
	}
	out.PendingWebhooksHasMore = pending.GetHasMore()
	return out, nil
}
