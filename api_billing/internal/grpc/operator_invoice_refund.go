package grpc

import (
	"context"
	"database/sql"
	"errors"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"strings"
)

func (s *PurserServer) AdminRefundInvoice(ctx context.Context, req *purserpb.AdminRefundInvoiceRequest) (*purserpb.AdminRefundInvoiceResponse, error) {
	if err := requireAssignmentOperator(ctx); err != nil {
		return nil, err
	}
	for _, v := range []string{req.GetTenantId(), req.GetInvoiceId(), req.GetRequestId()} {
		if _, err := uuid.Parse(v); err != nil {
			return nil, status.Error(codes.InvalidArgument, "tenant_id, invoice_id and stable request_id must be UUIDs")
		}
	}
	reason := strings.TrimSpace(req.GetReason())
	if reason == "" {
		return nil, status.Error(codes.InvalidArgument, "an audit reason is required")
	}
	if req.GetConfirmed() && len(req.GetExpectedFingerprint()) != 64 {
		return nil, status.Error(codes.InvalidArgument, "preview the exact refund before confirmation")
	}
	if s.billing == nil {
		return nil, status.Error(codes.Unavailable, "billing service unavailable")
	}
	actor := middleware.GetUserID(ctx)
	if actor == "" {
		actor = "service"
	}
	out, err := s.billing.ReviewInvoiceRefund(ctx, req.TenantId, req.InvoiceId, req.RequestId, reason, actor, req.Confirmed, req.ExpectedFingerprint)
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "refund not queued: %v", err)
	}
	return &purserpb.AdminRefundInvoiceResponse{RequestId: out.RequestID, InvoiceId: out.InvoiceID, Provider: out.Provider, AmountMinor: out.AmountMinor, Currency: out.Currency, Status: out.Status, Fingerprint: out.Fingerprint, Detail: out.Detail, ProviderRefundId: out.ProviderRefundID}, nil
}

func (s *PurserServer) AdminGetInvoiceRefund(ctx context.Context, req *purserpb.AdminGetInvoiceRefundRequest) (*purserpb.AdminRefundInvoiceResponse, error) {
	if err := requireAssignmentOperator(ctx); err != nil {
		return nil, err
	}
	for _, v := range []string{req.GetTenantId(), req.GetRequestId()} {
		if _, err := uuid.Parse(v); err != nil {
			return nil, status.Error(codes.InvalidArgument, "tenant and request must be UUIDs")
		}
	}
	if s.billing == nil {
		return nil, status.Error(codes.Unavailable, "billing service unavailable")
	}
	out, err := s.billing.GetInvoiceRefund(ctx, req.TenantId, req.RequestId)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "refund receipt not found for tenant")
	}
	if err != nil {
		return nil, status.Error(codes.Unavailable, "refund receipt unavailable; reuse this same reference")
	}
	return &purserpb.AdminRefundInvoiceResponse{RequestId: out.RequestID, InvoiceId: out.InvoiceID, Provider: out.Provider, AmountMinor: out.AmountMinor, Currency: out.Currency, Status: out.Status, Fingerprint: out.Fingerprint, Detail: out.Detail, ProviderRefundId: out.ProviderRefundID}, nil
}
