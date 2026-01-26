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
			ClusterId:  input.ClusterID,
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
	if input.ClusterID != nil {
		req.ClusterId = input.ClusterID
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
		return demo.GenerateBootstrapTokens(), nil
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

// DoGetBootstrapTokensConnection retrieves bootstrap tokens with pagination (service token auth required)
func (r *Resolver) DoGetBootstrapTokensConnection(ctx context.Context, kind *string, first *int, after *string, last *int, before *string) (*model.BootstrapTokenConnection, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic bootstrap tokens connection")
		tokens := demo.GenerateBootstrapTokens()
		edges := make([]*model.BootstrapTokenEdge, len(tokens))
		for i, token := range tokens {
			cursor := pagination.EncodeCursor(token.CreatedAt.AsTime(), token.Id)
			edges[i] = &model.BootstrapTokenEdge{
				Cursor: cursor,
				Node:   token,
			}
		}
		edgeNodes := make([]*pb.BootstrapToken, 0, len(edges))
		for _, edge := range edges {
			if edge != nil {
				edgeNodes = append(edgeNodes, edge.Node)
			}
		}
		return &model.BootstrapTokenConnection{
			Edges:      edges,
			Nodes:      edgeNodes,
			PageInfo:   &model.PageInfo{HasPreviousPage: false, HasNextPage: false},
			TotalCount: len(tokens),
		}, nil
	}

	// Require service token authentication
	if !middleware.HasServiceToken(ctx) {
		return nil, fmt.Errorf("service token authentication required")
	}

	// Build pagination request
	paginationReq := &pb.CursorPaginationRequest{First: 50}
	if first != nil {
		paginationReq.First = int32(*first)
	}
	if after != nil {
		paginationReq.After = after
	}
	if last != nil {
		paginationReq.Last = int32(*last)
	}
	if before != nil {
		paginationReq.Before = before
	}

	// Call Quartermaster to get tokens
	kindFilter := ""
	if kind != nil {
		kindFilter = *kind
	}
	tokensResp, err := r.Clients.Quartermaster.ListBootstrapTokens(ctx, kindFilter, "", paginationReq)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get bootstrap tokens")
		return nil, fmt.Errorf("failed to get bootstrap tokens: %w", err)
	}

	// Build edges
	edges := make([]*model.BootstrapTokenEdge, len(tokensResp.Tokens))
	for i, token := range tokensResp.Tokens {
		cursor := pagination.EncodeCursor(token.CreatedAt.AsTime(), token.Id)
		edges[i] = &model.BootstrapTokenEdge{
			Cursor: cursor,
			Node:   token,
		}
	}

	// Build page info from backend response
	pag := tokensResp.GetPagination()
	pageInfo := &model.PageInfo{
		HasPreviousPage: pag.GetHasPreviousPage(),
		HasNextPage:     pag.GetHasNextPage(),
	}
	if pag.GetStartCursor() != "" {
		sc := pag.GetStartCursor()
		pageInfo.StartCursor = &sc
	}
	if pag.GetEndCursor() != "" {
		ec := pag.GetEndCursor()
		pageInfo.EndCursor = &ec
	}

	edgeNodes := make([]*pb.BootstrapToken, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.BootstrapTokenConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: int(pag.GetTotalCount()),
	}, nil
}
