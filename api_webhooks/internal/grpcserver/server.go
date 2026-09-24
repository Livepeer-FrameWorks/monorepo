// Package grpcserver implements BosunService.
package grpcserver

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"frameworks/api_webhooks/internal/ledger"
	"frameworks/api_webhooks/internal/metrics"
	"frameworks/api_webhooks/internal/validate"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/authz"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pagination"
	bosunpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/bosun"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/restream"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Tester sends a test delivery.
type Tester interface {
	SendTest(ctx context.Context, ep ledger.Endpoint) (ledger.Delivery, ledger.Attempt, error)
}

// Server implements bosunpb.BosunServiceServer.
type Server struct {
	bosunpb.UnimplementedBosunServiceServer
	Store   *ledger.Store
	Tester  Tester
	Policy  restream.DestinationPolicy
	Metrics *metrics.Metrics
	Logger  logging.Logger
}

// tenantFromContext returns the tenant of the authenticated call. Every RPC
// acts on that tenant only; no request field names one.
func tenantFromContext(ctx context.Context) (string, error) {
	tenantID := strings.TrimSpace(ctxkeys.GetTenantID(ctx))
	if tenantID == "" {
		return "", status.Error(codes.PermissionDenied, "webhook calls require a tenant context")
	}
	if _, err := uuid.Parse(tenantID); err != nil {
		return "", status.Error(codes.PermissionDenied, "the tenant context is not a tenant ID")
	}
	return tenantID, nil
}

// tenantForWrite enforces the same scope and owner/admin policy as the gateway.
// Validated service calls are trusted internal callers; metadata alone is not authority.
func tenantForWrite(ctx context.Context) (string, error) {
	tenantID, err := tenantFromContext(ctx)
	if err != nil {
		return "", err
	}
	if middleware.IsServiceCall(ctx) {
		return tenantID, nil
	}
	switch ctxkeys.GetAuthType(ctx) {
	case "jwt", "wallet":
	case "api_token":
		if !slices.ContainsFunc(ctxkeys.GetPermissions(ctx), func(p string) bool { return strings.TrimSpace(p) == "developer:write" }) {
			return "", status.Error(codes.PermissionDenied, "webhook changes require developer:write scope")
		}
	default:
		return "", status.Error(codes.PermissionDenied, "webhook changes require an authenticated actor")
	}
	decision := authz.Default.Can(ctx, authz.Identity{
		UserID:           ctxkeys.GetUserID(ctx),
		TenantID:         tenantID,
		Role:             ctxkeys.GetRole(ctx),
		Permissions:      ctxkeys.GetPermissions(ctx),
		PlatformOperator: ctxkeys.IsPlatformOperator(ctx),
	}, authz.ActionManageWebhooks, authz.Resource{OwnerTenantID: tenantID})
	if !decision.Allow {
		return "", status.Error(codes.PermissionDenied, decision.Reason)
	}
	return tenantID, nil
}

func (s *Server) toStatus(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ledger.ErrNotFound):
		return status.Error(codes.NotFound, "not found")
	case errors.Is(err, ledger.ErrEndpointLimit):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, ledger.ErrTestRateLimited):
		return status.Error(codes.ResourceExhausted, err.Error())
	case errors.Is(err, ledger.ErrPrecondition):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, validate.ErrInvalid):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, validate.ErrResolution):
		return status.Error(codes.Unavailable, err.Error())
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return status.FromContextError(err).Err()
	default:
		if s.Logger != nil {
			s.Logger.WithError(err).Error("Webhook API call failed")
		}
		return status.Error(codes.Internal, "internal error")
	}
}

// ListWebhookEndpoints returns every endpoint of the tenant, oldest first.
// A tenant has at most 10, so the response is one page.
func (s *Server) ListWebhookEndpoints(ctx context.Context, _ *bosunpb.ListWebhookEndpointsRequest) (*bosunpb.ListWebhookEndpointsResponse, error) {
	tenantID, err := tenantFromContext(ctx)
	if err != nil {
		return nil, err
	}
	endpoints, err := s.Store.ListEndpoints(ctx, tenantID)
	if err != nil {
		return nil, s.toStatus(err)
	}
	resp := &bosunpb.ListWebhookEndpointsResponse{Pagination: &commonpb.CursorPaginationResponse{TotalCount: int32(len(endpoints))}}
	for _, ep := range endpoints {
		resp.Endpoints = append(resp.Endpoints, endpointProto(ep))
	}
	if n := len(endpoints); n > 0 {
		start := pagination.EncodeCursor(endpoints[0].CreatedAt, endpoints[0].ID)
		end := pagination.EncodeCursor(endpoints[n-1].CreatedAt, endpoints[n-1].ID)
		resp.Pagination.StartCursor, resp.Pagination.EndCursor = &start, &end
	}
	return resp, nil
}

