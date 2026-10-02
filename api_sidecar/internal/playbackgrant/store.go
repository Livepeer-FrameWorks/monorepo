// Package playbackgrant holds, in memory only, the playback grants Foghorn
// issues to this edge and the viewer sessions Foghorn admitted here.
//
// Mist fires PLAY_REWRITE for every HLS request of a viewer (master playlist,
// each variant playlist, each segment and part). Foghorn decides the first
// request of a session; after that the session is known here by the token
// Mist hands out (`tkn`, carried on every playlist and segment URL), the
// viewer address, the Mist stream and the protocol, and its later requests are
// answered from the grant without a Foghorn round trip. A grant alone never
// admits anybody while Foghorn can be asked: new sessions still go to Foghorn,
// which owns placement, capacity, billing and accounting. Only when Foghorn
// cannot be reached is a new session checked against a held, unexpired grant
// (public and JWT policies; webhook policies are refused).
//
// Everything here is re-derivable: grants from Foghorn, sessions from Mist's
// own session list. A restart loses nothing that cannot be rebuilt with one
// grant fetch per stream.
package playbackgrant

import (
	"context"
	"crypto/sha256"
	"errors"
	"math/rand/v2"
	"net"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
)

const (
	// A session that made no request for this long is forgotten. Its next
	// request, if any, is decided by Foghorn again.
	sessionIdleLimit = 3 * time.Minute
	// A grant nobody used for this long is dropped, unless its stream is
	// active in Mist on this edge: during a control outage that grant is what
	// admits the stream's new viewers.
	grantIdleLimit = 10 * time.Minute
	// Mist's active-stream list older than this no longer keeps grants.
	activeStreamsValidity = 3 * time.Minute
	// A grant that still serves sessions is fetched again this long before it
	// expires, so a renewal the push missed still reaches the edge.
	grantRefreshMargin = time.Hour
	// Fetches for one stream are at least this far apart.
	grantFetchInterval = time.Minute
	// Sessions whose admission must be re-decided by Foghorn (webhook policy
	// change, sessions admitted on the grant during an outage) are spread
	// over this window so one change never becomes a burst of USER_NEW.
	recheckWindow = 30 * time.Second
	// A session refused locally stays refused this long, so the USER_NEW
	// Mist re-runs after the invalidation is answered here.
	refusalHold = 2 * time.Minute
	// Tokens are judged with the same clock skew Foghorn allows.
	viewerJWTSkewTolerance = 60 * time.Second
	fetchTimeout           = 5 * time.Second
	mistCallTimeout        = 5 * time.Second
)

var errNoFetcher = errors.New("no playback grant fetcher")

// MistSessions is the part of Mist's API the store drives.
type MistSessions interface {
	InvalidateSessionIDs(ctx context.Context, sessionIDs []string) error
	ViewerSessions(ctx context.Context) ([]mist.ViewerSession, error)
	StopSessionsMultipleContext(ctx context.Context, streamNames []string) error
}

// GrantFetcher asks Foghorn for the current grant of one Mist stream.
type GrantFetcher func(ctx context.Context, internalName string) (*ipcpb.PlaybackGrant, error)

// Options configures a Store.
type Options struct {
	Logger logging.Logger
	Mist   MistSessions
	Fetch  GrantFetcher
	// Now defaults to time.Now.
	Now func() time.Time
	// Jitter returns a delay in [0, window); defaults to a uniform random one.
	Jitter func(window time.Duration) time.Duration
	// Async runs the Mist calls ApplyGrant starts; defaults to a goroutine,
	// so a grant is installed in order on the control stream's receive loop
	// without waiting for Mist.
	Async func(func())
}

type sessionKey struct {
	token     string
	host      string
	stream    string
	connector string
}

type session struct {
	key       sessionKey
	sessionID string
	// origin is the canonical request origin USER_NEW reported; observed is
	// false while only PLAY_REWRITE, which carries no request headers, has
	// been seen for the session.
	origin   string
	observed bool
	// credential is the playback credential the session was admitted with (fwjwt, or a direct
	// request's jwt). The session key's token is Mist's session token, which for a playback
	// FrameWorks resolved is the playback session and not a credential.
	credential string
	kid        string
	admittedAt time.Time
	lastUsed   time.Time
	// local marks a session admitted on the grant while Foghorn could not be
	// asked; Foghorn re-decides it once the control stream is back.
	local bool
}

// rebuiltSession is a live Mist viewer session found after a Helmsman
// restart. Mist's session list has no token, so the record binds to the first
// request from the same address on the same stream and protocol.
type rebuiltSession struct {
	sessionID string
	host      string
	stream    string
	protocols []string
	seenAt    time.Time
}

