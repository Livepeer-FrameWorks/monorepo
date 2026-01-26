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

	tenantID := tenantIDFromContext(ctx)
	userID := userIDFromContext(ctx)
	r.sendServiceEvent(ctx, &pb.ServiceEvent{
		EventType:    apiEventTokenCreated,
		ResourceType: "api_token",
		ResourceId:   tokenResp.Id,
		Payload: &pb.ServiceEvent_AuthEvent{
			AuthEvent: &pb.AuthEvent{
				UserId:   userID,
				TenantId: tenantID,
				AuthType: "api_token",
				TokenId:  tokenResp.Id,
			},
		},
	})

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

	tenantID := tenantIDFromContext(ctx)
	userID := userIDFromContext(ctx)
	r.sendServiceEvent(ctx, &pb.ServiceEvent{
		EventType:    apiEventTokenRevoked,
		ResourceType: "api_token",
		ResourceId:   id,
		Payload: &pb.ServiceEvent_AuthEvent{
			AuthEvent: &pb.AuthEvent{
				UserId:   userID,
				TenantId: tenantID,
				AuthType: "api_token",
				TokenId:  id,
			},
		},
	})

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
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo developer tokens connection")
		tokens := demo.GenerateDeveloperTokens()
		return r.buildDeveloperTokensConnectionFromSlice(tokens, first, after, last, before), nil
	}

	// Build bidirectional pagination request
	paginationReq := buildDeveloperTokensPaginationRequest(first, after, last, before)

	// Call Commodore with pagination
	resp, err := r.Clients.Commodore.ListAPITokens(ctx, paginationReq)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get developer tokens")
		return nil, fmt.Errorf("failed to get developer tokens: %w", err)
	}

	return r.buildDeveloperTokensConnectionFromResponse(resp), nil
}

// buildDeveloperTokensPaginationRequest creates a proto pagination request from GraphQL params
func buildDeveloperTokensPaginationRequest(first *int, after *string, last *int, before *string) *pb.CursorPaginationRequest {
	req := &pb.CursorPaginationRequest{}

	if first != nil {
		req.First = int32(pagination.ClampLimit(*first))
	} else if last == nil {
		req.First = int32(pagination.DefaultLimit)
	}

	if after != nil && *after != "" {
		req.After = after
	}

	if last != nil {
		req.Last = int32(pagination.ClampLimit(*last))
	}

	if before != nil && *before != "" {
		req.Before = before
	}

	return req
}

// buildDeveloperTokensConnectionFromResponse constructs a connection from gRPC response
func (r *Resolver) buildDeveloperTokensConnectionFromResponse(resp *pb.ListAPITokensResponse) *model.DeveloperTokensConnection {
	tokens := resp.GetTokens()
	edges := make([]*model.DeveloperTokenEdge, len(tokens))
	for i, token := range tokens {
		cursor := pagination.EncodeCursor(token.CreatedAt.AsTime(), token.Id)
		edges[i] = &model.DeveloperTokenEdge{
			Cursor: cursor,
			Node:   token,
		}
	}

	pag := resp.GetPagination()
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

	edgeNodes := make([]*pb.APITokenInfo, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.DeveloperTokensConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: int(pag.GetTotalCount()),
	}
}

// buildDeveloperTokensConnectionFromSlice constructs a connection from a slice (demo mode)
func (r *Resolver) buildDeveloperTokensConnectionFromSlice(tokens []*pb.APITokenInfo, first *int, after *string, last *int, before *string) *model.DeveloperTokensConnection {
	total := len(tokens)

	limit := pagination.DefaultLimit
	if first != nil {
		limit = pagination.ClampLimit(*first)
	} else if last != nil {
		limit = pagination.ClampLimit(*last)
	}

	if limit > total {
		limit = total
	}

	paginatedTokens := tokens
	if len(tokens) > limit {
		paginatedTokens = tokens[:limit]
	}

	edges := make([]*model.DeveloperTokenEdge, len(paginatedTokens))
	for i, token := range paginatedTokens {
		cursor := pagination.EncodeCursor(token.CreatedAt.AsTime(), token.Id)
		edges[i] = &model.DeveloperTokenEdge{
			Cursor: cursor,
			Node:   token,
		}
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: after != nil && *after != "",
		HasNextPage:     len(tokens) > limit,
	}
	if len(edges) > 0 {
		pageInfo.StartCursor = &edges[0].Cursor
		pageInfo.EndCursor = &edges[len(edges)-1].Cursor
	}

	edgeNodes := make([]*pb.APITokenInfo, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.DeveloperTokensConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: total,
	}
}
