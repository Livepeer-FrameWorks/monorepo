package resolvers

import (
	"context"
	"fmt"
	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

func (r *Resolver) DoPreviewFreeCancellation(ctx context.Context, tier string) (*model.FreeCancellationReview, error) {
	if err := requireBillingScope(ctx, "billing:write"); err != nil {
		return nil, err
	}
	tenant := ctxkeys.GetTenantID(ctx)
	if tenant == "" {
		return nil, middleware.ErrTenantContextRequired
	}
	if middleware.IsDemoMode(ctx) {
		return nil, middleware.InvalidInput("cancellation reviews are unavailable in demo mode")
	}
	out, err := r.Clients.Purser.PreviewFreeCancellation(ctx, &purserpb.ChangeBillingTierRequest{TenantId: tenant, TierId: tier})
	if err != nil {
		return nil, err
	}
	if out == nil || out.QuotedAt == nil || out.ExpiresAt == nil || out.EffectiveAt == nil {
		return nil, fmt.Errorf("missing cancellation review")
	}
	return &model.FreeCancellationReview{QuotedAt: out.QuotedAt.AsTime(), ExpiresAt: out.ExpiresAt.AsTime(), EffectiveAt: out.EffectiveAt.AsTime(), Fingerprint: out.Fingerprint, DueNowMinor: int(out.DueNowMinor), RefundNowMinor: int(out.RefundNowMinor), WalletCreditNowMinor: int(out.WalletCreditNowMinor), Currency: out.Currency, FutureUsageMessage: out.FutureUsageMessage}, nil
}
