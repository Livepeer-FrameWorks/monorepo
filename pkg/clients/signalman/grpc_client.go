package signalman

import (
	"context"
	"fmt"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	signalmanpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/signalman"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

const DefaultServerName = "signalman.internal"

// GRPCClient is the unary gRPC client for Signalman. Realtime subscriptions use
// Dialer, which owns stream lifetime and fanout.
type GRPCClient struct {
	conn    *grpc.ClientConn
	client  signalmanpb.SignalmanServiceClient
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
	// ServiceToken for service-to-service authentication (fallback when no user JWT)
	ServiceToken  string
	AllowInsecure bool
	CACertFile    string
	ServerName    string
}

// authInterceptor propagates authentication to gRPC metadata.
// This reads user_id, tenant_id, and jwt_token from the Go context (set by Gateway middleware)
// and adds them to outgoing gRPC metadata for downstream services.
// If no user JWT is available, it falls back to the service token for service-to-service calls.
func authInterceptor(serviceToken string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		ctx = attachAuthMetadata(ctx, serviceToken)
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

func attachAuthMetadata(ctx context.Context, serviceToken string) context.Context {
	md := metadata.MD{}
	if existingMD, ok := metadata.FromOutgoingContext(ctx); ok {
		md = existingMD.Copy()
	}
	md.Delete("authorization")
	md.Delete("x-user-id")
	md.Delete("x-tenant-id")

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

	return metadata.NewOutgoingContext(ctx, md)
}

// NewGRPCClient creates a new gRPC client for Signalman
func NewGRPCClient(config GRPCConfig) (*GRPCClient, error) {
	if config.Timeout == 0 {
		config.Timeout = 30 * time.Second
	}

	tlsCfg := grpcutil.ClientTLSConfig{
		CACertFile:        config.CACertFile,
		ServerName:        config.ServerName,
		DefaultServerName: DefaultServerName,
		AllowInsecure:     config.AllowInsecure,
	}
	transport, err := grpcutil.ClientTLS(tlsCfg, config.Logger)
	if err != nil {
		return nil, fmt.Errorf("configure Signalman gRPC TLS: %w", err)
	}

	conn, err := grpc.NewClient(
		config.GRPCAddr,
		transport,
		grpc.WithChainUnaryInterceptor(
			authInterceptor(config.ServiceToken),
			clients.FailsafeUnaryInterceptor("signalman", config.Logger),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Signalman gRPC: %w", err)
	}

	return &GRPCClient{
		conn:    conn,
		client:  signalmanpb.NewSignalmanServiceClient(conn),
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

// GetHubStats returns hub statistics (admin/monitoring)
func (c *GRPCClient) GetHubStats(ctx context.Context) (*signalmanpb.HubStats, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	return c.client.GetHubStats(ctx, &signalmanpb.GetHubStatsRequest{})
}
