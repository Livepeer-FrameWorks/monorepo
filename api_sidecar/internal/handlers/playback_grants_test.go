package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"frameworks/api_sidecar/internal/control"
	"frameworks/api_sidecar/internal/playbackgrant"
	"frameworks/api_sidecar/internal/playbackgrant/playbackgranttest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

type fakeMistSessions struct {
	mu          sync.Mutex
	live        []mist.ViewerSession
	invalidated []string
}

func (f *fakeMistSessions) InvalidateSessionIDs(_ context.Context, ids []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invalidated = append(f.invalidated, ids...)
	return nil
}

func (f *fakeMistSessions) ViewerSessions(context.Context) ([]mist.ViewerSession, error) {
	return f.live, nil
}

func (f *fakeMistSessions) invalidatedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := slices.Clone(f.invalidated)
	slices.Sort(out)
	return out
}

// foghornCalls counts the blocking triggers that reached Foghorn, by type.
type foghornCalls struct {
	mu     sync.Mutex
	byType map[string]int
}

func (c *foghornCalls) add(triggerType string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byType == nil {
		c.byType = map[string]int{}
	}
	c.byType[triggerType]++
}

func (c *foghornCalls) get(triggerType string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.byType[triggerType]
}

func installTestPlaybackGrants(t *testing.T, sessions playbackgrant.MistSessions, fetch playbackgrant.GrantFetcher) *playbackgrant.Store {
	t.Helper()
	store := playbackgrant.NewStore(playbackgrant.Options{
		Logger: logger, Mist: sessions, Fetch: fetch, Async: func(f func()) { f() },
	})
	installPlaybackGrants(store)
	t.Cleanup(func() {
		playbackGrants = nil
		control.SetPlaybackGrantSessions(nil)
	})
	return store
}

func noGrantFetch(t *testing.T) playbackgrant.GrantFetcher {
	return func(context.Context, string) (*ipcpb.PlaybackGrant, error) {
		t.Helper()
		t.Error("unexpected grant fetch")
		return nil, errors.New("no grant")
	}
}

func playRewriteBody(requested, host, url string) string {
	return fmt.Sprintf("%s\n%s\nHLS\n%s", requested, host, url)
}

func userNewBody(stream, host, token, sessionID string) string {
	return fmt.Sprintf("%s\n%s\n%s\nHLS\nhttp://edge:8080/hls/%s/index.m3u8\n%s\nfalse\n\n\n", stream, host, token, stream, sessionID)
}

func playRewrite(t *testing.T, body string) (int, string, string) {
	t.Helper()
	ctx, rec := newWebhookContext(body)
	HandlePlayRewrite(ctx)
	return rec.Code, rec.Body.String(), rec.Header().Get("X-Mist-Trigger-Action")
}

func userNew(t *testing.T, body string) (int, string, string) {
	t.Helper()
	ctx, rec := newWebhookContext(body)
	HandleUserNew(ctx)
	return rec.Code, rec.Body.String(), rec.Header().Get("X-Mist-Trigger-Action")
}

