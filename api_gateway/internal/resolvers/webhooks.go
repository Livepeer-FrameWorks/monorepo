package resolvers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/demo"
	"frameworks/api_gateway/internal/loaders"
	"frameworks/api_gateway/internal/middleware"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/authz"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pagination"
	bosunpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/bosun"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	webhookDeliveriesDefaultPageSize = 50
	webhookDeliveriesMaxPageSize     = 200
	// webhookTestRetryAfterSeconds is Bosun's per-endpoint test interval.
	webhookTestRetryAfterSeconds = 10
	// webhookAllEventTypes subscribes an endpoint to every public type.
	webhookAllEventTypes = "*"
)

var errWebhooksUnavailable = errors.New("webhooks are unavailable")

// webhookReadCtx authorizes a webhook read and returns the context the Bosun
// call runs with. Bosun takes the tenant from the call identity only, so the
// resolved tenant is set on the context the client forwards as x-tenant-id.
func (r *Resolver) webhookReadCtx(ctx context.Context) (context.Context, error) {
	if err := middleware.RequirePermission(ctx, "developer:read"); err != nil {
		return nil, err
	}
	tenantID := tenantIDFromContext(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("webhook access requires a tenant")
	}
	if r.Clients == nil || r.Clients.Bosun == nil {
		return nil, errWebhooksUnavailable
	}
	return context.WithValue(ctx, ctxkeys.KeyTenantID, tenantID), nil
}

// webhookWriteCtx authorizes a webhook change: API tokens need
// developer:write, and the caller must be an owner or admin of the tenant. An
// authorization failure is returned as an AuthError union member.
func (r *Resolver) webhookWriteCtx(ctx context.Context) (context.Context, *model.AuthError, error) {
	tenantID := tenantIDFromContext(ctx)
	if denied := webhookWriteDenial(ctx, tenantID); denied != nil {
		return nil, denied, nil
	}
	if r.Clients == nil || r.Clients.Bosun == nil {
		return nil, nil, errWebhooksUnavailable
	}
	return context.WithValue(ctx, ctxkeys.KeyTenantID, tenantID), nil, nil
}

func webhookWriteDenial(ctx context.Context, tenantID string) *model.AuthError {
	if err := middleware.RequireTenantAction(ctx, "developer:write", authz.ActionManageWebhooks, tenantID); err != nil {
		return &model.AuthError{Message: err.Error()}
	}
	if tenantID == "" {
		return &model.AuthError{Message: "webhook changes require a tenant"}
	}
	return nil
}

// WebhookEventTypes returns the public event types an endpoint can subscribe
// to, sorted, followed by "*".
func WebhookEventTypes() []string {
	out := []string{}
	for _, spec := range events.Specs() {
		if spec.Public() {
			out = append(out, spec.Type)
		}
	}
	return append(out, webhookAllEventTypes)
}

// DoWebhookEventTypes returns WebhookEventTypes for an authenticated caller.
func (r *Resolver) DoWebhookEventTypes(ctx context.Context) ([]string, error) {
	if middleware.IsDemoMode(ctx) {
		return WebhookEventTypes(), nil
	}
	if err := middleware.RequirePermission(ctx, "developer:read"); err != nil {
		return nil, err
	}
	return WebhookEventTypes(), nil
}

