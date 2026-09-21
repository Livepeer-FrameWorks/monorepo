// Package bosun is the gRPC client of Bosun, the outbound webhook service.
package bosun

import (
	"context"
	"fmt"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	bosunpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/bosun"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// DefaultServerName is the TLS name of Bosun's internal gRPC certificate.
const DefaultServerName = "bosun.internal"

// testTimeout bounds TestWebhookEndpoint, which waits for one HTTP attempt of
// up to 10 seconds plus signing and settlement.
const testTimeout = 20 * time.Second

// GRPCClient calls BosunService. Every call acts on the tenant of the caller's
// context: the caller's JWT and identity are forwarded, and without a JWT the
// call carries the service token with the tenant as metadata.
type GRPCClient struct {
	conn    *grpc.ClientConn
	client  bosunpb.BosunServiceClient
	logger  logging.Logger
	timeout time.Duration
}

// GRPCConfig configures a GRPCClient.
type GRPCConfig struct {
	GRPCAddr      string
	Timeout       time.Duration
	Logger        logging.Logger
	ServiceToken  string
	AllowInsecure bool
	CACertFile    string
	ServerName    string
}

func authInterceptor(serviceToken string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		return invoker(attachAuthMetadata(ctx, serviceToken), method, req, reply, cc, opts...)
	}
}

// attachAuthMetadata replaces any inbound identity metadata with the caller's
// JWT and identity, or with the service token when the caller has no JWT.
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
	if jwtToken := ctxkeys.GetJWTToken(ctx); jwtToken != "" {
		md.Set("authorization", "Bearer "+jwtToken)
	} else if serviceToken != "" {
		md.Set("authorization", "Bearer "+serviceToken)
	}
	return metadata.NewOutgoingContext(ctx, md)
}

