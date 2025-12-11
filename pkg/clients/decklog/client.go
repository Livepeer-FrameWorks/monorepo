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
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"

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

// Health queries the gRPC health status of the Decklog server.
// If service is empty (""), it checks overall server health.
func (c *Client) Health(ctx context.Context, service string) (grpc_health_v1.HealthCheckResponse_ServingStatus, error) {
	hc := grpc_health_v1.NewHealthClient(c.conn)
	resp, err := hc.Check(ctx, &grpc_health_v1.HealthCheckRequest{Service: service})
	if err != nil {
		return grpc_health_v1.HealthCheckResponse_UNKNOWN, err
	}
	return resp.GetStatus(), nil
}

// BatchedClient provides direct protobuf event sending for services like Foghorn
type BatchedClient struct {
	conn         *grpc.ClientConn
	client       pb.DecklogServiceClient
	logger       logging.Logger
	source       string
	serviceToken string
}

// BatchedClientConfig represents configuration for the batched Decklog client
type BatchedClientConfig struct {
	Target        string
	AllowInsecure bool
	CACertFile    string
	Timeout       time.Duration
	Source        string // Source identifier for all events (e.g., "foghorn")
	ServiceToken  string // Service token for authentication
}

// NewBatchedClient creates a new Decklog gRPC client
func NewBatchedClient(cfg BatchedClientConfig, logger logging.Logger) (*BatchedClient, error) {
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

	source := cfg.Source
	if source == "" {
		source = "unknown"
	}

	client := &BatchedClient{
		conn:         conn,
		client:       pb.NewDecklogServiceClient(conn),
		logger:       logger,
		source:       source,
		serviceToken: cfg.ServiceToken,
	}

	logger.WithFields(logging.Fields{
		"target": cfg.Target,
		"source": source,
	}).Info("Decklog client initialized")

	return client, nil
}

// authContext returns a context with service token authorization metadata
func (c *BatchedClient) authContext() context.Context {
	ctx := context.Background()
	if c.serviceToken != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+c.serviceToken)
	}
	return ctx
}

// SendTrigger sends an enriched MistTrigger to Decklog
func (c *BatchedClient) SendTrigger(trigger *pb.MistTrigger) error {
	ctx := c.authContext()
	_, err := c.client.SendEvent(ctx, trigger)
	if err != nil {
		c.logger.WithFields(logging.Fields{
			"trigger_type": trigger.GetTriggerType(),
			"node_id":      trigger.GetNodeId(),
			"error":        err,
		}).Error("Failed to send trigger to Decklog")
		return fmt.Errorf("failed to send trigger: %w", err)
	}

	c.logger.WithFields(logging.Fields{
		"trigger_type": trigger.GetTriggerType(),
		"node_id":      trigger.GetNodeId(),
		"request_id":   trigger.GetRequestId(),
	}).Debug("Trigger sent to Decklog")

	return nil
}

// SendLoadBalancing sends load balancing data to Decklog
func (c *BatchedClient) SendLoadBalancing(data *pb.LoadBalancingData) error {
	ctx := c.authContext()
	// Wrap into unified envelope
	trigger := &pb.MistTrigger{
		TriggerType: "LOAD_BALANCING",
		TriggerPayload: &pb.MistTrigger_LoadBalancingData{
			LoadBalancingData: data,
		},
	}
	_, err := c.client.SendEvent(ctx, trigger)
	if err != nil {
		c.logger.WithFields(logging.Fields{
			"selected_node": data.GetSelectedNode(),
			"client_ip":     data.GetClientIp(),
			"error":         err,
		}).Error("Failed to send load balancing data to Decklog")
		return fmt.Errorf("failed to send load balancing data: %w", err)
	}

	c.logger.WithFields(logging.Fields{
		"selected_node": data.GetSelectedNode(),
		"client_ip":     data.GetClientIp(),
	}).Debug("Load balancing data sent to Decklog")

	return nil
}

// SendClipLifecycle sends clip lifecycle data to Decklog
func (c *BatchedClient) SendClipLifecycle(data *pb.ClipLifecycleData) error {
	ctx := c.authContext()
	trigger := &pb.MistTrigger{
		TriggerType: "CLIP_LIFECYCLE",
		TriggerPayload: &pb.MistTrigger_ClipLifecycleData{
			ClipLifecycleData: data,
		},
	}
	_, err := c.client.SendEvent(ctx, trigger)
	if err != nil {
		c.logger.WithFields(logging.Fields{
			"clip_hash": data.GetClipHash(),
			"stage":     data.GetStage().String(),
			"error":     err,
		}).Error("Failed to send clip lifecycle data to Decklog")
		return fmt.Errorf("failed to send clip lifecycle data: %w", err)
	}

	c.logger.WithFields(logging.Fields{
		"clip_hash": data.GetClipHash(),
		"stage":     data.GetStage().String(),
	}).Debug("Clip lifecycle data sent to Decklog")

	return nil
}

// SendDVRLifecycle sends DVR lifecycle data to Decklog
func (c *BatchedClient) SendDVRLifecycle(data *pb.DVRLifecycleData) error {
	ctx := c.authContext()
	trigger := &pb.MistTrigger{
		TriggerType: "DVR_LIFECYCLE",
		TriggerPayload: &pb.MistTrigger_DvrLifecycleData{
			DvrLifecycleData: data,
		},
	}
	_, err := c.client.SendEvent(ctx, trigger)
	if err != nil {
		c.logger.WithFields(logging.Fields{
			"dvr_hash": data.GetDvrHash(),
			"status":   data.GetStatus().String(),
			"error":    err,
		}).Error("Failed to send DVR lifecycle data to Decklog")
		return fmt.Errorf("failed to send DVR lifecycle data: %w", err)
	}

	c.logger.WithFields(logging.Fields{
		"dvr_hash": data.GetDvrHash(),
		"status":   data.GetStatus().String(),
	}).Debug("DVR lifecycle data sent to Decklog")

	return nil
}

// Close gracefully shuts down the client
func (c *BatchedClient) Close() error {
	if c.conn != nil {
		c.conn.Close()
	}

	c.logger.WithField("source", c.source).Info("Decklog client closed")
	return nil
}

// Health queries the gRPC health status of the Decklog server.
// If service is empty (""), it checks overall server health.
func (c *BatchedClient) Health(ctx context.Context, service string) (grpc_health_v1.HealthCheckResponse_ServingStatus, error) {
	hc := grpc_health_v1.NewHealthClient(c.conn)
	resp, err := hc.Check(ctx, &grpc_health_v1.HealthCheckRequest{Service: service})
	if err != nil {
		return grpc_health_v1.HealthCheckResponse_UNKNOWN, err
	}
	return resp.GetStatus(), nil
}