// DoWebhookEndpointsConnection lists the tenant's endpoints. A tenant has at
// most 10, so Bosun returns them in one page and the page argument only caps
// the count returned.
func (r *Resolver) DoWebhookEndpointsConnection(ctx context.Context, page *model.ConnectionInput) (*model.WebhookEndpointsConnection, error) {
	if middleware.IsDemoMode(ctx) {
		return webhookEndpointsConnection(demo.GenerateWebhookEndpoints(), page), nil
	}
	callCtx, err := r.webhookReadCtx(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := r.Clients.Bosun.ListWebhookEndpoints(callCtx, &bosunpb.ListWebhookEndpointsRequest{})
	if err != nil {
		return nil, fmt.Errorf("list webhook endpoints: %w", err)
	}
	return webhookEndpointsConnection(resp.GetEndpoints(), page), nil
}

// DoWebhookEndpoint returns one endpoint, or nil when the tenant has no
// endpoint with that ID.
func (r *Resolver) DoWebhookEndpoint(ctx context.Context, id string) (*model.WebhookEndpoint, error) {
	id = strings.TrimSpace(id)
	if middleware.IsDemoMode(ctx) {
		for _, ep := range demo.GenerateWebhookEndpoints() {
			if ep.GetId() == id {
				return webhookEndpointFromProto(ep), nil
			}
		}
		return nil, nil
	}
	callCtx, err := r.webhookReadCtx(ctx)
	if err != nil {
		return nil, err
	}
	ep, err := r.Clients.Bosun.GetWebhookEndpoint(callCtx, id)
	if err != nil {
		if isWebhookNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get webhook endpoint: %w", err)
	}
	return webhookEndpointFromProto(ep), nil
}

// WebhookDeliveryFilter holds the webhookDeliveriesConnection filters.
type WebhookDeliveryFilter struct {
	EndpointID    *string
	Statuses      []model.WebhookDeliveryStatus
	EventType     *string
	EventID       *string
	CreatedAfter  *time.Time
	CreatedBefore *time.Time
}

// DoWebhookDeliveriesConnection lists deliveries newest first. The page's
// delivery IDs are registered with the request's attempt loader, so the
// attemptHistory fields of all nodes load in one Bosun call.
func (r *Resolver) DoWebhookDeliveriesConnection(ctx context.Context, filter WebhookDeliveryFilter, page *model.ConnectionInput) (*model.WebhookDeliveriesConnection, error) {
	pageReq, err := webhookDeliveryPagination(page)
	if err != nil {
		return nil, err
	}
	req := &bosunpb.ListWebhookDeliveriesRequest{
		EndpointId: strings.TrimSpace(deref(filter.EndpointID)),
		EventType:  strings.TrimSpace(deref(filter.EventType)),
		EventId:    strings.TrimSpace(deref(filter.EventID)),
		Pagination: pageReq,
	}
	for _, st := range filter.Statuses {
		req.Statuses = append(req.Statuses, webhookDeliveryStatusToProto(st))
	}
	if filter.CreatedAfter != nil {
		req.CreatedAfter = timestamppb.New(*filter.CreatedAfter)
	}
	if filter.CreatedBefore != nil {
		req.CreatedBefore = timestamppb.New(*filter.CreatedBefore)
	}
	if middleware.IsDemoMode(ctx) {
		resp := demoWebhookDeliveries(req)
		conn := webhookDeliveriesConnection(resp)
		for i, d := range resp.GetDeliveries() {
			conn.Nodes[i].AttemptHistory = webhookDeliveryWithAttempts(d, demo.GenerateWebhookDeliveryAttempts(d)).AttemptHistory
		}
		return conn, nil
	}
	callCtx, err := r.webhookReadCtx(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := r.Clients.Bosun.ListWebhookDeliveries(callCtx, req)
	if err != nil {
		return nil, fmt.Errorf("list webhook deliveries: %w", err)
	}
	conn := webhookDeliveriesConnection(resp)
	if l := loaders.FromContext(ctx); l != nil && l.WebhookAttempts != nil {
		for _, node := range conn.Nodes {
			l.WebhookAttempts.Register(node.ID)
		}
	}
	return conn, nil
}

// DoWebhookDeliveryAttemptHistory resolves WebhookDelivery.attemptHistory.
// Deliveries mapped with their attempts return them as they are; the others
// load through the request's batching loader, or one call without it.
func (r *Resolver) DoWebhookDeliveryAttemptHistory(ctx context.Context, obj *model.WebhookDelivery) ([]*model.WebhookDeliveryAttempt, error) {
	if obj == nil {
		return []*model.WebhookDeliveryAttempt{}, nil
	}
	if obj.AttemptHistory != nil {
		return obj.AttemptHistory, nil
	}
	if middleware.IsDemoMode(ctx) {
		for _, d := range demo.GenerateWebhookDeliveries() {
			if d.GetId() == obj.ID {
				return webhookDeliveryWithAttempts(d, demo.GenerateWebhookDeliveryAttempts(d)).AttemptHistory, nil
			}
		}
		return []*model.WebhookDeliveryAttempt{}, nil
	}
	callCtx, err := r.webhookReadCtx(ctx)
	if err != nil {
		return nil, err
	}
	var attempts []*bosunpb.WebhookDeliveryAttempt
	if l := loaders.FromContext(ctx); l != nil && l.WebhookAttempts != nil {
		attempts, err = l.WebhookAttempts.Load(callCtx, obj.ID)
	} else {
		var resp *bosunpb.ListAttemptsForDeliveriesResponse
		resp, err = r.Clients.Bosun.ListAttemptsForDeliveries(callCtx, []string{obj.ID})
		for _, d := range resp.GetDeliveries() {
			attempts = d.GetAttempts()
		}
	}
	if err != nil {
		return nil, fmt.Errorf("list webhook delivery attempts: %w", err)
	}
	out := make([]*model.WebhookDeliveryAttempt, 0, len(attempts))
	for _, a := range attempts {
		if converted := webhookAttemptFromProto(a); converted != nil {
			out = append(out, converted)
		}
	}
	return out, nil
}

// DoWebhookDelivery returns one delivery with its attempt history, or nil when
// the tenant has no delivery with that ID.
func (r *Resolver) DoWebhookDelivery(ctx context.Context, id string) (*model.WebhookDelivery, error) {
	id = strings.TrimSpace(id)
	if middleware.IsDemoMode(ctx) {
		for _, d := range demo.GenerateWebhookDeliveries() {
			if d.GetId() == id {
				return webhookDeliveryWithAttempts(d, demo.GenerateWebhookDeliveryAttempts(d)), nil
			}
		}
		return nil, nil
	}
	callCtx, err := r.webhookReadCtx(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := r.Clients.Bosun.GetWebhookDelivery(callCtx, id)
	if err != nil {
		if isWebhookNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get webhook delivery: %w", err)
	}
	return webhookDeliveryWithAttempts(resp.GetDelivery(), resp.GetAttempts()), nil
}

// DoCreateWebhookEndpoint creates an endpoint. The result is the only read of
// the new signing secret.
func (r *Resolver) DoCreateWebhookEndpoint(ctx context.Context, input model.CreateWebhookEndpointInput) (model.CreateWebhookEndpointResult, error) {
	if middleware.IsDemoMode(ctx) {
		return nil, errDemoUnavailable("Webhook changes")
	}
	callCtx, authErr, err := r.webhookWriteCtx(ctx)
	if authErr != nil || err != nil {
		return webhookAuthResult[model.CreateWebhookEndpointResult](authErr, err)
	}
	resp, err := r.Clients.Bosun.CreateWebhookEndpoint(callCtx, &bosunpb.CreateWebhookEndpointRequest{
		Url:         strings.TrimSpace(input.URL),
		Description: deref(input.Description),
		EventTypes:  input.EventTypes,
		ApiVersion:  strings.TrimSpace(deref(input.APIVersion)),
	})
	if err != nil {
		return webhookMutationError[model.CreateWebhookEndpointResult](err, "WebhookEndpoint", "", "create webhook endpoint")
	}
	return webhookEndpointSecretFromProto(resp), nil
}

// DoUpdateWebhookEndpoint changes the given fields of an endpoint.
func (r *Resolver) DoUpdateWebhookEndpoint(ctx context.Context, id string, input model.UpdateWebhookEndpointInput) (model.UpdateWebhookEndpointResult, error) {
	if middleware.IsDemoMode(ctx) {
		return nil, errDemoUnavailable("Webhook changes")
	}
	callCtx, authErr, err := r.webhookWriteCtx(ctx)
	if authErr != nil || err != nil {
		return webhookAuthResult[model.UpdateWebhookEndpointResult](authErr, err)
	}
	id = strings.TrimSpace(id)
	req := &bosunpb.UpdateWebhookEndpointRequest{EndpointId: id, Description: input.Description}
	if input.URL != nil {
		url := strings.TrimSpace(*input.URL)
		req.Url = &url
	}
	if input.EventTypes != nil {
		req.EventTypes = input.EventTypes
		req.SetEventTypes = true
	}
	ep, err := r.Clients.Bosun.UpdateWebhookEndpoint(callCtx, req)
	if err != nil {
		return webhookMutationError[model.UpdateWebhookEndpointResult](err, "WebhookEndpoint", id, "update webhook endpoint")
	}
	return webhookEndpointFromProto(ep), nil
}

// DoDeleteWebhookEndpoint deletes an endpoint with its secrets and history.
func (r *Resolver) DoDeleteWebhookEndpoint(ctx context.Context, id string) (model.DeleteWebhookEndpointResult, error) {
	if middleware.IsDemoMode(ctx) {
		return nil, errDemoUnavailable("Webhook changes")
	}
	callCtx, authErr, err := r.webhookWriteCtx(ctx)
	if authErr != nil || err != nil {
		return webhookAuthResult[model.DeleteWebhookEndpointResult](authErr, err)
	}
	id = strings.TrimSpace(id)
	resp, err := r.Clients.Bosun.DeleteWebhookEndpoint(callCtx, id)
	if err != nil {
		return webhookMutationError[model.DeleteWebhookEndpointResult](err, "WebhookEndpoint", id, "delete webhook endpoint")
	}
	deleted := resp.GetEndpointId()
	if deleted == "" {
		deleted = id
	}
	return &model.DeleteSuccess{Success: true, DeletedID: deleted}, nil
}

// DoEnableWebhookEndpoint re-enables an endpoint.
func (r *Resolver) DoEnableWebhookEndpoint(ctx context.Context, id string) (model.WebhookEndpointResult, error) {
	return r.webhookEndpointStateChange(ctx, id, "enable webhook endpoint", func(callCtx context.Context, id string) (*bosunpb.WebhookEndpoint, error) {
		return r.Clients.Bosun.EnableWebhookEndpoint(callCtx, id)
	})
}

// DoDisableWebhookEndpoint disables an endpoint.
func (r *Resolver) DoDisableWebhookEndpoint(ctx context.Context, id string) (model.WebhookEndpointResult, error) {
	return r.webhookEndpointStateChange(ctx, id, "disable webhook endpoint", func(callCtx context.Context, id string) (*bosunpb.WebhookEndpoint, error) {
		return r.Clients.Bosun.DisableWebhookEndpoint(callCtx, id)
	})
}

func (r *Resolver) webhookEndpointStateChange(ctx context.Context, id, op string, call func(context.Context, string) (*bosunpb.WebhookEndpoint, error)) (model.WebhookEndpointResult, error) {
	if middleware.IsDemoMode(ctx) {
		return nil, errDemoUnavailable("Webhook changes")
	}
	callCtx, authErr, err := r.webhookWriteCtx(ctx)
	if authErr != nil || err != nil {
		return webhookAuthResult[model.WebhookEndpointResult](authErr, err)
	}
	id = strings.TrimSpace(id)
	ep, err := call(callCtx, id)
	if err != nil {
		return webhookMutationError[model.WebhookEndpointResult](err, "WebhookEndpoint", id, op)
	}
	return webhookEndpointFromProto(ep), nil
}

// DoRotateWebhookEndpointSecret replaces the signing secret. The result is the
// only read of the new secret.
func (r *Resolver) DoRotateWebhookEndpointSecret(ctx context.Context, id string, revokePrevious *bool) (model.RotateWebhookEndpointSecretResult, error) {
	if middleware.IsDemoMode(ctx) {
		return nil, errDemoUnavailable("Webhook changes")
	}
	callCtx, authErr, err := r.webhookWriteCtx(ctx)
	if authErr != nil || err != nil {
		return webhookAuthResult[model.RotateWebhookEndpointSecretResult](authErr, err)
	}
	id = strings.TrimSpace(id)
	resp, err := r.Clients.Bosun.RotateWebhookEndpointSecret(callCtx, id, revokePrevious != nil && *revokePrevious)
	if err != nil {
		return webhookMutationError[model.RotateWebhookEndpointSecretResult](err, "WebhookEndpoint", id, "rotate webhook secret")
	}
	return webhookEndpointSecretFromProto(resp), nil
}

// DoTestWebhookEndpoint sends a signed webhook.test event and returns the
// attempt. A second test within Bosun's interval is a RateLimitError.
func (r *Resolver) DoTestWebhookEndpoint(ctx context.Context, id string) (model.TestWebhookEndpointResult, error) {
	if middleware.IsDemoMode(ctx) {
		return nil, errDemoUnavailable("Webhook changes")
	}
	callCtx, authErr, err := r.webhookWriteCtx(ctx)
	if authErr != nil || err != nil {
		return webhookAuthResult[model.TestWebhookEndpointResult](authErr, err)
	}
	id = strings.TrimSpace(id)
	resp, err := r.Clients.Bosun.TestWebhookEndpoint(callCtx, id)
	if err != nil {
		return webhookMutationError[model.TestWebhookEndpointResult](err, "WebhookEndpoint", id, "test webhook endpoint")
	}
	return &model.WebhookTestResult{
		Delivery: webhookDeliveryWithAttempts(resp.GetDelivery(), []*bosunpb.WebhookDeliveryAttempt{resp.GetAttempt()}),
		Attempt:  webhookAttemptFromProto(resp.GetAttempt()),
	}, nil
}

// DoReplayWebhookDelivery sends a finished delivery again under the same ID.
func (r *Resolver) DoReplayWebhookDelivery(ctx context.Context, id string) (model.ReplayWebhookDeliveryResult, error) {
	if middleware.IsDemoMode(ctx) {
		return nil, errDemoUnavailable("Webhook changes")
	}
	callCtx, authErr, err := r.webhookWriteCtx(ctx)
	if authErr != nil || err != nil {
		return webhookAuthResult[model.ReplayWebhookDeliveryResult](authErr, err)
	}
	id = strings.TrimSpace(id)
	d, err := r.Clients.Bosun.ReplayWebhookDelivery(callCtx, id)
	if err != nil {
		return webhookMutationError[model.ReplayWebhookDeliveryResult](err, "WebhookDelivery", id, "replay webhook delivery")
	}
	return webhookDeliveryFromProto(d), nil
}

// DoReplayWebhookDeliveries replays one endpoint's failed and skipped
// deliveries created in [createdAfter, createdBefore).
func (r *Resolver) DoReplayWebhookDeliveries(ctx context.Context, endpointID string, createdAfter, createdBefore time.Time) (model.ReplayWebhookDeliveriesResult, error) {
	if middleware.IsDemoMode(ctx) {
		return nil, errDemoUnavailable("Webhook changes")
	}
	if !createdBefore.After(createdAfter) {
		return &model.ValidationError{Message: "createdBefore must be after createdAfter", Field: strPtr("createdBefore")}, nil
	}
	callCtx, authErr, err := r.webhookWriteCtx(ctx)
	if authErr != nil || err != nil {
		return webhookAuthResult[model.ReplayWebhookDeliveriesResult](authErr, err)
	}
	endpointID = strings.TrimSpace(endpointID)
	resp, err := r.Clients.Bosun.ReplayWebhookDeliveries(callCtx, &bosunpb.ReplayWebhookDeliveriesRequest{
		EndpointId:    endpointID,
		CreatedAfter:  timestamppb.New(createdAfter),
		CreatedBefore: timestamppb.New(createdBefore),
	})
	if err != nil {
		return webhookMutationError[model.ReplayWebhookDeliveriesResult](err, "WebhookEndpoint", endpointID, "replay webhook deliveries")
	}
	return &model.WebhookReplayResult{ReplayedCount: int(resp.GetReplayedCount()), HasMore: resp.GetHasMore()}, nil
}

// webhookAuthResult returns an authorization failure as the union's AuthError
// member, or passes a transport error through.
func webhookAuthResult[T any](authErr *model.AuthError, err error) (T, error) {
	var zero T
	if err != nil {
		return zero, err
	}
	if result, ok := any(authErr).(T); ok {
		return result, nil
	}
	return zero, errors.New(authErr.Message)
}

// webhookMutationError maps a Bosun error to the member of union T that
// represents it, or returns the error when T has no such member.
func webhookMutationError[T any](err error, resourceType, resourceID, op string) (T, error) {
	var zero T
	if mapped := mapWebhookError(err, resourceType, resourceID); mapped != nil {
		if result, ok := mapped.(T); ok {
			return result, nil
		}
	}
	return zero, fmt.Errorf("%s: %w", op, err)
}

// mapWebhookError maps Bosun's status codes to error union members:
// InvalidArgument and FailedPrecondition (including the endpoint limit and a
// replay of a pending or test delivery) are ValidationErrors, NotFound a
// NotFoundError, ResourceExhausted (the test interval) a RateLimitError, and
// PermissionDenied an AuthError. Other codes are not mapped.
func mapWebhookError(err error, resourceType, resourceID string) any {
	st, ok := status.FromError(err)
	if !ok {
		return nil
	}
	switch st.Code() {
	case codes.InvalidArgument:
		v := &model.ValidationError{Message: st.Message(), Code: strPtr("INVALID_ARGUMENT")}
		if field := webhookFieldFromMessage(st.Message()); field != "" {
			v.Field = strPtr(field)
		}
		return v
	case codes.FailedPrecondition:
		return &model.ValidationError{Message: st.Message(), Code: strPtr("FAILED_PRECONDITION")}
	case codes.NotFound:
		return &model.NotFoundError{
			Message:      fmt.Sprintf("%s not found", resourceType),
			Code:         strPtr("NOT_FOUND"),
			ResourceType: resourceType,
			ResourceID:   resourceID,
		}
	case codes.ResourceExhausted:
		retryAfter := webhookTestRetryAfterSeconds
		return &model.RateLimitError{Message: st.Message(), Code: strPtr("RATE_LIMITED"), RetryAfter: &retryAfter}
	case codes.PermissionDenied:
		return &model.AuthError{Message: st.Message(), Code: strPtr("PERMISSION_DENIED")}
	default:
		return nil
	}
}

func webhookFieldFromMessage(msg string) string {
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "url"), strings.Contains(lower, "host"), strings.Contains(lower, "address"):
		return "url"
	case strings.Contains(lower, "event type"):
		return "eventTypes"
	case strings.Contains(lower, "description"):
		return "description"
	case strings.Contains(lower, "api version"):
		return "apiVersion"
	default:
		return ""
	}
}

func isWebhookNotFound(err error) bool {
	switch status.Code(err) {
	case codes.NotFound, codes.InvalidArgument:
		return true
	default:
		return false
	}
}

func webhookDeliveryPagination(page *model.ConnectionInput) (*commonpb.CursorPaginationRequest, error) {
	req := &commonpb.CursorPaginationRequest{First: webhookDeliveriesDefaultPageSize}
	if page == nil {
		return req, nil
	}
	if page.Last != nil || page.Before != nil {
		return nil, fmt.Errorf("webhook deliveries support forward pagination only")
	}
	if page.First != nil && *page.First > 0 {
		req.First = int32(min(*page.First, webhookDeliveriesMaxPageSize))
	}
	if page.After != nil && *page.After != "" {
		after := *page.After
		req.After = &after
	}
	return req, nil
}

func webhookTime(ts *timestamppb.Timestamp) *time.Time {
	if ts == nil {
		return nil
	}
	t := ts.AsTime()
	return &t
}

func webhookRequiredTime(ts *timestamppb.Timestamp) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.AsTime()
}

