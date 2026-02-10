package signalman

import (
	"context"
	"fmt"
	"sync"
	"time"

	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// GRPCClient is the gRPC streaming client for Signalman
type GRPCClient struct {
	conn   *grpc.ClientConn
	client pb.SignalmanServiceClient
	logger logging.Logger

	// Subscription state
	stream    pb.SignalmanService_SubscribeClient
	eventChan chan *pb.SignalmanEvent
	errorChan chan error
	stopChan  chan struct{}
	doneChan  chan struct{}
	mutex     sync.RWMutex
	connected bool
	channels  []pb.Channel
	userID    string
	tenantID  string
}

// GRPCConfig represents the configuration for the gRPC client
type GRPCConfig struct {
	// GRPCAddr is the gRPC server address (host:port, no scheme)
	GRPCAddr string
	// Timeout for gRPC calls
	Timeout time.Duration
	// Logger for the client
	Logger logging.Logger
	// UserID for subscription context
	UserID string
	// TenantID for subscription context
	TenantID string
	// ServiceToken for service-to-service authentication (fallback when no user JWT)
	ServiceToken string
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

	// Use user's JWT from context if available, otherwise fall back to service token
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

// EventHandler is a function that handles incoming events
type EventHandler func(event *pb.SignalmanEvent) error

// NewGRPCClient creates a new gRPC client for Signalman
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
		return nil, fmt.Errorf("failed to connect to Signalman gRPC: %w", err)
	}

	return &GRPCClient{
		conn:      conn,
		client:    pb.NewSignalmanServiceClient(conn),
		logger:    config.Logger,
		eventChan: make(chan *pb.SignalmanEvent, 256),
		errorChan: make(chan error, 1),
		stopChan:  make(chan struct{}),
		doneChan:  make(chan struct{}),
		userID:    config.UserID,
		tenantID:  config.TenantID,
	}, nil
}

// Close closes the gRPC connection
func (c *GRPCClient) Close() error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.connected {
		close(c.stopChan)
		<-c.doneChan
		c.connected = false
	}

	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// Connect establishes the bidirectional stream
func (c *GRPCClient) Connect(ctx context.Context) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.connected {
		return fmt.Errorf("already connected")
	}

	stream, err := c.client.Subscribe(ctx)
	if err != nil {
		return fmt.Errorf("failed to establish stream: %w", err)
	}

	c.stream = stream
	c.connected = true
	c.stopChan = make(chan struct{})
	c.doneChan = make(chan struct{})

	// Start receiver goroutine
	go c.receiveLoop()

	c.logger.Info("Connected to Signalman gRPC stream")
	return nil
}

// receiveLoop reads messages from the server stream
func (c *GRPCClient) receiveLoop() {
	defer func() {
		c.mutex.Lock()
		c.connected = false
		c.mutex.Unlock()
		close(c.doneChan)
	}()

	for {
		select {
		case <-c.stopChan:
			return
		default:
		}

		msg, err := c.stream.Recv()
		if err != nil {
			select {
			case <-c.stopChan:
				return
			case c.errorChan <- err:
			default:
			}
			return
		}

		switch m := msg.Message.(type) {
		case *pb.ServerMessage_Event:
			select {
			case c.eventChan <- m.Event:
			default:
				c.logger.Warn("Event channel full, dropping event")
			}
		case *pb.ServerMessage_SubscriptionConfirmed:
			c.mutex.Lock()
			c.channels = m.SubscriptionConfirmed.SubscribedChannels
			c.mutex.Unlock()
			c.logger.WithFields(logging.Fields{
				"channels": c.channels,
			}).Info("Subscription confirmed")
		case *pb.ServerMessage_Pong:
			// Pong received, connection is alive
		case *pb.ServerMessage_Error:
			c.logger.WithFields(logging.Fields{
				"code":    m.Error.Code,
				"message": m.Error.Message,
			}).Error("Server error received")
		}
	}
}

