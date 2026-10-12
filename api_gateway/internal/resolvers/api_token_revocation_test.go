package resolvers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	signalmanclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/signalman"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	signalmanpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/signalman"

	"github.com/99designs/gqlgen/graphql/handler/testserver"
	"github.com/gorilla/websocket"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/anypb"
)

// signalmanBroker stands in for the region's Signalman replicas: every
// replica consumes every domain event, so an event published here reaches
// every open stream of its tenant and channel, whichever Bridge opened it.
type signalmanBroker struct {
	mu      sync.Mutex
	streams map[*fakeStream]struct{}
}

func newSignalmanBroker() *signalmanBroker {
	return &signalmanBroker{streams: map[*fakeStream]struct{}{}}
}

func (b *signalmanBroker) Open(ctx context.Context, addr string, key signalmanclient.StreamKey) (signalmanclient.EventStream, error) {
	stream := &fakeStream{key: key, addr: addr, events: make(chan *signalmanpb.SignalmanEvent, 16), done: make(chan struct{})}
	b.mu.Lock()
	b.streams[stream] = struct{}{}
	b.mu.Unlock()
	go func() {
		select {
		case <-ctx.Done():
			stream.Close()
		case <-stream.done:
		}
		b.mu.Lock()
		delete(b.streams, stream)
		b.mu.Unlock()
	}()
	return stream, nil
}

func (b *signalmanBroker) Close() error { return nil }

// publish delivers event to every open stream of tenantID on channel.
func (b *signalmanBroker) publish(t *testing.T, tenantID string, channel signalmanpb.Channel, event *signalmanpb.SignalmanEvent) {
	t.Helper()
	b.mu.Lock()
	targets := make([]*fakeStream, 0, len(b.streams))
	for stream := range b.streams {
		if stream.key.TenantID == tenantID && stream.key.Channel == channel {
			targets = append(targets, stream)
		}
	}
	b.mu.Unlock()
	for _, stream := range targets {
		stream.push(t, event)
	}
}

// waitStreams waits up to three seconds until n streams of tenantID on
// channel are open; a replica that opens none fails the close assertions.
func (b *signalmanBroker) waitStreams(t *testing.T, tenantID string, channel signalmanpb.Channel, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		b.mu.Lock()
		count := 0
		for stream := range b.streams {
			if stream.key.TenantID == tenantID && stream.key.Channel == channel {
				count++
			}
		}
		b.mu.Unlock()
		if count >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Logf("%d %s streams of tenant %s did not open", n, channel, tenantID)
}

func apiTokenRevokedEvent(t *testing.T, tenantID, tokenID string) *signalmanpb.SignalmanEvent {
	t.Helper()
	data, err := anypb.New(&publicv1.ApiTokenRevoked{TokenId: tokenID})
	if err != nil {
		t.Fatal(err)
	}
	return &signalmanpb.SignalmanEvent{
		EventType: signalmanpb.EventType_EVENT_TYPE_TENANT_EVENT,
		Channel:   signalmanpb.Channel_CHANNEL_EVENTS,
		TenantId:  &tenantID,
		Data: &signalmanpb.EventData{Payload: &signalmanpb.EventData_TenantEvent{TenantEvent: &signalmanpb.TenantEvent{
			Id: "evt-" + tokenID, Type: "api_token.revoked", Data: data,
		}}},
	}
}

// tokenValidator accepts every API token in tokens, keyed by the token value,
// whether or not it was revoked, so only the revocation path can close a
// socket.
type tokenValidator struct {
	commodorepb.UnimplementedInternalServiceServer
	tokens map[string]*commodorepb.ValidateAPITokenResponse
}

func (v *tokenValidator) ValidateAPIToken(_ context.Context, req *commodorepb.ValidateAPITokenRequest) (*commodorepb.ValidateAPITokenResponse, error) {
	if resp, ok := v.tokens[req.GetToken()]; ok {
		return resp, nil
	}
	return &commodorepb.ValidateAPITokenResponse{Valid: false}, nil
}

func validatorClients(t *testing.T, tokens map[string]*commodorepb.ValidateAPITokenResponse) *clients.ServiceClients {
	t.Helper()
	return internalServiceClients(t, &tokenValidator{tokens: tokens})
}

