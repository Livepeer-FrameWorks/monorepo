package resolvers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	signalmanclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/signalman"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	signalmanpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/signalman"

	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/gorilla/websocket"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const replicaJWTSecret = "jwt-secret"

func sessionToken(t *testing.T, userID, tenantID string) string {
	t.Helper()
	token, err := auth.GenerateSessionJWT(userID, tenantID, userID+"@example.com", "owner", nil, time.Now(), []byte(replicaJWTSecret))
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func sessionsRevokedEvent(t *testing.T, tenantID, userID string, revokedAt time.Time) *signalmanpb.SignalmanEvent {
	t.Helper()
	data, err := anypb.New(&internalv1.UserSessionsRevoked{
		UserId: userID, RevokedAt: timestamppb.New(revokedAt), Reason: internalv1.SessionRevocationReason_SESSION_REVOCATION_REASON_LOGOUT,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &signalmanpb.SignalmanEvent{
		EventType: signalmanpb.EventType_EVENT_TYPE_TENANT_EVENT,
		Channel:   signalmanpb.Channel_CHANNEL_SESSIONS,
		TenantId:  &tenantID,
		Data: &signalmanpb.EventData{Payload: &signalmanpb.EventData_TenantEvent{TenantEvent: &signalmanpb.TenantEvent{
			Id: "evt-logout-" + userID, Type: "user.sessions_revoked", Data: data,
		}}},
	}
}

// Signing out through one Bridge replica emits user.sessions_revoked, which
// closes that user's session sockets on every replica with 4403 within two
// seconds, and leaves another user's session socket and the user's API-token
// socket open.
func TestSignOutClosesTheUsersSessionSocketsOnEveryBridgeReplica(t *testing.T) {
	const tenantID = "tenant-1"
	sc := validatorClients(t, map[string]*commodorepb.ValidateAPITokenResponse{
		"token-value": {Valid: true, UserId: "user-1", TenantId: tenantID, Role: "owner", TokenId: "token-1"},
	})
	broker := newSignalmanBroker()
	replicaA := newBridgeReplica(t, broker, sc)
	replicaB := newBridgeReplica(t, broker, sc)

	userToken := sessionToken(t, "user-1", tenantID)
	onA := openTokenSocket(t, replicaA, userToken)
	onB := openTokenSocket(t, replicaB, userToken)
	otherUser := openTokenSocket(t, replicaB, sessionToken(t, "user-2", tenantID))
	apiToken := openTokenSocket(t, replicaB, "token-value")
	broker.waitStreams(t, "", signalmanpb.Channel_CHANNEL_SESSIONS, 2)

	broker.publish(t, "", signalmanpb.Channel_CHANNEL_SESSIONS, sessionsRevokedEvent(t, tenantID, "user-1", time.Now()))

	for name, conn := range map[string]*websocket.Conn{"the replica that signed out": onA, "the other replica": onB} {
		if code := socketCloseCode(t, conn, 2*time.Second); code != middleware.WebsocketCloseForbidden {
			t.Fatalf("session socket on %s closed with %d within 2s, want %d (0 = still open)", name, code, middleware.WebsocketCloseForbidden)
		}
	}
	for name, conn := range map[string]*websocket.Conn{"another user's session": otherUser, "the user's API token": apiToken} {
		if code := socketCloseCode(t, conn, 300*time.Millisecond); code != 0 {
			t.Fatalf("socket of %s closed with %d, want it open", name, code)
		}
	}
}

// recordingSockets records the revocations an upstream stream hands over.
type recordingSockets struct {
	mu       sync.Mutex
	tokens   []string
	sessions []string
	seen     chan struct{}
}

func (r *recordingSockets) CloseAPIToken(tenantID, tokenID string) int {
	r.mu.Lock()
	r.tokens = append(r.tokens, tenantID+"/"+tokenID)
	r.mu.Unlock()
	r.seen <- struct{}{}
	return 0
}

func (r *recordingSockets) CloseUserSessions(tenantID, userID string, _ time.Time) int {
	r.mu.Lock()
	r.sessions = append(r.sessions, tenantID+"/"+userID)
	r.mu.Unlock()
	r.seen <- struct{}{}
	return 0
}

// A tenant's CHANNEL_EVENTS stream hands only that tenant's
// api_token.revoked to the credential sockets, the platform-wide
// CHANNEL_SESSIONS stream hands each user.sessions_revoked under the tenant
// it names, and the API-token watch takes none of the tenant's GraphQL
// subscription slots.
func TestCredentialRevocationsReachTheSocketsOnlyForTheStreamsTenant(t *testing.T) {
	broker := newSignalmanBroker()
	sm := newSubscriptionManager(logging.NewLoggerWithService("test"), SubscriptionManagerConfig{
		SignalmanAddrs: []string{"signalman.internal:19005"}, MaxSubscriptionsPerTenant: 1,
	}, broker)
	t.Cleanup(func() { _ = sm.Shutdown() })
	sockets := &recordingSockets{seen: make(chan struct{}, 8)}
	sm.SetCredentialSockets(sockets)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sm.WatchAPITokenRevocations(ctx, "tenant-1", func() {})
	go sm.WatchSessionRevocations(ctx, func() {})
	broker.waitStreams(t, "tenant-1", signalmanpb.Channel_CHANNEL_EVENTS, 1)
	broker.waitStreams(t, "", signalmanpb.Channel_CHANNEL_SESSIONS, 1)

	if _, err := sm.SubscribeToTenantEvents(ctx, ConnectionConfig{TenantID: "tenant-1"}, TenantEventFilter{}); err != nil {
		t.Fatalf("the watch took the tenant's only subscription slot: %v", err)
	}

	otherTenant := apiTokenRevokedEvent(t, "tenant-1", "token-2")
	otherTenantID := "tenant-2"
	otherTenant.TenantId = &otherTenantID
	broker.publish(t, "tenant-1", signalmanpb.Channel_CHANNEL_EVENTS, otherTenant)
	otherTenantLogout := sessionsRevokedEvent(t, "tenant-2", "user-9", time.Now())
	broker.publish(t, "", signalmanpb.Channel_CHANNEL_SESSIONS, otherTenantLogout)

	created, err := anypb.New(&publicv1.ApiTokenCreated{TokenId: "token-9"})
	if err != nil {
		t.Fatal(err)
	}
	createdEvent := apiTokenRevokedEvent(t, "tenant-1", "token-9")
	createdEvent.Data.GetTenantEvent().Type = "api_token.created"
	createdEvent.Data.GetTenantEvent().Data = created
	broker.publish(t, "tenant-1", signalmanpb.Channel_CHANNEL_EVENTS, createdEvent)

	broker.publish(t, "tenant-1", signalmanpb.Channel_CHANNEL_EVENTS, apiTokenRevokedEvent(t, "tenant-1", "token-1"))
	broker.publish(t, "", signalmanpb.Channel_CHANNEL_SESSIONS, sessionsRevokedEvent(t, "tenant-1", "user-1", time.Now()))

	for range 3 {
		select {
		case <-sockets.seen:
		case <-time.After(2 * time.Second):
			t.Fatal("a revocation did not reach the sockets")
		}
	}
	select {
	case <-sockets.seen:
	case <-time.After(100 * time.Millisecond):
	}
	sockets.mu.Lock()
	defer sockets.mu.Unlock()
	if len(sockets.tokens) != 1 || sockets.tokens[0] != "tenant-1/token-1" {
		t.Fatalf("sockets received token revocations %v, want only tenant-1/token-1", sockets.tokens)
	}
	if len(sockets.sessions) != 2 || sockets.sessions[0] != "tenant-2/user-9" || sockets.sessions[1] != "tenant-1/user-1" {
		t.Fatalf("sockets received session revocations %v, want tenant-2/user-9 then tenant-1/user-1", sockets.sessions)
	}
}

// tenantEventsSchema serves `subscription { tenantEvents { type } }` through
// Resolver.DoTenantEvents, the path Bridge's generated schema takes.
func tenantEventsSchema(r *Resolver) graphql.ExecutableSchema {
	schema := gqlparser.MustLoadSchema(&ast.Source{Input: `
		type Query { name: String! }
		type TenantEvent { type: String! }
		type Subscription { tenantEvents: TenantEvent! }
	`})
	return &graphql.ExecutableSchemaMock{
		SchemaFunc: func() *ast.Schema { return schema },
		ComplexityFunc: func(context.Context, string, string, int, map[string]any) (int, bool) {
			return 0, false
		},
		ExecFunc: func(ctx context.Context) graphql.ResponseHandler {
			if graphql.GetOperationContext(ctx).Operation.Operation != ast.Subscription {
				return graphql.OneShot(graphql.ErrorResponse(ctx, "only subscriptions"))
			}
			events, err := r.DoTenantEvents(ctx, nil, nil)
			if err != nil {
				return graphql.OneShot(graphql.ErrorResponse(ctx, "%s", err.Error()))
			}
			return func(ctx context.Context) *graphql.Response {
				select {
				case <-ctx.Done():
					return nil
				case event, ok := <-events:
					if !ok {
						return nil
					}
					data, _ := json.Marshal(map[string]any{"tenantEvents": map[string]string{"type": event.GetType()}})
					return &graphql.Response{Data: data}
				}
			}
		},
	}
}

// A revoked API token's socket on another replica receives neither the
// token's own api_token.revoked nor any later event of the tenant's stream:
// the replica stops delivery on the socket before it fans the revocation out
// to the socket's tenantEvents subscription.
func TestRevokedTokensSocketReceivesNoEventFromItsRevocationOn(t *testing.T) {
	const tenantID = "tenant-1"
	sc := validatorClients(t, map[string]*commodorepb.ValidateAPITokenResponse{
		"token-value": {
			Valid: true, UserId: "user-1", TenantId: tenantID, Role: "owner", TokenId: "token-1",
			Permissions: []string{"developer:read", "streams:read"},
		},
	})
	broker := newSignalmanBroker()
	url, sm := tenantEventsReplica(t, broker, sc)

	conn := dialTenantEvents(t, url, "token-value")
	broker.waitStreams(t, tenantID, signalmanpb.Channel_CHANNEL_EVENTS, 1)
	// The watch and the tenantEvents subscription share the stream.
	waitUpstreamSubscribers(t, sm, signalmanclient.StreamKey{TenantID: tenantID, Channel: signalmanpb.Channel_CHANNEL_EVENTS}, 2)

	// The subscription delivers the tenant's events while the token is valid.
	broker.publish(t, tenantID, signalmanpb.Channel_CHANNEL_EVENTS, streamLiveEvent(t, tenantID, "stream-before"))
	if got := nextTenantEventType(t, conn, 2*time.Second); got != "stream.live" {
		t.Fatalf("subscription delivered %q before the revocation, want stream.live", got)
	}

	broker.publish(t, tenantID, signalmanpb.Channel_CHANNEL_EVENTS, apiTokenRevokedEvent(t, tenantID, "token-1"))
	broker.publish(t, tenantID, signalmanpb.Channel_CHANNEL_EVENTS, streamLiveEvent(t, tenantID, "stream-after"))

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			var closeErr *websocket.CloseError
			if !errors.As(err, &closeErr) || closeErr.Code != middleware.WebsocketCloseForbidden {
				t.Fatalf("revoked socket ended with %v, want close %d", err, middleware.WebsocketCloseForbidden)
			}
			return
		}
		var msg struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(data, &msg)
		if msg.Type == "next" {
			t.Fatalf("revoked socket received an event from its revocation on: %s", data)
		}
	}
}

func streamLiveEvent(t *testing.T, tenantID, streamID string) *signalmanpb.SignalmanEvent {
	t.Helper()
	data, err := anypb.New(&publicv1.StreamLive{StreamId: streamID})
	if err != nil {
		t.Fatal(err)
	}
	return &signalmanpb.SignalmanEvent{
		EventType: signalmanpb.EventType_EVENT_TYPE_TENANT_EVENT,
		Channel:   signalmanpb.Channel_CHANNEL_EVENTS,
		TenantId:  &tenantID,
		Data: &signalmanpb.EventData{Payload: &signalmanpb.EventData_TenantEvent{TenantEvent: &signalmanpb.TenantEvent{
			Id: "evt-" + streamID, Type: "stream.live", Data: data,
		}}},
	}
}

// tenantEventsReplica is a Bridge replica serving tenantEvents over its
// WebSocket transport, wired as Bridge's main wires it.
func tenantEventsReplica(t *testing.T, broker streamOpener, sc *clients.ServiceClients) (string, *SubscriptionManager) {
	t.Helper()
	sm := newSubscriptionManager(logging.NewLoggerWithService("test"), SubscriptionManagerConfig{SignalmanAddrs: []string{"signalman.internal:19005"}}, broker)
	t.Cleanup(func() { _ = sm.Shutdown() })
	sessions := middleware.NewWebsocketSessions(sm.WatchAPITokenRevocations)
	sm.SetCredentialSockets(sessions)
	srv := handler.New(tenantEventsSchema(&Resolver{SubManager: sm, Logger: logging.NewLoggerWithService("test")}))
	srv.AddTransport(middleware.GraphQLWebsocketTransport(sc, []byte(replicaJWTSecret), nil, websocket.Upgrader{}, 0, sessions, middleware.DefaultWebsocketRevalidateInterval))
	httpSrv := httptest.NewServer(srv)
	t.Cleanup(httpSrv.Close)
	return strings.Replace(httpSrv.URL, "http://", "ws://", 1), sm
}

// waitUpstreamSubscribers waits up to three seconds until key's upstream
// stream has n attached subscribers.
func waitUpstreamSubscribers(t *testing.T, sm *SubscriptionManager, key signalmanclient.StreamKey, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		sm.mu.Lock()
		entry := sm.upstreams[key]
		sm.mu.Unlock()
		if entry != nil && entry.fanout.Len() >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%d subscribers did not attach to the %s stream of %s", n, key.Channel, key.TenantID)
}

func dialTenantEvents(t *testing.T, url, token string) *websocket.Conn {
	t.Helper()
	dialer := websocket.Dialer{Subprotocols: []string{"graphql-transport-ws"}}
	conn, resp, err := dialer.Dial(url, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.WriteJSON(map[string]any{"type": "connection_init", "payload": map[string]any{"Authorization": "Bearer " + token}}); err != nil {
		t.Fatalf("write init: %v", err)
	}
	if got := readWSMessageType(t, conn, 2*time.Second); got != "connection_ack" {
		t.Fatalf("init answered %q, want connection_ack", got)
	}
	if err := conn.WriteJSON(map[string]any{"id": "1", "type": "subscribe", "payload": map[string]any{"query": "subscription { tenantEvents { type } }"}}); err != nil {
		t.Fatalf("write subscribe: %v", err)
	}
	return conn
}

func nextTenantEventType(t *testing.T, conn *websocket.Conn, within time.Duration) string {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(within))
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var msg struct {
			Type    string `json:"type"`
			Payload struct {
				Data struct {
					TenantEvents struct {
						Type string `json:"type"`
					} `json:"tenantEvents"`
				} `json:"data"`
				Errors json.RawMessage `json:"errors"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("decode %s: %v", data, err)
		}
		switch msg.Type {
		case "next":
			return msg.Payload.Data.TenantEvents.Type
		case "error":
			t.Fatalf("subscription failed: %s", data)
		}
	}
}
