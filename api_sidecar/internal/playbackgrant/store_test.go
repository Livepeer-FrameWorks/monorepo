package playbackgrant

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"frameworks/api_sidecar/internal/playbackgrant/playbackgranttest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"google.golang.org/protobuf/types/known/timestamppb"
)

type recordingMist struct {
	mu    sync.Mutex
	calls [][]string
}

func (m *recordingMist) InvalidateSessionIDs(_ context.Context, ids []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, slices.Clone(ids))
	return nil
}

func (m *recordingMist) ViewerSessions(context.Context) ([]mist.ViewerSession, error) {
	return nil, nil
}

func (m *recordingMist) invalidated() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, call := range m.calls {
		out = append(out, call...)
	}
	slices.Sort(out)
	return out
}

type testClock struct{ now time.Time }

func (c *testClock) Now() time.Time { return c.now }

func newTestStore(t *testing.T, m MistSessions, jitter func(time.Duration) time.Duration) (*Store, *testClock) {
	t.Helper()
	clock := &testClock{now: time.Now()}
	return NewStore(Options{Mist: m, Now: clock.Now, Jitter: jitter, Async: func(f func()) { f() }}), clock
}

func viewer(stream, host, token, sessionID string) *ipcpb.ViewerConnectTrigger {
	return &ipcpb.ViewerConnectTrigger{StreamName: stream, Host: host, ViewerToken: token, Connector: "HLS", SessionId: sessionID}
}

func TestSessionTokenReadsTheURLTheWayMistDoes(t *testing.T) {
	for url, want := range map[string]string{
		"http://e/hls/x/index.m3u8":                   "",
		"http://e/hls/x/1/0.ts?tkn=abc":               "abc",
		"http://e/hls/x/1/0.ts?sid=s1":                "s1",
		"http://e/hls/x/1/0.ts?sessId=s2":             "s2",
		"http://e/hls/x/index.m3u8?tkn=abc&jwt=token": "token",
	} {
		if got := SessionToken(url); got != want {
			t.Errorf("SessionToken(%q) = %q, want %q", url, got, want)
		}
	}
}

// A webhook policy change sends every session back to Foghorn, one at a time
// over the recheck window; a second change before they are due does not add
// a second recheck for the same session.
func TestWebhookPolicyChangeSpreadsRechecksAndCoalesces(t *testing.T) {
	m := &recordingMist{}
	var offsets []time.Duration
	store, clock := newTestStore(t, m, func(window time.Duration) time.Duration {
		d := time.Duration(len(offsets)+1) * window / 10
		offsets = append(offsets, d)
		return d
	})
	store.ApplyGrant(playbackgranttest.Grant("live+s", ipcpb.PlaybackGrantPolicyKind_PLAYBACK_GRANT_POLICY_KIND_PUBLIC, nil, "pb"))
	for i, id := range []string{"a", "b", "c"} {
		store.AdmitSession(viewer("live+s", "192.0.2."+string(rune('1'+i)), "tkn-"+id, id), false)
	}
	hook := playbackgranttest.Grant("live+s", ipcpb.PlaybackGrantPolicyKind_PLAYBACK_GRANT_POLICY_KIND_CONNECTED, nil, "pb")
	store.ApplyGrant(hook)
	if got := m.invalidated(); len(got) != 0 {
		t.Fatalf("policy change invalidated %v at once; rechecks must be spread", got)
	}
	store.RecheckStreams([]string{"live+s"})
	if len(offsets) != 3 {
		t.Fatalf("scheduled %d rechecks, want one per session", len(offsets))
	}
	clock.now = clock.now.Add(recheckWindow / 10)
	store.Tick()
	if got := m.invalidated(); len(got) != 1 {
		t.Fatalf("after the first slot %d sessions were rechecked, want 1", len(got))
	}
	clock.now = clock.now.Add(recheckWindow)
	store.Tick()
	if got := m.invalidated(); !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Fatalf("rechecked %v, want each session once", got)
	}
}

// Sessions admitted on the grant while Foghorn was unreachable are sent back
// to Foghorn once the control stream returns, which accounts and re-decides
// them; sessions Foghorn admitted are left alone.
func TestReconnectSendsLocallyAdmittedSessionsToFoghorn(t *testing.T) {
	m := &recordingMist{}
	store, clock := newTestStore(t, m, func(time.Duration) time.Duration { return 0 })
	store.fetch = func(_ context.Context, internal string) (*ipcpb.PlaybackGrant, error) {
		return playbackgranttest.Grant(internal, ipcpb.PlaybackGrantPolicyKind_PLAYBACK_GRANT_POLICY_KIND_PUBLIC, nil, "pb"), nil
	}
	store.ApplyGrant(playbackgranttest.Grant("live+s", ipcpb.PlaybackGrantPolicyKind_PLAYBACK_GRANT_POLICY_KIND_PUBLIC, nil, "pb"))
	store.AdmitSession(viewer("live+s", "192.0.2.1", "tkn-f", "by-foghorn"), false)
	store.AdmitSession(viewer("live+s", "192.0.2.2", "tkn-l", "on-grant"), true)
	store.rebuiltOnce = true
	store.ControlConnected(context.Background())
	clock.now = clock.now.Add(time.Second)
	store.Tick()
	if got := m.invalidated(); !slices.Equal(got, []string{"on-grant"}) {
		t.Fatalf("rechecked %v, want only the session admitted on the grant", got)
	}
	store.ControlConnected(context.Background())
	clock.now = clock.now.Add(time.Second)
	store.Tick()
	if got := m.invalidated(); len(got) != 1 {
		t.Fatalf("a second reconnect rechecked again: %v", got)
	}
}

