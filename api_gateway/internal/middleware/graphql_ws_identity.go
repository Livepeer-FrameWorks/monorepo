package middleware

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"frameworks/api_gateway/internal/clients"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// DefaultWebsocketRevalidateInterval bounds how long a GraphQL WebSocket keeps
// serving after its credential stopped authenticating when no revocation
// event reached this Bridge replica.
const DefaultWebsocketRevalidateInterval = time.Minute

// websocketRevalidateTimeout bounds one re-check of a socket's credential.
const websocketRevalidateTimeout = 10 * time.Second

var (
	// ErrWebsocketCredentialRevoked closes an authenticated connection whose
	// credential no longer authenticates: a revoked or expired API token, a
	// session JWT that no longer verifies, or a signed-out session.
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
	tokenID string
	// userID and issuedAt identify a session token; a revocation of the
	// user's sessions covers the tokens issued in or before its second.
	userID    string
	issuedAt  time.Time
	expiresAt time.Time
}

func (id websocketIdentity) kind() (RevocationKind, bool) {
	switch {
	case id.credential == "":
		return 0, false
	case id.tokenID != "":
		return RevocationAPITokens, true
	case id.userID != "":
		return RevocationSessions, true
	}
	return 0, false
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
	if result.AuthType == "jwt" {
		id.userID = result.UserID
		if result.IssuedAt != nil {
			id.issuedAt = *result.IssuedAt
		}
	}
	if result.ExpiresAt != nil {
		id.expiresAt = *result.ExpiresAt
	}
	return id
}

// websocketGate is closed when its connection's credential stops
// authenticating, before the connection itself closes.
type websocketGate struct {
	revoked chan struct{}
	once    sync.Once
}

func newWebsocketGate() *websocketGate {
	return &websocketGate{revoked: make(chan struct{})}
}

func (g *websocketGate) close() {
	g.once.Do(func() { close(g.revoked) })
}

type websocketGateKey struct{}

// WebsocketCredentialRevoked returns a channel that is closed once the
// credential of the GraphQL WebSocket connection ctx belongs to stopped
// authenticating; nil outside an authenticated connection. Subscriptions
// deliver no event once it is closed: a revocation closes it before the event
// announcing the revocation is passed to any subscription, so a socket never
// receives an event from after its revocation reached this replica.
func WebsocketCredentialRevoked(ctx context.Context) <-chan struct{} {
	if gate, ok := ctx.Value(websocketGateKey{}).(*websocketGate); ok {
		return gate.revoked
	}
	return nil
}

// RevocationKind names the revocations a feed carries.
type RevocationKind int

const (
	// RevocationAPITokens is api_token.revoked, on the tenant's public events.
	RevocationAPITokens RevocationKind = iota
	// RevocationSessions is user.sessions_revoked, the internal event that
	// ends every session of a user.
	RevocationSessions
	revocationKinds
)

// CredentialRevocationWatch keeps this replica following tenantID's
// credential revocations, committed through any Bridge replica, until ctx
// ends. The revocations themselves reach the WebsocketSessions through
// CloseAPIToken and CloseUserSessions. resumed(kind) is called whenever the
// stream that carries kind's revocations becomes subscribed (first and after
// every reconnect): a revocation committed before that may never have
// arrived.
type CredentialRevocationWatch func(ctx context.Context, tenantID string, resumed func(RevocationKind))

// WebsocketSessions tracks this replica's GraphQL WebSocket connections that
// authenticated with an API token or a session token, so a revocation closes
// them at once. A revocation handled here closes them directly; one handled
// by another replica arrives through the watch, which runs for every tenant
// with such a connection here. Whenever the watch's stream (re)subscribes,
// the tenant's credentials of that kind are re-checked once, so a revocation
// committed while the stream was not subscribed closes its connections then.
// Each connection's periodic re-check covers anything else. A nil
// *WebsocketSessions tracks nothing.
type WebsocketSessions struct {
	watch CredentialRevocationWatch

	mu sync.Mutex
	// check re-checks a credential: nil when it authenticates, an error
	// wrapping ErrAuthBackendUnavailable when it could not be checked.
	check   func(context.Context, websocketIdentity) error
	byToken map[websocketTokenKey]map[*websocketSession]struct{}
	byUser  map[websocketUserKey]map[*websocketSession]struct{}
	tenants map[string]*tenantRevocationWatch
	// Revocations seen in the last DefaultWebsocketRevalidateInterval, for a
	// connection whose credential was checked before the revocation and that
	// registers after it arrived.
	revokedTokens map[websocketTokenKey]time.Time
	revokedUsers  map[websocketUserKey]userRevocation
}

type websocketTokenKey struct {
	tenantID string
	tokenID  string
}

type websocketUserKey struct {
	tenantID string
	userID   string
}

type userRevocation struct {
	revokedAt time.Time
	seenAt    time.Time
}

type websocketSession struct {
	id    websocketIdentity
	close func(*WebsocketInitError)
}

