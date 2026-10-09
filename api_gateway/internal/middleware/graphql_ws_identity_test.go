package middleware

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"frameworks/api_gateway/internal/clients"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"

	"github.com/99designs/gqlgen/graphql/handler/testserver"
	"github.com/gorilla/websocket"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// switchableInternalService answers API-token validation with whatever the
// test last set, so a token can stop authenticating while a socket is open.
type switchableInternalService struct {
	commodorepb.UnimplementedInternalServiceServer
	mu   sync.Mutex
	resp *commodorepb.ValidateAPITokenResponse
	err  error
}

func (s *switchableInternalService) set(resp *commodorepb.ValidateAPITokenResponse, err error) {
	s.mu.Lock()
	s.resp, s.err = resp, err
	s.mu.Unlock()
}

func (s *switchableInternalService) ValidateAPIToken(context.Context, *commodorepb.ValidateAPITokenRequest) (*commodorepb.ValidateAPITokenResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resp, s.err
}

func validAPIToken() *commodorepb.ValidateAPITokenResponse {
	return &commodorepb.ValidateAPITokenResponse{
		Valid: true, UserId: "user-1", TenantId: "tenant-1", Role: "owner",
		TokenId: "token-1", Permissions: []string{"streams:read"},
	}
}

func switchableClients(t *testing.T) (*clients.ServiceClients, *switchableInternalService) {
	t.Helper()
	svc := &switchableInternalService{resp: validAPIToken()}
	addr, cleanup := startInternalService(t, svc)
	t.Cleanup(cleanup)
	client, err := commodore.NewGRPCClient(commodore.GRPCConfig{
		GRPCAddr:      addr,
		Timeout:       5 * time.Second,
		ServiceToken:  "service-token",
		AllowInsecure: true,
	})
	if err != nil {
		t.Fatalf("commodore client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return &clients.ServiceClients{Commodore: client}, svc
}

// openSubscription connects over graphql-transport-ws with authorization,
// starts a subscription, and returns once the first event arrived on it.
func openSubscription(t *testing.T, sc *clients.ServiceClients, authorization string, sessions *WebsocketSessions, revalidateEvery time.Duration) (*websocket.Conn, *testserver.TestServer) {
	t.Helper()
	srv := testserver.New()
	srv.AddTransport(GraphQLWebsocketTransport(sc, wsInitSecret, nil, websocket.Upgrader{}, 0, sessions, revalidateEvery))
	httpSrv := httptest.NewServer(srv)
	t.Cleanup(httpSrv.Close)

	dialer := websocket.Dialer{Subprotocols: []string{"graphql-transport-ws"}}
	conn, resp, err := dialer.Dial(strings.Replace(httpSrv.URL, "http://", "ws://", 1), nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	if err := conn.WriteJSON(map[string]any{"type": "connection_init", "payload": map[string]any{"Authorization": authorization}}); err != nil {
		t.Fatalf("write init: %v", err)
	}
	if got := readWSType(t, conn, 2*time.Second); got != "connection_ack" {
		t.Fatalf("init answered %q, want connection_ack", got)
	}
	if err := conn.WriteJSON(map[string]any{"id": "1", "type": "subscribe", "payload": map[string]any{"query": "subscription { name }"}}); err != nil {
		t.Fatalf("write subscribe: %v", err)
	}
	srv.SendNextSubscriptionMessage()
	if got := readWSType(t, conn, 2*time.Second); got != "next" {
		t.Fatalf("subscription answered %q, want next", got)
	}
	return conn, srv
}

// awaitClose reads until the server closes the connection or within passes,
// and returns the close code, or 0 when the connection stayed open. A data
// message after the credential ended fails the test.
func awaitClose(t *testing.T, conn *websocket.Conn, within time.Duration) int {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(within))
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			var closeErr *websocket.CloseError
			if errors.As(err, &closeErr) {
				return closeErr.Code
			}
			var netErr interface{ Timeout() bool }
			if errors.As(err, &netErr) && netErr.Timeout() {
				return 0
			}
			t.Fatalf("read: %v", err)
		}
		var msg struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("decode %s: %v", data, err)
		}
		if msg.Type == "next" {
			t.Fatalf("socket delivered an event after its credential ended: %s", data)
		}
	}
}

