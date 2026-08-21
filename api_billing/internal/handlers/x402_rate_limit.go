package handlers

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	"frameworks/api_billing/internal/database/purserdb"
)

func (h *X402Handler) consumeX402RateLimit(ctx context.Context, scope, identity string, limit int) error {
	identity = strings.TrimSpace(strings.ToLower(identity))
	if identity == "" || limit <= 0 {
		return nil
	}
	digest := sha256.Sum256([]byte(scope + "\x00" + identity))
	queries := purserdb.New(h.db)
	if err := queries.ExpireX402RateLimitWindows(ctx); err != nil {
		return fmt.Errorf("expire x402 rate limit windows: %w", err)
	}
	count, err := queries.ConsumeX402RateLimit(ctx, purserdb.ConsumeX402RateLimitParams{
		Scope: scope, IdentityHash: digest[:],
	})
	if err != nil {
		return fmt.Errorf("enforce x402 %s rate limit: %w", scope, err)
	}
	if count > int32(limit) {
		return fmt.Errorf("x402 %s rate limit exceeded; retry after the current minute", scope)
	}
	return nil
}

func (h *X402Handler) enforceQuoteRateLimits(ctx context.Context, tenantID, clientIP string) error {
	if err := h.consumeX402RateLimit(ctx, "quote_tenant", tenantID, 12); err != nil {
		return err
	}
	return h.consumeX402RateLimit(ctx, "quote_ip", clientIP, 30)
}

func (h *X402Handler) enforceSettlementRateLimits(ctx context.Context, tenantID, clientIP, network, payer, quoteID string) error {
	checks := []struct {
		scope    string
		identity string
		limit    int
	}{
		{"settle_tenant", tenantID, 30},
		{"settle_ip", clientIP, 60},
		{"settle_payer", network + ":" + payer, 10},
		{"settle_quote", quoteID, 5},
	}
	for _, check := range checks {
		if err := h.consumeX402RateLimit(ctx, check.scope, check.identity, check.limit); err != nil {
			return err
		}
	}
	return nil
}