// NewGRPCClient dials Bosun.
func NewGRPCClient(config GRPCConfig) (*GRPCClient, error) {
	if config.Timeout == 0 {
		config.Timeout = 10 * time.Second
	}
	transport, err := grpcutil.ClientTLS(grpcutil.ClientTLSConfig{
		CACertFile:        config.CACertFile,
		ServerName:        config.ServerName,
		DefaultServerName: DefaultServerName,
		AllowInsecure:     config.AllowInsecure,
	}, config.Logger)
	if err != nil {
		return nil, fmt.Errorf("configure Bosun gRPC TLS: %w", err)
	}
	conn, err := grpc.NewClient(
		config.GRPCAddr,
		transport,
		grpc.WithChainUnaryInterceptor(
			authInterceptor(config.ServiceToken),
			clients.FailsafeUnaryInterceptor("bosun", config.Logger),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Bosun gRPC: %w", err)
	}
	return newWithConn(conn, config.Logger, config.Timeout), nil
}

func newWithConn(conn *grpc.ClientConn, logger logging.Logger, timeout time.Duration) *GRPCClient {
	return &GRPCClient{conn: conn, client: bosunpb.NewBosunServiceClient(conn), logger: logger, timeout: timeout}
}

// Close closes the connection.
func (c *GRPCClient) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

func (c *GRPCClient) callCtx(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok || timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}

func call[Req, Resp any](c *GRPCClient, ctx context.Context, timeout time.Duration, fn func(context.Context, Req, ...grpc.CallOption) (Resp, error), req Req) (Resp, error) {
	ctx, cancel := c.callCtx(ctx, timeout)
	defer cancel()
	return fn(ctx, req)
}

// ListWebhookEndpoints lists the caller tenant's endpoints.
func (c *GRPCClient) ListWebhookEndpoints(ctx context.Context, req *bosunpb.ListWebhookEndpointsRequest) (*bosunpb.ListWebhookEndpointsResponse, error) {
	return call(c, ctx, c.timeout, c.client.ListWebhookEndpoints, req)
}

// GetWebhookEndpoint returns one endpoint.
func (c *GRPCClient) GetWebhookEndpoint(ctx context.Context, endpointID string) (*bosunpb.WebhookEndpoint, error) {
	return call(c, ctx, c.timeout, c.client.GetWebhookEndpoint, &bosunpb.GetWebhookEndpointRequest{EndpointId: endpointID})
}

// CreateWebhookEndpoint creates an endpoint and returns its signing secret.
func (c *GRPCClient) CreateWebhookEndpoint(ctx context.Context, req *bosunpb.CreateWebhookEndpointRequest) (*bosunpb.WebhookEndpointWithSecret, error) {
	return call(c, ctx, c.timeout, c.client.CreateWebhookEndpoint, req)
}

// UpdateWebhookEndpoint changes an endpoint's URL, description, or event types.
func (c *GRPCClient) UpdateWebhookEndpoint(ctx context.Context, req *bosunpb.UpdateWebhookEndpointRequest) (*bosunpb.WebhookEndpoint, error) {
	return call(c, ctx, c.timeout, c.client.UpdateWebhookEndpoint, req)
}

// DeleteWebhookEndpoint deletes an endpoint with its history.
func (c *GRPCClient) DeleteWebhookEndpoint(ctx context.Context, endpointID string) (*bosunpb.DeleteWebhookEndpointResponse, error) {
	return call(c, ctx, c.timeout, c.client.DeleteWebhookEndpoint, &bosunpb.DeleteWebhookEndpointRequest{EndpointId: endpointID})
}

// EnableWebhookEndpoint re-enables an endpoint.
func (c *GRPCClient) EnableWebhookEndpoint(ctx context.Context, endpointID string) (*bosunpb.WebhookEndpoint, error) {
	return call(c, ctx, c.timeout, c.client.EnableWebhookEndpoint, &bosunpb.EnableWebhookEndpointRequest{EndpointId: endpointID})
}

// DisableWebhookEndpoint disables an endpoint and skips its pending deliveries.
func (c *GRPCClient) DisableWebhookEndpoint(ctx context.Context, endpointID string) (*bosunpb.WebhookEndpoint, error) {
	return call(c, ctx, c.timeout, c.client.DisableWebhookEndpoint, &bosunpb.DisableWebhookEndpointRequest{EndpointId: endpointID})
}

// RotateWebhookEndpointSecret replaces the signing secret and returns the new one.
func (c *GRPCClient) RotateWebhookEndpointSecret(ctx context.Context, endpointID string, revokePrevious bool) (*bosunpb.WebhookEndpointWithSecret, error) {
	return call(c, ctx, c.timeout, c.client.RotateWebhookEndpointSecret, &bosunpb.RotateWebhookEndpointSecretRequest{EndpointId: endpointID, RevokePrevious: revokePrevious})
}

// TestWebhookEndpoint sends a test event and waits for the attempt.
func (c *GRPCClient) TestWebhookEndpoint(ctx context.Context, endpointID string) (*bosunpb.TestWebhookEndpointResponse, error) {
	return call(c, ctx, max(c.timeout, testTimeout), c.client.TestWebhookEndpoint, &bosunpb.TestWebhookEndpointRequest{EndpointId: endpointID})
}

// ListWebhookDeliveries lists deliveries newest first.
func (c *GRPCClient) ListWebhookDeliveries(ctx context.Context, req *bosunpb.ListWebhookDeliveriesRequest) (*bosunpb.ListWebhookDeliveriesResponse, error) {
	return call(c, ctx, c.timeout, c.client.ListWebhookDeliveries, req)
}

// GetWebhookDelivery returns one delivery with its attempts.
func (c *GRPCClient) GetWebhookDelivery(ctx context.Context, deliveryID string) (*bosunpb.GetWebhookDeliveryResponse, error) {
	return call(c, ctx, c.timeout, c.client.GetWebhookDelivery, &bosunpb.GetWebhookDeliveryRequest{DeliveryId: deliveryID})
}

// ReplayWebhookDelivery sends a finished delivery again under the same ID.
func (c *GRPCClient) ReplayWebhookDelivery(ctx context.Context, deliveryID string) (*bosunpb.WebhookDelivery, error) {
	return call(c, ctx, c.timeout, c.client.ReplayWebhookDelivery, &bosunpb.ReplayWebhookDeliveryRequest{DeliveryId: deliveryID})
}

// ReplayWebhookDeliveries replays failed and skipped deliveries of one endpoint in a range.
func (c *GRPCClient) ReplayWebhookDeliveries(ctx context.Context, req *bosunpb.ReplayWebhookDeliveriesRequest) (*bosunpb.ReplayWebhookDeliveriesResponse, error) {
	return call(c, ctx, c.timeout, c.client.ReplayWebhookDeliveries, req)
}