// internalServiceClients serves impl as Commodore's InternalService and
// returns service clients connected to it.
func internalServiceClients(t *testing.T, impl commodorepb.InternalServiceServer) *clients.ServiceClients {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := grpc.NewServer()
	commodorepb.RegisterInternalServiceServer(server, impl)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	client, err := commodore.NewGRPCClient(commodore.GRPCConfig{
		GRPCAddr: listener.Addr().String(), Timeout: 5 * time.Second, ServiceToken: "service-token", AllowInsecure: true,
	})
	if err != nil {
		t.Fatalf("commodore client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return &clients.ServiceClients{Commodore: client}
}

// bridgeReplica is one Bridge replica's WebSocket serving: its own
// subscription manager over the shared Signalman broker and its own socket
// registry, wired as Bridge's main wires them.
type bridgeReplica struct {
	sessions *middleware.WebsocketSessions
	url      string
}

func newBridgeReplica(t *testing.T, broker streamOpener, sc *clients.ServiceClients) bridgeReplica {
	t.Helper()
	sm := newSubscriptionManager(logging.NewLoggerWithService("test"), SubscriptionManagerConfig{SignalmanAddrs: []string{"signalman.internal:19005"}}, broker)
	t.Cleanup(func() { _ = sm.Shutdown() })
	sessions := middleware.NewWebsocketSessions(sm.WatchAPITokenRevocations)
	sm.SetCredentialSockets(sessions)
	go sm.WatchSessionRevocations(context.Background(), func() {})
	srv := testserver.New()
	srv.AddTransport(middleware.GraphQLWebsocketTransport(sc, []byte("jwt-secret"), nil, websocket.Upgrader{}, 0, sessions, middleware.DefaultWebsocketRevalidateInterval))
	httpSrv := httptest.NewServer(srv)
	t.Cleanup(httpSrv.Close)
	return bridgeReplica{sessions: sessions, url: strings.Replace(httpSrv.URL, "http://", "ws://", 1)}
}

func readWSMessageType(t *testing.T, conn *websocket.Conn, timeout time.Duration) string {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var msg struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("decode %s: %v", data, err)
	}
	return msg.Type
}

// openTokenSocket connects to replica with an API token and returns once a
// subscription on it is running.
func openTokenSocket(t *testing.T, replica bridgeReplica, token string) *websocket.Conn {
	t.Helper()
	dialer := websocket.Dialer{Subprotocols: []string{"graphql-transport-ws"}}
	conn, resp, err := dialer.Dial(replica.url, nil)
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
	if err := conn.WriteJSON(map[string]any{"id": "1", "type": "subscribe", "payload": map[string]any{"query": "subscription { name }"}}); err != nil {
		t.Fatalf("write subscribe: %v", err)
	}
	return conn
}

// socketCloseCode reads until the server closes conn or within passes, and
// returns the close code, or 0 when the connection stayed open.
func socketCloseCode(t *testing.T, conn *websocket.Conn, within time.Duration) int {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(within))
	for {
		_, _, err := conn.ReadMessage()
		if err == nil {
			continue
		}
		var closeErr *websocket.CloseError
		if errors.As(err, &closeErr) {
			return closeErr.Code
		}
		var netErr interface{ Timeout() bool }
		if errors.As(err, &netErr) && netErr.Timeout() {
			return 0
		}
		if errors.Is(err, io.EOF) {
			return -1
		}
		t.Fatalf("read: %v", err)
	}
}

// Revoking an API token through one Bridge replica closes the token's
// sockets on every replica with 4403 within two seconds, well before the
// minute-long re-check, and leaves the tenant's other tokens' sockets open.
func TestAPITokenRevocationClosesItsSocketsOnEveryBridgeReplica(t *testing.T) {
	const tenantID = "tenant-1"
	sc := validatorClients(t, map[string]*commodorepb.ValidateAPITokenResponse{
		"revoked-token-value": {Valid: true, UserId: "user-1", TenantId: tenantID, Role: "owner", TokenId: "token-1"},
		"other-token-value":   {Valid: true, UserId: "user-1", TenantId: tenantID, Role: "owner", TokenId: "token-2"},
	})
	broker := newSignalmanBroker()
	replicaA := newBridgeReplica(t, broker, sc)
	replicaB := newBridgeReplica(t, broker, sc)

	onA := openTokenSocket(t, replicaA, "revoked-token-value")
	onB := openTokenSocket(t, replicaB, "revoked-token-value")
	otherOnB := openTokenSocket(t, replicaB, "other-token-value")
	broker.waitStreams(t, tenantID, signalmanpb.Channel_CHANNEL_EVENTS, 2)

	// DoRevokeDeveloperToken closes the revoking replica's sockets once
	// Commodore committed the revocation; Commodore's api_token.revoked
	// reaches every Signalman replica.
	replicaA.sessions.CloseAPIToken(tenantID, "token-1")
	broker.publish(t, tenantID, signalmanpb.Channel_CHANNEL_EVENTS, apiTokenRevokedEvent(t, tenantID, "token-1"))

	if code := socketCloseCode(t, onA, 2*time.Second); code != middleware.WebsocketCloseForbidden {
		t.Fatalf("socket on the revoking replica closed with %d, want %d", code, middleware.WebsocketCloseForbidden)
	}
	if code := socketCloseCode(t, onB, 2*time.Second); code != middleware.WebsocketCloseForbidden {
		t.Fatalf("socket on the other replica closed with %d within 2s, want %d (0 = still open)", code, middleware.WebsocketCloseForbidden)
	}
	if code := socketCloseCode(t, otherOnB, 300*time.Millisecond); code != 0 {
		t.Fatalf("socket of another token closed with %d, want it open", code)
	}
}