// One viewer fetching an HLS stream fires PLAY_REWRITE for the master
// playlist, each variant playlist and every segment. Foghorn admits the first
// request and the session; the rest carry the session's token and are
// answered by the edge.
func TestAdmittedHLSSessionReachesFoghornOnce(t *testing.T) {
	setupTriggerTest(t, "tenant-grant")
	store := installTestPlaybackGrants(t, &fakeMistSessions{}, noGrantFetch(t))
	calls := &foghornCalls{}
	stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
		calls.add(trigger.GetTriggerType())
		switch trigger.GetTriggerType() {
		case string(mist.TriggerPlayRewrite):
			// Foghorn pushes the grant on the stream before its answer.
			store.ApplyGrant(playbackgranttest.Grant("live+s1", ipcpb.PlaybackGrantPolicyKind_PLAYBACK_GRANT_POLICY_KIND_PUBLIC, nil, "pb1"))
			return &control.MistTriggerResult{Response: "live+s1", Action: ipcpb.MistTriggerAction_MIST_TRIGGER_ACTION_VALUE}, nil
		case string(mist.TriggerUserNew):
			return &control.MistTriggerResult{Response: "true", Action: ipcpb.MistTriggerAction_MIST_TRIGGER_ACTION_VALUE}, nil
		}
		return &control.MistTriggerResult{}, nil
	})

	if code, body, _ := playRewrite(t, playRewriteBody("pb1", "192.0.2.10", "http://edge:8080/hls/pb1/index.m3u8")); code != http.StatusOK || body != "live+s1" {
		t.Fatalf("master playlist = %d %q", code, body)
	}
	if code, body, _ := userNew(t, userNewBody("live+s1", "::ffff:192.0.2.10", "tkn-1", "sess-1")); code != http.StatusOK || body != "true" {
		t.Fatalf("USER_NEW = %d %q", code, body)
	}
	for i := range 20 {
		url := fmt.Sprintf("http://edge:8080/hls/pb1/1/%d.ts?tkn=tkn-1", i)
		if i%5 == 0 {
			url = "http://edge:8080/hls/pb1/1/index.m3u8?tkn=tkn-1"
		}
		code, body, action := playRewrite(t, playRewriteBody("pb1", "192.0.2.10", url))
		if code != http.StatusOK || body != "live+s1" || action != "value" {
			t.Fatalf("request %d = %d %q action %q", i, code, body, action)
		}
	}
	if got := calls.get(string(mist.TriggerPlayRewrite)); got != 1 {
		t.Fatalf("PLAY_REWRITE reached Foghorn %d times for one admitted session, want 1", got)
	}

	// The token alone is not the session: another address, another token or a
	// new playback from the same address, which carries its own playback session,
	// is Foghorn's decision.
	newPlayback, err := mist.NewViewerSessionID()
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		playRewriteBody("pb1", "203.0.113.7", "http://edge:8080/hls/pb1/1/0.ts?tkn=tkn-1"),
		playRewriteBody("pb1", "192.0.2.10", "http://edge:8080/hls/pb1/1/0.ts?tkn=tkn-other"),
		playRewriteBody("pb1", "192.0.2.10", "http://edge:8080/hls/pb1/index.m3u8?tkn="+newPlayback),
	} {
		playRewrite(t, body)
	}
	if got := calls.get(string(mist.TriggerPlayRewrite)); got != 4 {
		t.Fatalf("PLAY_REWRITE reached Foghorn %d times, want 4 (each non-session request asks Foghorn)", got)
	}
}

// With Foghorn unreachable, sessions it admitted keep being served; a new
// session of a stream whose policy Foghorn decides per session is refused,
// while a new session of a public stream is checked against the held grant.
func TestFoghornOutageServesAdmittedSessionsAndRefusesNewWebhookSessions(t *testing.T) {
	setupTriggerTest(t, "tenant-grant")
	store := installTestPlaybackGrants(t, &fakeMistSessions{}, noGrantFetch(t))
	store.ApplyGrant(playbackgranttest.Grant("live+hook", ipcpb.PlaybackGrantPolicyKind_PLAYBACK_GRANT_POLICY_KIND_CONNECTED, nil, "pbhook"))
	store.ApplyGrant(playbackgranttest.Grant("live+open", ipcpb.PlaybackGrantPolicyKind_PLAYBACK_GRANT_POLICY_KIND_PUBLIC, nil, "pbopen"))

	foghornUp := true
	calls := &foghornCalls{}
	stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
		calls.add(trigger.GetTriggerType())
		if !foghornUp {
			return &control.MistTriggerResult{Abort: true, ErrorCode: ipcpb.IngestErrorCode_INGEST_ERROR_TIMEOUT}, errors.New("gRPC control stream not connected")
		}
		return &control.MistTriggerResult{Response: "true", Action: ipcpb.MistTriggerAction_MIST_TRIGGER_ACTION_VALUE}, nil
	})
	if code, body, _ := userNew(t, userNewBody("live+hook", "192.0.2.10", "tkn-hook", "sess-hook")); code != http.StatusOK || body != "true" {
		t.Fatalf("USER_NEW with Foghorn up = %d %q", code, body)
	}

	foghornUp = false
	before := calls.get(string(mist.TriggerPlayRewrite))
	for i := range 5 {
		code, body, _ := playRewrite(t, playRewriteBody("pbhook", "192.0.2.10", fmt.Sprintf("http://edge:8080/hls/pbhook/1/%d.ts?tkn=tkn-hook", i)))
		if code != http.StatusOK || body != "live+hook" {
			t.Fatalf("admitted session request %d during outage = %d %q", i, code, body)
		}
	}
	if got := calls.get(string(mist.TriggerPlayRewrite)); got != before {
		t.Fatalf("admitted session asked Foghorn %d times during the outage", got-before)
	}

	if code, _, _ := playRewrite(t, playRewriteBody("pbhook", "198.51.100.4", "http://edge:8080/hls/pbhook/index.m3u8")); code != http.StatusServiceUnavailable {
		t.Fatalf("new webhook-policy request during outage = %d, want 503", code)
	}
	if code, _, _ := userNew(t, userNewBody("live+hook", "198.51.100.4", "tkn-new", "sess-new")); code != http.StatusServiceUnavailable {
		t.Fatalf("new webhook-policy session during outage = %d, want 503", code)
	}

	if code, body, action := playRewrite(t, playRewriteBody("pbopen", "198.51.100.5", "http://edge:8080/hls/pbopen/index.m3u8")); code != http.StatusOK || body != "live+open" || action != "value" {
		t.Fatalf("new public request during outage = %d %q %q", code, body, action)
	}
	if code, body, _ := userNew(t, userNewBody("live+open", "198.51.100.5", "tkn-open", "sess-open")); code != http.StatusOK || body != "true" {
		t.Fatalf("new public session during outage = %d %q", code, body)
	}
	if code, body, _ := playRewrite(t, playRewriteBody("pbopen", "198.51.100.5", "http://edge:8080/hls/pbopen/1/0.ts?tkn=tkn-open")); code != http.StatusOK || body != "live+open" {
		t.Fatalf("locally admitted session = %d %q", code, body)
	}

	// No grant: the edge has no authority of its own.
	if code, _, _ := playRewrite(t, playRewriteBody("pbunknown", "198.51.100.6", "http://edge:8080/hls/pbunknown/index.m3u8")); code != http.StatusServiceUnavailable {
		t.Fatalf("ungranted stream during outage = %d, want 503", code)
	}
}