// GetWebhookEndpoint returns one endpoint.
func (s *Server) GetWebhookEndpoint(ctx context.Context, req *bosunpb.GetWebhookEndpointRequest) (*bosunpb.WebhookEndpoint, error) {
	tenantID, err := tenantFromContext(ctx)
	if err != nil {
		return nil, err
	}
	ep, err := s.Store.GetEndpoint(ctx, tenantID, req.GetEndpointId())
	if err != nil {
		return nil, s.toStatus(err)
	}
	return endpointProto(ep), nil
}

// CreateWebhookEndpoint validates and creates an endpoint.
func (s *Server) CreateWebhookEndpoint(ctx context.Context, req *bosunpb.CreateWebhookEndpointRequest) (*bosunpb.WebhookEndpointWithSecret, error) {
	tenantID, err := tenantForWrite(ctx)
	if err != nil {
		return nil, err
	}
	in, err := s.validateNew(ctx, req)
	if err != nil {
		return nil, s.toStatus(err)
	}
	ep, key, err := s.Store.CreateEndpoint(ctx, tenantID, in)
	if err != nil {
		return nil, s.toStatus(err)
	}
	return &bosunpb.WebhookEndpointWithSecret{Endpoint: endpointProto(ep), Secret: key.String()}, nil
}

func (s *Server) validateNew(ctx context.Context, req *bosunpb.CreateWebhookEndpointRequest) (ledger.NewEndpoint, error) {
	url, err := validate.URL(ctx, s.Policy, req.GetUrl())
	if err != nil {
		return ledger.NewEndpoint{}, err
	}
	description, err := validate.Description(req.GetDescription())
	if err != nil {
		return ledger.NewEndpoint{}, err
	}
	types, err := validate.EventTypes(req.GetEventTypes())
	if err != nil {
		return ledger.NewEndpoint{}, err
	}
	version, err := validate.APIVersion(req.GetApiVersion())
	if err != nil {
		return ledger.NewEndpoint{}, err
	}
	return ledger.NewEndpoint{URL: url, Description: description, EventTypes: types, APIVersion: version}, nil
}

func (s *Server) validateUpdate(ctx context.Context, req *bosunpb.UpdateWebhookEndpointRequest) (ledger.EndpointUpdate, error) {
	var update ledger.EndpointUpdate
	if req.Url != nil {
		url, err := validate.URL(ctx, s.Policy, req.GetUrl())
		if err != nil {
			return update, err
		}
		update.URL = &url
	}
	if req.Description != nil {
		description, err := validate.Description(req.GetDescription())
		if err != nil {
			return update, err
		}
		update.Description = &description
	}
	if req.GetSetEventTypes() {
		types, err := validate.EventTypes(req.GetEventTypes())
		if err != nil {
			return update, err
		}
		update.EventTypes = types
	}
	return update, nil
}

// UpdateWebhookEndpoint validates and applies the given fields.
func (s *Server) UpdateWebhookEndpoint(ctx context.Context, req *bosunpb.UpdateWebhookEndpointRequest) (*bosunpb.WebhookEndpoint, error) {
	tenantID, err := tenantForWrite(ctx)
	if err != nil {
		return nil, err
	}
	update, err := s.validateUpdate(ctx, req)
	if err != nil {
		return nil, s.toStatus(err)
	}
	ep, err := s.Store.UpdateEndpoint(ctx, tenantID, req.GetEndpointId(), update)
	if err != nil {
		return nil, s.toStatus(err)
	}
	return endpointProto(ep), nil
}

