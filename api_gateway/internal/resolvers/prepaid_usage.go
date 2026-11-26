package resolvers

import (
	"context"
	"fmt"
	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
)

func (r *Resolver) DoEnablePrepaidUsage(ctx context.Context) (*model.EnablePrepaidUsagePayload, error) {
	if err := requireBillingScope(ctx, "billing:write"); err != nil {
		return nil, err
	}
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, middleware.ErrTenantContextRequired
	}
	if middleware.IsDemoMode(ctx) {
		return &model.EnablePrepaidUsagePayload{Activated: true, TierName: "payg", BillingModel: "prepaid"}, nil
	}
	out, err := r.Clients.Purser.EnablePrepaidUsage(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, fmt.Errorf("missing funding receipt; reload billing before retrying")
	}
	return &model.EnablePrepaidUsagePayload{Activated: out.Activated, AwaitingFunds: out.AwaitingFunds, TierName: out.TierName, BillingModel: out.BillingModel}, nil
}