type refusal struct {
	reason string
	until  time.Time
}

type grant struct {
	msg          *ipcpb.PlaybackGrant
	internal     string
	names        map[string]struct{}
	validUntil   time.Time
	policyDigest [32]byte
	keys         []auth.SigningKey
	lastUsed     time.Time
	lastFetch    time.Time
}

// Store is safe for concurrent use.
type Store struct {
	logger logging.Logger
	mist   MistSessions
	fetch  GrantFetcher
	now    func() time.Time
	jitter func(time.Duration) time.Duration
	async  func(func())

	mu          sync.Mutex
	grants      map[string]*grant
	names       map[string]string
	sessions    map[sessionKey]*session
	rebuilt     map[string]*rebuiltSession
	refused     map[string]refusal
	rechecks    map[string]time.Time
	fetching    map[string]bool
	lastFetchAt map[string]time.Time
	rebuiltOnce bool
	// liveStreams is Mist's latest authoritative active-stream list.
	liveStreams   map[string]struct{}
	liveStreamsAt time.Time
}

// NewStore returns an empty store.
func NewStore(opts Options) *Store {
	s := &Store{
		logger: opts.Logger, mist: opts.Mist, fetch: opts.Fetch, now: opts.Now, jitter: opts.Jitter, async: opts.Async,
		grants: map[string]*grant{}, names: map[string]string{}, sessions: map[sessionKey]*session{},
		rebuilt: map[string]*rebuiltSession{}, refused: map[string]refusal{}, rechecks: map[string]time.Time{},
		fetching: map[string]bool{}, lastFetchAt: map[string]time.Time{},
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.async == nil {
		s.async = func(f func()) { go f() }
	}
	if s.jitter == nil {
		s.jitter = func(window time.Duration) time.Duration {
			if window <= 0 {
				return 0
			}
			return rand.N(window)
		}
	}
	return s
}

// Run sweeps idle state, refreshes grants near expiry and releases due
// rechecks until ctx ends.
func (s *Store) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Tick()
		}
	}
}

// SessionToken returns the Mist session token a request URL carries, read the
// way Mist reads it (jwt overrides tkn, then sid, then sessId). Cookie and
// bearer tokens are not visible to PLAY_REWRITE, so such requests are always
// decided by Foghorn.
func SessionToken(requestURL string) string {
	values := requestQuery(requestURL)
	if values == nil {
		return ""
	}
	if jwt := values.Get("jwt"); jwt != "" {
		return jwt
	}
	for _, name := range []string{"tkn", "sid", "sessId"} {
		if v := values.Get(name); v != "" {
			return v
		}
	}
	return ""
}

func requestQuery(requestURL string) url.Values {
	requestURL = strings.TrimSpace(requestURL)
	if requestURL == "" {
		return nil
	}
	u, err := url.Parse(requestURL)
	if err != nil {
		return nil
	}
	return u.Query()
}

// NormalizeHost makes Mist's address spellings comparable: an IPv4 address
// mapped into IPv6 is the IPv4 address.
func NormalizeHost(host string) string {
	host = strings.TrimSpace(host)
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			return v4.String()
		}
		return ip.String()
	}
	return host
}