// DeleteWebhookEndpoint deletes an endpoint.
func (s *Server) DeleteWebhookEndpoint(ctx context.Context, req *bosunpb.DeleteWebhookEndpointRequest) (*bosunpb.DeleteWebhookEndpointResponse, error) {
	tenantID, err := tenantForWrite(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.Store.DeleteEndpoint(ctx, tenantID, req.GetEndpointId()); err != nil {
		return nil, s.toStatus(err)
	}
	return &bosunpb.DeleteWebhookEndpointResponse{EndpointId: req.GetEndpointId()}, nil
}

// EnableWebhookEndpoint re-enables an endpoint.
func (s *Server) EnableWebhookEndpoint(ctx context.Context, req *bosunpb.EnableWebhookEndpointRequest) (*bosunpb.WebhookEndpoint, error) {
	tenantID, err := tenantForWrite(ctx)
	if err != nil {
		return nil, err
	}
	ep, err := s.Store.EnableEndpoint(ctx, tenantID, req.GetEndpointId())
	if err != nil {
		return nil, s.toStatus(err)
	}
	return endpointProto(ep), nil
}

// DisableWebhookEndpoint disables an endpoint.
func (s *Server) DisableWebhookEndpoint(ctx context.Context, req *bosunpb.DisableWebhookEndpointRequest) (*bosunpb.WebhookEndpoint, error) {
	tenantID, err := tenantForWrite(ctx)
	if err != nil {
		return nil, err
	}
	ep, err := s.Store.DisableEndpoint(ctx, tenantID, req.GetEndpointId())
	if err != nil {
		return nil, s.toStatus(err)
	}
	return endpointProto(ep), nil
}

// RotateWebhookEndpointSecret replaces the signing secret.
func (s *Server) RotateWebhookEndpointSecret(ctx context.Context, req *bosunpb.RotateWebhookEndpointSecretRequest) (*bosunpb.WebhookEndpointWithSecret, error) {
	tenantID, err := tenantForWrite(ctx)
	if err != nil {
		return nil, err
	}
	ep, key, err := s.Store.RotateSecret(ctx, tenantID, req.GetEndpointId(), req.GetRevokePrevious())
	if err != nil {
		return nil, s.toStatus(err)
	}
	return &bosunpb.WebhookEndpointWithSecret{Endpoint: endpointProto(ep), Secret: key.String()}, nil
}

// TestWebhookEndpoint sends a test event, at most once per endpoint per 10
// seconds across all replicas.
func (s *Server) TestWebhookEndpoint(ctx context.Context, req *bosunpb.TestWebhookEndpointRequest) (*bosunpb.TestWebhookEndpointResponse, error) {
	tenantID, err := tenantForWrite(ctx)
	if err != nil {
		return nil, err
	}
	ep, err := s.Store.ClaimTestSlot(ctx, tenantID, req.GetEndpointId())
	if err != nil {
		return nil, s.toStatus(err)
	}
	d, a, err := s.Tester.SendTest(ctx, ep)
	if err != nil {
		return nil, s.toStatus(err)
	}
	return &bosunpb.TestWebhookEndpointResponse{Delivery: deliveryProto(d), Attempt: attemptProto(a)}, nil
}

// ListWebhookDeliveries lists deliveries newest first.
func (s *Server) ListWebhookDeliveries(ctx context.Context, req *bosunpb.ListWebhookDeliveriesRequest) (*bosunpb.ListWebhookDeliveriesResponse, error) {
	tenantID, err := tenantFromContext(ctx)
	if err != nil {
		return nil, err
	}
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if params.Direction != pagination.Forward {
		return nil, status.Error(codes.InvalidArgument, "backward pagination is not supported")
	}
	filter := ledger.DeliveryFilter{
		EndpointID: strings.TrimSpace(req.GetEndpointId()),
		EventType:  strings.TrimSpace(req.GetEventType()),
		EventID:    strings.TrimSpace(req.GetEventId()),
		Limit:      params.Limit,
	}
	for _, st := range req.GetStatuses() {
		value, ok := deliveryStatusFromProto(st)
		if !ok {
			return nil, status.Errorf(codes.InvalidArgument, "unknown delivery status %s", st)
		}
		filter.Statuses = append(filter.Statuses, value)
	}
	if req.GetCreatedAfter() != nil {
		t := req.GetCreatedAfter().AsTime()
		filter.CreatedAfter = &t
	}
	if req.GetCreatedBefore() != nil {
		t := req.GetCreatedBefore().AsTime()
		filter.CreatedBefore = &t
	}
	if params.Cursor != nil {
		if _, parseErr := uuid.Parse(params.Cursor.ID); parseErr != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid cursor")
		}
		t := params.Cursor.Timestamp
		filter.AfterCreatedAt, filter.AfterID = &t, params.Cursor.ID
	}
	deliveries, total, err := s.Store.ListDeliveries(ctx, tenantID, filter)
	if err != nil {
		return nil, s.toStatus(err)
	}
	hasMore := len(deliveries) > params.Limit
	if hasMore {
		deliveries = deliveries[:params.Limit]
	}
	resp := &bosunpb.ListWebhookDeliveriesResponse{}
	for _, d := range deliveries {
		resp.Deliveries = append(resp.Deliveries, deliveryProto(d))
	}
	var start, end string
	if n := len(deliveries); n > 0 {
		start = pagination.EncodeCursor(deliveries[0].CreatedAt, deliveries[0].ID)
		end = pagination.EncodeCursor(deliveries[n-1].CreatedAt, deliveries[n-1].ID)
	}
	resp.Pagination = pagination.BuildResponse(len(deliveries)+boolToInt(hasMore), params.Limit, pagination.Forward, int32(total), start, end)
	resp.Pagination.HasPreviousPage = params.Cursor != nil
	return resp, nil
}

