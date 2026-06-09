package control

import (
	"strings"
	"testing"

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
// artifact whose authoritative cluster is X: yes if empty/local/served, or if X
// is an authorized tenant peer; otherwise no (cross-cluster reauth at the front
// door).
func TestAuthoritativeClusterServable(t *testing.T) {
	// Empty authoritative cluster is always serveable.
	if !AuthoritativeClusterServable("", nil) {
		t.Fatal("empty authoritative cluster should be serveable")
	}
	// A foreign cluster with no peer authorization is NOT serveable.
	if AuthoritativeClusterServable("remote-x", nil) {
		t.Fatal("unauthorized foreign cluster must not be serveable")
	}
	// Same foreign cluster, now listed as an authorized peer → serveable.
	peers := []*clusterpeerpb.TenantClusterPeer{{ClusterId: "remote-x"}}
	if !AuthoritativeClusterServable("remote-x", peers) {
		t.Fatal("authorized peer cluster should be serveable")
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

// AppendCorrelationID stamps the viewer id as ?fwcid=... ; empty inputs are
// passed through unchanged.
func TestAppendCorrelationID(t *testing.T) {
	if got := AppendCorrelationID("", "v1"); got != "" {
		t.Errorf("empty url should pass through, got %q", got)
	}
	if got := AppendCorrelationID("https://h/x", ""); got != "https://h/x" {
		t.Errorf("empty viewer id should pass through, got %q", got)
	}
	got := AppendCorrelationID("https://h/x?a=1", "v1")
	if !strings.Contains(got, "fwcid=v1") || !strings.Contains(got, "a=1") {
		t.Errorf("expected fwcid + preserved query, got %q", got)
	}
}

// AppendViewerCorrelationID stamps the id onto the primary endpoint, its derived
// outputs, and every fallback. A nil response or empty id is a no-op.
func TestAppendViewerCorrelationID(t *testing.T) {
	AppendViewerCorrelationID(nil, "v1") // must not panic

	resp := &sharedpb.ViewerEndpointResponse{
		Primary: &sharedpb.ViewerEndpoint{
			Url:     "https://h/primary",
			Outputs: map[string]*sharedpb.OutputEndpoint{"mp4": {Url: "https://h/out.mp4"}},
		},
		Fallbacks: []*sharedpb.ViewerEndpoint{{Url: "https://h/fallback"}},
	}
	AppendViewerCorrelationID(resp, "v1")

	if !strings.Contains(resp.Primary.GetUrl(), "fwcid=v1") {
		t.Errorf("primary url not stamped: %q", resp.Primary.GetUrl())
	}
	if !strings.Contains(resp.Primary.GetOutputs()["mp4"].GetUrl(), "fwcid=v1") {
		t.Errorf("primary output not stamped: %q", resp.Primary.GetOutputs()["mp4"].GetUrl())
	}
	if !strings.Contains(resp.Fallbacks[0].GetUrl(), "fwcid=v1") {
		t.Errorf("fallback not stamped: %q", resp.Fallbacks[0].GetUrl())
	}
}
