package control

import (
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
)

// PlaybackEdgeRedirectURL reduces any base URL to host-only and rebuilds a
// canonical https://<host>/play/<id> redirect — dropping scheme and path so a
// cross-cluster edge redirect is always well-formed. An unusable base yields "".
func TestPlaybackEdgeRedirectURL(t *testing.T) {
	cases := []struct{ base, id, want string }{
		{"https://edge.example.com:8080/foo", "pb1", "https://edge.example.com:8080/play/pb1"},
		{"http://edge.example.com", "pb2", "https://edge.example.com/play/pb2"},
		{"edge.example.com/x/y", "pb3", "https://edge.example.com/play/pb3"},
		{"  ", "pb4", ""},
		{"https://", "pb5", ""},
	}
	for _, c := range cases {
		if got := PlaybackEdgeRedirectURL(c.base, c.id); got != c.want {
			t.Errorf("PlaybackEdgeRedirectURL(%q,%q) = %q, want %q", c.base, c.id, got, c.want)
		}
	}
}

// AuthoritativeClusterServable decides whether THIS foghorn may serve an
// artifact whose authoritative cluster is X. Unresolved tenant/cluster context
// fails closed; a current peer entry authorizes a foreign cluster.
func TestAuthoritativeClusterServable(t *testing.T) {
	freshClusterAccessCache(t)
	if AuthoritativeClusterServable("", "", nil) {
		t.Fatal("unresolved tenant/cluster context must fail closed")
	}
	// A foreign cluster with no peer authorization is NOT serveable.
	if AuthoritativeClusterServable("remote-x", "tenant-a", nil) {
		t.Fatal("unauthorized foreign cluster must not be serveable")
	}
	// Same foreign cluster, now listed as an authorized peer → serveable.
	peers := []*clusterpeerpb.TenantClusterPeer{{ClusterId: "remote-x"}}
	if !AuthoritativeClusterServable("remote-x", "tenant-a", peers) {
		t.Fatal("authorized peer cluster should be serveable")
	}
	if _, ok := clusterAccessCache.Peek("access:tenant-a"); ok {
		t.Fatal("front-door authorization must not mutate process-local USER_NEW state")
	}
}

// isAuthorizedPeerCluster: empty cluster or empty peer list is never authorized;
// a member match authorizes.
func TestIsAuthorizedPeerCluster(t *testing.T) {
	peers := []*clusterpeerpb.TenantClusterPeer{{ClusterId: "c1"}, {ClusterId: "c2"}}
	if isAuthorizedPeerCluster("", peers) {
		t.Error("empty cluster id must not be authorized")
	}
	if isAuthorizedPeerCluster("c1", nil) {
		t.Error("empty peer list must not authorize")
	}
	if !isAuthorizedPeerCluster("c2", peers) {
		t.Error("member cluster should be authorized")
	}
	if isAuthorizedPeerCluster("c3", peers) {
		t.Error("non-member cluster must not be authorized")
	}
}

func TestEnsureTrailingSlash(t *testing.T) {
	if got := EnsureTrailingSlash("x"); got != "x/" {
		t.Errorf("EnsureTrailingSlash(x) = %q, want x/", got)
	}
	if got := EnsureTrailingSlash("x/"); got != "x/" {
		t.Errorf("EnsureTrailingSlash(x/) = %q, want x/ (idempotent)", got)
	}
}

// toWebSocketURL maps http(s)→ws(s), preserves existing ws(s), resolves
// scheme-relative (//host) by the secureDefault, and passes through anything else.
func TestToWebSocketURL(t *testing.T) {
	cases := []struct {
		raw    string
		secure bool
		want   string
	}{
		{"", true, ""},
		{"ws://h/x", true, "ws://h/x"},
		{"wss://h/x", true, "wss://h/x"},
		{"//h/x", true, "wss://h/x"},
		{"//h/x", false, "ws://h/x"},
		{"https://h/x", true, "wss://h/x"},
		{"http://h/x", false, "ws://h/x"},
		{"ftp://h/x", true, "ftp://h/x"}, // unknown scheme passes through
	}
	for _, c := range cases {
		if got := toWebSocketURL(c.raw, c.secure); got != c.want {
			t.Errorf("toWebSocketURL(%q,%v) = %q, want %q", c.raw, c.secure, got, c.want)
		}
	}
}