// ServeAdmitted answers a PLAY_REWRITE from a session Foghorn admitted on this
// edge. It returns the Mist stream when the request carries the session's
// token from the session's address, for the session's stream and protocol,
// and the stream's grant is valid. A new playback carries a playback session
// this edge has not admitted, so it has no session here and goes to Foghorn,
// which activates the reservation its destination made for it.
func (s *Store) ServeAdmitted(requested, host, connector, requestURL string) (string, bool) {
	token := SessionToken(requestURL)
	if token == "" {
		return "", false
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.validGrantForNameLocked(requested, now)
	if g == nil {
		return "", false
	}
	key := sessionKey{token: token, host: NormalizeHost(host), stream: g.internal, connector: connector}
	sess := s.sessions[key]
	if sess == nil {
		sess = s.bindRebuiltLocked(key, g, now)
		if sess == nil {
			return "", false
		}
	}
	sess.lastUsed = now
	g.lastUsed = now
	return g.internal, true
}

// Decision is a local verdict on a new session.
type Decision struct {
	Allow    bool
	Internal string
	Kid      string
	// Reason explains a refusal, or why no local verdict exists.
	Reason string
}

// DecideNewRequest is the PLAY_REWRITE verdict for a new session while Foghorn
// cannot be asked. The grant maps the name; the session's credential and
// origin are checked by USER_NEW, which Mist runs for every new session.
func (s *Store) DecideNewRequest(requested string) Decision {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	internal, ok := s.names[requested]
	if !ok {
		return Decision{Reason: "no playback grant for this stream"}
	}
	g := s.grants[internal]
	if g == nil || !now.Before(g.validUntil) {
		return Decision{Reason: "playback grant expired"}
	}
	if g.msg.GetPolicy().GetKind() == ipcpb.PlaybackGrantPolicyKind_PLAYBACK_GRANT_POLICY_KIND_CONNECTED {
		return Decision{Reason: "stream policy decides every new session in Foghorn"}
	}
	g.lastUsed = now
	return Decision{Allow: true, Internal: g.internal}
}

// DecideNewSession is the USER_NEW verdict for a new session against the
// stream's grant while Foghorn cannot be asked.
func (s *Store) DecideNewSession(vc *ipcpb.ViewerConnectTrigger) Decision {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.grants[vc.GetStreamName()]
	if g == nil {
		return Decision{Reason: "no playback grant for this stream"}
	}
	if !now.Before(g.validUntil) {
		return Decision{Reason: "playback grant expired"}
	}
	if g.msg.GetPolicy().GetKind() == ipcpb.PlaybackGrantPolicyKind_PLAYBACK_GRANT_POLICY_KIND_CONNECTED {
		return Decision{Reason: "stream policy decides every new session in Foghorn"}
	}
	facts := sessionFacts{token: mist.ViewerJWT(vc.GetRequestUrl(), vc.GetViewerToken()), observed: vc.Origin != nil || vc.Referer != nil, at: now}
	if facts.observed {
		facts.origin = auth.RequestOrigin(vc.GetOrigin(), vc.GetReferer())
	}
	kid, reason := g.admits(facts)
	if reason != "" {
		return Decision{Reason: reason}
	}
	g.lastUsed = now
	return Decision{Allow: true, Internal: g.internal, Kid: kid}
}

// Refused reports a session this edge refused after re-checking it against a
// newer grant; the USER_NEW Mist re-runs for it is answered with the refusal.
func (s *Store) Refused(sessionID string) (string, bool) {
	if sessionID == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.refused[sessionID]
	if !ok || !s.now().Before(r.until) {
		return "", false
	}
	return r.reason, true
}

// AdmitRequest records Foghorn's PLAY_REWRITE admission. A request that
// carries a session token becomes a session right away; one without (the
// first request, before Mist handed out a token) becomes one at USER_NEW.
// It reports whether the stream still needs a grant fetched.
func (s *Store) AdmitRequest(requested, internal, host, connector, requestURL string) bool {
	now := s.now()
	token := SessionToken(requestURL)
	s.mu.Lock()
	defer s.mu.Unlock()
	if g := s.grants[internal]; g != nil {
		if _, known := g.names[requested]; !known && requested != "" {
			// Foghorn just resolved this name to the stream.
			g.names[requested] = struct{}{}
			s.names[requested] = internal
		}
	}
	if token != "" {
		key := sessionKey{token: token, host: NormalizeHost(host), stream: internal, connector: connector}
		if s.sessions[key] == nil {
			credential := mist.ViewerJWT(requestURL, token)
			s.sessions[key] = &session{key: key, admittedAt: now, lastUsed: now, credential: credential, kid: tokenKid(credential)}
		} else {
			s.sessions[key].lastUsed = now
		}
	}
	return s.needsFetchLocked(internal, now)
}

// AdmitSession records a USER_NEW admission: by Foghorn, or on the grant while
// Foghorn could not be asked (local).
func (s *Store) AdmitSession(vc *ipcpb.ViewerConnectTrigger, local bool) {
	token := vc.GetViewerToken()
	if token == "" {
		return
	}
	now := s.now()
	key := sessionKey{token: token, host: NormalizeHost(vc.GetHost()), stream: vc.GetStreamName(), connector: vc.GetConnector()}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[key]
	if sess == nil {
		sess = &session{key: key, admittedAt: now}
		s.sessions[key] = sess
	}
	sess.lastUsed = now
	sess.sessionID = vc.GetSessionId()
	if credential := mist.ViewerJWT(vc.GetRequestUrl(), token); credential != "" {
		sess.credential = credential
		sess.kid = tokenKid(credential)
	}
	sess.observed = vc.Origin != nil || vc.Referer != nil
	if sess.observed {
		sess.origin = auth.RequestOrigin(vc.GetOrigin(), vc.GetReferer())
	}
	sess.local = local
	delete(s.refused, vc.GetSessionId())
	delete(s.rebuilt, vc.GetSessionId())
}

// ForgetSession drops a session Foghorn refused at USER_NEW.
func (s *Store) ForgetSession(vc *ipcpb.ViewerConnectTrigger) {
	key := sessionKey{token: vc.GetViewerToken(), host: NormalizeHost(vc.GetHost()), stream: vc.GetStreamName(), connector: vc.GetConnector()}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, key)
}