// Revoking a signing key reaches the edge as a new grant. The edge re-checks
// its sessions against the facts they were admitted with and invalidates, in
// Mist, only the sessions whose token was signed by the revoked key; Mist's
// re-run USER_NEW for them is refused here. Foghorn is not asked.
func TestRevokedKidInvalidatesOnlyItsSessionsWithoutFoghorn(t *testing.T) {
	setupTriggerTest(t, "tenant-grant")
	mistSessions := &fakeMistSessions{}
	store := installTestPlaybackGrants(t, mistSessions, noGrantFetch(t))
	k1 := playbackgranttest.NewSigningKey(t, "kid-1")
	k2 := playbackgranttest.NewSigningKey(t, "kid-2")
	store.ApplyGrant(playbackgranttest.Grant("live+jwt", ipcpb.PlaybackGrantPolicyKind_PLAYBACK_GRANT_POLICY_KIND_JWT, []playbackgranttest.SigningKey{k1, k2}, "pbjwt"))

	calls := &foghornCalls{}
	stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
		calls.add(trigger.GetTriggerType())
		return &control.MistTriggerResult{Response: "true", Action: ipcpb.MistTriggerAction_MIST_TRIGGER_ACTION_VALUE}, nil
	})
	exp := time.Now().Add(time.Hour)
	tokens := map[string]string{
		"sess-1a": k1.Token(t, "viewer-1a", exp),
		"sess-1b": k1.Token(t, "viewer-1b", exp),
		"sess-2":  k2.Token(t, "viewer-2", exp),
	}
	hosts := map[string]string{"sess-1a": "192.0.2.1", "sess-1b": "192.0.2.2", "sess-2": "192.0.2.3"}
	for id, token := range tokens {
		if code, body, _ := userNew(t, userNewBody("live+jwt", hosts[id], token, id)); code != http.StatusOK || body != "true" {
			t.Fatalf("USER_NEW %s = %d %q", id, code, body)
		}
	}
	admitted := calls.get(string(mist.TriggerUserNew)) + calls.get(string(mist.TriggerPlayRewrite))

	revoked := playbackgranttest.Grant("live+jwt", ipcpb.PlaybackGrantPolicyKind_PLAYBACK_GRANT_POLICY_KIND_JWT, []playbackgranttest.SigningKey{k2}, "pbjwt")
	revoked.ObjectAuthorityVersion = 2
	store.ApplyGrant(revoked)

	if got := mistSessions.invalidatedIDs(); !slices.Equal(got, []string{"sess-1a", "sess-1b"}) {
		t.Fatalf("invalidated sessions = %v, want the revoked kid's sessions only", got)
	}
	if code, body, action := userNew(t, userNewBody("live+jwt", hosts["sess-1a"], tokens["sess-1a"], "sess-1a")); code != http.StatusOK || body != "false" || action != "deny" {
		t.Fatalf("re-run USER_NEW of a revoked-kid session = %d %q %q, want a local refusal", code, body, action)
	}
	if code, body, _ := playRewrite(t, playRewriteBody("pbjwt", hosts["sess-2"], "http://edge:8080/hls/pbjwt/1/0.ts?tkn="+tokens["sess-2"])); code != http.StatusOK || body != "live+jwt" {
		t.Fatalf("surviving session = %d %q", code, body)
	}
	if got := calls.get(string(mist.TriggerUserNew)) + calls.get(string(mist.TriggerPlayRewrite)); got != admitted {
		t.Fatalf("the revocation asked Foghorn %d times", got-admitted)
	}
}

