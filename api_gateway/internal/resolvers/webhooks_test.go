package resolvers

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients/clientstest"
	"frameworks/api_gateway/internal/demo"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	bosunpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/bosun"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	webhookTestTenant   = "7a1d0000-0000-4000-8000-0000000000b1"
	webhookTestEndpoint = "e0000000-0000-4000-8000-000000000001"
	webhookTestDelivery = "d0000000-0000-4000-8000-000000000001"
	webhookTestSecret   = "whsec_dGVzdC1zZWNyZXQtdGhhdC1tdXN0LW5vdC1sZWFr"
)

func webhookCtx(role string) context.Context {
	ctx := clientstest.AuthedCtx(webhookTestTenant)
	ctx = context.WithValue(ctx, ctxkeys.KeyRole, role)
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "user-1")
	return context.WithValue(ctx, ctxkeys.KeyJWTToken, "user-jwt")
}

func webhookAPITokenCtx(permissions ...string) context.Context {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "api_token")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, webhookTestTenant)
	ctx = context.WithValue(ctx, ctxkeys.KeyRole, "owner")
	return context.WithValue(ctx, ctxkeys.KeyPermissions, permissions)
}

func testWebhookEndpointProto() *bosunpb.WebhookEndpoint {
	return &bosunpb.WebhookEndpoint{
		Id:             webhookTestEndpoint,
		Url:            "https://hooks.example.com/in",
		EventTypes:     []string{"stream.live"},
		ApiVersion:     "v1",
		Status:         bosunpb.WebhookEndpointStatus_WEBHOOK_ENDPOINT_STATUS_DISABLED,
		DisabledReason: bosunpb.WebhookEndpointDisabledReason_WEBHOOK_ENDPOINT_DISABLED_REASON_FAILING,
		CreatedAt:      timestamppb.Now(),
		UpdatedAt:      timestamppb.Now(),
	}
}

func testWebhookDeliveryProto() *bosunpb.WebhookDelivery {
	return &bosunpb.WebhookDelivery{
		Id:         webhookTestDelivery,
		EndpointId: webhookTestEndpoint,
		EventId:    "0192f000-0000-7000-8000-000000000001",
		EventType:  "stream.live",
		Kind:       bosunpb.WebhookDeliveryKind_WEBHOOK_DELIVERY_KIND_EVENT,
		Status:     bosunpb.WebhookDeliveryStatus_WEBHOOK_DELIVERY_STATUS_FAILED,
		Attempts:   2,
		CreatedAt:  timestamppb.Now(),
		UpdatedAt:  timestamppb.Now(),
	}
}

