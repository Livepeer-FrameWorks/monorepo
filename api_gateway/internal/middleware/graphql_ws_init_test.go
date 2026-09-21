package middleware

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"frameworks/api_gateway/internal/clients"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"

	"github.com/99designs/gqlgen/graphql/handler/testserver"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/gorilla/websocket"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var wsInitSecret = []byte("ws-init-secret")

// wsInitClientsWith returns service clients whose Commodore answers every
// API-token validation with resp and err.
func wsInitClientsWith(t *testing.T, resp *commodorepb.ValidateAPITokenResponse, err error) *clients.ServiceClients {
	t.Helper()
	addr, cleanup := startInternalService(t, newFakeInternalService(resp, err))
	t.Cleanup(cleanup)
	client, dialErr := commodore.NewGRPCClient(commodore.GRPCConfig{
		GRPCAddr:      addr,
		Timeout:       5 * time.Second,
		ServiceToken:  "service-token",
		AllowInsecure: true,
	})
	if dialErr != nil {
		t.Fatalf("commodore client: %v", dialErr)
	}
	t.Cleanup(func() { _ = client.Close() })
	return &clients.ServiceClients{Commodore: client}
}

// wsInitClients returns service clients whose Commodore rejects every API token.
func wsInitClients(t *testing.T) *clients.ServiceClients {
	return wsInitClientsWith(t, &commodorepb.ValidateAPITokenResponse{Valid: false}, nil)
}

// wsOutcome is how the server answered connection_init: the first message
// type, or "close" with the close code and reason.
type wsOutcome struct {
	first  string
	code   int
	reason string
}

// dialGraphQLWS connects over graphql-transport-ws to a gqlgen server using
// Bridge's GraphQLWebsocketTransport and sends connection_init with payload.
// cookie, when set, is the access_token cookie Bridge captures at upgrade.
func dialGraphQLWS(t *testing.T, serviceClients *clients.ServiceClients, payload map[string]any, cookie string) wsOutcome {
	t.Helper()
	srv := testserver.New()
	srv.AddTransport(GraphQLWebsocketTransport(serviceClients, wsInitSecret, nil, websocket.Upgrader{}, 0))
	// Bridge's /graphql/ws route puts the upgrade's access_token cookie in the
	// request context before handing the request to gqlgen.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cookie != "" {
			r = r.WithContext(context.WithValue(r.Context(), ctxkeys.KeyWSCookieToken, cookie))
		}
		srv.ServeHTTP(w, r)
	})
	httpSrv := httptest.NewServer(handler)
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

	if err := conn.WriteJSON(map[string]any{"type": "connection_init", "payload": payload}); err != nil {
		t.Fatalf("write init: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			var closeErr *websocket.CloseError
			if errors.As(err, &closeErr) {
				return wsOutcome{first: "close", code: closeErr.Code, reason: closeErr.Text}
			}
			t.Fatalf("read: %v", err)
		}
		var msg struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("decode %s: %v", data, err)
		}
		if msg.Type == "ping" || msg.Type == "ka" {
			continue
		}
		return wsOutcome{first: msg.Type}
	}
}

// expiredSessionJWT is an interactive session token signed with wsInitSecret
// that expired a minute ago, the shape a browser holds after its 15-minute
// session lapses.
func expiredSessionJWT(t *testing.T, userID, tenantID string) string {
	t.Helper()
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]any{
		"user_id":   userID,
		"tenant_id": tenantID,
		"email":     "user@example.com",
		"role":      "owner",
		"iat":       time.Now().Add(-16 * time.Minute).Unix(),
		"exp":       time.Now().Add(-time.Minute).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	signing := header + "." + enc.EncodeToString(claims)
	mac := hmac.New(sha256.New, wsInitSecret)
	mac.Write([]byte(signing))
	token := signing + "." + enc.EncodeToString(mac.Sum(nil))
	if _, err := auth.ValidateInteractiveJWT(token, wsInitSecret); !errors.Is(err, auth.ErrExpiredJWT) {
		t.Fatalf("fixture token: ValidateInteractiveJWT error = %v, want ErrExpiredJWT", err)
	}
	return token
}

func requireForbiddenClose(t *testing.T, name string, got wsOutcome) {
	t.Helper()
	if got.first != "close" || got.code != WebsocketCloseForbidden {
		t.Fatalf("%s: server answered %+v, want close %d before connection_ack", name, got, WebsocketCloseForbidden)
	}
}

func TestGraphQLWebsocketInitRejectsInvalidBearerWithForbidden(t *testing.T) {
	got := dialGraphQLWS(t, wsInitClients(t), map[string]any{"Authorization": "Bearer not-a-real-token"}, "")
	requireForbiddenClose(t, "invalid bearer token", got)
}

func TestGraphQLWebsocketInitRejectsMalformedAuthorizationWithForbidden(t *testing.T) {
	got := dialGraphQLWS(t, wsInitClients(t), map[string]any{"Authorization": "Token abc"}, "")
	requireForbiddenClose(t, "malformed Authorization", got)
}

func TestGraphQLWebsocketInitRejectsDelegatedJWTWithForbidden(t *testing.T) {
	delegated, err := auth.GenerateDelegatedAPITokenJWT("user-1", "tenant-1", "", "owner", "token-1", []string{"streams:read"}, "quartermaster", wsInitSecret)
	if err != nil {
		t.Fatal(err)
	}
	got := dialGraphQLWS(t, wsInitClients(t), map[string]any{"Authorization": "Bearer " + delegated}, "")
	requireForbiddenClose(t, "delegated JWT", got)
}