func webhookEndpointFromProto(ep *bosunpb.WebhookEndpoint) *model.WebhookEndpoint {
	if ep == nil {
		return nil
	}
	out := &model.WebhookEndpoint{
		ID:                      ep.GetId(),
		URL:                     ep.GetUrl(),
		Description:             ep.GetDescription(),
		EventTypes:              append([]string{}, ep.GetEventTypes()...),
		APIVersion:              ep.GetApiVersion(),
		Status:                  model.WebhookEndpointStatusEnabled,
		DisabledAt:              webhookTime(ep.GetDisabledAt()),
		ConsecutiveFailures:     int(ep.GetConsecutiveFailures()),
		FailingSince:            webhookTime(ep.GetFailingSince()),
		LastSuccessAt:           webhookTime(ep.GetLastSuccessAt()),
		LastFailureAt:           webhookTime(ep.GetLastFailureAt()),
		PreviousSecretExpiresAt: webhookTime(ep.GetPreviousSecretExpiresAt()),
		CreatedAt:               webhookRequiredTime(ep.GetCreatedAt()),
		UpdatedAt:               webhookRequiredTime(ep.GetUpdatedAt()),
	}
	if ep.GetStatus() == bosunpb.WebhookEndpointStatus_WEBHOOK_ENDPOINT_STATUS_DISABLED {
		out.Status = model.WebhookEndpointStatusDisabled
	}
	switch ep.GetDisabledReason() {
	case bosunpb.WebhookEndpointDisabledReason_WEBHOOK_ENDPOINT_DISABLED_REASON_USER:
		reason := model.WebhookEndpointDisabledReasonUser
		out.DisabledReason = &reason
	case bosunpb.WebhookEndpointDisabledReason_WEBHOOK_ENDPOINT_DISABLED_REASON_FAILING:
		reason := model.WebhookEndpointDisabledReasonFailing
		out.DisabledReason = &reason
	}
	return out
}