// A restarted Helmsman knows no sessions. It rebuilds them from Mist's live
// session list and fetches one grant per stream, after which the viewers'
// next requests are answered locally instead of each asking Foghorn.
func TestRestartRebuildsSessionsWithOneGrantFetchPerStream(t *testing.T) {
	setupTriggerTest(t, "tenant-grant")
	mistSessions := &fakeMistSessions{live: []mist.ViewerSession{
		{SessionID: "sA", Host: "::ffff:192.0.2.10", Stream: "live+s1", Protocol: "HLS"},
		{SessionID: "sB", Host: "::ffff:192.0.2.11", Stream: "live+s1", Protocol: "HLS"},
		{SessionID: "sC", Host: "192.0.2.12", Stream: "live+s2", Protocol: "HLS,HTTP"},
	}}
	fetches := map[string]int{}
	var fetchMu sync.Mutex
	store := installTestPlaybackGrants(t, mistSessions, func(_ context.Context, internal string) (*ipcpb.PlaybackGrant, error) {
		fetchMu.Lock()
		fetches[internal]++
		fetchMu.Unlock()
		return playbackgranttest.Grant(internal, ipcpb.PlaybackGrantPolicyKind_PLAYBACK_GRANT_POLICY_KIND_PUBLIC, nil, "pb-"+internal), nil
	})
	calls := &foghornCalls{}
	stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
		calls.add(trigger.GetTriggerType())
		return &control.MistTriggerResult{Response: "live+s1", Action: ipcpb.MistTriggerAction_MIST_TRIGGER_ACTION_VALUE}, nil
	})

	store.ControlConnected(context.Background())
	if len(fetches) != 2 || fetches["live+s1"] != 1 || fetches["live+s2"] != 1 {
		t.Fatalf("grant fetches = %v, want one per stream", fetches)
	}

	for i := range 10 {
		for _, req := range []struct{ requested, host, token, want string }{
			{"pb-live+s1", "192.0.2.10", "tkn-a", "live+s1"},
			{"pb-live+s1", "192.0.2.11", "tkn-b", "live+s1"},
			{"pb-live+s2", "192.0.2.12", "tkn-c", "live+s2"},
		} {
			url := fmt.Sprintf("http://edge:8080/hls/%s/1/%d.ts?tkn=%s", req.requested, i, req.token)
			if code, body, _ := playRewrite(t, playRewriteBody(req.requested, req.host, url)); code != http.StatusOK || body != req.want {
				t.Fatalf("rebuilt session %s request %d = %d %q", req.token, i, code, body)
			}
		}
	}
	if got := calls.get(string(mist.TriggerPlayRewrite)); got != 0 {
		t.Fatalf("rebuilt sessions asked Foghorn %d times", got)
	}
	// An address with no live Mist session is a new viewer.
	playRewrite(t, playRewriteBody("pb-live+s1", "192.0.2.99", "http://edge:8080/hls/pb-live+s1/1/0.ts?tkn=tkn-z"))
	if got := calls.get(string(mist.TriggerPlayRewrite)); got != 1 {
		t.Fatalf("new viewer after restart reached Foghorn %d times, want 1", got)
	}
	if len(fetches) != 2 || fetches["live+s1"] != 1 || fetches["live+s2"] != 1 {
		t.Fatalf("grant fetches after serving = %v, want still one per stream", fetches)
	}
}
