package deckhand

import (
	"context"
	"fmt"
	"time"

	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// GRPCClient is the gRPC client for Deckhand (Support Messaging)
type GRPCClient struct {
	conn    *grpc.ClientConn
	client  pb.DeckhandServiceClient
	logger  logging.Logger
	timeout time.Duration
}

// GRPCConfig represents the configuration for the gRPC client
type GRPCConfig struct {
	// GRPCAddr is the gRPC server address (host:port, no scheme)
	GRPCAddr string
	// Timeout for gRPC calls
	Timeout time.Duration
	// Logger for the client
	Logger logging.Logger
	// ServiceToken for service-to-service authentication
	ServiceToken string
}

// authInterceptor propagates authentication to gRPC metadata.
func authInterceptor(serviceToken string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		md := metadata.MD{}

		if userID := ctxkeys.GetUserID(ctx); userID != "" {
			md.Set("x-user-id", userID)
		}
		if tenantID := ctxkeys.GetTenantID(ctx); tenantID != "" {
			md.Set("x-tenant-id", tenantID)
		}

		// Use user's JWT from context if available, otherwise fall back to service token
		if jwtToken := ctxkeys.GetJWTToken(ctx); jwtToken != "" {
			md.Set("authorization", "Bearer "+jwtToken)
		} else if serviceToken != "" {
			md.Set("authorization", "Bearer "+serviceToken)
		}

		// Merge with existing outgoing metadata if any
		if existingMD, ok := metadata.FromOutgoingContext(ctx); ok {
			md = metadata.Join(existingMD, md)
		}

		ctx = metadata.NewOutgoingContext(ctx, md)
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

// NewGRPCClient creates a new gRPC client for Deckhand
func NewGRPCClient(config GRPCConfig) (*GRPCClient, error) {
	if config.Timeout == 0 {
		config.Timeout = 30 * time.Second
	}

	conn, err := grpc.NewClient(
		config.GRPCAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.WaitForReady(true)),
		grpc.WithUnaryInterceptor(authInterceptor(config.ServiceToken)),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Deckhand gRPC: %w", err)
	}

	return &GRPCClient{
		conn:    conn,
		client:  pb.NewDeckhandServiceClient(conn),
		logger:  config.Logger,
		timeout: config.Timeout,
	}, nil
}

// Close closes the gRPC connection
func (c *GRPCClient) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// ListConversations returns all conversations for a tenant
func (c *GRPCClient) ListConversations(ctx context.Context, tenantID string, page, perPage int32) (*pb.ListConversationsResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	return c.client.ListConversations(ctx, &pb.ListConversationsRequest{
		TenantId: tenantID,
		Page:     page,
		PerPage:  perPage,
	})
}

// SearchConversations searches conversations for a tenant by keyword.
func (c *GRPCClient) SearchConversations(ctx context.Context, tenantID, query string, page, perPage int32) (*pb.SearchConversationsResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	return c.client.SearchConversations(ctx, &pb.SearchConversationsRequest{
		TenantId: tenantID,
		Query:    query,
		Page:     page,
		PerPage:  perPage,
	})
}

// GetConversation returns a single conversation by ID
func (c *GRPCClient) GetConversation(ctx context.Context, conversationID string) (*pb.DeckhandConversation, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	return c.client.GetConversation(ctx, &pb.GetConversationRequest{
		ConversationId: conversationID,
	})
}

// CreateConversation creates a new conversation
func (c *GRPCClient) CreateConversation(ctx context.Context, subject, initialMessage string, customAttrs map[string]string) (*pb.DeckhandConversation, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	return c.client.CreateConversation(ctx, &pb.CreateConversationRequest{
		Subject:          subject,
		InitialMessage:   initialMessage,
		CustomAttributes: customAttrs,
	})
}

// ListMessages returns messages for a conversation
func (c *GRPCClient) ListMessages(ctx context.Context, conversationID string, page, perPage int32) (*pb.ListMessagesResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	return c.client.ListMessages(ctx, &pb.ListMessagesRequest{
		ConversationId: conversationID,
		Page:           page,
		PerPage:        perPage,
	})
}

// SendMessage sends a message in a conversation
func (c *GRPCClient) SendMessage(ctx context.Context, conversationID, content string) (*pb.DeckhandMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	return c.client.SendMessage(ctx, &pb.SendMessageRequest{
		ConversationId: conversationID,
		Content:        content,
	})
}