// tenantRevocationWatch is the watch of one tenant's revocations, kept while
// the tenant has tracked connections on this replica.
type tenantRevocationWatch struct {
	sessions map[*websocketSession]struct{}
	stop     context.CancelFunc
	// revalidating is set while a goroutine re-checks the kinds in pending;
	// a resume during that run is queued, never run concurrently.
	revalidating bool
	pending      [revocationKinds]bool
}

// NewWebsocketSessions returns an empty registry that follows revocations
// from other replicas through watch; a nil watch follows none.
func NewWebsocketSessions(watch CredentialRevocationWatch) *WebsocketSessions {
	return &WebsocketSessions{
		watch:         watch,
		byToken:       map[websocketTokenKey]map[*websocketSession]struct{}{},
		byUser:        map[websocketUserKey]map[*websocketSession]struct{}{},
		tenants:       map[string]*tenantRevocationWatch{},
		revokedTokens: map[websocketTokenKey]time.Time{},
		revokedUsers:  map[websocketUserKey]userRevocation{},
	}
}

// useCheck sets how a resumed watch re-checks credentials.
func (s *WebsocketSessions) useCheck(check func(context.Context, websocketIdentity) error) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.check = check
	s.mu.Unlock()
}

// register tracks a connection and returns its unregister function. revoked
// is true when a revocation of the connection's credential already reached
// this replica; the connection is then not tracked and must be refused.
func (s *WebsocketSessions) register(session *websocketSession) (unregister func(), revoked bool) {
	kind, tracked := session.id.kind()
	if s == nil || !tracked || session.id.tenantID == "" {
		return func() {}, false
	}
	id := session.id
	tokenKey := websocketTokenKey{tenantID: id.tenantID, tokenID: id.tokenID}
	userKey := websocketUserKey{tenantID: id.tenantID, userID: id.userID}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(time.Now())
	switch kind {
	case RevocationAPITokens:
		if _, ok := s.revokedTokens[tokenKey]; ok {
			return func() {}, true
		}
		addSession(s.byToken, tokenKey, session)
	case RevocationSessions:
		if rev, ok := s.revokedUsers[userKey]; ok && sessionRevokedBy(id, rev.revokedAt) {
			return func() {}, true
		}
		addSession(s.byUser, userKey, session)
	}
	s.watchTenantLocked(id.tenantID, session)
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		switch kind {
		case RevocationAPITokens:
			removeSession(s.byToken, tokenKey, session)
		case RevocationSessions:
			removeSession(s.byUser, userKey, session)
		}
		s.unwatchTenantLocked(id.tenantID, session)
	}, false
}

func addSession[K comparable](index map[K]map[*websocketSession]struct{}, key K, session *websocketSession) {
	set := index[key]
	if set == nil {
		set = map[*websocketSession]struct{}{}
		index[key] = set
	}
	set[session] = struct{}{}
}

func removeSession[K comparable](index map[K]map[*websocketSession]struct{}, key K, session *websocketSession) {
	delete(index[key], session)
	if len(index[key]) == 0 {
		delete(index, key)
	}
}

// sessionRevokedBy reports whether a revocation of the user's sessions at
// revokedAt covers id's session token. Token issue times have whole-second
// precision, so a token issued in the revocation's second is covered.
func sessionRevokedBy(id websocketIdentity, revokedAt time.Time) bool {
	return id.issuedAt.Unix() <= revokedAt.Unix()
}

func (s *WebsocketSessions) watchTenantLocked(tenantID string, session *websocketSession) {
	if watch := s.tenants[tenantID]; watch != nil {
		watch.sessions[session] = struct{}{}
		return
	}
	watch := &tenantRevocationWatch{sessions: map[*websocketSession]struct{}{session: {}}, stop: func() {}}
	s.tenants[tenantID] = watch
	if s.watch == nil {
		return
	}
	ctx, stop := context.WithCancel(context.Background())
	watch.stop = stop
	go s.watch(ctx, tenantID, func(kind RevocationKind) { s.resume(tenantID, watch, kind) })
}

func (s *WebsocketSessions) unwatchTenantLocked(tenantID string, session *websocketSession) {
	watch := s.tenants[tenantID]
	if watch == nil {
		return
	}
	delete(watch.sessions, session)
	if len(watch.sessions) > 0 {
		return
	}
	watch.stop()
	delete(s.tenants, tenantID)
}

// resume re-checks, once, the credentials of kind held by watch's tenant on
// this replica.
func (s *WebsocketSessions) resume(tenantID string, watch *tenantRevocationWatch, kind RevocationKind) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tenants[tenantID] != watch {
		return
	}
	watch.pending[kind] = true
	if watch.revalidating {
		return
	}
	watch.revalidating = true
	go s.revalidate(tenantID, watch)
}