func webhookEndpointSecretFromProto(resp *bosunpb.WebhookEndpointWithSecret) *model.WebhookEndpointSecret {
	return &model.WebhookEndpointSecret{
		Endpoint: webhookEndpointFromProto(resp.GetEndpoint()),
		Secret:   resp.GetSecret(),
	}
}

func webhookEndpointsConnection(endpoints []*bosunpb.WebhookEndpoint, page *model.ConnectionInput) *model.WebhookEndpointsConnection {
	nodes := make([]*model.WebhookEndpoint, 0, len(endpoints))
	for _, ep := range endpoints {
		if converted := webhookEndpointFromProto(ep); converted != nil {
			nodes = append(nodes, converted)
		}
	}
	total := len(nodes)
	hasNext := false
	if page != nil && page.First != nil && *page.First > 0 && *page.First < len(nodes) {
		nodes = nodes[:*page.First]
		hasNext = true
	}
	edges := make([]*model.WebhookEndpointEdge, 0, len(nodes))
	for _, node := range nodes {
		edges = append(edges, &model.WebhookEndpointEdge{Cursor: pagination.EncodeCursor(node.CreatedAt, node.ID), Node: node})
	}
	info := &model.PageInfo{HasNextPage: hasNext}
	if n := len(edges); n > 0 {
		info.StartCursor, info.EndCursor = &edges[0].Cursor, &edges[n-1].Cursor
	}
	return &model.WebhookEndpointsConnection{Edges: edges, Nodes: nodes, PageInfo: info, TotalCount: total}
}