func TestExpiredGrantAndEndedSessionStopLocalAnswers(t *testing.T) {
	store, clock := newTestStore(t, &recordingMist{}, nil)
	grant := playbackgranttest.Grant("live+s", ipcpb.PlaybackGrantPolicyKind_PLAYBACK_GRANT_POLICY_KIND_PUBLIC, nil, "pb")
	grant.ValidUntil = timestamppb.New(clock.now.Add(2 * time.Minute))
	store.ApplyGrant(grant)
	store.AdmitSession(viewer("live+s", "::ffff:192.0.2.1", "tkn", "sess"), false)
	url := "http://e/hls/pb/1/0.ts?tkn=tkn"
	if got, ok := store.ServeAdmitted("pb", "192.0.2.1", "HLS", url); !ok || got != "live+s" {
		t.Fatalf("admitted session = %q %v", got, ok)
	}
	if _, ok := store.ServeAdmitted("pb", "192.0.2.1", "WebRTC", url); ok {
		t.Fatal("another protocol was answered as the session")
	}
	store.EndSession("sess")
	if _, ok := store.ServeAdmitted("pb", "192.0.2.1", "HLS", url); ok {
		t.Fatal("ended session still answered")
	}
	store.AdmitSession(viewer("live+s", "192.0.2.1", "tkn", "sess2"), false)
	clock.now = clock.now.Add(3 * time.Minute)
	if _, ok := store.ServeAdmitted("pb", "192.0.2.1", "HLS", url); ok {
		t.Fatal("session answered past its grant's validity")
	}
	if d := store.DecideNewRequest("pb"); d.Allow {
		t.Fatal("new session admitted on an expired grant")
	}
}

// A revoked grant ends local answers for the stream and sends each of its
// sessions back to Mist's USER_NEW, which this edge then refuses.
func TestRevokedGrantInvalidatesTheStreamsSessions(t *testing.T) {
	m := &recordingMist{}
	store, _ := newTestStore(t, m, nil)
	store.ApplyGrant(playbackgranttest.Grant("live+s", ipcpb.PlaybackGrantPolicyKind_PLAYBACK_GRANT_POLICY_KIND_PUBLIC, nil, "pb"))
	store.AdmitSession(viewer("live+s", "192.0.2.1", "t1", "s1"), false)
	store.AdmitSession(viewer("live+other", "192.0.2.1", "t2", "s2"), false)
	store.ApplyGrant(&ipcpb.PlaybackGrant{InternalName: "live+s", Revoked: true, RevokedReason: "tenant suspended"})
	if got := m.invalidated(); !slices.Equal(got, []string{"s1"}) {
		t.Fatalf("invalidated %v, want the revoked stream's session", got)
	}
	if _, refused := store.Refused("s1"); !refused {
		t.Fatal("the revoked stream's session is not refused on its re-run")
	}
	if _, ok := store.ServeAdmitted("pb", "192.0.2.1", "HLS", "http://e/hls/pb/0.ts?tkn=t1"); ok {
		t.Fatal("revoked stream still answered locally")
	}
}

func TestJWTSessionOnGrantChecksTokenAndOrigin(t *testing.T) {
	store, _ := newTestStore(t, &recordingMist{}, nil)
	key := playbackgranttest.NewSigningKey(t, "kid-1")
	grant := playbackgranttest.Grant("live+s", ipcpb.PlaybackGrantPolicyKind_PLAYBACK_GRANT_POLICY_KIND_JWT, []playbackgranttest.SigningKey{key}, "pb")
	grant.Policy.AllowedOrigins = []string{"https://site.example"}
	store.ApplyGrant(grant)
	good, bad := "https://site.example", "https://other.example"
	valid := key.Token(t, "v", time.Now().Add(time.Hour))
	expired := key.Token(t, "v", time.Now().Add(-time.Hour))
	cases := []struct {
		name   string
		vc     *ipcpb.ViewerConnectTrigger
		reason string
	}{
		{"valid", &ipcpb.ViewerConnectTrigger{StreamName: "live+s", ViewerToken: valid, Origin: &good, Referer: new(string)}, ""},
		{"expired", &ipcpb.ViewerConnectTrigger{StreamName: "live+s", ViewerToken: expired, Origin: &good, Referer: new(string)}, "jwt-expired"},
		{"missing token", &ipcpb.ViewerConnectTrigger{StreamName: "live+s", Origin: &good, Referer: new(string)}, "missing-token"},
		{"foreign origin", &ipcpb.ViewerConnectTrigger{StreamName: "live+s", ViewerToken: valid, Origin: &bad, Referer: new(string)}, "origin-not-allowed"},
		{"origin unobservable", &ipcpb.ViewerConnectTrigger{StreamName: "live+s", ViewerToken: valid}, "origin-unobservable"},
	}
	for _, c := range cases {
		d := store.DecideNewSession(c.vc)
		if d.Allow != (c.reason == "") || d.Reason != c.reason {
			t.Errorf("%s: allow=%v reason=%q, want reason %q", c.name, d.Allow, d.Reason, c.reason)
		}
	}
}
