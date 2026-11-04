package grpcserver

import (
	"context"
	"net/url"
	"strings"

	"frameworks/api_webhooks/internal/ledger"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	bosunpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/bosun"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) DiagnoseWebhooks(ctx context.Context, req *bosunpb.DiagnoseWebhooksRequest) (*bosunpb.DiagnoseWebhooksResponse, error) {
	if err := middleware.RequirePlatformOperatorJWT(ctx); err != nil {
		return nil, err
	}
	tenant, err := uuid.Parse(strings.TrimSpace(req.GetTenantId()))
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	endpoints, err := s.Store.ListEndpoints(ctx, tenant.String())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read webhook endpoints: %v", err)
	}
	out := &bosunpb.DiagnoseWebhooksResponse{TenantId: tenant.String()}
	for _, ep := range endpoints {
		deliveries, total, err := s.Store.ListDeliveries(ctx, tenant.String(), ledger.DeliveryFilter{EndpointID: ep.ID, Limit: 20})
		if err != nil {
			return nil, status.Errorf(codes.Internal, "read webhook deliveries: %v", err)
		}
		if len(deliveries) > 20 {
			deliveries = deliveries[:20]
		}
		endpoint := endpointProto(ep)
		// Endpoint URLs can contain signing tokens unrelated to Bosun's own secret.
		if u, err := url.Parse(endpoint.Url); err == nil {
			u.User = nil
			u.RawQuery = ""
			u.Fragment = ""
			endpoint.Url = u.String()
		} else {
			endpoint.Url = "[invalid URL]"
		}
		endpoint.Description = ""
		item := &bosunpb.WebhookEndpointDiagnosis{Endpoint: endpoint, DeliveryCount: int32(total)}
		for _, d := range deliveries {
			item.Deliveries = append(item.Deliveries, deliveryProto(d))
		}
		out.Endpoints = append(out.Endpoints, item)
	}
	return out, nil
}
