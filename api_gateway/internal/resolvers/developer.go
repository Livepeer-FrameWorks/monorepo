package resolvers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/pkg/models"
)

// DoCreateDeveloperToken creates a new developer token
func (r *Resolver) DoCreateDeveloperToken(ctx context.Context, input model.CreateDeveloperTokenInput) (*model.DeveloperToken, error) {
	// Extract JWT token from context (set by auth middleware)
	userToken, ok := ctx.Value("jwt_token").(string)
	if !ok {
		return nil, fmt.Errorf("user not authenticated")
	}

	// Convert GraphQL input to Commodore request
	req := &models.CreateAPITokenRequest{
		TokenName: input.Name,
	}

	// Handle optional permissions
	if input.Permissions != nil {
		// Split permissions string by comma or semicolon if provided as a single string
		perms := strings.Split(*input.Permissions, ",")
		for i, perm := range perms {
			perms[i] = strings.TrimSpace(perm)
		}
		req.Permissions = perms
	}

	// Handle optional expiration
	if input.ExpiresIn != nil {
		// Convert days to actual expiration date
		expiry := time.Now().AddDate(0, 0, *input.ExpiresIn)
		req.ExpiresAt = &expiry
	}

	// Call Commodore to create token
	tokenResp, err := r.Clients.Commodore.CreateAPIToken(ctx, userToken, req)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to create developer token")
		return nil, fmt.Errorf("failed to create developer token: %w", err)
	}

	// Convert response to GraphQL model
	return &model.DeveloperToken{
		ID:          tokenResp.ID,
		Name:        tokenResp.TokenName,
		Token:       &tokenResp.TokenValue, // Only returned on creation
		Permissions: strings.Join(tokenResp.Permissions, ", "),
		Status:      "active",
		CreatedAt:   time.Now(), // Use current time since API doesn't return this
		ExpiresAt:   tokenResp.ExpiresAt,
	}, nil
}

// DoRevokeDeveloperToken revokes a developer token
func (r *Resolver) DoRevokeDeveloperToken(ctx context.Context, id string) (bool, error) {
	// Extract JWT token from context
	userToken, ok := ctx.Value("jwt_token").(string)
	if !ok {
		return false, fmt.Errorf("user not authenticated")
	}

	// Call Commodore to revoke token
	_, err := r.Clients.Commodore.RevokeAPIToken(ctx, userToken, id)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to revoke developer token")
		return false, fmt.Errorf("failed to revoke developer token: %w", err)
	}

	return true, nil
}

// DoGetDeveloperTokens retrieves all developer tokens for the authenticated user
func (r *Resolver) DoGetDeveloperTokens(ctx context.Context) ([]*model.DeveloperToken, error) {
	// Extract JWT token from context
	userToken, ok := ctx.Value("jwt_token").(string)
	if !ok {
		return nil, fmt.Errorf("user not authenticated")
	}

	// Call Commodore to get tokens
	tokensResp, err := r.Clients.Commodore.GetAPITokens(ctx, userToken)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get developer tokens")
		return nil, fmt.Errorf("failed to get developer tokens: %w", err)
	}

	// Convert response to GraphQL models
	result := make([]*model.DeveloperToken, len(tokensResp.Tokens))
	for i, token := range tokensResp.Tokens {
		result[i] = &model.DeveloperToken{
			ID:          token.ID,
			Name:        token.TokenName,
			Token:       nil, // Never return token value in list (security)
			Permissions: strings.Join(token.Permissions, ", "),
			Status:      token.Status,
			LastUsedAt:  token.LastUsedAt,
			ExpiresAt:   token.ExpiresAt,
			CreatedAt:   token.CreatedAt,
		}
	}

	return result, nil
}