func webhookDeliveryStatusToProto(st model.WebhookDeliveryStatus) bosunpb.WebhookDeliveryStatus {
	switch st {
	case model.WebhookDeliveryStatusPending:
		return bosunpb.WebhookDeliveryStatus_WEBHOOK_DELIVERY_STATUS_PENDING
	case model.WebhookDeliveryStatusSucceeded:
		return bosunpb.WebhookDeliveryStatus_WEBHOOK_DELIVERY_STATUS_SUCCEEDED
	case model.WebhookDeliveryStatusFailed:
		return bosunpb.WebhookDeliveryStatus_WEBHOOK_DELIVERY_STATUS_FAILED
	case model.WebhookDeliveryStatusSkipped:
		return bosunpb.WebhookDeliveryStatus_WEBHOOK_DELIVERY_STATUS_SKIPPED
	default:
		return bosunpb.WebhookDeliveryStatus_WEBHOOK_DELIVERY_STATUS_UNSPECIFIED
	}
}

func webhookDeliveryStatusFromProto(st bosunpb.WebhookDeliveryStatus) model.WebhookDeliveryStatus {
	switch st {
	case bosunpb.WebhookDeliveryStatus_WEBHOOK_DELIVERY_STATUS_SUCCEEDED:
		return model.WebhookDeliveryStatusSucceeded
	case bosunpb.WebhookDeliveryStatus_WEBHOOK_DELIVERY_STATUS_FAILED:
		return model.WebhookDeliveryStatusFailed
	case bosunpb.WebhookDeliveryStatus_WEBHOOK_DELIVERY_STATUS_SKIPPED:
		return model.WebhookDeliveryStatusSkipped
	default:
		return model.WebhookDeliveryStatusPending
	}
}

