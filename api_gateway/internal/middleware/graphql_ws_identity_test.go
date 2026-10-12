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

	"github.com/99designs/gqlgen/graphql"
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
	// session answers ValidateSession; nil answers Unimplemented, as a
	// Commodore without session checks does.
	session    *commodorepb.ValidateSessionResponse
	sessionErr error
}

func (s *switchableInternalService) set(resp *commodorepb.ValidateAPITokenResponse, err error) {
	s.mu.Lock()
	s.resp, s.err = resp, err
	s.mu.Unlock()
}

func (s *switchableInternalService) setSession(valid bool, err error) {
	s.mu.Lock()
	s.session, s.sessionErr = &commodorepb.ValidateSessionResponse{Valid: valid}, err
	s.mu.Unlock()
}

func (s *switchableInternalService) ValidateAPIToken(context.Context, *commodorepb.ValidateAPITokenRequest) (*commodorepb.ValidateAPITokenResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resp, s.err
}

func (s *switchableInternalService) ValidateSession(ctx context.Context, req *commodorepb.ValidateSessionRequest) (*commodorepb.ValidateSessionResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session == nil && s.sessionErr == nil {
		return s.UnimplementedInternalServiceServer.ValidateSession(ctx, req)
	}
	if s.sessionErr != nil {
		return nil, s.sessionErr
	}
	return s.session, nil
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
	sessions := NewWebsocketSessions(nil)
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

// recordingWatch records the tenants it follows and lets the test report a
// revocation stream as (re)subscribed.
type recordingWatch struct {
	mu      sync.Mutex
	started chan string
	stopped chan string
	resumed map[string]func(RevocationKind)
}

func newRecordingWatch() *recordingWatch {
	return &recordingWatch{started: make(chan string, 8), stopped: make(chan string, 8), resumed: map[string]func(RevocationKind){}}
}

func (w *recordingWatch) watch(ctx context.Context, tenantID string, resumed func(RevocationKind)) {
	w.mu.Lock()
	w.resumed[tenantID] = resumed
	w.mu.Unlock()
	w.started <- tenantID
	<-ctx.Done()
	w.stopped <- tenantID
}

func (w *recordingWatch) resume(tenantID string, kind RevocationKind) {
	w.mu.Lock()
	resumed := w.resumed[tenantID]
	w.mu.Unlock()
	resumed(kind)
}

func receiveTenant(t *testing.T, ch <-chan string, what string) string {
	t.Helper()
	select {
	case tenantID := <-ch:
		return tenantID
	case <-time.After(2 * time.Second):
		t.Fatalf("watch was not %s", what)
		return ""
	}
}

// The registry watches a tenant's revocations while the tenant has an
// API-token socket here, closes the revoked token's sockets with 4403 when a
// revocation arrives, and stops watching once the last of them closed.
func TestGraphQLWebsocketWatchesRevocationsWhileTheTenantHasSockets(t *testing.T) {
	sc, _ := switchableClients(t)
	watch := newRecordingWatch()
	sessions := NewWebsocketSessions(watch.watch)
	conn, _ := openSubscription(t, sc, "Bearer fw_api_token_value", sessions, 0)

	if got := receiveTenant(t, watch.started, "started"); got != "tenant-1" {
		t.Fatalf("watch started for %q, want tenant-1", got)
	}
	if n := sessions.CloseAPIToken("tenant-1", "token-1"); n != 1 {
		t.Fatalf("revocation closed %d sockets, want 1", n)
	}
	if code := awaitClose(t, conn, 2*time.Second); code != WebsocketCloseForbidden {
		t.Fatalf("after a revocation the socket closed with %d, want %d", code, WebsocketCloseForbidden)
	}
	if got := receiveTenant(t, watch.stopped, "stopped"); got != "tenant-1" {
		t.Fatalf("watch stopped for %q, want tenant-1", got)
	}
}

// A session socket is watched too: signing out ends it on every replica.
func TestGraphQLWebsocketSessionSocketIsWatched(t *testing.T) {
	sc, _ := switchableClients(t)
	watch := newRecordingWatch()
	sessions := NewWebsocketSessions(watch.watch)
	_, _ = openSubscription(t, sc, "Bearer "+sessionJWTIssuedAt(t, time.Now(), time.Now().Add(time.Hour)), sessions, 0)

	if got := receiveTenant(t, watch.started, "started"); got != "tenant-1" {
		t.Fatalf("watch started for %q, want tenant-1", got)
	}
}

// Revoking a user's sessions closes the sockets of the session tokens issued
// in or before the revocation's second with 4403 and leaves a socket of a
// session opened later, and other users' sockets, open.
func TestGraphQLWebsocketClosesTheSignedOutUsersSessionSockets(t *testing.T) {
	sc, _ := switchableClients(t)
	sessions := NewWebsocketSessions(nil)
	revokedAt := time.Now().Truncate(time.Second)
	before, _ := openSubscription(t, sc, "Bearer "+sessionJWTIssuedAt(t, revokedAt.Add(-time.Minute), revokedAt.Add(time.Hour)), sessions, 0)
	sameSecond, _ := openSubscription(t, sc, "Bearer "+sessionJWTIssuedAt(t, revokedAt, revokedAt.Add(time.Hour)), sessions, 0)
	later, _ := openSubscription(t, sc, "Bearer "+sessionJWTIssuedAt(t, revokedAt.Add(time.Second), revokedAt.Add(time.Hour)), sessions, 0)

	if n := sessions.CloseUserSessions("tenant-1", "user-2", revokedAt); n != 0 {
		t.Fatalf("another user's revocation closed %d sockets, want 0", n)
	}
	if n := sessions.CloseUserSessions("tenant-1", "user-1", revokedAt.Add(500*time.Millisecond)); n != 2 {
		t.Fatalf("revocation closed %d sockets, want 2", n)
	}
	for name, conn := range map[string]*websocket.Conn{"issued before": before, "issued in the same second": sameSecond} {
		if code := awaitClose(t, conn, 2*time.Second); code != WebsocketCloseForbidden {
			t.Fatalf("socket of a session %s the revocation closed with %d, want %d", name, code, WebsocketCloseForbidden)
		}
	}
	if code := awaitClose(t, later, 300*time.Millisecond); code != 0 {
		t.Fatalf("socket of a session opened after the revocation closed with %d, want it open", code)
	}
}

// A revocation that reached this replica after a socket's credential was
// checked, but before the socket registered, refuses the socket at
// connection_init.
func TestGraphQLWebsocketRefusesACredentialRevokedWhileItConnected(t *testing.T) {
	sc, _ := switchableClients(t)
	sessions := NewWebsocketSessions(nil)
	sessions.CloseAPIToken("tenant-1", "token-1")
	issued := time.Now().Add(-time.Minute)
	sessions.CloseUserSessions("tenant-1", "user-1", issued)

	for name, authorization := range map[string]string{
		"revoked API token":  "Bearer fw_api_token_value",
		"signed-out session": "Bearer " + sessionJWTIssuedAt(t, issued, time.Now().Add(time.Hour)),
	} {
		outcome := dialGraphQLWSWith(t, sc, sessions, authorization)
		if outcome.first != "close" || outcome.code != WebsocketCloseForbidden {
			t.Fatalf("%s: connection_init answered %+v, want close %d", name, outcome, WebsocketCloseForbidden)
		}
	}
	if outcome := dialGraphQLWSWith(t, sc, sessions, "Bearer "+sessionJWTIssuedAt(t, time.Now().Add(time.Second), time.Now().Add(time.Hour))); outcome.first != "connection_ack" {
		t.Fatalf("a session opened after the revocation: connection_init answered %+v, want connection_ack", outcome)
	}
}

// A signed-out session's token still verifies until it expires, but Commodore
// reports it revoked: connection_init refuses it with 4403, and a socket
// opened before the sign-out closes at its next re-check.
func TestGraphQLWebsocketRejectsASignedOutSessionToken(t *testing.T) {
	sc, commodoreSvc := switchableClients(t)
	token := "Bearer " + sessionJWTIssuedAt(t, time.Now(), time.Now().Add(time.Hour))
	const interval = 100 * time.Millisecond
	commodoreSvc.setSession(true, nil)
	conn, _ := openSubscription(t, sc, token, nil, interval)

	commodoreSvc.setSession(false, nil)
	if code := awaitClose(t, conn, 20*interval); code != WebsocketCloseForbidden {
		t.Fatalf("after sign-out the socket closed with %d, want %d at its re-check", code, WebsocketCloseForbidden)
	}
	if outcome := dialGraphQLWSWith(t, sc, nil, token); outcome.first != "close" || outcome.code != WebsocketCloseForbidden {
		t.Fatalf("connection_init with a signed-out session answered %+v, want close %d", outcome, WebsocketCloseForbidden)
	}
}

// When Commodore cannot say whether a session was signed out, the locally
// verified session token is served.
func TestGraphQLWebsocketAcceptsASessionTokenWhenTheSessionCheckIsUnavailable(t *testing.T) {
	sc, commodoreSvc := switchableClients(t)
	token := "Bearer " + sessionJWTIssuedAt(t, time.Now(), time.Now().Add(time.Hour))
	if outcome := dialGraphQLWSWith(t, sc, nil, token); outcome.first != "connection_ack" {
		t.Fatalf("with a Commodore that has no session check, connection_init answered %+v, want connection_ack", outcome)
	}
	commodoreSvc.setSession(false, status.Error(codes.Unavailable, "commodore down"))
	if outcome := dialGraphQLWSWith(t, sc, nil, token); outcome.first != "connection_ack" {
		t.Fatalf("with Commodore unavailable, connection_init answered %+v, want connection_ack", outcome)
	}
}

// Closing a socket for a revoked credential closes its revocation gate first,
// so no subscription delivers an event on it from then on.
func TestGraphQLWebsocketGateClosesBeforeTheSocket(t *testing.T) {
	sc, _ := switchableClients(t)
	sessions := NewWebsocketSessions(nil)
	gates := make(chan (<-chan struct{}), 1)
	srv := testserver.New()
	srv.AroundOperations(func(ctx context.Context, next graphql.OperationHandler) graphql.ResponseHandler {
		gates <- WebsocketCredentialRevoked(ctx)
		return next(ctx)
	})
	srv.AddTransport(GraphQLWebsocketTransport(sc, wsInitSecret, nil, websocket.Upgrader{}, 0, sessions, 0))
	httpSrv := httptest.NewServer(srv)
	t.Cleanup(httpSrv.Close)
	conn := dialAndInit(t, strings.Replace(httpSrv.URL, "http://", "ws://", 1), "Bearer fw_api_token_value")
	if err := conn.WriteJSON(map[string]any{"id": "1", "type": "subscribe", "payload": map[string]any{"query": "subscription { name }"}}); err != nil {
		t.Fatalf("write subscribe: %v", err)
	}
	var gate <-chan struct{}
	select {
	case gate = <-gates:
	case <-time.After(2 * time.Second):
		t.Fatal("the subscription did not start")
	}
	if gate == nil {
		t.Fatal("an authenticated subscription has no revocation gate")
	}
	select {
	case <-gate:
		t.Fatal("the gate is closed before any revocation")
	default:
	}
	sessions.CloseAPIToken("tenant-1", "token-1")
	select {
	case <-gate:
	default:
		t.Fatal("CloseAPIToken returned before the socket's gate closed")
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

// sessionJWTExpiringAt is an interactive session token of user-1 signed with
// wsInitSecret that expires at exp.
func sessionJWTExpiringAt(t *testing.T, exp time.Time) string {
	t.Helper()
	return sessionJWTIssuedAt(t, time.Now(), exp)
}

// dialGraphQLWSWith sends connection_init with authorization to a server
// tracking its connections in sessions, and returns how it answered.
func dialGraphQLWSWith(t *testing.T, sc *clients.ServiceClients, sessions *WebsocketSessions, authorization string) wsOutcome {
	t.Helper()
	return dialGraphQLWSSessions(t, sc, sessions, map[string]any{"Authorization": authorization}, "")
}

// dialAndInit connects to url and returns once connection_init with
// authorization was acknowledged.
func dialAndInit(t *testing.T, url, authorization string) *websocket.Conn {
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
	if err := conn.WriteJSON(map[string]any{"type": "connection_init", "payload": map[string]any{"Authorization": authorization}}); err != nil {
		t.Fatalf("write init: %v", err)
	}
	if got := readWSType(t, conn, 2*time.Second); got != "connection_ack" {
		t.Fatalf("init answered %q, want connection_ack", got)
	}
	return conn
}

// sessionJWTIssuedAt is an interactive session token of user-1 signed with
// wsInitSecret, issued at iat and expiring at exp.
func sessionJWTIssuedAt(t *testing.T, iat, exp time.Time) string {
	t.Helper()
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]any{
		"user_id":   "user-1",
		"tenant_id": "tenant-1",
		"email":     "user@example.com",
		"role":      "owner",
		"iat":       iat.Unix(),
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