// Each Bosun status code a webhook mutation can return maps to its union
// member: the endpoint limit and replay preconditions are ValidationErrors,
// the test interval a RateLimitError, a missing or foreign ID a NotFoundError.
// A transport failure stays a GraphQL error.
func TestWebhookMutationsMapBosunErrorsToUnionMembers(t *testing.T) {
	limit := status.Error(codes.FailedPrecondition, "a tenant can have at most 10 webhook endpoints")
	badURL := status.Error(codes.InvalidArgument, "url must use https")
	notFound := status.Error(codes.NotFound, "not found")
	rateLimited := status.Error(codes.ResourceExhausted, "the endpoint was tested less than 10 seconds ago")
	pending := status.Error(codes.FailedPrecondition, "a pending delivery cannot be replayed")
	down := status.Error(codes.Unavailable, "connection refused")

	fake := &clientstest.FakeBosun{}
	r := platformResolverWith(clientstest.WithBosun(fake))
	ctx := webhookCtx("owner")
	input := model.CreateWebhookEndpointInput{URL: "https://hooks.example.com", EventTypes: []string{"*"}}

	fake.CreateWebhookEndpointFn = func(context.Context, *bosunpb.CreateWebhookEndpointRequest) (*bosunpb.WebhookEndpointWithSecret, error) {
		return nil, limit
	}
	got, err := r.DoCreateWebhookEndpoint(ctx, input)
	if v, ok := got.(*model.ValidationError); err != nil || !ok || !strings.Contains(v.Message, "at most 10") {
		t.Fatalf("endpoint limit = (%#v, %v), want ValidationError", got, err)
	}

	fake.CreateWebhookEndpointFn = func(context.Context, *bosunpb.CreateWebhookEndpointRequest) (*bosunpb.WebhookEndpointWithSecret, error) {
		return nil, badURL
	}
	got, err = r.DoCreateWebhookEndpoint(ctx, input)
	if v, ok := got.(*model.ValidationError); err != nil || !ok || v.Field == nil || *v.Field != "url" {
		t.Fatalf("invalid url = (%#v, %v), want ValidationError on url", got, err)
	}

	fake.CreateWebhookEndpointFn = func(context.Context, *bosunpb.CreateWebhookEndpointRequest) (*bosunpb.WebhookEndpointWithSecret, error) {
		return nil, down
	}
	if got, err = r.DoCreateWebhookEndpoint(ctx, input); err == nil || got != nil {
		t.Fatalf("transport failure = (%#v, %v), want a GraphQL error", got, err)
	}

	fake.TestWebhookEndpointFn = func(context.Context, string) (*bosunpb.TestWebhookEndpointResponse, error) {
		return nil, rateLimited
	}
	testResult, err := r.DoTestWebhookEndpoint(ctx, webhookTestEndpoint)
	if v, ok := testResult.(*model.RateLimitError); err != nil || !ok || v.RetryAfter == nil || *v.RetryAfter != 10 {
		t.Fatalf("test rate limit = (%#v, %v), want RateLimitError retryAfter 10", testResult, err)
	}

	fake.EnableWebhookEndpointFn = func(context.Context, string) (*bosunpb.WebhookEndpoint, error) { return nil, notFound }
	enabled, err := r.DoEnableWebhookEndpoint(ctx, " "+webhookTestEndpoint+" ")
	if v, ok := enabled.(*model.NotFoundError); err != nil || !ok || v.ResourceID != webhookTestEndpoint || v.ResourceType != "WebhookEndpoint" {
		t.Fatalf("enable unknown = (%#v, %v), want NotFoundError for the endpoint", enabled, err)
	}

	fake.ReplayWebhookDeliveryFn = func(context.Context, string) (*bosunpb.WebhookDelivery, error) { return nil, pending }
	replayed, err := r.DoReplayWebhookDelivery(ctx, webhookTestDelivery)
	if _, ok := replayed.(*model.ValidationError); err != nil || !ok {
		t.Fatalf("replay pending = (%#v, %v), want ValidationError", replayed, err)
	}

	fake.ReplayWebhookDeliveriesFn = func(context.Context, *bosunpb.ReplayWebhookDeliveriesRequest) (*bosunpb.ReplayWebhookDeliveriesResponse, error) {
		return nil, notFound
	}
	now := time.Now()
	ranged, err := r.DoReplayWebhookDeliveries(ctx, webhookTestEndpoint, now.Add(-time.Hour), now)
	if _, ok := ranged.(*model.NotFoundError); err != nil || !ok {
		t.Fatalf("range replay unknown endpoint = (%#v, %v), want NotFoundError", ranged, err)
	}
	ranged, err = r.DoReplayWebhookDeliveries(ctx, webhookTestEndpoint, now, now.Add(-time.Hour))
	if _, ok := ranged.(*model.ValidationError); err != nil || !ok {
		t.Fatalf("inverted range = (%#v, %v), want ValidationError", ranged, err)
	}
}

