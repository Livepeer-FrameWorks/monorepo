package handlers

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
)

func (h *X402Handler) consumeX402RateLimit(ctx context.Context, scope, identity string, limit int) error {
	identity = strings.TrimSpace(strings.ToLower(identity))
	if identity == "" || limit <= 0 {
		return nil
	}
	digest := sha256.Sum256([]byte(scope + "\x00" + identity))
	if _, err := h.db.ExecContext(ctx, `
		DELETE FROM purser.x402_rate_limit_windows
		WHERE window_started_at < NOW() - INTERVAL '24 hours'
	`); err != nil {
		return fmt.Errorf("expire x402 rate limit windows: %w", err)
	}
	var count int
	err := h.db.QueryRowContext(ctx, `
		INSERT INTO purser.x402_rate_limit_windows (
			scope, identity_hash, window_started_at, request_count
		) VALUES ($1, $2, date_trunc('minute', NOW()), 1)
		ON CONFLICT (scope, identity_hash, window_started_at) DO UPDATE
		SET request_count = purser.x402_rate_limit_windows.request_count + 1,
		    updated_at = NOW()
		RETURNING request_count
	`, scope, digest[:]).Scan(&count)
	if err != nil {
		return fmt.Errorf("enforce x402 %s rate limit: %w", scope, err)
	}
	if count > limit {
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