// GetWebhookDelivery returns a delivery with its attempts.
func (s *Server) GetWebhookDelivery(ctx context.Context, req *bosunpb.GetWebhookDeliveryRequest) (*bosunpb.GetWebhookDeliveryResponse, error) {
	tenantID, err := tenantFromContext(ctx)
	if err != nil {
		return nil, err
	}
	d, attempts, err := s.Store.GetDelivery(ctx, tenantID, req.GetDeliveryId())
	if err != nil {
		return nil, s.toStatus(err)
	}
	resp := &bosunpb.GetWebhookDeliveryResponse{Delivery: deliveryProto(d)}
	for _, a := range attempts {
		resp.Attempts = append(resp.Attempts, attemptProto(a))
	}
	return resp, nil
}

// ListAttemptsForDeliveries returns the attempts of several deliveries of
// the calling tenant.
func (s *Server) ListAttemptsForDeliveries(ctx context.Context, req *bosunpb.ListAttemptsForDeliveriesRequest) (*bosunpb.ListAttemptsForDeliveriesResponse, error) {
	tenantID, err := tenantFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if len(req.GetDeliveryIds()) > pagination.MaxLimit {
		return nil, status.Errorf(codes.InvalidArgument, "at most %d delivery IDs per call", pagination.MaxLimit)
	}
	byDelivery, err := s.Store.ListAttemptsForDeliveries(ctx, tenantID, req.GetDeliveryIds())
	if err != nil {
		return nil, s.toStatus(err)
	}
	resp := &bosunpb.ListAttemptsForDeliveriesResponse{}
	for _, id := range req.GetDeliveryIds() {
		attempts, ok := byDelivery[id]
		if !ok {
			continue
		}
		delete(byDelivery, id)
		entry := &bosunpb.DeliveryAttempts{DeliveryId: id}
		for _, a := range attempts {
			entry.Attempts = append(entry.Attempts, attemptProto(a))
		}
		resp.Deliveries = append(resp.Deliveries, entry)
	}
	return resp, nil
}

// ReplayWebhookDelivery replays one delivery.
func (s *Server) ReplayWebhookDelivery(ctx context.Context, req *bosunpb.ReplayWebhookDeliveryRequest) (*bosunpb.WebhookDelivery, error) {
	tenantID, err := tenantForWrite(ctx)
	if err != nil {
		return nil, err
	}
	d, err := s.Store.ReplayDelivery(ctx, tenantID, req.GetDeliveryId())
	if err != nil {
		return nil, s.toStatus(err)
	}
	s.Metrics.Replay("single", 1)
	return deliveryProto(d), nil
}