func TestGraphQLWebsocketInitClosesRetryableWhenTheAuthBackendFails(t *testing.T) {
	for _, code := range []codes.Code{codes.Unavailable, codes.DeadlineExceeded, codes.Internal} {
		t.Run(code.String(), func(t *testing.T) {
			sc := wsInitClientsWith(t, nil, status.Error(code, "commodore down"))
			got := dialGraphQLWS(t, sc, map[string]any{"Authorization": "Bearer fw_api_token_value"}, "")
			if got.first != "close" || got.code != WebsocketCloseTryAgainLater {
				t.Fatalf("auth backend %s: server answered %+v, want close %d (retryable), not the auth close", code, got, WebsocketCloseTryAgainLater)
			}
		})
	}
}

// A browser that sends an expired session token in connectionParams while
// holding a fresh cookie is rejected: a present Authorization value names
// the identity the client asked for, and Bridge never substitutes another
// credential for it.
func TestGraphQLWebsocketInitRejectsExpiredParamsTokenEvenWithAValidCookie(t *testing.T) {
	fresh, err := auth.GenerateJWT("user-1", "tenant-1", "user@example.com", "owner", wsInitSecret)
	if err != nil {
		t.Fatal(err)
	}
	got := dialGraphQLWS(t, wsInitClients(t), map[string]any{"Authorization": "Bearer " + expiredSessionJWT(t, "user-1", "tenant-1")}, fresh)
	requireForbiddenClose(t, "expired params token with a valid cookie", got)
}

// The webapp reconnect after its session token expired: the client sends no
// Authorization value and the upgrade carries the refreshed HttpOnly cookie,
// so the connection is acknowledged as the cookie's user.
func TestGraphQLWebsocketReconnectAfterSessionExpiryAuthenticatesFromTheCookie(t *testing.T) {
	sc := wsInitClients(t)
	expired := expiredSessionJWT(t, "user-1", "tenant-1")
	if got := dialGraphQLWS(t, sc, map[string]any{}, expired); got.first != "connection_ack" {
		t.Fatalf("expired cookie: server answered %+v, want connection_ack as anonymous", got)
	}
	refreshed, err := auth.GenerateJWT("user-1", "tenant-1", "user@example.com", "owner", wsInitSecret)
	if err != nil {
		t.Fatal(err)
	}
	if got := dialGraphQLWS(t, sc, map[string]any{}, refreshed); got.first != "connection_ack" {
		t.Fatalf("refreshed cookie: server answered %+v, want connection_ack", got)
	}
	ctx := context.WithValue(context.Background(), ctxkeys.KeyWSCookieToken, refreshed)
	ctx, _, err = GraphQLWebsocketInit(sc, wsInitSecret, nil)(ctx, transport.InitPayload{})
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if user := GetUserFromContext(ctx); user == nil || user.UserID != "user-1" || user.TenantID != "tenant-1" {
		t.Fatalf("user = %#v, want user-1/tenant-1 from the cookie", user)
	}
}

func TestGraphQLWebsocketInitAcceptsMissingAuthorizationAsAnonymous(t *testing.T) {
	got := dialGraphQLWS(t, wsInitClients(t), map[string]any{}, "")
	if got.first != "connection_ack" {
		t.Fatalf("no Authorization: server answered %+v, want connection_ack", got)
	}
}

func TestGraphQLWebsocketInitKeepsInvalidCookieAnonymous(t *testing.T) {
	got := dialGraphQLWS(t, wsInitClients(t), map[string]any{}, "expired-cookie-token")
	if got.first != "connection_ack" {
		t.Fatalf("invalid cookie: server answered %+v, want connection_ack", got)
	}
}

func TestGraphQLWebsocketInitAcceptsValidJWT(t *testing.T) {
	token, err := auth.GenerateJWT("user-1", "tenant-1", "user@example.com", "admin", wsInitSecret)
	if err != nil {
		t.Fatal(err)
	}
	got := dialGraphQLWS(t, wsInitClients(t), map[string]any{"Authorization": "Bearer " + token}, "")
	if got.first != "connection_ack" {
		t.Fatalf("valid JWT: server answered %+v, want connection_ack", got)
	}

	ctx, _, err := GraphQLWebsocketInit(wsInitClients(t), wsInitSecret, nil)(context.Background(), transport.InitPayload{"Authorization": "Bearer " + token})
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	user := GetUserFromContext(ctx)
	if user == nil || user.UserID != "user-1" || user.TenantID != "tenant-1" {
		t.Fatalf("user = %#v, want user-1/tenant-1", user)
	}
}

func TestGraphQLWebsocketInitCookieFallbackAuthenticates(t *testing.T) {
	token, err := auth.GenerateJWT("user-2", "tenant-2", "cookie@example.com", "member", wsInitSecret)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), ctxkeys.KeyWSCookieToken, token)
	ctx, _, err = GraphQLWebsocketInit(wsInitClients(t), wsInitSecret, nil)(ctx, transport.InitPayload{})
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if user := GetUserFromContext(ctx); user == nil || user.TenantID != "tenant-2" {
		t.Fatalf("user = %#v, want tenant-2", user)
	}
}
