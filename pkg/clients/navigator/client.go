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
}

// NewClient creates a new Navigator gRPC client
func NewClient(config Config) (*Client, error) {
	if config.Addr == "" {
		return nil, fmt.Errorf("Navigator address is required")
	}
	if config.Timeout == 0 {
		config.Timeout = 5 * time.Second
	}

	ctx, cancel := context.WithTimeout(context.Background(), config.Timeout)
	defer cancel()

	// For now, use insecure credentials for development. In production, use TLS.
	conn, err := grpc.DialContext(ctx, config.Addr, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
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
