package resolvers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/middleware"
	pb "frameworks/pkg/proto"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// DoCreateBootstrapToken creates a new bootstrap token (service token auth required)
func (r *Resolver) DoCreateBootstrapToken(ctx context.Context, input model.CreateBootstrapTokenInput) (*pb.BootstrapToken, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic bootstrap token")
		now := time.Now()
		exp := now.AddDate(0, 0, 30)
		if input.ExpiresIn != nil {
			exp = now.AddDate(0, 0, *input.ExpiresIn)
		}
		var usageLimit *int32
		if input.UsageLimit != nil {
			limit := int32(*input.UsageLimit)
			usageLimit = &limit
		}
		return &pb.BootstrapToken{
			Id:         "demo_bootstrap_token_001",
			Name:       input.Name,
			Token:      "bt_demo_12345678901234567890123456789012",
			Kind:       string(input.Type),
			UsageLimit: usageLimit,
			UsageCount: 0,
			ExpiresAt:  timestamppb.New(exp),
			CreatedAt:  timestamppb.New(now),
		}, nil
	}

	// Require service token authentication
	if !middleware.HasServiceToken(ctx) {
		return nil, fmt.Errorf("service token authentication required")
	}

	// Convert GraphQL input to Quartermaster request
	req := &pb.CreateBootstrapTokenRequest{
		Name: input.Name,
		Kind: string(input.Type),
	}
	if input.UsageLimit != nil {
		limit := int32(*input.UsageLimit)
		req.UsageLimit = &limit
	}

	// Handle optional expiration - convert days to TTL string
	if input.ExpiresIn != nil && *input.ExpiresIn > 0 {
		req.Ttl = fmt.Sprintf("%dh", *input.ExpiresIn*24) // Convert days to hours
	} else {
		req.Ttl = "24h" // Default to 24 hours
	}

	// Call Quartermaster to create token
	tokenResp, err := r.Clients.Quartermaster.CreateBootstrapToken(ctx, req)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to create bootstrap token")
		return nil, fmt.Errorf("failed to create bootstrap token: %w", err)
	}

	// Return proto token directly
	token := tokenResp.GetToken()
	if token == nil {
		return nil, fmt.Errorf("empty token in response")
	}
	return token, nil
}

// DoRevokeBootstrapToken revokes a bootstrap token (service token auth required)
func (r *Resolver) DoRevokeBootstrapToken(ctx context.Context, id string) (model.RevokeBootstrapTokenResult, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: revoke bootstrap token noop")
		return &model.DeleteSuccess{Success: true, DeletedID: id}, nil
	}

	// Require service token authentication
	if !middleware.HasServiceToken(ctx) {
		return &model.AuthError{Message: "Service token authentication required", Code: strPtr("UNAUTHENTICATED")}, nil
	}

	// Call Quartermaster to revoke token
	err := r.Clients.Quartermaster.RevokeBootstrapToken(ctx, id)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to revoke bootstrap token")
		if strings.Contains(err.Error(), "not found") {
			return &model.NotFoundError{
				Message:      "Bootstrap token not found",
				Code:         strPtr("NOT_FOUND"),
				ResourceType: "BootstrapToken",
				ResourceID:   id,
			}, nil
		}
		return nil, fmt.Errorf("failed to revoke bootstrap token: %w", err)
	}

	return &model.DeleteSuccess{Success: true, DeletedID: id}, nil
}

// DoGetBootstrapTokens retrieves all bootstrap tokens (service token auth required)
func (r *Resolver) DoGetBootstrapTokens(ctx context.Context) ([]*pb.BootstrapToken, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic bootstrap tokens")
		now := time.Now()
		usageLimit1 := int32(10)
		usageLimit2 := int32(1)

		return []*pb.BootstrapToken{
			{
				Id:         "demo_bootstrap_edge_001",
				Name:       "Edge Node Bootstrap - US West",
				Kind:       "EDGE_NODE",
				UsageLimit: &usageLimit1,
				UsageCount: 3,
				ExpiresAt:  timestamppb.New(now.AddDate(0, 1, 0)),
				CreatedAt:  timestamppb.New(now.Add(-7 * 24 * time.Hour)),
				UsedAt:     timestamppb.New(now.Add(-2 * time.Hour)),
			},
			{
				Id:         "demo_bootstrap_service_001",
				Name:       "Service Bootstrap - Transcoder",
				Kind:       "SERVICE",
				UsageLimit: &usageLimit2,
				UsageCount: 0,
				ExpiresAt:  timestamppb.New(now.AddDate(0, 0, 7)),
				CreatedAt:  timestamppb.New(now.Add(-24 * time.Hour)),
			},
		}, nil
	}

	// Require service token authentication
	if !middleware.HasServiceToken(ctx) {
		return nil, fmt.Errorf("service token authentication required")
	}

	// Call Quartermaster to get tokens (get all types with pagination)
	tokensResp, err := r.Clients.Quartermaster.ListBootstrapTokens(ctx, "", "", &pb.CursorPaginationRequest{First: 100})
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get bootstrap tokens")
		return nil, fmt.Errorf("failed to get bootstrap tokens: %w", err)
	}

	return tokensResp.Tokens, nil
}
