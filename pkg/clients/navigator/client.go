package navigator

import (
	"context"
	"fmt"
	"time"

	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto" // Import generated protobuf code

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// Client represents a Navigator gRPC client
type Client struct {
	conn    *grpc.ClientConn
	service pb.NavigatorServiceClient
	logger  logging.Logger
}

// Config represents the configuration for the Navigator client
type Config struct {
	Addr    string
	Timeout time.Duration
	Logger  logging.Logger
	// ServiceToken for service-to-service auth (optional)
	ServiceToken string
}

func authInterceptor(serviceToken string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if serviceToken == "" {
			return invoker(ctx, method, req, reply, cc, opts...)
		}

		md := metadata.MD{}
		md.Set("authorization", "Bearer "+serviceToken)

		if existingMD, ok := metadata.FromOutgoingContext(ctx); ok {
			md = metadata.Join(existingMD, md)
		}

		ctx = metadata.NewOutgoingContext(ctx, md)
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

// NewClient creates a new Navigator gRPC client
func NewClient(config Config) (*Client, error) {
	if config.Addr == "" {
		return nil, fmt.Errorf("navigator address is required")
	}
	if config.Timeout == 0 {
		config.Timeout = 5 * time.Second
	}

	// DialContext+WithBlock ensures we fail fast if Navigator is unreachable.
	// grpc.NewClient ignores WithBlock/WithTimeout, deferring failures to first RPC.
	ctx, cancel := context.WithTimeout(context.Background(), config.Timeout)
	defer cancel()
	conn, err := grpc.DialContext( //nolint:staticcheck // Need DialContext+WithBlock to fail fast; grpc.NewClient defers failures to first RPC.
		ctx,
		config.Addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(authInterceptor(config.ServiceToken)),
		grpc.WithBlock(), //nolint:staticcheck // See DialContext comment above.
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Navigator gRPC server: %w", err)
	}

	return &Client{
		conn:    conn,
		service: pb.NewNavigatorServiceClient(conn),
		logger:  config.Logger,
	}, nil
}

// Close closes the gRPC client connection
func (c *Client) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// SyncDNS calls the Navigator service to synchronize DNS records
func (c *Client) SyncDNS(ctx context.Context, req *pb.SyncDNSRequest) (*pb.SyncDNSResponse, error) {
	resp, err := c.service.SyncDNS(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to sync DNS: %w", err)
	}
	return resp, nil
}

// IssueCertificate calls the Navigator service to issue or renew a certificate
func (c *Client) IssueCertificate(ctx context.Context, req *pb.IssueCertificateRequest) (*pb.IssueCertificateResponse, error) {
	resp, err := c.service.IssueCertificate(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to issue certificate: %w", err)
	}
	return resp, nil
}

// GetHealth provides a basic health check for the Navigator service

func (c *Client) GetHealth(ctx context.Context) error {

	// A simple way to check connectivity is to call a dummy RPC or ping if one existed.

	// For now, we can rely on the gRPC connection status or implement a dedicated HealthCheck RPC in Navigator.

	// Since no specific health check RPC is defined in dns.proto, we'll just check the connection.

	if c.conn.GetState() == connectivity.TransientFailure { // Or other unhealthy states

		return fmt.Errorf("navigator gRPC connection is in a transient failure state")

	}

	return nil

}

// GetCertificate retrieves an existing certificate from Navigator.
func (c *Client) GetCertificate(ctx context.Context, req *pb.GetCertificateRequest) (*pb.GetCertificateResponse, error) {
	resp, err := c.service.GetCertificate(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to get certificate: %w", err)
	}
	return resp, nil
}