// webhookDeliveryFromProto maps a delivery without attempt history. A nil
// AttemptHistory means not loaded; the attemptHistory field resolver loads it.
func webhookDeliveryFromProto(d *bosunpb.WebhookDelivery) *model.WebhookDelivery {
	if d == nil {
		return nil
	}
	out := &model.WebhookDelivery{
		ID:             d.GetId(),
		EndpointID:     d.GetEndpointId(),
		EventID:        optionalString(d.GetEventId()),
		EventType:      d.GetEventType(),
		Kind:           model.WebhookDeliveryKindEvent,
		Status:         webhookDeliveryStatusFromProto(d.GetStatus()),
		Attempts:       int(d.GetAttempts()),
		NextAttemptAt:  webhookTime(d.GetNextAttemptAt()),
		LastStatusCode: int(d.GetLastStatusCode()),
		LastErrorClass: d.GetLastErrorClass(),
		DeliveredAt:    webhookTime(d.GetDeliveredAt()),
		ReplayCount:    int(d.GetReplayCount()),
		LastReplayedAt: webhookTime(d.GetLastReplayedAt()),
		CreatedAt:      webhookRequiredTime(d.GetCreatedAt()),
		UpdatedAt:      webhookRequiredTime(d.GetUpdatedAt()),
	}
	if d.GetKind() == bosunpb.WebhookDeliveryKind_WEBHOOK_DELIVERY_KIND_TEST {
		out.Kind = model.WebhookDeliveryKindTest
	}
	return out
}