// The signing secret is readable only from the create and rotate results; the
// read responses, serialized as GraphQL would, carry no secret.
func TestWebhookSecretOnlyOnCreateAndRotate(t *testing.T) {
	withSecret := &bosunpb.WebhookEndpointWithSecret{Endpoint: testWebhookEndpointProto(), Secret: webhookTestSecret}
	fake := &clientstest.FakeBosun{
		CreateWebhookEndpointFn: func(context.Context, *bosunpb.CreateWebhookEndpointRequest) (*bosunpb.WebhookEndpointWithSecret, error) {
			return withSecret, nil
		},
		RotateWebhookEndpointSecretFn: func(_ context.Context, _ string, revoke bool) (*bosunpb.WebhookEndpointWithSecret, error) {
			if !revoke {
				t.Fatal("revokePrevious was not passed through")
			}
			return withSecret, nil
		},
		GetWebhookEndpointFn: func(context.Context, string) (*bosunpb.WebhookEndpoint, error) {
			return withSecret.GetEndpoint(), nil
		},
		ListWebhookEndpointsFn: func(context.Context, *bosunpb.ListWebhookEndpointsRequest) (*bosunpb.ListWebhookEndpointsResponse, error) {
			return &bosunpb.ListWebhookEndpointsResponse{Endpoints: []*bosunpb.WebhookEndpoint{withSecret.GetEndpoint()}}, nil
		},
	}
	r := platformResolverWith(clientstest.WithBosun(fake))
	ctx := webhookCtx("admin")

	created, err := r.DoCreateWebhookEndpoint(ctx, model.CreateWebhookEndpointInput{URL: "https://hooks.example.com", EventTypes: []string{"*"}})
	if s, ok := created.(*model.WebhookEndpointSecret); err != nil || !ok || s.Secret != webhookTestSecret || s.Endpoint.ID != webhookTestEndpoint {
		t.Fatalf("create = (%#v, %v), want the endpoint with its secret", created, err)
	}
	revoke := true
	rotated, err := r.DoRotateWebhookEndpointSecret(ctx, webhookTestEndpoint, &revoke)
	if s, ok := rotated.(*model.WebhookEndpointSecret); err != nil || !ok || s.Secret != webhookTestSecret {
		t.Fatalf("rotate = (%#v, %v), want the new secret", rotated, err)
	}

	one, err := r.DoWebhookEndpoint(ctx, webhookTestEndpoint)
	if err != nil || one == nil {
		t.Fatalf("get = (%v, %v)", one, err)
	}
	list, err := r.DoWebhookEndpointsConnection(ctx, nil)
	if err != nil || len(list.Nodes) != 1 {
		t.Fatalf("list = (%v, %v)", list, err)
	}
	for name, value := range map[string]any{"webhookEndpoint": one, "webhookEndpointsConnection": list} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "whsec_") || strings.Contains(strings.ToLower(string(raw)), "secret\"") {
			t.Fatalf("%s leaked a secret: %s", name, raw)
		}
	}
}

