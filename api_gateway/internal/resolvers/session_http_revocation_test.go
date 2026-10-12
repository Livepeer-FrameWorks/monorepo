package resolvers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	signalmanpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/signalman"

	"github.com/gin-gonic/gin"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// revocationLister is Commodore's InternalService answering
// ListRecentSessionRevocations from the revocations the test committed.
type revocationLister struct {
	commodorepb.UnimplementedInternalServiceServer
	mu          sync.Mutex
	revocations []*commodorepb.SessionRevocation
	calls       int
}

func (l *revocationLister) commit(tenantID, userID string, at time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.revocations = append(l.revocations, &commodorepb.SessionRevocation{TenantId: tenantID, UserId: userID, RevokedAt: timestamppb.New(at)})
}

func (l *revocationLister) ListRecentSessionRevocations(_ context.Context, req *commodorepb.ListRecentSessionRevocationsRequest) (*commodorepb.ListRecentSessionRevocationsResponse, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	resp := &commodorepb.ListRecentSessionRevocationsResponse{}
	for _, rev := range l.revocations {
		if rev.GetRevokedAt().AsTime().After(req.GetSince().AsTime()) {
			resp.Revocations = append(resp.Revocations, rev)
		}
	}
	return resp, nil
}

func (l *revocationLister) listCalls() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls
}

// httpReplica is one Bridge replica's session enforcement and HTTP
// authentication, wired as Bridge's main wires them: its session
// revocations follow the platform-wide stream and backfill from Commodore on
// every (re)subscription, and its GraphQL and auth routes authenticate with
// Bridge's middleware. Only one replica per process can enforce, as in
// production.
func httpReplica(t *testing.T, broker streamOpener, sc *clients.ServiceClients) string {
	t.Helper()
	sm := newSubscriptionManager(logging.NewLoggerWithService("test"), SubscriptionManagerConfig{SignalmanAddrs: []string{"signalman.internal:19005"}}, broker)
	t.Cleanup(func() { _ = sm.Shutdown() })
	sessions := middleware.NewWebsocketSessions(sm.WatchAPITokenRevocations)
	sm.SetCredentialSockets(sessions)
	middleware.EnforceSessionRevocations(sessions.SessionRevocations())
	t.Cleanup(func() { middleware.EnforceSessionRevocations(nil) })
	go sm.WatchSessionRevocations(context.Background(), func() {
		sessions.ResumeSessionRevocations(sc.Commodore.ListRecentSessionRevocations, nil)
	})

	gin.SetMode(gin.TestMode)
	router := gin.New()
	ok := func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) }
	router.POST("/graphql", middleware.PublicOrJWTAuth([]byte(replicaJWTSecret), sc), ok)
	router.GET("/auth/me", middleware.RequireJWTAuth([]byte(replicaJWTSecret)), ok)
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return srv.URL
}

// httpStatuses returns the status of a GraphQL POST and of GET /auth/me
// authenticated with token.
func httpStatuses(t *testing.T, base, token string) (graphql, me int) {
	t.Helper()
	do := func(method, path, body string) int {
		req, err := http.NewRequestWithContext(context.Background(), method, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	return do(http.MethodPost, "/graphql", `{"query":"query { streams { id } }"}`), do(http.MethodGet, "/auth/me", "")
}

func waitStatuses(t *testing.T, base, token string, want int, within time.Duration) (int, int) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		graphql, me := httpStatuses(t, base, token)
		if (graphql == want && me == want) || time.Now().After(deadline) {
			return graphql, me
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A sign-out handled by another Bridge replica reaches this one on the
// platform-wide session stream: its HTTP routes then refuse the signed-out
// session token with 401, while another user's session token keeps working.
func TestSignedOutSessionTokenIsRefusedOverHTTPByAReplicaThatDidNotSignItOut(t *testing.T) {
	const tenantID = "tenant-1"
	lister := &revocationLister{}
	sc := internalServiceClients(t, lister)
	broker := newSignalmanBroker()
	base := httpReplica(t, broker, sc)
	signedOut := sessionToken(t, "user-1", tenantID)
	other := sessionToken(t, "user-2", tenantID)
	if graphql, me := httpStatuses(t, base, signedOut); graphql != http.StatusOK || me != http.StatusOK {
		t.Fatalf("before sign-out the session token answered %d/%d, want 200/200", graphql, me)
	}
	broker.waitStreams(t, "", signalmanpb.Channel_CHANNEL_SESSIONS, 1)

	// The replica that handled the sign-out is another one: this replica only
	// sees Commodore's user.sessions_revoked.
	broker.publish(t, "", signalmanpb.Channel_CHANNEL_SESSIONS, sessionsRevokedEvent(t, tenantID, "user-1", time.Now()))

	if graphql, me := waitStatuses(t, base, signedOut, http.StatusUnauthorized, 2*time.Second); graphql != http.StatusUnauthorized || me != http.StatusUnauthorized {
		t.Fatalf("after sign-out on another replica the session token answered %d/%d over HTTP, want 401/401", graphql, me)
	}
	if graphql, me := httpStatuses(t, base, other); graphql != http.StatusOK || me != http.StatusOK {
		t.Fatalf("another user's session token answered %d/%d, want 200/200", graphql, me)
	}
}

// A replica whose session stream was not subscribed when a sign-out was
// committed reads it from Commodore once the stream is subscribed again,
// and then refuses the signed-out token.
func TestSignOutMissedWhileTheSessionStreamReconnectsIsBackfilled(t *testing.T) {
	const tenantID = "tenant-1"
	lister := &revocationLister{}
	sc := internalServiceClients(t, lister)
	broker := newGatedBroker()
	base := httpReplica(t, broker, sc)
	signedOut := sessionToken(t, "user-1", tenantID)
	broker.waitStreams(t, "", signalmanpb.Channel_CHANNEL_SESSIONS, 1)
	deadline := time.Now().Add(2 * time.Second)
	for lister.listCalls() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	broker.hold()
	broker.disconnect("", signalmanpb.Channel_CHANNEL_SESSIONS)
	// Commodore committed the sign-out; its event was dispatched while no
	// session stream of this replica was subscribed, so it never arrives.
	lister.commit(tenantID, "user-1", time.Now())
	broker.release()

	if graphql, me := waitStatuses(t, base, signedOut, http.StatusUnauthorized, 3*time.Second); graphql != http.StatusUnauthorized || me != http.StatusUnauthorized {
		t.Fatalf("a sign-out committed while the session stream reconnected left the token answering %d/%d, want 401/401", graphql, me)
	}
}