// Subscribe subscribes to the specified channels
func (c *GRPCClient) Subscribe(channels ...pb.Channel) error {
	c.mutex.RLock()
	if !c.connected {
		c.mutex.RUnlock()
		return fmt.Errorf("not connected")
	}
	stream := c.stream
	c.mutex.RUnlock()

	req := &pb.ClientMessage{
		Message: &pb.ClientMessage_Subscribe{
			Subscribe: &pb.SubscribeRequest{
				Channels: channels,
			},
		},
	}

	if c.userID != "" {
		req.Message.(*pb.ClientMessage_Subscribe).Subscribe.UserId = &c.userID //nolint:errcheck // type just set above
	}
	if c.tenantID != "" {
		req.Message.(*pb.ClientMessage_Subscribe).Subscribe.TenantId = &c.tenantID //nolint:errcheck // type just set above
	}

	if err := stream.Send(req); err != nil {
		return fmt.Errorf("failed to send subscribe: %w", err)
	}

	c.logger.WithFields(logging.Fields{
		"channels": channels,
	}).Info("Sent subscribe request")

	return nil
}

// Unsubscribe unsubscribes from the specified channels
func (c *GRPCClient) Unsubscribe(channels ...pb.Channel) error {
	c.mutex.RLock()
	if !c.connected {
		c.mutex.RUnlock()
		return fmt.Errorf("not connected")
	}
	stream := c.stream
	c.mutex.RUnlock()

	req := &pb.ClientMessage{
		Message: &pb.ClientMessage_Unsubscribe{
			Unsubscribe: &pb.UnsubscribeRequest{
				Channels: channels,
			},
		},
	}

	if err := stream.Send(req); err != nil {
		return fmt.Errorf("failed to send unsubscribe: %w", err)
	}

	return nil
}

// Ping sends a ping to the server
func (c *GRPCClient) Ping() error {
	c.mutex.RLock()
	if !c.connected {
		c.mutex.RUnlock()
		return fmt.Errorf("not connected")
	}
	stream := c.stream
	c.mutex.RUnlock()

	req := &pb.ClientMessage{
		Message: &pb.ClientMessage_Ping{
			Ping: &pb.Ping{
				TimestampMs: time.Now().UnixMilli(),
			},
		},
	}

	return stream.Send(req)
}

// Events returns the channel for receiving events
func (c *GRPCClient) Events() <-chan *pb.SignalmanEvent {
	return c.eventChan
}

// Errors returns the channel for receiving errors
func (c *GRPCClient) Errors() <-chan error {
	return c.errorChan
}

// IsConnected returns whether the client is connected
func (c *GRPCClient) IsConnected() bool {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	return c.connected
}

// GetSubscribedChannels returns the currently subscribed channels
func (c *GRPCClient) GetSubscribedChannels() []pb.Channel {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	result := make([]pb.Channel, len(c.channels))
	copy(result, c.channels)
	return result
}

// StartEventHandler starts a goroutine that calls the handler for each event
func (c *GRPCClient) StartEventHandler(handler EventHandler) {
	go func() {
		for event := range c.eventChan {
			if err := handler(event); err != nil {
				c.logger.WithError(err).WithFields(logging.Fields{
					"event_type": event.EventType,
					"channel":    event.Channel,
				}).Error("Event handler error")
			}
		}
	}()
}

// GetHubStats returns hub statistics (admin/monitoring)
func (c *GRPCClient) GetHubStats(ctx context.Context) (*pb.HubStats, error) {
	return c.client.GetHubStats(ctx, &pb.GetHubStatsRequest{})
}

// ============================================================================
// Convenience methods for common channel subscriptions
// ============================================================================

// SubscribeToStreams subscribes to the streams channel
func (c *GRPCClient) SubscribeToStreams() error {
	return c.Subscribe(pb.Channel_CHANNEL_STREAMS)
}

// SubscribeToAnalytics subscribes to the analytics channel
func (c *GRPCClient) SubscribeToAnalytics() error {
	return c.Subscribe(pb.Channel_CHANNEL_ANALYTICS)
}

// SubscribeToSystem subscribes to the system channel
func (c *GRPCClient) SubscribeToSystem() error {
	return c.Subscribe(pb.Channel_CHANNEL_SYSTEM)
}

// SubscribeToAll subscribes to all channels
func (c *GRPCClient) SubscribeToAll() error {
	return c.Subscribe(pb.Channel_CHANNEL_ALL)
}

// SubscribeToMessaging subscribes to the messaging channel
func (c *GRPCClient) SubscribeToMessaging() error {
	return c.Subscribe(pb.Channel_CHANNEL_MESSAGING)
}

// SubscribeToAI subscribes to the AI channel (Skipper investigation events)
func (c *GRPCClient) SubscribeToAI() error {
	return c.Subscribe(pb.Channel_CHANNEL_AI)
}
