package middleware

import (
	"context"
	"errors"
	"sync"
	"time"

	"frameworks/api_gateway/internal/clients"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// DefaultWebsocketRevalidateInterval bounds how long a GraphQL WebSocket keeps
// serving after its credential stopped authenticating on a Bridge replica
// that did not itself handle the revocation.
const DefaultWebsocketRevalidateInterval = time.Minute

// websocketRevalidateTimeout bounds one re-check of a socket's credential.
const websocketRevalidateTimeout = 10 * time.Second

var (
	// ErrWebsocketCredentialRevoked closes an authenticated connection whose
	// credential no longer authenticates: a revoked or expired API token, or
	// a session JWT that no longer verifies.
	ErrWebsocketCredentialRevoked = &WebsocketInitError{Code: WebsocketCloseForbidden, Reason: "credential revoked"}
	// ErrWebsocketCredentialExpired closes an authenticated connection when
	// its session reaches its expiry. graphql-ws clients reconnect after
	// 4403, so the webapp reconnects with its refreshed cookie.
	ErrWebsocketCredentialExpired = &WebsocketInitError{Code: WebsocketCloseForbidden, Reason: "credential expired"}
)

// websocketIdentity is the credential a GraphQL WebSocket connection
// authenticated with at connection_init.
type websocketIdentity struct {
	// credential is the bearer token re-checked while the connection is open.
	// Wallet connections have none: re-running wallet login would mint a new
	// session, so they are bounded by expiresAt only.
	credential string
	tenantID   string
	// tokenID is the API token's ID; empty for sessions.
	tokenID   string
	expiresAt time.Time
}

type websocketIdentityKey struct{}

func withWebsocketIdentity(ctx context.Context, id websocketIdentity) context.Context {
	return context.WithValue(ctx, websocketIdentityKey{}, id)
}

func websocketIdentityFrom(ctx context.Context) (websocketIdentity, bool) {
	id, ok := ctx.Value(websocketIdentityKey{}).(websocketIdentity)
	return id, ok
}

func bearerIdentity(token string, result *AuthResult) websocketIdentity {
	id := websocketIdentity{credential: token, tenantID: result.TenantID, tokenID: result.TokenID}
	if result.ExpiresAt != nil {
		id.expiresAt = *result.ExpiresAt
	}
	return id
}

// WebsocketSessions tracks this replica's authenticated GraphQL WebSocket
// connections by API token, so a revocation handled here closes that token's
// connections at once. Revocations handled by another replica reach a
// connection through its periodic re-check instead. A nil *WebsocketSessions
// tracks nothing.
type WebsocketSessions struct {
	mu      sync.Mutex
	byToken map[websocketTokenKey]map[*websocketSession]struct{}
}

type websocketTokenKey struct {
	tenantID string
	tokenID  string
}

type websocketSession struct {
	close func(*WebsocketInitError)
}

// NewWebsocketSessions returns an empty registry.
func NewWebsocketSessions() *WebsocketSessions {
	return &WebsocketSessions{byToken: map[websocketTokenKey]map[*websocketSession]struct{}{}}
}

func (s *WebsocketSessions) register(key websocketTokenKey, session *websocketSession) func() {
	if s == nil || key.tokenID == "" {
		return func() {}
	}
	s.mu.Lock()
	set := s.byToken[key]
	if set == nil {
		set = map[*websocketSession]struct{}{}
		s.byToken[key] = set
	}
	set[session] = struct{}{}
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		delete(s.byToken[key], session)
		if len(s.byToken[key]) == 0 {
			delete(s.byToken, key)
		}
		s.mu.Unlock()
	}
}

// CloseAPIToken closes, with 4403, every connection on this replica that
// authenticated with the tenant's API token tokenID, and returns how many it
// closed.
func (s *WebsocketSessions) CloseAPIToken(tenantID, tokenID string) int {
	if s == nil || tenantID == "" || tokenID == "" {
		return 0
	}
	s.mu.Lock()
	sessions := make([]*websocketSession, 0, len(s.byToken[websocketTokenKey{tenantID: tenantID, tokenID: tokenID}]))
	for session := range s.byToken[websocketTokenKey{tenantID: tenantID, tokenID: tokenID}] {
		sessions = append(sessions, session)
	}
	s.mu.Unlock()
	for _, session := range sessions {
		session.close(ErrWebsocketCredentialRevoked)
	}
	return len(sessions)
}

// websocketIdentityWatch keeps an authenticated connection's identity valid
// for as long as the connection is open.
type websocketIdentityWatch struct {
	serviceClients *clients.ServiceClients
	jwtSecret      []byte
	logger         logging.Logger
	sessions       *WebsocketSessions
	interval       time.Duration
}

// watch closes the connection through closeConn when its session expires or
// a re-check finds the credential no longer authenticates, and returns when
// ctx, the connection's context, ends. A re-check that cannot reach the
// authentication backend keeps the connection: an unavailable backend says
// nothing about the credential.
func (w websocketIdentityWatch) watch(ctx context.Context, id websocketIdentity, closeConn func(*WebsocketInitError)) {
	unregister := w.sessions.register(websocketTokenKey{tenantID: id.tenantID, tokenID: id.tokenID}, &websocketSession{close: closeConn})
	defer unregister()

	var expiry <-chan time.Time
	if !id.expiresAt.IsZero() {
		timer := time.NewTimer(time.Until(id.expiresAt))
		defer timer.Stop()
		expiry = timer.C
	}
	var recheck <-chan time.Time
	if id.credential != "" && w.interval > 0 {
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()
		recheck = ticker.C
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-expiry:
			closeConn(ErrWebsocketCredentialExpired)
			return
		case <-recheck:
			// The connection's context carries the authenticated caller, which
			// outgoing clients forward; the re-check runs as Bridge itself.
			checkCtx, cancel := context.WithTimeout(context.Background(), websocketRevalidateTimeout)
			_, err := AuthenticateBearerToken(checkCtx, id.credential, w.serviceClients, w.jwtSecret)
			cancel()
			switch {
			case err == nil:
			case errors.Is(err, ErrAuthBackendUnavailable):
				if w.logger != nil {
					w.logger.WithError(err).Warn("WebSocket credential re-check could not reach the authentication backend")
				}
			default:
				closeConn(ErrWebsocketCredentialRevoked)
				return
			}
		}
	}
}