// Every Bosun call carries the caller's tenant and JWT; reads need
// developer:read on an API token, and changes need an owner or admin with
// developer:write, refused before Bosun is called.
func TestWebhookResolversPassTenantAndEnforceAccess(t *testing.T) {
	calls := 0
	fake := &clientstest.FakeBosun{
		ListWebhookDeliveriesFn: func(ctx context.Context, req *bosunpb.ListWebhookDeliveriesRequest) (*bosunpb.ListWebhookDeliveriesResponse, error) {
			calls++
			if got := ctxkeys.GetTenantID(ctx); got != webhookTestTenant {
				t.Fatalf("tenant = %q, want %q", got, webhookTestTenant)
			}
			if req.GetEndpointId() != webhookTestEndpoint || req.GetEventType() != "stream.live" || len(req.GetStatuses()) != 2 ||
				req.GetStatuses()[0] != bosunpb.WebhookDeliveryStatus_WEBHOOK_DELIVERY_STATUS_FAILED ||
				req.GetCreatedAfter() == nil || req.GetPagination().GetFirst() != 200 {
				t.Fatalf("filters not passed through: %+v", req)
			}
			return &bosunpb.ListWebhookDeliveriesResponse{
				Deliveries: []*bosunpb.WebhookDelivery{testWebhookDeliveryProto()},
				Pagination: &commonpb.CursorPaginationResponse{TotalCount: 1},
			}, nil
		},
		CreateWebhookEndpointFn: func(ctx context.Context, req *bosunpb.CreateWebhookEndpointRequest) (*bosunpb.WebhookEndpointWithSecret, error) {
			calls++
			if got := ctxkeys.GetTenantID(ctx); got != webhookTestTenant || ctxkeys.GetJWTToken(ctx) != "user-jwt" {
				t.Fatalf("create identity: tenant=%q jwt=%q", got, ctxkeys.GetJWTToken(ctx))
			}
			if req.GetUrl() != "https://hooks.example.com" || req.GetApiVersion() != "" {
				t.Fatalf("create request = %+v", req)
			}
			return &bosunpb.WebhookEndpointWithSecret{Endpoint: testWebhookEndpointProto(), Secret: webhookTestSecret}, nil
		},
	}
	r := platformResolverWith(clientstest.WithBosun(fake))

	endpoint, eventType := webhookTestEndpoint, "stream.live"
	after := time.Now().Add(-time.Hour)
	first := 1000
	filter := WebhookDeliveryFilter{
		EndpointID:   &endpoint,
		EventType:    &eventType,
		Statuses:     []model.WebhookDeliveryStatus{model.WebhookDeliveryStatusFailed, model.WebhookDeliveryStatusSkipped},
		CreatedAfter: &after,
	}
	conn, err := r.DoWebhookDeliveriesConnection(webhookCtx("member"), filter, &model.ConnectionInput{First: &first})
	if err != nil || len(conn.Nodes) != 1 || conn.Nodes[0].Status != model.WebhookDeliveryStatusFailed || len(conn.Nodes[0].AttemptHistory) != 0 {
		t.Fatalf("deliveries = (%+v, %v)", conn, err)
	}
	if conn.PageInfo.EndCursor == nil || *conn.PageInfo.EndCursor != conn.Edges[0].Cursor {
		t.Fatalf("page info = %+v, want the edge cursor as end cursor", conn.PageInfo)
	}

	input := model.CreateWebhookEndpointInput{URL: " https://hooks.example.com ", EventTypes: []string{"*"}}
	if _, err := r.DoCreateWebhookEndpoint(webhookCtx("owner"), input); err != nil {
		t.Fatalf("owner create: %v", err)
	}
	before := calls

	if res, err := r.DoCreateWebhookEndpoint(webhookCtx("member"), input); err != nil {
		t.Fatalf("member create err = %v", err)
	} else if _, ok := res.(*model.AuthError); !ok {
		t.Fatalf("member create = %#v, want AuthError", res)
	}
	if res, _ := r.DoCreateWebhookEndpoint(webhookAPITokenCtx("developer:read"), input); res == nil {
		t.Fatal("API token without developer:write created an endpoint")
	} else if _, ok := res.(*model.AuthError); !ok {
		t.Fatalf("read-only token create = %#v, want AuthError", res)
	}
	if _, err := r.DoWebhookDeliveriesConnection(webhookAPITokenCtx("streams:read"), filter, nil); err == nil {
		t.Fatal("API token without developer:read listed deliveries")
	}
	if _, err := r.DoWebhookEndpoint(context.Background(), webhookTestEndpoint); err == nil {
		t.Fatal("unauthenticated read succeeded")
	}
	if calls != before {
		t.Fatalf("refused calls reached Bosun: %d calls after the owner create", calls-before)
	}
}

// webhookDelivery loads the attempt history; an unknown ID is null.
func TestWebhookDeliveryLoadsAttemptHistory(t *testing.T) {
	fake := &clientstest.FakeBosun{
		GetWebhookDeliveryFn: func(_ context.Context, id string) (*bosunpb.GetWebhookDeliveryResponse, error) {
			if id != webhookTestDelivery {
				return nil, status.Error(codes.NotFound, "not found")
			}
			return &bosunpb.GetWebhookDeliveryResponse{
				Delivery: testWebhookDeliveryProto(),
				Attempts: []*bosunpb.WebhookDeliveryAttempt{
					{Id: "a1", AttemptNumber: 1, StatusCode: 500, ErrorClass: "http_status", AttemptedAt: timestamppb.Now()},
					{Id: "a2", AttemptNumber: 2, ErrorClass: "timeout", LatencyMs: 10000, AttemptedAt: timestamppb.Now()},
				},
			}, nil
		},
	}
	r := platformResolverWith(clientstest.WithBosun(fake))
	d, err := r.DoWebhookDelivery(webhookCtx("viewer"), webhookTestDelivery)
	if err != nil || d == nil || len(d.AttemptHistory) != 2 || d.AttemptHistory[1].ErrorClass != "timeout" || d.EventID == nil {
		t.Fatalf("delivery = (%+v, %v)", d, err)
	}
	if missing, err := r.DoWebhookDelivery(webhookCtx("viewer"), "other"); err != nil || missing != nil {
		t.Fatalf("unknown delivery = (%v, %v), want (nil, nil)", missing, err)
	}
}

