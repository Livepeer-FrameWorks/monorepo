package bosun

import (
	"context"

	bosunpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/bosun"
)

// Interface is the full method surface of the concrete client so callers can
// inject fakes in tests.
type Interface interface {
	Close() error
	ListWebhookEndpoints(ctx context.Context, req *bosunpb.ListWebhookEndpointsRequest) (*bosunpb.ListWebhookEndpointsResponse, error)
	GetWebhookEndpoint(ctx context.Context, endpointID string) (*bosunpb.WebhookEndpoint, error)
	CreateWebhookEndpoint(ctx context.Context, req *bosunpb.CreateWebhookEndpointRequest) (*bosunpb.WebhookEndpointWithSecret, error)
	UpdateWebhookEndpoint(ctx context.Context, req *bosunpb.UpdateWebhookEndpointRequest) (*bosunpb.WebhookEndpoint, error)
	DeleteWebhookEndpoint(ctx context.Context, endpointID string) (*bosunpb.DeleteWebhookEndpointResponse, error)
	EnableWebhookEndpoint(ctx context.Context, endpointID string) (*bosunpb.WebhookEndpoint, error)
	DisableWebhookEndpoint(ctx context.Context, endpointID string) (*bosunpb.WebhookEndpoint, error)
	RotateWebhookEndpointSecret(ctx context.Context, endpointID string, revokePrevious bool) (*bosunpb.WebhookEndpointWithSecret, error)
	TestWebhookEndpoint(ctx context.Context, endpointID string) (*bosunpb.TestWebhookEndpointResponse, error)
	ListWebhookDeliveries(ctx context.Context, req *bosunpb.ListWebhookDeliveriesRequest) (*bosunpb.ListWebhookDeliveriesResponse, error)
	GetWebhookDelivery(ctx context.Context, deliveryID string) (*bosunpb.GetWebhookDeliveryResponse, error)
	ReplayWebhookDelivery(ctx context.Context, deliveryID string) (*bosunpb.WebhookDelivery, error)
	ReplayWebhookDeliveries(ctx context.Context, req *bosunpb.ReplayWebhookDeliveriesRequest) (*bosunpb.ReplayWebhookDeliveriesResponse, error)
}

var _ Interface = (*GRPCClient)(nil)