// ReplayWebhookDeliveries replays failed and skipped deliveries in a range.
func (s *Server) ReplayWebhookDeliveries(ctx context.Context, req *bosunpb.ReplayWebhookDeliveriesRequest) (*bosunpb.ReplayWebhookDeliveriesResponse, error) {
	tenantID, err := tenantForWrite(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetCreatedAfter() == nil || req.GetCreatedBefore() == nil {
		return nil, status.Error(codes.InvalidArgument, "created_after and created_before are required")
	}
	n, more, err := s.Store.ReplayRange(ctx, tenantID, req.GetEndpointId(), req.GetCreatedAfter().AsTime(), req.GetCreatedBefore().AsTime())
	if err != nil {
		return nil, s.toStatus(err)
	}
	s.Metrics.Replay("range", n)
	return &bosunpb.ReplayWebhookDeliveriesResponse{ReplayedCount: int32(n), HasMore: more}, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func ts(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return timestamppb.New(*t)
}

func endpointProto(ep ledger.Endpoint) *bosunpb.WebhookEndpoint {
	out := &bosunpb.WebhookEndpoint{
		Id:                      ep.ID,
		Url:                     ep.URL,
		Description:             ep.Description,
		EventTypes:              ep.EventTypes,
		ApiVersion:              ep.APIVersion,
		Status:                  bosunpb.WebhookEndpointStatus_WEBHOOK_ENDPOINT_STATUS_ENABLED,
		DisabledAt:              ts(ep.DisabledAt),
		ConsecutiveFailures:     int32(ep.ConsecutiveFailures),
		FailingSince:            ts(ep.FailingSince),
		LastSuccessAt:           ts(ep.LastSuccessAt),
		LastFailureAt:           ts(ep.LastFailureAt),
		PreviousSecretExpiresAt: ts(ep.PreviousSecretExpiresAt),
		CreatedAt:               timestamppb.New(ep.CreatedAt),
		UpdatedAt:               timestamppb.New(ep.UpdatedAt),
	}
	if ep.Status == ledger.StatusDisabled {
		out.Status = bosunpb.WebhookEndpointStatus_WEBHOOK_ENDPOINT_STATUS_DISABLED
	}
	switch ep.DisabledReason {
	case ledger.DisabledByUser:
		out.DisabledReason = bosunpb.WebhookEndpointDisabledReason_WEBHOOK_ENDPOINT_DISABLED_REASON_USER
	case ledger.DisabledByFailing:
		out.DisabledReason = bosunpb.WebhookEndpointDisabledReason_WEBHOOK_ENDPOINT_DISABLED_REASON_FAILING
	}
	return out
}

func deliveryStatusFromProto(st bosunpb.WebhookDeliveryStatus) (string, bool) {
	switch st {
	case bosunpb.WebhookDeliveryStatus_WEBHOOK_DELIVERY_STATUS_PENDING:
		return ledger.DeliveryPending, true
	case bosunpb.WebhookDeliveryStatus_WEBHOOK_DELIVERY_STATUS_SUCCEEDED:
		return ledger.DeliverySucceeded, true
	case bosunpb.WebhookDeliveryStatus_WEBHOOK_DELIVERY_STATUS_FAILED:
		return ledger.DeliveryFailed, true
	case bosunpb.WebhookDeliveryStatus_WEBHOOK_DELIVERY_STATUS_SKIPPED:
		return ledger.DeliverySkipped, true
	default:
		return "", false
	}
}

func deliveryProto(d ledger.Delivery) *bosunpb.WebhookDelivery {
	out := &bosunpb.WebhookDelivery{
		Id:             d.ID,
		EndpointId:     d.EndpointID,
		EventId:        d.EventID,
		EventType:      d.EventType,
		Kind:           bosunpb.WebhookDeliveryKind_WEBHOOK_DELIVERY_KIND_EVENT,
		Attempts:       int32(d.Attempts),
		NextAttemptAt:  ts(d.NextAttemptAt),
		LastStatusCode: int32(d.LastStatusCode),
		LastErrorClass: d.LastErrorClass,
		DeliveredAt:    ts(d.DeliveredAt),
		ReplayCount:    int32(d.ReplayCount),
		LastReplayedAt: ts(d.LastReplayedAt),
		CreatedAt:      timestamppb.New(d.CreatedAt),
		UpdatedAt:      timestamppb.New(d.UpdatedAt),
	}
	if d.Kind == ledger.KindTest {
		out.Kind = bosunpb.WebhookDeliveryKind_WEBHOOK_DELIVERY_KIND_TEST
	}
	switch d.Status {
	case ledger.DeliveryPending:
		out.Status = bosunpb.WebhookDeliveryStatus_WEBHOOK_DELIVERY_STATUS_PENDING
	case ledger.DeliverySucceeded:
		out.Status = bosunpb.WebhookDeliveryStatus_WEBHOOK_DELIVERY_STATUS_SUCCEEDED
	case ledger.DeliveryFailed:
		out.Status = bosunpb.WebhookDeliveryStatus_WEBHOOK_DELIVERY_STATUS_FAILED
	case ledger.DeliverySkipped:
		out.Status = bosunpb.WebhookDeliveryStatus_WEBHOOK_DELIVERY_STATUS_SKIPPED
	}
	return out
}

func attemptProto(a ledger.Attempt) *bosunpb.WebhookDeliveryAttempt {
	return &bosunpb.WebhookDeliveryAttempt{
		Id:              a.ID,
		AttemptNumber:   int32(a.AttemptNumber),
		StatusCode:      int32(a.StatusCode),
		ErrorClass:      a.ErrorClass,
		LatencyMs:       int32(a.LatencyMS),
		ResponseExcerpt: a.ResponseExcerpt,
		AttemptedAt:     timestamppb.New(a.AttemptedAt),
	}
}