func TestWebhookResolversWithoutBosunAreUnavailable(t *testing.T) {
	r := platformResolverWith()
	if _, err := r.DoWebhookEndpointsConnection(webhookCtx("owner"), nil); !errors.Is(err, errWebhooksUnavailable) {
		t.Fatalf("list without Bosun = %v, want errWebhooksUnavailable", err)
	}
	if _, err := r.DoDisableWebhookEndpoint(webhookCtx("owner"), webhookTestEndpoint); !errors.Is(err, errWebhooksUnavailable) {
		t.Fatalf("disable without Bosun = %v, want errWebhooksUnavailable", err)
	}
}

// Event types are the registry's public types plus "*", and never an
// internal type.
func TestWebhookEventTypesArePublicTypesPlusWildcard(t *testing.T) {
	types := WebhookEventTypes()
	if len(types) < 2 || types[len(types)-1] != "*" {
		t.Fatalf("types = %v, want public types followed by *", types)
	}
	seen := map[string]bool{}
	for _, eventType := range types[:len(types)-1] {
		spec, ok := events.Lookup(eventType)
		if !ok || !spec.Public() {
			t.Fatalf("%q is not a registered public type", eventType)
		}
		seen[eventType] = true
	}
	for _, spec := range events.Specs() {
		if spec.Public() != seen[spec.Type] {
			t.Fatalf("%q public=%v listed=%v", spec.Type, spec.Public(), seen[spec.Type])
		}
	}
	if _, err := platformResolverWith().DoWebhookEventTypes(context.Background()); err == nil {
		t.Fatal("unauthenticated webhookEventTypes succeeded")
	}
}

// Demo mode serves the demo endpoints and deliveries with no Bosun client,
// applies the delivery filters, and refuses changes.
func TestWebhookResolversDemoMode(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyDemoMode, true)
	r := platformResolverWith()

	endpoints, err := r.DoWebhookEndpointsConnection(ctx, nil)
	if err != nil || len(endpoints.Nodes) != len(demo.GenerateWebhookEndpoints()) {
		t.Fatalf("demo endpoints = (%v, %v)", endpoints, err)
	}
	ep, err := r.DoWebhookEndpoint(ctx, demo.DemoWebhookEndpointDisabledID)
	if err != nil || ep == nil || ep.Status != model.WebhookEndpointStatusDisabled || ep.DisabledReason == nil || *ep.DisabledReason != model.WebhookEndpointDisabledReasonFailing {
		t.Fatalf("demo disabled endpoint = (%+v, %v)", ep, err)
	}

	endpointID := demo.DemoWebhookEndpointID
	failed, err := r.DoWebhookDeliveriesConnection(ctx, WebhookDeliveryFilter{
		EndpointID: &endpointID,
		Statuses:   []model.WebhookDeliveryStatus{model.WebhookDeliveryStatusFailed},
	}, nil)
	if err != nil || len(failed.Nodes) != 1 || failed.Nodes[0].Status != model.WebhookDeliveryStatusFailed || failed.TotalCount != 1 {
		t.Fatalf("demo failed deliveries = (%+v, %v)", failed, err)
	}
	d, err := r.DoWebhookDelivery(ctx, failed.Nodes[0].ID)
	if err != nil || d == nil || len(d.AttemptHistory) != d.Attempts {
		t.Fatalf("demo delivery = (%+v, %v), want one attempt per attempt count", d, err)
	}
	if types, err := r.DoWebhookEventTypes(ctx); err != nil || types[len(types)-1] != "*" {
		t.Fatalf("demo event types = (%v, %v)", types, err)
	}
	if _, err := r.DoCreateWebhookEndpoint(ctx, model.CreateWebhookEndpointInput{URL: "https://x.example", EventTypes: []string{"*"}}); err == nil {
		t.Fatal("demo create succeeded")
	}
}
