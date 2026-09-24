package clientstest

import (
	"context"

	"frameworks/api_gateway/internal/clients"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/bosun"
	bosunpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/bosun"
)

// FakeBosun stubs the Bosun webhook client.
type FakeBosun struct {
	bosun.Interface

	ListWebhookEndpointsFn        func(ctx context.Context, req *bosunpb.ListWebhookEndpointsRequest) (*bosunpb.ListWebhookEndpointsResponse, error)
	GetWebhookEndpointFn          func(ctx context.Context, endpointID string) (*bosunpb.WebhookEndpoint, error)
	CreateWebhookEndpointFn       func(ctx context.Context, req *bosunpb.CreateWebhookEndpointRequest) (*bosunpb.WebhookEndpointWithSecret, error)
	UpdateWebhookEndpointFn       func(ctx context.Context, req *bosunpb.UpdateWebhookEndpointRequest) (*bosunpb.WebhookEndpoint, error)
	DeleteWebhookEndpointFn       func(ctx context.Context, endpointID string) (*bosunpb.DeleteWebhookEndpointResponse, error)
	EnableWebhookEndpointFn       func(ctx context.Context, endpointID string) (*bosunpb.WebhookEndpoint, error)
	DisableWebhookEndpointFn      func(ctx context.Context, endpointID string) (*bosunpb.WebhookEndpoint, error)
	RotateWebhookEndpointSecretFn func(ctx context.Context, endpointID string, revokePrevious bool) (*bosunpb.WebhookEndpointWithSecret, error)
	TestWebhookEndpointFn         func(ctx context.Context, endpointID string) (*bosunpb.TestWebhookEndpointResponse, error)
	ListWebhookDeliveriesFn       func(ctx context.Context, req *bosunpb.ListWebhookDeliveriesRequest) (*bosunpb.ListWebhookDeliveriesResponse, error)
	GetWebhookDeliveryFn          func(ctx context.Context, deliveryID string) (*bosunpb.GetWebhookDeliveryResponse, error)
	ListAttemptsForDeliveriesFn   func(ctx context.Context, deliveryIDs []string) (*bosunpb.ListAttemptsForDeliveriesResponse, error)
	ReplayWebhookDeliveryFn       func(ctx context.Context, deliveryID string) (*bosunpb.WebhookDelivery, error)
	ReplayWebhookDeliveriesFn     func(ctx context.Context, req *bosunpb.ReplayWebhookDeliveriesRequest) (*bosunpb.ReplayWebhookDeliveriesResponse, error)
}

// WithBosun wires a FakeBosun into the ServiceClients.
func WithBosun(f *FakeBosun) func(*clients.ServiceClients) {
	return func(sc *clients.ServiceClients) { sc.Bosun = f }
}

func (f *FakeBosun) Close() error { return nil }

func (f *FakeBosun) ListWebhookEndpoints(ctx context.Context, req *bosunpb.ListWebhookEndpointsRequest) (*bosunpb.ListWebhookEndpointsResponse, error) {
	if f.ListWebhookEndpointsFn == nil {
		panic("clientstest: FakeBosun.ListWebhookEndpoints not stubbed")
	}
	return f.ListWebhookEndpointsFn(ctx, req)
}

func (f *FakeBosun) GetWebhookEndpoint(ctx context.Context, endpointID string) (*bosunpb.WebhookEndpoint, error) {
	if f.GetWebhookEndpointFn == nil {
		panic("clientstest: FakeBosun.GetWebhookEndpoint not stubbed")
	}
	return f.GetWebhookEndpointFn(ctx, endpointID)
}

func (f *FakeBosun) CreateWebhookEndpoint(ctx context.Context, req *bosunpb.CreateWebhookEndpointRequest) (*bosunpb.WebhookEndpointWithSecret, error) {
	if f.CreateWebhookEndpointFn == nil {
		panic("clientstest: FakeBosun.CreateWebhookEndpoint not stubbed")
	}
	return f.CreateWebhookEndpointFn(ctx, req)
}

func (f *FakeBosun) UpdateWebhookEndpoint(ctx context.Context, req *bosunpb.UpdateWebhookEndpointRequest) (*bosunpb.WebhookEndpoint, error) {
	if f.UpdateWebhookEndpointFn == nil {
		panic("clientstest: FakeBosun.UpdateWebhookEndpoint not stubbed")
	}
	return f.UpdateWebhookEndpointFn(ctx, req)
}