// EndSession drops the session Mist ended (USER_END).
func (s *Store) EndSession(sessionID string) {
	if sessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, sess := range s.sessions {
		if sess.sessionID == sessionID {
			delete(s.sessions, key)
		}
	}
	delete(s.rebuilt, sessionID)
	delete(s.rechecks, sessionID)
}

// ApplyGrant installs a grant from Foghorn. When the policy changed, held
// sessions are re-checked here against the facts they were admitted with;
// only sessions that now fail are invalidated in Mist, which ends them when
// the re-run USER_NEW is refused. Sessions of a stream whose policy is now
// decided per session by Foghorn are re-checked there, spread over
// recheckWindow. A revoked grant ends the stream's local answers and stops
// every Mist session of the stream, including the ones this store never tied
// to a Mist session id.
func (s *Store) ApplyGrant(msg *ipcpb.PlaybackGrant) {
	if msg == nil || strings.TrimSpace(msg.GetInternalName()) == "" {
		return
	}
	now := s.now()
	internal := msg.GetInternalName()
	var invalidate []string
	var refusals []logRefusal

	s.mu.Lock()
	old := s.grants[internal]
	if msg.GetRevoked() {
		s.dropGrantLocked(internal)
		reason := "playback authority revoked: " + msg.GetRevokedReason()
		for key, sess := range s.sessions {
			if key.stream != internal {
				continue
			}
			delete(s.sessions, key)
			if sess.sessionID != "" {
				invalidate = append(invalidate, sess.sessionID)
				s.refused[sess.sessionID] = refusal{reason: reason, until: now.Add(refusalHold)}
				refusals = append(refusals, logRefusal{sessionID: sess.sessionID, reason: reason})
			}
		}
		for id, rb := range s.rebuilt {
			if rb.stream == internal {
				delete(s.rebuilt, id)
				invalidate = append(invalidate, id)
				s.refused[id] = refusal{reason: reason, until: now.Add(refusalHold)}
				refusals = append(refusals, logRefusal{sessionID: id, reason: reason})
			}
		}
		s.mu.Unlock()
		s.logRefusals(internal, refusals)
		s.async(func() { s.stopRevokedStream(internal, invalidate) })
		return
	}

	g := newGrant(msg, now)
	if old != nil {
		// Names Foghorn resolved on this edge stay mapped for as long as the
		// stream's grant does.
		for name := range old.names {
			g.names[name] = struct{}{}
		}
		g.lastUsed = old.lastUsed
		g.lastFetch = old.lastFetch
		s.dropGrantLocked(internal)
	}
	s.grants[internal] = g
	for name := range g.names {
		s.names[name] = internal
	}
	policyChanged := old != nil && old.policyDigest != g.policyDigest
	connected := g.msg.GetPolicy().GetKind() == ipcpb.PlaybackGrantPolicyKind_PLAYBACK_GRANT_POLICY_KIND_CONNECTED
	if policyChanged {
		for key, sess := range s.sessions {
			if key.stream != internal {
				continue
			}
			if connected || (!sess.observed && g.restrictsOrigin()) {
				s.scheduleRecheckLocked(sess.sessionID, now)
				continue
			}
			if _, reason := g.admits(sessionFacts{token: sess.credential, origin: sess.origin, observed: sess.observed, at: sess.admittedAt}); reason != "" {
				delete(s.sessions, key)
				if sess.sessionID != "" {
					invalidate = append(invalidate, sess.sessionID)
					s.refused[sess.sessionID] = refusal{reason: reason, until: now.Add(refusalHold)}
				}
				refusals = append(refusals, logRefusal{sessionID: sess.sessionID, kid: sess.kid, reason: reason})
			}
		}
		if connected || g.msg.GetPolicy().GetKind() != ipcpb.PlaybackGrantPolicyKind_PLAYBACK_GRANT_POLICY_KIND_PUBLIC {
			for id, rb := range s.rebuilt {
				if rb.stream == internal {
					s.scheduleRecheckLocked(id, now)
				}
			}
		}
	}
	s.mu.Unlock()
	if s.logger != nil {
		s.logger.WithFields(logging.Fields{
			"internal_name":            internal,
			"policy_kind":              msg.GetPolicy().GetKind().String(),
			"object_authority_version": msg.GetObjectAuthorityVersion(),
			"tenant_authority_version": msg.GetTenantAuthorityVersion(),
			"valid_until":              g.validUntil.UTC().Format(time.RFC3339),
			"policy_changed":           policyChanged,
			"sessions_refused":         len(refusals),
		}).Info("Playback grant applied")
	}
	s.logRefusals(internal, refusals)
	s.async(func() { s.invalidate(internal, invalidate) })
}