func webhookDeliveryWithAttempts(d *bosunpb.WebhookDelivery, attempts []*bosunpb.WebhookDeliveryAttempt) *model.WebhookDelivery {
	out := webhookDeliveryFromProto(d)
	if out == nil {
		return nil
	}
	out.AttemptHistory = make([]*model.WebhookDeliveryAttempt, 0, len(attempts))
	for _, a := range attempts {
		if converted := webhookAttemptFromProto(a); converted != nil {
			out.AttemptHistory = append(out.AttemptHistory, converted)
		}
	}
	return out
}

func webhookAttemptFromProto(a *bosunpb.WebhookDeliveryAttempt) *model.WebhookDeliveryAttempt {
	if a == nil {
		return nil
	}
	return &model.WebhookDeliveryAttempt{
		ID:              a.GetId(),
		AttemptNumber:   int(a.GetAttemptNumber()),
		StatusCode:      int(a.GetStatusCode()),
		ErrorClass:      a.GetErrorClass(),
		LatencyMs:       int(a.GetLatencyMs()),
		ResponseExcerpt: a.GetResponseExcerpt(),
		AttemptedAt:     webhookRequiredTime(a.GetAttemptedAt()),
	}
}

func webhookDeliveriesConnection(resp *bosunpb.ListWebhookDeliveriesResponse) *model.WebhookDeliveriesConnection {
	nodes := make([]*model.WebhookDelivery, 0, len(resp.GetDeliveries()))
	for _, d := range resp.GetDeliveries() {
		if converted := webhookDeliveryFromProto(d); converted != nil {
			nodes = append(nodes, converted)
		}
	}
	// Edge cursors use Bosun's (created_at, id) keyset encoding, so any edge's
	// cursor resumes the list after that delivery.
	edges := make([]*model.WebhookDeliveryEdge, 0, len(nodes))
	for _, node := range nodes {
		edges = append(edges, &model.WebhookDeliveryEdge{Cursor: pagination.EncodeCursor(node.CreatedAt, node.ID), Node: node})
	}
	pag := resp.GetPagination()
	info := &model.PageInfo{HasNextPage: pag.GetHasNextPage(), HasPreviousPage: pag.GetHasPreviousPage()}
	if n := len(edges); n > 0 {
		info.StartCursor, info.EndCursor = &edges[0].Cursor, &edges[n-1].Cursor
	}
	return &model.WebhookDeliveriesConnection{Edges: edges, Nodes: nodes, PageInfo: info, TotalCount: int(pag.GetTotalCount())}
}

