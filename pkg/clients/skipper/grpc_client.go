package skipper

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

// GRPCClient is the gRPC client for Skipper.
type GRPCClient struct {
	conn   *grpc.ClientConn
	client pb.SkipperChatServiceClient
	logger logging.Logger
}

// GRPCConfig holds the configuration for the Skipper gRPC client.
type GRPCConfig struct {
	GRPCAddr     string
	Timeout      time.Duration
	Logger       logging.Logger
	ServiceToken string
}

func authInterceptor(serviceToken string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		ctx = attachAuthMetadata(ctx, serviceToken)
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

func streamAuthInterceptor(serviceToken string) grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		ctx = attachAuthMetadata(ctx, serviceToken)
		return streamer(ctx, desc, cc, method, opts...)
	}
}

func attachAuthMetadata(ctx context.Context, serviceToken string) context.Context {
	md := metadata.MD{}

	if userID := ctxkeys.GetUserID(ctx); userID != "" {
		md.Set("x-user-id", userID)
	}
	if tenantID := ctxkeys.GetTenantID(ctx); tenantID != "" {
		md.Set("x-tenant-id", tenantID)
	}

	if jwtToken := ctxkeys.GetJWTToken(ctx); jwtToken != "" {
		md.Set("authorization", "Bearer "+jwtToken)
	} else if serviceToken != "" {
		md.Set("authorization", "Bearer "+serviceToken)
	}

	if existingMD, ok := metadata.FromOutgoingContext(ctx); ok {
		md = metadata.Join(existingMD, md)
	}

	return metadata.NewOutgoingContext(ctx, md)
}

// NewGRPCClient creates a new gRPC client for Skipper.
func NewGRPCClient(config GRPCConfig) (*GRPCClient, error) {
	if config.Timeout == 0 {
		config.Timeout = 30 * time.Second
	}

	conn, err := grpc.NewClient(
		config.GRPCAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.WaitForReady(true)),
		grpc.WithUnaryInterceptor(authInterceptor(config.ServiceToken)),
		grpc.WithStreamInterceptor(streamAuthInterceptor(config.ServiceToken)),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Skipper gRPC: %w", err)
	}

	return &GRPCClient{
		conn:   conn,
		client: pb.NewSkipperChatServiceClient(conn),
		logger: config.Logger,
	}, nil
}

// Close closes the gRPC connection.
func (c *GRPCClient) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// Chat opens a server-streaming Chat RPC. The caller iterates over the
// returned stream to receive SkipperChatEvent messages.
func (c *GRPCClient) Chat(ctx context.Context, req *pb.SkipperChatRequest) (grpc.ServerStreamingClient[pb.SkipperChatEvent], error) {
	return c.client.Chat(ctx, req)
}

// ListConversations lists conversations for the authenticated user.
func (c *GRPCClient) ListConversations(ctx context.Context, limit, offset int32) (*pb.ListSkipperConversationsResponse, error) {
	return c.client.ListConversations(ctx, &pb.ListSkipperConversationsRequest{
		Limit:  limit,
		Offset: offset,
	})
}

// GetConversation returns a single conversation with messages.
func (c *GRPCClient) GetConversation(ctx context.Context, id string) (*pb.SkipperConversationDetail, error) {
	return c.client.GetConversation(ctx, &pb.GetSkipperConversationRequest{
		Id: id,
	})
}

// DeleteConversation deletes a conversation.
func (c *GRPCClient) DeleteConversation(ctx context.Context, id string) (*pb.DeleteSkipperConversationResponse, error) {
	return c.client.DeleteConversation(ctx, &pb.DeleteSkipperConversationRequest{
		Id: id,
	})
}

// UpdateConversationTitle updates the title of a conversation.
func (c *GRPCClient) UpdateConversationTitle(ctx context.Context, id, title string) (*pb.SkipperConversationSummary, error) {
	return c.client.UpdateConversationTitle(ctx, &pb.UpdateSkipperConversationTitleRequest{
		Id:    id,
		Title: title,
	})
}