// StopStreams forgets the grants and sessions of streams whose sessions
// Foghorn stopped (tenant suspension): nothing of them is answered locally
// any more.
func (s *Store) StopStreams(names []string, reason string) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, name := range names {
		internal := s.resolveStreamLocked(name)
		s.dropGrantLocked(internal)
		for key, sess := range s.sessions {
			if key.stream == internal {
				delete(s.sessions, key)
				if sess.sessionID != "" {
					s.refused[sess.sessionID] = refusal{reason: "sessions stopped by Foghorn: " + reason, until: now.Add(refusalHold)}
				}
			}
		}
		for id, rb := range s.rebuilt {
			if rb.stream == internal {
				delete(s.rebuilt, id)
			}
		}
	}
}

// RecheckStreams handles Foghorn's request to re-check the sessions of
// streams after a playback policy or signing-key change. A stream whose
// grant this edge holds is re-checked here: a per-session (webhook) policy
// re-asks Foghorn for each session spread over recheckWindow, any other policy
// fetches the stream's grant once and re-checks locally. It returns the
// streams this edge holds no grant for, whose sessions Mist must re-check.
func (s *Store) RecheckStreams(names []string) []string {
	now := s.now()
	var untracked, fetch []string
	s.mu.Lock()
	for _, name := range names {
		internal := s.resolveStreamLocked(name)
		g := s.grants[internal]
		if g == nil {
			untracked = append(untracked, name)
			continue
		}
		if g.msg.GetPolicy().GetKind() == ipcpb.PlaybackGrantPolicyKind_PLAYBACK_GRANT_POLICY_KIND_CONNECTED {
			for key, sess := range s.sessions {
				if key.stream == internal {
					s.scheduleRecheckLocked(sess.sessionID, now)
				}
			}
			for id, rb := range s.rebuilt {
				if rb.stream == internal {
					s.scheduleRecheckLocked(id, now)
				}
			}
			continue
		}
		fetch = append(fetch, internal)
	}
	s.mu.Unlock()
	for _, internal := range fetch {
		if err := s.fetchGrant(internal, true); err != nil {
			untracked = append(untracked, internal)
		}
	}
	return untracked
}

// ControlConnected runs after every registration with Foghorn. The first one
// after start rebuilds the session table from Mist's live sessions. Every one
// fetches the grant of each stream with sessions (one request per stream), so
// a policy change made while the edge was cut off is re-checked, and sends
// sessions admitted on the grant during the outage back to Foghorn, spread
// over recheckWindow, which accounts and re-decides them.
func (s *Store) ControlConnected(ctx context.Context) {
	s.mu.Lock()
	needRebuild := !s.rebuiltOnce
	s.mu.Unlock()
	if needRebuild && s.mist != nil {
		callCtx, cancel := context.WithTimeout(ctx, mistCallTimeout)
		live, err := s.mist.ViewerSessions(callCtx)
		cancel()
		if err != nil {
			if s.logger != nil {
				s.logger.WithError(err).Warn("Cannot list Mist viewer sessions; their next requests are decided by Foghorn")
			}
		} else {
			s.rebuildFrom(live)
		}
	}

	now := s.now()
	streams := map[string]struct{}{}
	s.mu.Lock()
	for key, sess := range s.sessions {
		streams[key.stream] = struct{}{}
		if sess.local {
			sess.local = false
			s.scheduleRecheckLocked(sess.sessionID, now)
		}
	}
	for _, rb := range s.rebuilt {
		streams[rb.stream] = struct{}{}
	}
	s.mu.Unlock()
	for internal := range streams {
		_ = s.fetchGrant(internal, true) //nolint:errcheck // a stream without a grant is decided by Foghorn, which fetchGrant logs
	}
}

