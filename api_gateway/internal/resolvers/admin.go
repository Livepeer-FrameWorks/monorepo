package resolvers

import (
	"context"
	"fmt"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/middleware"
	qmapi "frameworks/pkg/api/quartermaster"
	"frameworks/pkg/models"
)

// DoCreateBootstrapToken creates a new bootstrap token (service token auth required)
func (r *Resolver) DoCreateBootstrapToken(ctx context.Context, input model.CreateBootstrapTokenInput) (*models.BootstrapToken, error) {
	// Check if demo mode
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo bootstrap token creation")
		tokenValue := "bt_demo_12345678901234567890123456789012"
		now := time.Now()
		exp := now.AddDate(0, 0, 30) // 30 days default
		if input.ExpiresIn != nil {
			exp = now.AddDate(0, 0, *input.ExpiresIn)
		}
		return &models.BootstrapToken{
			ID:         "demo_bootstrap_token_001",
			Name:       input.Name,
			Token:      tokenValue,
			Type:       string(input.Type),
			UsageLimit: input.UsageLimit,
			UsageCount: 0,
			ExpiresAt:  &exp,
			CreatedAt:  now,
			IsActive:   true,
		}, nil
	}

	// Require service token authentication
	if !middleware.HasServiceToken(ctx) {
		return nil, fmt.Errorf("service token authentication required")
	}

	// Convert GraphQL input to Quartermaster request
	req := &qmapi.CreateBootstrapTokenRequest{
		Kind: string(input.Type), // Map Type enum to Kind field
	}

	// Handle optional expiration - convert days to TTL string
	if input.ExpiresIn != nil && *input.ExpiresIn > 0 {
		req.TTL = fmt.Sprintf("%dh", *input.ExpiresIn*24) // Convert days to hours
	} else {
		req.TTL = "24h" // Default to 24 hours
	}

	// Note: The current Quartermaster API doesn't support name or usage limit fields
	// These would need to be added to Quartermaster or stored separately

	// Call Quartermaster to create token
	tokenResp, err := r.Clients.Quartermaster.CreateBootstrapToken(ctx, req)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to create bootstrap token")
		return nil, fmt.Errorf("failed to create bootstrap token: %w", err)
	}

	// Convert response to bound model
	// The Quartermaster response has a Token field that contains the BootstrapToken struct
	return &models.BootstrapToken{
		ID:         tokenResp.Token.ID,
		Name:       input.Name, // Use name from input since Quartermaster doesn't store it
		Token:      tokenResp.Token.Token,
		Type:       tokenResp.Token.Kind, // Map Kind back to Type
		UsageLimit: input.UsageLimit,     // Use from input since Quartermaster doesn't support it yet
		UsageCount: 0,                    // Not tracked by current Quartermaster API
		ExpiresAt:  &tokenResp.Token.ExpiresAt,
		CreatedAt:  time.Now(), // Use current time since not in response
		LastUsedAt: tokenResp.Token.UsedAt,
		IsActive:   tokenResp.Token.UsedAt == nil, // Consider active if not used
	}, nil
}

// DoRevokeBootstrapToken revokes a bootstrap token (service token auth required)
func (r *Resolver) DoRevokeBootstrapToken(ctx context.Context, id string) (bool, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo bootstrap token revocation")
		return true, nil
	}

	// Require service token authentication
	if !middleware.HasServiceToken(ctx) {
		return false, fmt.Errorf("service token authentication required")
	}

	// Call Quartermaster to revoke token
	err := r.Clients.Quartermaster.RevokeBootstrapToken(ctx, id)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to revoke bootstrap token")
		return false, fmt.Errorf("failed to revoke bootstrap token: %w", err)
	}

	return true, nil
}

// DoGetBootstrapTokens retrieves all bootstrap tokens (service token auth required)
func (r *Resolver) DoGetBootstrapTokens(ctx context.Context) ([]*models.BootstrapToken, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo bootstrap tokens")
		now := time.Now()
		exp1 := now.AddDate(0, 1, 0)
		exp2 := now.AddDate(0, 0, 7)
		usageLimit1 := 10
		usageLimit2 := 1

		return []*models.BootstrapToken{
			{
				ID:         "demo_bootstrap_edge_001",
				Name:       "Edge Node Bootstrap - US West",
				Type:       "EDGE_NODE",
				UsageLimit: &usageLimit1,
				UsageCount: 3,
				ExpiresAt:  &exp1,
				CreatedAt:  now.Add(-7 * 24 * time.Hour),
				LastUsedAt: func() *time.Time { t := now.Add(-2 * time.Hour); return &t }(),
				IsActive:   true,
			},
			{
				ID:         "demo_bootstrap_service_001",
				Name:       "Service Bootstrap - Transcoder",
				Type:       "SERVICE",
				UsageLimit: &usageLimit2,
				UsageCount: 0,
				ExpiresAt:  &exp2,
				CreatedAt:  now.Add(-24 * time.Hour),
				IsActive:   true,
			},
		}, nil
	}

	// Require service token authentication
	if !middleware.HasServiceToken(ctx) {
		return nil, fmt.Errorf("service token authentication required")
	}

	// Call Quartermaster to get tokens
	tokensResp, err := r.Clients.Quartermaster.ListBootstrapTokens(ctx)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get bootstrap tokens")
		return nil, fmt.Errorf("failed to get bootstrap tokens: %w", err)
	}

	// Convert response to bound models
	result := make([]*models.BootstrapToken, len(tokensResp.Tokens))
	for i, token := range tokensResp.Tokens {
		// Map Quartermaster BootstrapToken to our model
		// Note: Quartermaster doesn't store Name, UsageLimit, UsageCount in the current API
		result[i] = &models.BootstrapToken{
			ID:         token.ID,
			Name:       fmt.Sprintf("Token %s", token.ID[:8]), // Generate a display name
			Type:       token.Kind,                            // Map Kind to Type
			UsageLimit: nil,                                   // Not tracked by current Quartermaster API
			UsageCount: 0,                                     // Not tracked by current Quartermaster API
			ExpiresAt:  &token.ExpiresAt,
			CreatedAt:  time.Now(), // Not in response, use a placeholder
			LastUsedAt: token.UsedAt,
			IsActive:   token.UsedAt == nil, // Consider active if not used
		}
	}

	return result, nil
}
