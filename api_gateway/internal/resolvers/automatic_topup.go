package resolvers

import (
	"context"
	"fmt"
	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

func automaticTopupModel(out *purserpb.AutomaticTopupSettings) (*purserpb.AutomaticTopupSettings, error) {
	if out == nil {
		return nil, fmt.Errorf("missing automatic top-up receipt")
	}
	return out, nil
}
func (r *Resolver) DoAutomaticTopup(ctx context.Context) (*purserpb.AutomaticTopupSettings, error) {
	if err := requireBillingScope(ctx, "billing:read"); err != nil {
		return nil, err
	}
	tenant := ctxkeys.GetTenantID(ctx)
	if tenant == "" {
		return nil, middleware.ErrTenantContextRequired
	}
	if middleware.IsDemoMode(ctx) {
		return &purserpb.AutomaticTopupSettings{Currency: "USD", UnavailableReason: "Automatic payments are unavailable in demo mode."}, nil
	}
	out, err := r.Clients.Purser.GetAutomaticTopup(ctx, &purserpb.GetAutomaticTopupRequest{TenantId: tenant})
	if err != nil {
		return nil, err
	}
	return automaticTopupModel(out)
}
func (r *Resolver) DoSetAutomaticTopup(ctx context.Context, input model.SetAutomaticTopupInput) (*purserpb.AutomaticTopupSettings, error) {
	if err := requireBillingScope(ctx, "billing:write"); err != nil {
		return nil, err
	}
	tenant := ctxkeys.GetTenantID(ctx)
	if tenant == "" {
		return nil, middleware.ErrTenantContextRequired
	}
	if middleware.IsDemoMode(ctx) {
		return nil, middleware.InvalidInput("automatic payments are unavailable in demo mode")
	}
	out, err := r.Clients.Purser.SetAutomaticTopup(ctx, &purserpb.SetAutomaticTopupRequest{TenantId: tenant, Enabled: input.Enabled, ThresholdEurCents: int64(input.ThresholdEurCents), NetAmountMinor: int64(input.NetAmountMinor), MonthlyGrossLimitMinor: int64(input.MonthlyGrossLimitMinor), Currency: input.Currency, ConsentVersion: input.ConsentVersion})
	if err != nil {
		return nil, err
	}
	return automaticTopupModel(out)
}
func (r *Resolver) DoPlatformAutomaticTopup(ctx context.Context, tenant string) (*purserpb.AutomaticTopupSettings, error) {
	if err := r.platformGate(ctx, "platform.tenant.billing.automatic_topup", tenant); err != nil {
		return nil, err
	}
	out, err := r.Clients.Purser.GetAutomaticTopup(ctx, &purserpb.GetAutomaticTopupRequest{TenantId: tenant})
	if err != nil {
		return nil, err
	}
	return automaticTopupModel(out)
}
func (r *Resolver) DoPlatformDisableAutomaticTopup(ctx context.Context, tenant, reason string) (*purserpb.AutomaticTopupSettings, error) {
	if err := r.platformBillingWrite(ctx, "disable_automatic_topup", tenant, reason); err != nil {
		return nil, err
	}
	out, err := r.Clients.Purser.AdminDisableAutomaticTopup(ctx, &purserpb.AdminDisableAutomaticTopupRequest{TenantId: tenant, Reason: reason})
	if err != nil {
		return nil, err
	}
	return automaticTopupModel(out)
}
func (r *Resolver) DoPlatformRefundInvoice(ctx context.Context, tenant, invoice, request, reason string, confirmed bool, fingerprint string) (*model.PlatformInvoiceRefund, error) {
	if err := r.platformBillingWrite(ctx, "refund_invoice", tenant, reason); err != nil {
		return nil, err
	}
	out, err := r.Clients.Purser.AdminRefundInvoice(ctx, &purserpb.AdminRefundInvoiceRequest{TenantId: tenant, InvoiceId: invoice, RequestId: request, Reason: reason, Confirmed: confirmed, ExpectedFingerprint: fingerprint})
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, fmt.Errorf("missing refund receipt; inspect the same request reference before retrying")
	}
	return &model.PlatformInvoiceRefund{RequestID: out.RequestId, InvoiceID: out.InvoiceId, Provider: out.Provider, AmountMinor: int(out.AmountMinor), Currency: out.Currency, Status: out.Status, Fingerprint: out.Fingerprint, Detail: out.Detail, ProviderRefundID: out.ProviderRefundId}, nil
}