func (s *Store) rebuildFrom(live []mist.ViewerSession) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	known := map[string]bool{}
	for _, sess := range s.sessions {
		if sess.sessionID != "" {
			known[sess.sessionID] = true
		}
	}
	for _, v := range live {
		if known[v.SessionID] {
			continue
		}
		s.rebuilt[v.SessionID] = &rebuiltSession{
			sessionID: v.SessionID, host: NormalizeHost(v.Host), stream: v.Stream,
			protocols: strings.Split(v.Protocol, ","), seenAt: now,
		}
	}
	s.rebuiltOnce = true
	if s.logger != nil {
		s.logger.WithField("sessions", len(live)).Info("Rebuilt playback sessions from Mist")
	}
}

// FetchIfMissing fetches the stream's grant in the background when this edge
// holds none (or one about to expire). Foghorn pushes a grant with the first
// admission of a stream on an edge; this covers a push lost to a reconnect.
func (s *Store) FetchIfMissing(internal string) {
	go func() { _ = s.fetchGrant(internal, false) }() //nolint:errcheck // logged by fetchGrant
}

// ObserveActiveStreams records the Mist streams active on this edge, from an
// authoritative Mist stream list.
func (s *Store) ObserveActiveStreams(streams map[string]struct{}) {
	now := s.now()
	live := make(map[string]struct{}, len(streams))
	for name := range streams {
		live[name] = struct{}{}
	}
	s.mu.Lock()
	s.liveStreams, s.liveStreamsAt = live, now
	s.mu.Unlock()
}

// Tick expires idle sessions and grants, refreshes grants that serve
// sessions or live streams and are close to expiry, and releases due rechecks.
func (s *Store) Tick() {
	now := s.now()
	var refresh []string
	var due []string
	s.mu.Lock()
	active := map[string]bool{}
	if now.Sub(s.liveStreamsAt) <= activeStreamsValidity {
		for name := range s.liveStreams {
			active[name] = true
		}
	}
	for key, sess := range s.sessions {
		if now.Sub(sess.lastUsed) > sessionIdleLimit {
			delete(s.sessions, key)
			continue
		}
		active[key.stream] = true
	}
	for id, rb := range s.rebuilt {
		if now.Sub(rb.seenAt) > sessionIdleLimit {
			delete(s.rebuilt, id)
			continue
		}
		active[rb.stream] = true
	}
	for id, r := range s.refused {
		if !now.Before(r.until) {
			delete(s.refused, id)
		}
	}
	for internal, g := range s.grants {
		if !active[internal] {
			if now.Sub(g.lastUsed) > grantIdleLimit || !now.Before(g.validUntil) {
				s.dropGrantLocked(internal)
			}
			continue
		}
		if g.validUntil.Sub(now) < grantRefreshMargin && s.needsFetchLocked(internal, now) {
			refresh = append(refresh, internal)
		}
	}
	for id, at := range s.rechecks {
		if !now.Before(at) {
			due = append(due, id)
			delete(s.rechecks, id)
		}
	}
	s.mu.Unlock()
	for _, internal := range refresh {
		s.FetchIfMissing(internal)
	}
	if len(due) > 0 {
		s.invalidate("", due)
	}
}

func (s *Store) fetchGrant(internal string, force bool) error {
	if s.fetch == nil || internal == "" {
		return errNoFetcher
	}
	now := s.now()
	s.mu.Lock()
	if s.fetching[internal] || (!force && !s.needsFetchLocked(internal, now)) {
		s.mu.Unlock()
		return nil
	}
	s.fetching[internal] = true
	s.lastFetchAt[internal] = now
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.fetching, internal)
		s.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()
	msg, err := s.fetch(ctx, internal)
	if err != nil {
		if s.logger != nil {
			s.logger.WithError(err).WithField("internal_name", internal).
				Info("No playback grant for stream; its requests are decided by Foghorn")
		}
		return err
	}
	s.ApplyGrant(msg)
	return nil
}

// needsFetchLocked: no usable grant, or one within the refresh margin, and
// no fetch for the stream in the last grantFetchInterval.
func (s *Store) needsFetchLocked(internal string, now time.Time) bool {
	if last, ok := s.lastFetchAt[internal]; ok && now.Sub(last) < grantFetchInterval {
		return false
	}
	g := s.grants[internal]
	return g == nil || g.validUntil.Sub(now) < grantRefreshMargin
}

func (s *Store) validGrantForNameLocked(requested string, now time.Time) *grant {
	internal, ok := s.names[requested]
	if !ok {
		return nil
	}
	g := s.grants[internal]
	if g == nil || !now.Before(g.validUntil) {
		return nil
	}
	return g
}

