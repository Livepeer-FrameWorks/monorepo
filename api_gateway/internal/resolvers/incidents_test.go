package resolvers

import (
	"context"
	"testing"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients/clientstest"
	"frameworks/api_gateway/internal/middleware"

	signalmanclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/signalman"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	lookoutpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/lookout"
	signalmanpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/signalman"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/tenants"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const incidentTestTenant = "7a1d0000-0000-4000-8000-000000000001"

func incidentTenantCtx(operator bool) context.Context {
	ctx := clientstest.AuthedCtx(incidentTestTenant)
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "user-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyJWTToken, "user-jwt")
	if operator {
		ctx = context.WithValue(ctx, ctxkeys.KeyPlatformOperator, true)
	}
	return ctx
}

func testIncident() *lookoutpb.Incident {
	return &lookoutpb.Incident{
		Id:          "1dc1de17-0000-4000-8000-0000000000aa",
		Scope:       lookoutpb.IncidentScope_INCIDENT_SCOPE_TENANT,
		TenantId:    incidentTestTenant,
		Status:      lookoutpb.IncidentStatus_INCIDENT_STATUS_FIRING,
		Title:       "Edge node heartbeat missing",
		StartedAt:   timestamppb.Now(),
		LastAlertAt: timestamppb.Now(),
		CreatedAt:   timestamppb.Now(),
		UpdatedAt:   timestamppb.Now(),
	}
}

// A tenant incident list must reach Lookout as the tenant only. The user's JWT
// is never forwarded: an operator's JWT would make Lookout list every tenant.
func TestIncidentsConnectionCallsLookoutAsTenantWithoutJWT(t *testing.T) {
	for _, operator := range []bool{false, true} {
		called := false
		r := platformResolverWith(clientstest.WithLookout(&clientstest.FakeLookout{
			ListIncidentsFn: func(ctx context.Context, req *lookoutpb.ListIncidentsRequest) (*lookoutpb.ListIncidentsResponse, error) {
				called = true
				if got := ctxkeys.GetTenantID(ctx); got != incidentTestTenant {
					t.Fatalf("tenant = %q, want %q", got, incidentTestTenant)
				}
				if got := ctxkeys.GetUserID(ctx); got != "user-1" {
					t.Fatalf("user = %q, want user-1", got)
				}
				if ctxkeys.GetJWTToken(ctx) != "" || ctxkeys.IsPlatformOperator(ctx) {
					t.Fatal("tenant incident list forwarded caller JWT or operator grant")
				}
				if req.GetScope() != lookoutpb.IncidentScope_INCIDENT_SCOPE_TENANT {
					t.Fatalf("scope = %v, want tenant", req.GetScope())
				}
				return &lookoutpb.ListIncidentsResponse{Incidents: []*lookoutpb.Incident{testIncident()}}, nil
			},
		}))
		conn, err := r.DoIncidentsConnection(incidentTenantCtx(operator), nil, &model.IncidentFilterInput{Statuses: []model.IncidentStatus{model.IncidentStatusFiring}})
		if err != nil || !called || len(conn.Nodes) != 1 || conn.Nodes[0].Scope != model.IncidentScopeTenant {
			t.Fatalf("operator=%v: conn=%+v err=%v called=%v", operator, conn, err, called)
		}
	}
}

func TestPlatformIncidentsRejectsNonOperatorsBeforeLookout(t *testing.T) {
	r := platformResolverWith(clientstest.WithLookout(&clientstest.FakeLookout{}))
	if _, err := r.DoPlatformIncidents(incidentTenantCtx(false), nil, nil); err == nil {
		t.Fatal("non-operator listed platform incidents")
	}
}

func TestPlatformIncidentsCallLookoutWithoutIdentity(t *testing.T) {
	scope := model.IncidentScopePlatform
	r := platformResolverWith(clientstest.WithLookout(&clientstest.FakeLookout{
		ListIncidentsFn: func(ctx context.Context, req *lookoutpb.ListIncidentsRequest) (*lookoutpb.ListIncidentsResponse, error) {
			requireStrippedIdentity(t, ctx)
			if req.GetScope() != lookoutpb.IncidentScope_INCIDENT_SCOPE_PLATFORM {
				t.Fatalf("scope = %v, want platform", req.GetScope())
			}
			return &lookoutpb.ListIncidentsResponse{}, nil
		},
	}))
	if _, err := r.DoPlatformIncidents(operatorCtx(), nil, &model.PlatformIncidentFilterInput{Scope: &scope}); err != nil {
		t.Fatal(err)
	}
}

// Operator actions call Lookout without a tenant so every scope is reachable,
// while the operator's user ID is kept for the incident timeline.
func TestIncidentMutationAsOperatorKeepsActorOnly(t *testing.T) {
	r := platformResolverWith(clientstest.WithLookout(&clientstest.FakeLookout{
		AcknowledgeIncidentFn: func(ctx context.Context, incidentID string) (*lookoutpb.IncidentMutationResponse, error) {
			if ctxkeys.GetTenantID(ctx) != "" || ctxkeys.GetJWTToken(ctx) != "" || ctxkeys.IsPlatformOperator(ctx) {
				t.Fatal("operator incident action carried tenant, JWT, or operator grant")
			}
			if got := ctxkeys.GetUserID(ctx); got != "operator-1" {
				t.Fatalf("actor = %q, want operator-1", got)
			}
			return &lookoutpb.IncidentMutationResponse{Incident: testIncident()}, nil
		},
	}))
	result, err := r.DoAcknowledgeIncident(operatorCtx(), testIncident().GetId())
	if _, ok := result.(*model.Incident); err != nil || !ok {
		t.Fatalf("result=%T err=%v", result, err)
	}
}

func TestAssignIncidentOnlyToCallerOrNobody(t *testing.T) {
	var sent []string
	r := platformResolverWith(clientstest.WithLookout(&clientstest.FakeLookout{
		AssignIncidentFn: func(_ context.Context, _ string, assignee string) (*lookoutpb.IncidentMutationResponse, error) {
			sent = append(sent, assignee)
			return &lookoutpb.IncidentMutationResponse{Incident: testIncident()}, nil
		},
	}))
	other := "someone-else"
	if result, err := r.DoAssignIncident(incidentTenantCtx(false), "id", &other); err != nil {
		t.Fatal(err)
	} else if _, ok := result.(*model.ValidationError); !ok {
		t.Fatalf("assigning another user returned %T, want ValidationError", result)
	}
	self := "user-1"
	for _, assignee := range []*string{&self, nil} {
		if result, err := r.DoAssignIncident(incidentTenantCtx(false), "id", assignee); err != nil {
			t.Fatal(err)
		} else if _, ok := result.(*model.Incident); !ok {
			t.Fatalf("assign %v returned %T", assignee, result)
		}
	}
	if len(sent) != 2 || sent[0] != "user-1" || sent[1] != "" {
		t.Fatalf("assignees sent to Lookout = %v, want [user-1 \"\"]", sent)
	}
}

func TestIncidentMutationMapsLookoutErrors(t *testing.T) {
	cases := []struct {
		code codes.Code
		want string
	}{
		{codes.NotFound, "not_found"},
		{codes.FailedPrecondition, "validation"},
		{codes.InvalidArgument, "validation"},
		{codes.PermissionDenied, "auth"},
	}
	for _, tc := range cases {
		r := platformResolverWith(clientstest.WithLookout(&clientstest.FakeLookout{
			ResolveIncidentFn: func(context.Context, string) (*lookoutpb.IncidentMutationResponse, error) {
				return nil, status.Error(tc.code, "refused")
			},
		}))
		result, err := r.DoResolveIncident(incidentTenantCtx(false), "id")
		if err != nil {
			t.Fatalf("%v: unexpected transport error %v", tc.code, err)
		}
		got := ""
		switch result.(type) {
		case *model.NotFoundError:
			got = "not_found"
		case *model.ValidationError:
			got = "validation"
		case *model.AuthError:
			got = "auth"
		}
		if got != tc.want {
			t.Fatalf("%v mapped to %T, want %s", tc.code, result, tc.want)
		}
	}
}

func TestIncidentEventsOnlyReachTheirTenant(t *testing.T) {
	other := "7a1d0000-0000-4000-8000-000000000002"
	event := func(envelopeTenant *string, payloadTenant string) *signalmanpb.SignalmanEvent {
		return &signalmanpb.SignalmanEvent{
			EventType: signalmanpb.EventType_EVENT_TYPE_INCIDENT_UPDATED,
			TenantId:  envelopeTenant,
			Data: &signalmanpb.EventData{Payload: &signalmanpb.EventData_IncidentUpdated{IncidentUpdated: &ipcpb.IncidentEvent{
				IncidentId: "incident", TenantId: payloadTenant, Status: "firing",
			}}},
		}
	}
	tenant := incidentTestTenant
	cases := []struct {
		name  string
		event *signalmanpb.SignalmanEvent
		want  bool
	}{
		{"own tenant", event(&tenant, tenant), true},
		{"tenantless broadcast", event(nil, tenant), false},
		{"other tenant", event(&other, other), false},
		{"payload tenant mismatch", event(&tenant, other), false},
	}
	for _, tc := range cases {
		if got := incidentEventForTenant(incidentTestTenant, tc.event); got != tc.want {
			t.Fatalf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
	if incidentEventForTenant("", event(&tenant, tenant)) {
		t.Fatal("subscriber without a tenant received an incident event")
	}
}

func incidentSubscriberCtx(ctx context.Context, tenantID string, operator bool) context.Context {
	return context.WithValue(ctx, ctxkeys.KeyUser, &middleware.UserContext{
		UserID:           "user-" + tenantID,
		TenantID:         tenantID,
		Role:             "owner",
		PlatformOperator: operator,
	})
}

func incidentSignalmanEvent(channel signalmanpb.Channel, envelopeTenant *string, incidentID, payloadTenant string) *signalmanpb.SignalmanEvent {
	return &signalmanpb.SignalmanEvent{
		EventType: signalmanpb.EventType_EVENT_TYPE_INCIDENT_UPDATED,
		Channel:   channel,
		TenantId:  envelopeTenant,
		Data: &signalmanpb.EventData{Payload: &signalmanpb.EventData_IncidentUpdated{IncidentUpdated: &ipcpb.IncidentEvent{
			IncidentId: incidentID, TenantId: payloadTenant, Status: "firing", Change: "opened",
		}}},
	}
}

// A verified operator's liveIncidentUpdates reads the tenantless platform
// channel and receives every incident, including platform-scope incidents and
// other tenants' incidents. Operators of different tenants share that one
// upstream, and it reconnects without the subscriptions resubscribing.
func TestIncidentUpdatesForOperatorsUsePlatformChannel(t *testing.T) {
	opener := newFakeOpener()
	r := platformResolverWith()
	r.SubManager = testSubscriptionManager(t, opener, SubscriptionManagerConfig{})

	systemOperator, err := r.DoIncidentUpdates(incidentSubscriberCtx(operatorCtx(), tenants.SystemTenantID.String(), true))
	if err != nil {
		t.Fatalf("operator subscription: %v", err)
	}
	otherOperatorCtx := context.WithValue(clientstest.AuthedCtx(incidentTestTenant), ctxkeys.KeyPlatformOperator, true)
	otherOperator, err := r.DoIncidentUpdates(incidentSubscriberCtx(otherOperatorCtx, incidentTestTenant, true))
	if err != nil {
		t.Fatalf("second operator subscription: %v", err)
	}

	upstream := opener.waitOpened(t)
	if upstream.key != (signalmanclient.StreamKey{Channel: signalmanpb.Channel_CHANNEL_PLATFORM}) {
		t.Fatalf("operator upstream key = %+v, want the tenantless platform channel", upstream.key)
	}
	select {
	case extra := <-opener.opened:
		t.Fatalf("operators opened a second upstream %+v", extra.key)
	case <-time.After(50 * time.Millisecond):
	}

	upstream.push(t, incidentSignalmanEvent(signalmanpb.Channel_CHANNEL_PLATFORM, nil, "platform-incident", ""))
	for _, updates := range []<-chan *model.IncidentUpdatedEvent{systemOperator, otherOperator} {
		if got := receiveUpdate(t, updates); got.IncidentID != "platform-incident" {
			t.Fatalf("operator update = %+v, want platform-incident", got)
		}
	}

	close(upstream.events)
	reconnected := opener.waitOpened(t)
	if reconnected.key != upstream.key {
		t.Fatalf("reconnected upstream key = %+v, want %+v", reconnected.key, upstream.key)
	}
	reconnected.push(t, incidentSignalmanEvent(signalmanpb.Channel_CHANNEL_PLATFORM, nil, "tenant-b-incident", "7a1d0000-0000-4000-8000-00000000000b"))
	for _, updates := range []<-chan *model.IncidentUpdatedEvent{systemOperator, otherOperator} {
		if got := receiveUpdate(t, updates); got.IncidentID != "tenant-b-incident" {
			t.Fatalf("operator update after reconnect = %+v, want tenant-b-incident", got)
		}
	}
}

// Without the operator grant, even a member of the system tenant gets the
// tenant subscription: it never opens the platform channel, and platform
// incidents or other tenants' incidents are not delivered.
func TestIncidentUpdatesForNonOperatorsStayTenantScoped(t *testing.T) {
	opener := newFakeOpener()
	r := platformResolverWith()
	r.SubManager = testSubscriptionManager(t, opener, SubscriptionManagerConfig{})
	systemTenant := tenants.SystemTenantID.String()

	updates, err := r.DoIncidentUpdates(incidentSubscriberCtx(clientstest.AuthedCtx(systemTenant), systemTenant, false))
	if err != nil {
		t.Fatalf("tenant subscription: %v", err)
	}
	upstream := opener.waitOpened(t)
	if upstream.key != (signalmanclient.StreamKey{TenantID: systemTenant, Channel: signalmanpb.Channel_CHANNEL_SYSTEM}) {
		t.Fatalf("tenant upstream key = %+v, want the system tenant's system channel", upstream.key)
	}

	other := incidentTestTenant
	upstream.push(t, incidentSignalmanEvent(signalmanpb.Channel_CHANNEL_SYSTEM, nil, "tenantless", ""))
	upstream.push(t, incidentSignalmanEvent(signalmanpb.Channel_CHANNEL_PLATFORM, nil, "platform", ""))
	upstream.push(t, incidentSignalmanEvent(signalmanpb.Channel_CHANNEL_SYSTEM, &other, "other-tenant", other))
	upstream.push(t, incidentSignalmanEvent(signalmanpb.Channel_CHANNEL_SYSTEM, &systemTenant, "own", systemTenant))
	if got := receiveUpdate(t, updates); got.IncidentID != "own" {
		t.Fatalf("tenant update = %+v, want only its own incident", got)
	}
	expectNoUpdate(t, updates)
	select {
	case extra := <-opener.opened:
		t.Fatalf("tenant subscription opened another upstream %+v", extra.key)
	case <-time.After(50 * time.Millisecond):
	}
}