func (f *FakeBosun) DeleteWebhookEndpoint(ctx context.Context, endpointID string) (*bosunpb.DeleteWebhookEndpointResponse, error) {
	if f.DeleteWebhookEndpointFn == nil {
		panic("clientstest: FakeBosun.DeleteWebhookEndpoint not stubbed")
	}
	return f.DeleteWebhookEndpointFn(ctx, endpointID)
}

func (f *FakeBosun) EnableWebhookEndpoint(ctx context.Context, endpointID string) (*bosunpb.WebhookEndpoint, error) {
	if f.EnableWebhookEndpointFn == nil {
		panic("clientstest: FakeBosun.EnableWebhookEndpoint not stubbed")
	}
	return f.EnableWebhookEndpointFn(ctx, endpointID)
}

func (f *FakeBosun) DisableWebhookEndpoint(ctx context.Context, endpointID string) (*bosunpb.WebhookEndpoint, error) {
	if f.DisableWebhookEndpointFn == nil {
		panic("clientstest: FakeBosun.DisableWebhookEndpoint not stubbed")
	}
	return f.DisableWebhookEndpointFn(ctx, endpointID)
}

func (f *FakeBosun) RotateWebhookEndpointSecret(ctx context.Context, endpointID string, revokePrevious bool) (*bosunpb.WebhookEndpointWithSecret, error) {
	if f.RotateWebhookEndpointSecretFn == nil {
		panic("clientstest: FakeBosun.RotateWebhookEndpointSecret not stubbed")
	}
	return f.RotateWebhookEndpointSecretFn(ctx, endpointID, revokePrevious)
}

func (f *FakeBosun) TestWebhookEndpoint(ctx context.Context, endpointID string) (*bosunpb.TestWebhookEndpointResponse, error) {
	if f.TestWebhookEndpointFn == nil {
		panic("clientstest: FakeBosun.TestWebhookEndpoint not stubbed")
	}
	return f.TestWebhookEndpointFn(ctx, endpointID)
}

func (f *FakeBosun) ListWebhookDeliveries(ctx context.Context, req *bosunpb.ListWebhookDeliveriesRequest) (*bosunpb.ListWebhookDeliveriesResponse, error) {
	if f.ListWebhookDeliveriesFn == nil {
		panic("clientstest: FakeBosun.ListWebhookDeliveries not stubbed")
	}
	return f.ListWebhookDeliveriesFn(ctx, req)
}

func (f *FakeBosun) GetWebhookDelivery(ctx context.Context, deliveryID string) (*bosunpb.GetWebhookDeliveryResponse, error) {
	if f.GetWebhookDeliveryFn == nil {
		panic("clientstest: FakeBosun.GetWebhookDelivery not stubbed")
	}
	return f.GetWebhookDeliveryFn(ctx, deliveryID)
}

func (f *FakeBosun) ListAttemptsForDeliveries(ctx context.Context, deliveryIDs []string) (*bosunpb.ListAttemptsForDeliveriesResponse, error) {
	if f.ListAttemptsForDeliveriesFn == nil {
		panic("clientstest: FakeBosun.ListAttemptsForDeliveries not stubbed")
	}
	return f.ListAttemptsForDeliveriesFn(ctx, deliveryIDs)
}

func (f *FakeBosun) ReplayWebhookDelivery(ctx context.Context, deliveryID string) (*bosunpb.WebhookDelivery, error) {
	if f.ReplayWebhookDeliveryFn == nil {
		panic("clientstest: FakeBosun.ReplayWebhookDelivery not stubbed")
	}
	return f.ReplayWebhookDeliveryFn(ctx, deliveryID)
}

func (f *FakeBosun) ReplayWebhookDeliveries(ctx context.Context, req *bosunpb.ReplayWebhookDeliveriesRequest) (*bosunpb.ReplayWebhookDeliveriesResponse, error) {
	if f.ReplayWebhookDeliveriesFn == nil {
		panic("clientstest: FakeBosun.ReplayWebhookDeliveries not stubbed")
	}
	return f.ReplayWebhookDeliveriesFn(ctx, req)
}
