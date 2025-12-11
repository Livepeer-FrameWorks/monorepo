package resolvers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/demo"
	"frameworks/api_gateway/internal/middleware"
	"frameworks/pkg/pagination"
	pb "frameworks/pkg/proto"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// DoCreateDeveloperToken creates a new developer token
func (r *Resolver) DoCreateDeveloperToken(ctx context.Context, input model.CreateDeveloperTokenInput) (*pb.APITokenInfo, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo developer token creation")
		// Return a demo token creation response
		now := time.Now()
		exp := now.AddDate(0, 6, 0)
		demoTokenValue := "fwk_demo_" + fmt.Sprintf("%d", now.UnixNano())[:16]
		return &pb.APITokenInfo{
			Id:          "demo_dev_token_001",
			TokenName:   input.Name,
			TokenValue:  &demoTokenValue,
			Permissions: []string{"streams:read", "streams:write", "analytics:read"},
			Status:      "active",
			CreatedAt:   timestamppb.New(now),
			ExpiresAt:   timestamppb.New(exp),
		}, nil
	}

	// User context is propagated via gRPC interceptor from context
	// No need to pass userToken explicitly

	// Convert GraphQL input to Commodore request
	req := &pb.CreateAPITokenRequest{
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
		req.ExpiresAt = timestamppb.New(expiry)
	}

	// Call Commodore to create token
	tokenResp, err := r.Clients.Commodore.CreateAPIToken(ctx, req)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to create developer token")
		return nil, fmt.Errorf("failed to create developer token: %w", err)
	}

	// Convert response to APITokenInfo (matching gqlgen binding)
	// Include TokenValue for creation response - it's only available on creation
	return &pb.APITokenInfo{
		Id:          tokenResp.Id,
		TokenName:   tokenResp.TokenName,
		TokenValue:  &tokenResp.TokenValue,
		Permissions: tokenResp.Permissions,
		Status:      "active",
		ExpiresAt:   tokenResp.ExpiresAt,
		CreatedAt:   timestamppb.Now(),
	}, nil
}

// DoRevokeDeveloperToken revokes a developer token
func (r *Resolver) DoRevokeDeveloperToken(ctx context.Context, id string) (model.RevokeDeveloperTokenResult, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo developer token revocation")
		return &model.DeleteSuccess{Success: true, DeletedID: id}, nil
	}

	// User context is propagated via gRPC interceptor from context
	_, err := r.Clients.Commodore.RevokeAPIToken(ctx, id)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to revoke developer token")
		if strings.Contains(err.Error(), "not found") {
			return &model.NotFoundError{
				Message:      "Developer token not found",
				Code:         strPtr("NOT_FOUND"),
				ResourceType: "DeveloperToken",
				ResourceID:   id,
			}, nil
		}
		return nil, fmt.Errorf("failed to revoke developer token: %w", err)
	}

	return &model.DeleteSuccess{Success: true, DeletedID: id}, nil
}

// DoGetDeveloperTokens retrieves all developer tokens for the authenticated user
func (r *Resolver) DoGetDeveloperTokens(ctx context.Context) ([]*pb.APITokenInfo, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo developer tokens")
		return demo.GenerateDeveloperTokens(), nil
	}

	// User context is propagated via gRPC interceptor from context
	tokensResp, err := r.Clients.Commodore.ListAPITokens(ctx, nil)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get developer tokens")
		return nil, fmt.Errorf("failed to get developer tokens: %w", err)
	}

	return tokensResp.Tokens, nil
}

// DoGetDeveloperTokensConnection returns a Relay-style connection for developer tokens
func (r *Resolver) DoGetDeveloperTokensConnection(ctx context.Context, first *int, after *string, last *int, before *string) (*model.DeveloperTokensConnection, error) {
	// TODO: Implement bidirectional keyset pagination once Commodore supports it
	_ = last
	_ = before

	limit := pagination.DefaultLimit
	offset := 0

	if first != nil {
		limit = pagination.ClampLimit(*first)
	}

	// Get all tokens (existing implementation doesn't support pagination at source)
	tokens, err := r.DoGetDeveloperTokens(ctx)
	if err != nil {
		return nil, err
	}

	totalCount := len(tokens)

	// Apply pagination in-memory
	start := offset
	if start > totalCount {
		start = totalCount
	}
	end := start + limit
	if end > totalCount {
		end = totalCount
	}

	paginatedTokens := tokens[start:end]

	// Build edges (Node is *pb.APITokenInfo per gqlgen binding)
	edges := make([]*model.DeveloperTokenEdge, len(paginatedTokens))
	for i, token := range paginatedTokens {
		cursor := pagination.EncodeIndexCursor(start + i)
		edges[i] = &model.DeveloperTokenEdge{
			Cursor: cursor,
			Node:   token,
		}
	}

	// Build pageInfo
	hasMore := end < totalCount
	pageInfo := &model.PageInfo{
		HasPreviousPage: offset > 0,
		HasNextPage:     hasMore,
	}
	if len(edges) > 0 {
		firstCursor := pagination.EncodeIndexCursor(start)
		lastCursor := pagination.EncodeIndexCursor(end - 1)
		pageInfo.StartCursor = &firstCursor
		pageInfo.EndCursor = &lastCursor
	}

	return &model.DeveloperTokensConnection{
		Edges:      edges,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}
