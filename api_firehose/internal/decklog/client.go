package decklog

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"time"

	"frameworks/pkg/logging"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "frameworks/pkg/proto"
)

// Client represents a gRPC client for Decklog
type Client struct {
	conn   *grpc.ClientConn
	client pb.DecklogServiceClient
	logger logging.Logger
}

// ClientConfig represents configuration for the Decklog client
type ClientConfig struct {
	Target        string
	AllowInsecure bool
	CACertFile    string
	Timeout       time.Duration
}

// NewClient creates a new Decklog gRPC client
func NewClient(cfg ClientConfig, logger logging.Logger) (*Client, error) {
	var opts []grpc.DialOption

	if cfg.AllowInsecure {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	} else {
		// Load CA cert for server verification
		caCert, err := os.ReadFile(cfg.CACertFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read CA cert: %w", err)
		}

		certPool := x509.NewCertPool()
		if !certPool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("failed to append CA cert")
		}

		creds := credentials.NewTLS(&tls.Config{
			RootCAs: certPool,
		})
		opts = append(opts, grpc.WithTransportCredentials(creds))
	}

	// Add timeout
	if cfg.Timeout > 0 {
		opts = append(opts, grpc.WithTimeout(cfg.Timeout))
	}

	// Connect to server
	conn, err := grpc.Dial(cfg.Target, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to dial server: %w", err)
	}

	return &Client{
		conn:   conn,
		client: pb.NewDecklogServiceClient(conn),
		logger: logger,
	}, nil
}

// Close closes the client connection
func (c *Client) Close() error {
	return c.conn.Close()
}

// SendEvent sends a prepared event to Decklog.
// Any missing timestamp will be populated automatically.
func (c *Client) SendEvent(ctx context.Context, event *pb.Event) error {
	if event.Timestamp == nil {
		event.Timestamp = timestamppb.Now()
	}

	_, err := c.client.SendEvent(ctx, event)
	if err != nil {
		return fmt.Errorf("failed to send event: %w", err)
	}

	return nil
}

// StreamEvents creates a bidirectional stream for sending events
func (c *Client) StreamEvents(ctx context.Context) (pb.DecklogService_StreamEventsClient, error) {
	stream, err := c.client.StreamEvents(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create stream: %w", err)
	}

	return stream, nil
}

// CheckHealth checks Decklog's health
func (c *Client) CheckHealth(ctx context.Context) (string, error) {
	resp, err := c.client.CheckHealth(ctx, &pb.HealthRequest{})
	if err != nil {
		return "", fmt.Errorf("health check failed: %w", err)
	}

	return resp.Status, nil
}
