package grpc

import (
	"context"
	"frameworks/api_billing/internal/handlers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"strings"
)

func automaticTopupProto(v handlers.AutomaticTopupState) *purserpb.AutomaticTopupSettings {
	return &purserpb.AutomaticTopupSettings{Enabled: v.Enabled, ThresholdEurCents: v.ThresholdEUR, NetAmountMinor: v.NetAmountMinor, MonthlyGrossLimitMinor: v.MonthlyGrossLimitMinor, Currency: v.Currency, PausedReason: v.PausedReason, MonthlyReservedOrSpentMinor: v.MonthlySpentMinor, Available: v.Available, UnavailableReason: v.UnavailableReason, ConsentVersion: v.ConsentVersion}
}
func (s *PurserServer) GetAutomaticTopup(ctx context.Context, req *purserpb.GetAutomaticTopupRequest) (*purserpb.AutomaticTopupSettings, error) {
	if _, err := uuid.Parse(req.GetTenantId()); err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	if s.billing == nil {
		return nil, status.Error(codes.Unavailable, "billing service unavailable")
	}
	out, err := s.billing.GetAutomaticTopup(ctx, req.TenantId)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "automatic top-up settings unavailable")
	}
	return automaticTopupProto(out), nil
}
func (s *PurserServer) SetAutomaticTopup(ctx context.Context, req *purserpb.SetAutomaticTopupRequest) (*purserpb.AutomaticTopupSettings, error) {
	if _, err := uuid.Parse(req.GetTenantId()); err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	if s.billing == nil {
		return nil, status.Error(codes.Unavailable, "billing service unavailable")
	}
	if req.GetEnabled() {
		if err := s.requireCompleteBillingDetails(ctx, req.TenantId); err != nil {
			return nil, err
		}
	}
	out, err := s.billing.SetAutomaticTopup(ctx, req.TenantId, handlers.AutomaticTopupPolicy{Enabled: req.Enabled, ThresholdEUR: req.ThresholdEurCents, NetAmountMinor: req.NetAmountMinor, MonthlyGrossLimitMinor: req.MonthlyGrossLimitMinor, Currency: strings.ToUpper(strings.TrimSpace(req.Currency)), ConsentVersion: req.ConsentVersion, Actor: middleware.GetUserID(ctx)})
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "automatic top-up settings not saved: %v", err)
	}
	return automaticTopupProto(out), nil
}
func (s *PurserServer) AdminDisableAutomaticTopup(ctx context.Context, req *purserpb.AdminDisableAutomaticTopupRequest) (*purserpb.AutomaticTopupSettings, error) {
	if err := requireAssignmentOperator(ctx); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.GetReason()) == "" {
		return nil, status.Error(codes.InvalidArgument, "an audit reason is required")
	}
	if _, err := uuid.Parse(req.GetTenantId()); err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	if s.billing == nil {
		return nil, status.Error(codes.Unavailable, "billing service unavailable")
	}
	if err := s.billing.DisableAutomaticTopup(ctx, req.TenantId, "operator:"+middleware.GetUserID(ctx)+": "+strings.TrimSpace(req.Reason)); err != nil {
		return nil, status.Error(codes.Internal, "automatic top-ups could not be disabled")
	}
	return s.GetAutomaticTopup(ctx, &purserpb.GetAutomaticTopupRequest{TenantId: req.TenantId})
}
