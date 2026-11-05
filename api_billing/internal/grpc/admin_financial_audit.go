package grpc

import (
	"context"
	"strings"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	billingstripe "frameworks/api_billing/internal/stripe"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// AdminFinancialAuditPage retains source rows for an operator's evidence bundle;
// mutable payment statuses are observed at captured_at, not reconstructed as of
// the requested occurrence cutoff.
func (s *PurserServer) AdminFinancialAuditPage(ctx context.Context, req *purserpb.AdminFinancialAuditPageRequest) (*purserpb.AdminFinancialAuditPageResponse, error) {
	if err := middleware.RequirePlatformOperatorJWT(ctx); err != nil {
		return nil, err
	}
	if req.GetFrom() == nil || req.GetThrough() == nil || req.GetFrom().CheckValid() != nil || req.GetThrough().CheckValid() != nil || !req.GetThrough().AsTime().After(req.GetFrom().AsTime()) || req.GetThrough().AsTime().After(time.Now()) {
		return nil, status.Error(codes.InvalidArgument, "a valid occurrence interval ending no later than now is required")
	}
	if strings.HasPrefix(req.GetSource(), "stripe_") {
		reader, ok := s.stripeClient.(interface {
			FinancialEvidencePage(context.Context, string, string, time.Time, time.Time) (*billingstripe.FinancialAuditPage, error)
		})
		if !ok {
			return nil, status.Error(codes.FailedPrecondition, "Stripe financial evidence reader is unavailable")
		}
		page, err := reader.FinancialEvidencePage(ctx, req.GetSource(), req.GetAfterId(), req.GetFrom().AsTime(), req.GetThrough().AsTime())
		if err != nil {
			return nil, status.Errorf(codes.Unavailable, "read Stripe financial evidence: %v", err)
		}
		s.logFinancialEvidenceRead(ctx, req, len(page.Records))
		return &purserpb.AdminFinancialAuditPageResponse{RecordsJson: page.Records, NextCursor: page.NextCursor, CapturedAt: timestamppb.Now()}, nil
	}
	if _, err := uuid.Parse(req.GetTenantId()); err != nil {
		return nil, status.Error(codes.InvalidArgument, "valid tenant_id required")
	}
	switch req.GetSource() {
	case "invoices", "payments", "simplified_invoices", "crypto_invoices", "credit_notes", "provider_settlements", "pending_topups", "balance_transactions":
	default:
		return nil, status.Error(codes.InvalidArgument, "unsupported financial source")
	}
	if req.GetAfterId() != "" {
		if _, err := uuid.Parse(req.GetAfterId()); err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid audit cursor")
		}
	}
	rows, err := purserdb.New(s.db).FinancialAuditPage(ctx, purserdb.FinancialAuditPageParams{TenantID: req.GetTenantId(), Source: req.GetSource(), FromAt: req.GetFrom().AsTime(), ThroughAt: req.GetThrough().AsTime(), AfterID: req.GetAfterId()})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read financial evidence: %v", err)
	}
	resp := &purserpb.AdminFinancialAuditPageResponse{CapturedAt: timestamppb.Now()}
	bytes := 0
	for index, row := range rows {
		if index == 100 || bytes+len(row.Payload) > 2*1024*1024 {
			if index == 0 {
				return nil, status.Error(codes.ResourceExhausted, "one financial record exceeds the evidence-page limit")
			}
			resp.NextCursor = rows[index-1].ID
			break
		}
		resp.RecordsJson = append(resp.RecordsJson, []byte(row.Payload))
		bytes += len(row.Payload)
	}
	s.logFinancialEvidenceRead(ctx, req, len(resp.RecordsJson))
	return resp, nil
}

func (s *PurserServer) logFinancialEvidenceRead(ctx context.Context, req *purserpb.AdminFinancialAuditPageRequest, count int) {
	if s.logger == nil {
		return
	}
	s.logger.WithFields(logging.Fields{"actor_user_id": ctxkeys.GetUserID(ctx), "tenant_id": req.GetTenantId(), "source": req.GetSource(), "from": req.GetFrom().AsTime(), "through": req.GetThrough().AsTime(), "records": count}).Info("Operator financial evidence read")
}