// demoWebhookDeliveries applies the request's filters to the demo deliveries.
func demoWebhookDeliveries(req *bosunpb.ListWebhookDeliveriesRequest) *bosunpb.ListWebhookDeliveriesResponse {
	statuses := map[bosunpb.WebhookDeliveryStatus]bool{}
	for _, st := range req.GetStatuses() {
		statuses[st] = true
	}
	out := &bosunpb.ListWebhookDeliveriesResponse{}
	for _, d := range demo.GenerateWebhookDeliveries() {
		switch {
		case req.GetEndpointId() != "" && d.GetEndpointId() != req.GetEndpointId():
			continue
		case len(statuses) > 0 && !statuses[d.GetStatus()]:
			continue
		case req.GetEventType() != "" && d.GetEventType() != req.GetEventType():
			continue
		case req.GetEventId() != "" && d.GetEventId() != req.GetEventId():
			continue
		case req.GetCreatedAfter() != nil && d.GetCreatedAt().AsTime().Before(req.GetCreatedAfter().AsTime()):
			continue
		case req.GetCreatedBefore() != nil && !d.GetCreatedAt().AsTime().Before(req.GetCreatedBefore().AsTime()):
			continue
		}
		out.Deliveries = append(out.Deliveries, d)
	}
	out.Pagination = &commonpb.CursorPaginationResponse{TotalCount: int32(len(out.Deliveries))}
	return out
}
