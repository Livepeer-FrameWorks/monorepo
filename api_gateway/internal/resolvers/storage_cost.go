package resolvers

import (
	"context"
	"sync"

	"frameworks/api_gateway/graph/model"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing/storagecost"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

// Storage cost projection. A single GraphQL request that returns N assets
// of a tenant projects N storageCost fields off the same StoragePricing
// snapshot; we cache the snapshot on the request context so we don't fan
// out one GetTenantBillingStatus RPC per asset. The cache is bound to the
// request — no process-wide retention.

// storagePricingCtxKey is the request-scoped key for cached pricing.
type storagePricingCtxKey struct{}

type storagePricingCacheEntry struct {
	mu      sync.Mutex
	loaded  bool
	pricing *purserpb.StoragePricing
	err     error
}

// WithStoragePricingCache attaches a fresh per-request cache to ctx.
// Wire this into the GraphQL middleware chain so resolver hits can reuse
// pricing across an operation. Without it, each call falls through to a
// fresh Purser RPC.
func WithStoragePricingCache(ctx context.Context) context.Context {
	return context.WithValue(ctx, storagePricingCtxKey{}, &storagePricingCacheEntry{})
}

func (r *Resolver) resolveStoragePricing(ctx context.Context, tenantID string) (*purserpb.StoragePricing, error) {
	if tenantID == "" || r.Clients.Purser == nil {
		return nil, nil
	}
	entry, hasCache := ctx.Value(storagePricingCtxKey{}).(*storagePricingCacheEntry)
	if !hasCache || entry == nil {
		// No request-scoped cache attached — single direct fetch.
		bs, err := r.Clients.Purser.GetTenantBillingStatus(ctx, tenantID)
		if err != nil {
			return nil, err
		}
		if bs == nil {
			return nil, nil
		}
		return bs.GetStoragePricing(), nil
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.loaded {
		return entry.pricing, entry.err
	}
	bs, err := r.Clients.Purser.GetTenantBillingStatus(ctx, tenantID)
	entry.loaded = true
	entry.err = err
	if bs != nil {
		entry.pricing = bs.GetStoragePricing()
	}
	return entry.pricing, entry.err
}

// ProjectStorageCostForCaller is the field-resolver entry point: looks up
// the caller's tenant ID from the request context and returns the marginal
// cost for sizeBytes. Returns nil when pricing or bytes are absent so the
// UI renders blank for self-hosted / unsized assets.
func (r *Resolver) ProjectStorageCostForCaller(ctx context.Context, sizeBytes int64) (*model.StorageCostProjection, error) {
	if sizeBytes <= 0 {
		return nil, nil
	}
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, nil
	}
	pricing, err := r.resolveStoragePricing(ctx, tenantID)
	if err != nil {
		// Don't fail the asset query because pricing is unavailable; log
		// and surface a null projection so the UI renders the row without cost.
		r.Logger.WithError(err).WithField("tenant_id", tenantID).Warn("storage cost: pricing lookup failed; returning null projection")
		return nil, nil
	}
	if pricing == nil {
		return nil, nil
	}
	p := storagecost.Project(pricing, sizeBytes)
	if p.PerMonth <= 0 {
		return nil, nil
	}
	return &model.StorageCostProjection{
		PerDay:   p.PerDay,
		PerMonth: p.PerMonth,
		Currency: p.Currency,
	}, nil
}