// AppendViewerSessionParam stamps the playback session as Mist's session token (?tkn=...);
// empty inputs are passed through unchanged.
func TestAppendViewerSessionParam(t *testing.T) {
	if got := AppendViewerSessionParam("", "v1"); got != "" {
		t.Errorf("empty url should pass through, got %q", got)
	}
	if got := AppendViewerSessionParam("https://h/x", ""); got != "https://h/x" {
		t.Errorf("empty session should pass through, got %q", got)
	}
	got := AppendViewerSessionParam("https://h/x?a=1", "v1")
	if !strings.Contains(got, "tkn=v1") || !strings.Contains(got, "a=1") {
		t.Errorf("expected tkn + preserved query, got %q", got)
	}
}

// AppendViewerSession stamps the session onto the primary endpoint, its derived
// outputs, and every fallback. A nil response or empty session is a no-op.
func TestAppendViewerSession(t *testing.T) {
	AppendViewerSession(nil, "v1") // must not panic

	resp := &sharedpb.ViewerEndpointResponse{
		Primary: &sharedpb.ViewerEndpoint{
			Url:     "https://h/primary",
			Outputs: map[string]*sharedpb.OutputEndpoint{"mp4": {Url: "https://h/out.mp4"}},
		},
		Fallbacks: []*sharedpb.ViewerEndpoint{{Url: "https://h/fallback"}},
	}
	AppendViewerSession(resp, "v1")

	if !strings.Contains(resp.Primary.GetUrl(), "tkn=v1") {
		t.Errorf("primary url not stamped: %q", resp.Primary.GetUrl())
	}
	if !strings.Contains(resp.Primary.GetOutputs()["mp4"].GetUrl(), "tkn=v1") {
		t.Errorf("primary output not stamped: %q", resp.Primary.GetOutputs()["mp4"].GetUrl())
	}
	if !strings.Contains(resp.Fallbacks[0].GetUrl(), "tkn=v1") {
		t.Errorf("fallback not stamped: %q", resp.Fallbacks[0].GetUrl())
	}
}

// Every resolved playback leaves with exactly one session on all of its URLs: the one a prepared
// destination reserved under when any endpoint carries it, otherwise a newly issued one. The
// resolved response itself is never modified, since endpoint catalogs can be shared.
func TestWithViewerSessionIssuesOneSessionPerPlayback(t *testing.T) {
	catalog := &sharedpb.ViewerEndpointResponse{
		Primary: &sharedpb.ViewerEndpoint{Url: "https://warm/hls/a/index.m3u8",
			Outputs: map[string]*sharedpb.OutputEndpoint{"MP4": {Url: "https://warm/a.mp4"}}},
		Fallbacks: []*sharedpb.ViewerEndpoint{{Url: "https://other/hls/a/index.m3u8"}},
	}
	first, err := WithViewerSession(catalog)
	if err != nil {
		t.Fatal(err)
	}
	second, err := WithViewerSession(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(catalog.GetPrimary().GetUrl(), "tkn=") || strings.Contains(catalog.GetPrimary().GetOutputs()["MP4"].GetUrl(), "tkn=") {
		t.Fatalf("the shared catalog was stamped: %v", catalog)
	}
	session := mist.ViewerSessionID(first.GetPrimary().GetUrl())
	if session == "" || session == mist.ViewerSessionID(second.GetPrimary().GetUrl()) {
		t.Fatalf("two resolutions got sessions %q and %q, want two distinct issued sessions",
			session, mist.ViewerSessionID(second.GetPrimary().GetUrl()))
	}
	for _, url := range []string{first.GetPrimary().GetOutputs()["MP4"].GetUrl(), first.GetFallbacks()[0].GetUrl()} {
		if mist.ViewerSessionID(url) != session {
			t.Errorf("%q does not carry the playback's session %q", url, session)
		}
	}

	prepared, err := mist.NewViewerSessionID()
	if err != nil {
		t.Fatal(err)
	}
	withPrepared := &sharedpb.ViewerEndpointResponse{
		Primary:   &sharedpb.ViewerEndpoint{Url: "https://warm/a.mp4"},
		Fallbacks: []*sharedpb.ViewerEndpoint{{Url: AppendViewerSessionParam("https://prepared/a.mp4", prepared)}},
	}
	stamped, err := WithViewerSession(withPrepared)
	if err != nil {
		t.Fatal(err)
	}
	if mist.ViewerSessionID(stamped.GetPrimary().GetUrl()) != prepared {
		t.Fatalf("primary %q does not carry the prepared destination's session %q", stamped.GetPrimary().GetUrl(), prepared)
	}
}