// revalidate runs the queued re-checks of watch one kind at a time until
// none is left. Connections sharing a credential share one check.
func (s *WebsocketSessions) revalidate(tenantID string, watch *tenantRevocationWatch) {
	for {
		s.mu.Lock()
		kind, found := RevocationKind(0), false
		for k := range revocationKinds {
			if watch.pending[k] {
				kind, found = k, true
				watch.pending[k] = false
				break
			}
		}
		if !found || s.tenants[tenantID] != watch {
			watch.revalidating = false
			s.mu.Unlock()
			return
		}
		check := s.check
		byCredential := map[string][]*websocketSession{}
		for session := range watch.sessions {
			if k, ok := session.id.kind(); ok && k == kind {
				byCredential[session.id.credential] = append(byCredential[session.id.credential], session)
			}
		}
		s.mu.Unlock()
		if check == nil {
			continue
		}
		for _, sessions := range byCredential {
			ctx, cancel := context.WithTimeout(context.Background(), websocketRevalidateTimeout)
			err := check(ctx, sessions[0].id)
			cancel()
			if err == nil || errors.Is(err, ErrAuthBackendUnavailable) {
				continue
			}
			for _, session := range sessions {
				session.close(ErrWebsocketCredentialRevoked)
			}
		}
	}
}

// pruneLocked forgets revocations older than the periodic re-check, which
// covers any connection they could still concern.
func (s *WebsocketSessions) pruneLocked(now time.Time) {
	cutoff := now.Add(-DefaultWebsocketRevalidateInterval)
	for key, seenAt := range s.revokedTokens {
		if seenAt.Before(cutoff) {
			delete(s.revokedTokens, key)
		}
	}
	for key, rev := range s.revokedUsers {
		if rev.seenAt.Before(cutoff) {
			delete(s.revokedUsers, key)
		}
	}
}

// CloseAPIToken closes, with 4403, every connection on this replica that
// authenticated with the tenant's API token tokenID, and returns how many it
// closed. Each connection stops delivering events before this returns.
func (s *WebsocketSessions) CloseAPIToken(tenantID, tokenID string) int {
	if s == nil || tenantID == "" || tokenID == "" {
		return 0
	}
	key := websocketTokenKey{tenantID: tenantID, tokenID: tokenID}
	s.mu.Lock()
	now := time.Now()
	s.pruneLocked(now)
	s.revokedTokens[key] = now
	sessions := make([]*websocketSession, 0, len(s.byToken[key]))
	for session := range s.byToken[key] {
		sessions = append(sessions, session)
	}
	s.mu.Unlock()
	for _, session := range sessions {
		session.close(ErrWebsocketCredentialRevoked)
	}
	return len(sessions)
}

// CloseUserSessions closes, with 4403, every connection on this replica that
// authenticated with a session token of the tenant's user userID issued in or
// before revokedAt's second, and returns how many it closed. A connection
// opened with a later session stays open. Each closed connection stops
// delivering events before this returns.
func (s *WebsocketSessions) CloseUserSessions(tenantID, userID string, revokedAt time.Time) int {
	if s == nil || tenantID == "" || userID == "" {
		return 0
	}
	key := websocketUserKey{tenantID: tenantID, userID: userID}
	s.mu.Lock()
	now := time.Now()
	s.pruneLocked(now)
	if prev, ok := s.revokedUsers[key]; !ok || revokedAt.After(prev.revokedAt) {
		s.revokedUsers[key] = userRevocation{revokedAt: revokedAt, seenAt: now}
	}
	var sessions []*websocketSession
	for session := range s.byUser[key] {
		if sessionRevokedBy(session.id, revokedAt) {
			sessions = append(sessions, session)
		}
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

// check re-checks a connection's credential: nil when it still
// authenticates, an error wrapping ErrAuthBackendUnavailable when it could
// not be checked, and any other error when it no longer authenticates.
func (w websocketIdentityWatch) check(ctx context.Context, id websocketIdentity) error {
	_, err := authenticateWebsocketBearer(ctx, id.credential, w.serviceClients, w.jwtSecret)
	return err
}

// authenticateWebsocketBearer authenticates a GraphQL WebSocket's bearer
// token. A session token must also still be live in Commodore: a signed-out
// session's token keeps verifying until it expires, but no longer holds
// realtime connections. When Commodore cannot answer for a session token,
// the locally verified result is returned with an error wrapping
// ErrAuthBackendUnavailable, and callers keep serving on it.
func authenticateWebsocketBearer(ctx context.Context, token string, serviceClients *clients.ServiceClients, jwtSecret []byte) (*AuthResult, error) {
	result, err := AuthenticateBearerToken(ctx, token, serviceClients, jwtSecret)
	if err != nil || result.AuthType != "jwt" || serviceClients == nil || serviceClients.Commodore == nil {
		return result, err
	}
	resp, err := serviceClients.Commodore.ValidateSession(ctx, token)
	if err != nil {
		return result, fmt.Errorf("%w: session check: %w", ErrAuthBackendUnavailable, err)
	}
	if !resp.GetValid() {
		return nil, fmt.Errorf("%w: session revoked", ErrInvalidCredential)
	}
	return result, nil
}

// watch closes the connection through closeConn when its session expires or
// a re-check finds the credential no longer authenticates, and returns when
// ctx, the connection's context, ends. A re-check that cannot reach the
// authentication backend keeps the connection: an unavailable backend says
// nothing about the credential.
func (w websocketIdentityWatch) watch(ctx context.Context, id websocketIdentity, closeConn func(*WebsocketInitError)) {
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
			err := w.check(checkCtx, id)
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