// A socket opened with an API token that is then revoked on another replica
// closes with 4403 at the next re-check, ending its subscriptions.
func TestGraphQLWebsocketClosesWhenItsAPITokenStopsAuthenticating(t *testing.T) {
	sc, commodoreSvc := switchableClients(t)
	const interval = 100 * time.Millisecond
	conn, _ := openSubscription(t, sc, "Bearer fw_api_token_value", nil, interval)

	commodoreSvc.set(&commodorepb.ValidateAPITokenResponse{Valid: false}, nil)
	if code := awaitClose(t, conn, 20*interval); code != WebsocketCloseForbidden {
		t.Fatalf("after revocation the socket closed with %d, want %d within %s", code, WebsocketCloseForbidden, 20*interval)
	}
}

// A revocation handled on this replica closes the token's sockets at once,
// without waiting for a re-check, and only the revoking tenant's.
func TestGraphQLWebsocketClosesAtOnceWhenItsAPITokenIsRevokedHere(t *testing.T) {
	sc, _ := switchableClients(t)
	sessions := NewWebsocketSessions()
	conn, _ := openSubscription(t, sc, "Bearer fw_api_token_value", sessions, 0)

	if n := sessions.CloseAPIToken("tenant-2", "token-1"); n != 0 {
		t.Fatalf("another tenant's revocation closed %d sockets, want 0", n)
	}
	if n := sessions.CloseAPIToken("tenant-1", "token-1"); n != 1 {
		t.Fatalf("revocation closed %d sockets, want 1", n)
	}
	if code := awaitClose(t, conn, 2*time.Second); code != WebsocketCloseForbidden {
		t.Fatalf("after revocation the socket closed with %d, want %d", code, WebsocketCloseForbidden)
	}
}

// A re-check that cannot reach Commodore says nothing about the token, so
// the socket stays open.
func TestGraphQLWebsocketKeepsTheSocketWhenTheRecheckBackendFails(t *testing.T) {
	sc, commodoreSvc := switchableClients(t)
	const interval = 50 * time.Millisecond
	conn, srv := openSubscription(t, sc, "Bearer fw_api_token_value", nil, interval)

	commodoreSvc.set(nil, status.Error(codes.Unavailable, "commodore down"))
	time.Sleep(6 * interval)
	srv.SendNextSubscriptionMessage()
	if got := readWSType(t, conn, 2*time.Second); got != "next" {
		t.Fatalf("socket answered %q while Commodore was unavailable, want next", got)
	}
}

// sessionJWTExpiringAt is an interactive session token signed with
// wsInitSecret that expires at exp.
func sessionJWTExpiringAt(t *testing.T, exp time.Time) string {
	t.Helper()
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]any{
		"user_id":   "user-1",
		"tenant_id": "tenant-1",
		"email":     "user@example.com",
		"role":      "owner",
		"iat":       time.Now().Unix(),
		"exp":       exp.Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	signing := header + "." + enc.EncodeToString(claims)
	mac := hmac.New(sha256.New, wsInitSecret)
	mac.Write([]byte(signing))
	token := signing + "." + enc.EncodeToString(mac.Sum(nil))
	if _, err := auth.ValidateInteractiveJWT(token, wsInitSecret); err != nil {
		t.Fatalf("fixture token: %v", err)
	}
	return token
}

// A socket authenticated with a session JWT closes with 4403 when the
// session expires, even with no re-check configured.
func TestGraphQLWebsocketClosesAtSessionExpiry(t *testing.T) {
	sc, _ := switchableClients(t)
	exp := time.Now().Add(2 * time.Second).Truncate(time.Second)
	conn, _ := openSubscription(t, sc, "Bearer "+sessionJWTExpiringAt(t, exp), nil, 0)

	code := awaitClose(t, conn, time.Until(exp)+2*time.Second)
	if code != WebsocketCloseForbidden {
		t.Fatalf("at session expiry the socket closed with %d, want %d", code, WebsocketCloseForbidden)
	}
	if early := time.Until(exp); early > 0 {
		t.Fatalf("socket closed %s before its session expired", early)
	}
}