// resolveStreamLocked maps a stream name as Foghorn names it in a command
// (runtime or bare internal name) to the Mist stream a grant is held under.
func (s *Store) resolveStreamLocked(name string) string {
	if _, ok := s.grants[name]; ok {
		return name
	}
	bare := mist.ExtractInternalName(name)
	for internal := range s.grants {
		if mist.ExtractInternalName(internal) == bare {
			return internal
		}
	}
	for key := range s.sessions {
		if mist.ExtractInternalName(key.stream) == bare {
			return key.stream
		}
	}
	return name
}

func (s *Store) dropGrantLocked(internal string) {
	g := s.grants[internal]
	if g == nil {
		return
	}
	for name := range g.names {
		if s.names[name] == internal {
			delete(s.names, name)
		}
	}
	delete(s.grants, internal)
}

func (s *Store) bindRebuiltLocked(key sessionKey, g *grant, now time.Time) *session {
	var matches []*rebuiltSession
	for _, rb := range s.rebuilt {
		if rb.host == key.host && rb.stream == key.stream && slices.Contains(rb.protocols, key.connector) {
			matches = append(matches, rb)
		}
	}
	if len(matches) == 0 {
		return nil
	}
	if g.msg.GetPolicy().GetKind() == ipcpb.PlaybackGrantPolicyKind_PLAYBACK_GRANT_POLICY_KIND_JWT {
		if _, reason := g.verifyToken(key.token, now); reason != "" {
			return nil
		}
	}
	sess := &session{key: key, admittedAt: now, lastUsed: now, kid: tokenKid(key.token)}
	if len(matches) == 1 {
		sess.sessionID = matches[0].sessionID
		delete(s.rebuilt, matches[0].sessionID)
	}
	// With several live sessions from one address (viewers behind one NAT)
	// the token cannot be tied to one of them; it is answered without a Mist
	// session id and the records stay for the address's other tokens. The
	// answer only maps the stream name: a token that is really a new viewer
	// opens a new Mist session, whose USER_NEW Foghorn decides.
	s.sessions[key] = sess
	return sess
}

func (s *Store) scheduleRecheckLocked(sessionID string, now time.Time) {
	if sessionID == "" {
		return
	}
	if _, pending := s.rechecks[sessionID]; pending {
		return
	}
	s.rechecks[sessionID] = now.Add(s.jitter(recheckWindow))
}

func (s *Store) invalidate(internal string, sessionIDs []string) {
	if len(sessionIDs) == 0 || s.mist == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), mistCallTimeout)
	defer cancel()
	if err := s.mist.InvalidateSessionIDs(ctx, sessionIDs); err != nil && s.logger != nil {
		s.logger.WithError(err).WithFields(logging.Fields{
			"internal_name": internal, "sessions": len(sessionIDs),
		}).Warn("Mist session invalidation failed")
	}
}

// stopRevokedStream disconnects every Mist session of a stream whose playback
// authority was revoked. Mist stops sessions by exact stream name, so the
// names are the grant's own plus every runtime name Mist lists a viewer of
// the stream under. Only when Mist refuses the stop are the tracked sessions
// invalidated instead, so each re-run USER_NEW meets the local refusal.
func (s *Store) stopRevokedStream(internal string, tracked []string) {
	if s.mist == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), mistCallTimeout)
	defer cancel()
	names := []string{internal}
	bare := mist.ExtractInternalName(internal)
	live, err := s.mist.ViewerSessions(ctx)
	if err != nil && s.logger != nil {
		s.logger.WithError(err).WithField("internal_name", internal).
			Warn("Cannot list Mist viewer sessions; stopping the revoked stream by its grant name only")
	}
	for _, v := range live {
		if mist.ExtractInternalName(v.Stream) == bare && !slices.Contains(names, v.Stream) {
			names = append(names, v.Stream)
		}
	}
	if err := s.mist.StopSessionsMultipleContext(ctx, names); err != nil {
		if s.logger != nil {
			s.logger.WithError(err).WithFields(logging.Fields{"internal_name": internal, "streams": names}).
				Warn("Mist refused to stop the revoked stream's sessions; invalidating the tracked ones")
		}
		s.invalidate(internal, tracked)
		return
	}
	if s.logger != nil {
		s.logger.WithFields(logging.Fields{"internal_name": internal, "streams": names}).
			Info("Stopped the viewer sessions of a stream whose playback authority was revoked")
	}
}

type logRefusal struct {
	sessionID string
	kid       string
	reason    string
}

func (s *Store) logRefusals(internal string, refusals []logRefusal) {
	if s.logger == nil {
		return
	}
	for _, r := range refusals {
		s.logger.WithFields(logging.Fields{
			"internal_name": internal, "session_id": r.sessionID, "kid": r.kid, "reason": r.reason,
		}).Info("Playback session refused on the updated grant")
	}
}

type sessionFacts struct {
	token    string
	origin   string
	observed bool
	at       time.Time
}

func newGrant(msg *ipcpb.PlaybackGrant, now time.Time) *grant {
	g := &grant{msg: msg, internal: msg.GetInternalName(), names: map[string]struct{}{}, lastUsed: now}
	if ts := msg.GetValidUntil(); ts != nil {
		g.validUntil = ts.AsTime()
	}
	g.names[msg.GetInternalName()] = struct{}{}
	for _, name := range msg.GetRequestedNames() {
		if name = strings.TrimSpace(name); name != "" {
			g.names[name] = struct{}{}
		}
	}
	for _, key := range msg.GetPolicy().GetActiveKeys() {
		g.keys = append(g.keys, auth.SigningKey{Kid: key.GetKid(), PublicKeyPEM: key.GetPublicKeyPem()})
	}
	policy, _ := proto.MarshalOptions{Deterministic: true}.Marshal(msg.GetPolicy()) //nolint:errcheck // a nil or unmarshalable policy digests as empty
	g.policyDigest = sha256.Sum256(policy)
	return g
}

func (g *grant) restrictsOrigin() bool {
	allowed := g.msg.GetPolicy().GetAllowedOrigins()
	return g.msg.GetPolicy().GetKind() != ipcpb.PlaybackGrantPolicyKind_PLAYBACK_GRANT_POLICY_KIND_PUBLIC &&
		len(allowed) > 0 && !slices.Contains(allowed, auth.AnyOrigin)
}

// admits applies the grant's policy the way Foghorn's evaluator does: the
// origin rule first (non-public policies), then the credential. It returns
// the token's key id and an empty reason when the session is admitted.
func (g *grant) admits(f sessionFacts) (string, string) {
	policy := g.msg.GetPolicy()
	if g.restrictsOrigin() {
		switch {
		case !f.observed:
			return "", "origin-unobservable"
		case f.origin == "":
			return "", "origin-missing"
		case !auth.OriginAllowed(policy.GetAllowedOrigins(), f.origin):
			return "", "origin-not-allowed"
		}
	}
	switch policy.GetKind() {
	case ipcpb.PlaybackGrantPolicyKind_PLAYBACK_GRANT_POLICY_KIND_PUBLIC:
		return "", ""
	case ipcpb.PlaybackGrantPolicyKind_PLAYBACK_GRANT_POLICY_KIND_JWT:
		return g.verifyToken(f.token, f.at)
	default:
		return "", "stream policy decides every new session in Foghorn"
	}
}

func (g *grant) verifyToken(token string, at time.Time) (string, string) {
	if token == "" {
		return "", "missing-token"
	}
	policy := g.msg.GetPolicy()
	if len(g.keys) == 0 {
		return "", "no-active-keys"
	}
	kid := tokenKid(token)
	_, err := auth.VerifyViewerJWT(token, g.keys, auth.VerifyOptions{
		AllowedKids: policy.GetAllowedKids(), RequiredAudience: policy.GetRequiredAudiences(),
		RequiredClaims: policy.GetRequiredClaimsJson(), SkewTolerance: viewerJWTSkewTolerance, At: at,
	})
	if err != nil {
		return kid, jwtRefusalReason(err)
	}
	return kid, ""
}

func tokenKid(token string) string {
	kid, err := auth.ViewerJWTKid(token)
	if err != nil {
		return ""
	}
	return kid
}

// jwtRefusalReason uses the reason names Foghorn's evaluator logs.
func jwtRefusalReason(err error) string {
	for _, c := range []struct {
		err    error
		reason string
	}{
		{auth.ErrTokenNotJWS, "jwt-not-a-jws"}, {auth.ErrMissingKid, "jwt-missing-kid"},
		{auth.ErrUnknownKid, "jwt-unknown-kid"}, {auth.ErrWrongAlgorithm, "jwt-wrong-alg"},
		{auth.ErrSignatureFailed, "jwt-sig-fail"}, {auth.ErrMissingExpiration, "jwt-missing-exp"},
		{auth.ErrTokenExpired, "jwt-expired"}, {auth.ErrTokenNotYetValid, "jwt-not-yet-valid"},
		{auth.ErrAudienceMismatch, "jwt-aud-mismatch"}, {auth.ErrRequiredClaimMiss, "jwt-claim-mismatch"},
		{auth.ErrInvalidPublicKey, "jwt-bad-public-key"},
	} {
		if errors.Is(err, c.err) {
			return c.reason
		}
	}
	return "jwt-verify-error"
}
