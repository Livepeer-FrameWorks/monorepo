package control

import (
	"math"
	"testing"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/state"
)

func TestArtifactGeoDistance(t *testing.T) {
	// Intent: a node with no geo (0,0) is treated as infinitely far so it sorts
	// last, never preferred over a node with real coordinates. A located node
	// delegates to the (already-tested) haversine.
	if d := artifactGeoDistance(52.0, 5.0, state.ArtifactNodeInfo{}); d != math.MaxFloat64 {
		t.Errorf("zero-coord node distance = %v, want MaxFloat64", d)
	}
	near := state.ArtifactNodeInfo{GeoLatitude: 52.1, GeoLongitude: 5.1}
	got := artifactGeoDistance(52.0, 5.0, near)
	want := CalculateGeoDistance(52.0, 5.0, 52.1, 5.1)
	if got != want {
		t.Errorf("located node distance = %v, want %v (delegates to CalculateGeoDistance)", got, want)
	}
	if got <= 0 || got == math.MaxFloat64 {
		t.Errorf("located node distance should be a small positive km value, got %v", got)
	}
}

func TestRankArtifactNodes(t *testing.T) {
	t.Run("empty returns nil", func(t *testing.T) {
		if got := rankArtifactNodes(nil, 0, 0, 5); got != nil {
			t.Errorf("empty input should return nil, got %v", got)
		}
	})

	t.Run("no viewer geo sorts by score then nodeID", func(t *testing.T) {
		// viewer (0,0) disables geo ranking; ordering is Score asc, then NodeID.
		in := []state.ArtifactNodeInfo{
			{NodeID: "c", Score: 5},
			{NodeID: "a", Score: 10},
			{NodeID: "b", Score: 5},
		}
		got := rankArtifactNodes(in, 0, 0, 0)
		order := []string{got[0].NodeID, got[1].NodeID, got[2].NodeID}
		// Score 5 (b,c by nodeID) before score 10 (a).
		want := []string{"b", "c", "a"}
		for i := range want {
			if order[i] != want[i] {
				t.Fatalf("order = %v, want %v", order, want)
			}
		}
	})

	t.Run("viewer geo sorts nearest first, zero-coord last", func(t *testing.T) {
		viewerLat, viewerLon := 52.0, 5.0
		in := []state.ArtifactNodeInfo{
			{NodeID: "far", GeoLatitude: 40.0, GeoLongitude: -74.0}, // New York-ish
			{NodeID: "none"}, // no geo → MaxFloat64
			{NodeID: "near", GeoLatitude: 52.2, GeoLongitude: 5.2}, // close to viewer
		}
		got := rankArtifactNodes(in, viewerLat, viewerLon, 0)
		if got[0].NodeID != "near" {
			t.Errorf("nearest should be first, got %q", got[0].NodeID)
		}
		if got[2].NodeID != "none" {
			t.Errorf("zero-coord node should sort last, got %q", got[2].NodeID)
		}
	})

	t.Run("maxNodes caps the result", func(t *testing.T) {
		in := []state.ArtifactNodeInfo{
			{NodeID: "a", Score: 1}, {NodeID: "b", Score: 2},
			{NodeID: "c", Score: 3}, {NodeID: "d", Score: 4},
		}
		if got := rankArtifactNodes(in, 0, 0, 2); len(got) != 2 {
			t.Fatalf("maxNodes=2 should cap to 2, got %d", len(got))
		}
		if got := rankArtifactNodes(in, 0, 0, 0); len(got) != 4 {
			t.Fatalf("maxNodes=0 should return all, got %d", len(got))
		}
		if got := rankArtifactNodes(in, 0, 0, 10); len(got) != 4 {
			t.Fatalf("maxNodes>len should return all, got %d", len(got))
		}
	})
}

func TestRankNodeScoresForArtifact(t *testing.T) {
	// Intent: cold-artifact relay reads run on the LOCAL cluster's edge, so
	// remote-cluster candidates (ClusterID != "") must be dropped; the rest map
	// 1:1 into ArtifactNodeInfo and are ranked (cap 5).
	in := []balancer.NodeWithScore{
		{NodeID: "local-1", Host: "h1", Score: 3, ClusterID: ""},
		{NodeID: "remote", Host: "h2", Score: 1, ClusterID: "peer-cluster"},
		{NodeID: "local-2", Host: "h3", Score: 1, ClusterID: ""},
	}
	got := rankNodeScoresForArtifact(in, 0, 0)
	if len(got) != 2 {
		t.Fatalf("remote-cluster node must be skipped: got %d nodes, want 2", len(got))
	}
	for _, n := range got {
		if n.NodeID == "remote" {
			t.Fatal("remote-cluster node leaked into ranking")
		}
	}
	// local-2 (score 1) ranks before local-1 (score 3); fields mapped through.
	if got[0].NodeID != "local-2" || got[0].Score != 1 || got[0].Host != "h3" {
		t.Errorf("first ranked = %+v, want local-2/score1/h3", got[0])
	}
}

func TestPreferredArtifactOutputKeys(t *testing.T) {
	cases := map[string][]string{
		"m3u8":    {"HLS", "DASH", "CMAF", "HDS"},
		"M3U8":    {"HLS", "DASH", "CMAF", "HDS"}, // case-insensitive
		"  mp4  ": {"HTTP", "MP4", "HLS", "DASH", "CMAF"},
		"webm":    {"HTTP", "WEBM", "HLS", "DASH", "CMAF"},
		"flv":     {"HTTP", "HLS", "DASH", "CMAF"}, // default
		"":        {"HTTP", "HLS", "DASH", "CMAF"},
	}
	for format, want := range cases {
		got := preferredArtifactOutputKeys(format)
		if len(got) != len(want) {
			t.Fatalf("preferredArtifactOutputKeys(%q) = %v, want %v", format, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("preferredArtifactOutputKeys(%q) = %v, want %v", format, got, want)
			}
		}
	}
}

func TestSelectPrimaryArtifactOutput(t *testing.T) {
	const baseURL, pid = "https://edge.example:8080", "abc123"

	t.Run("nil outputs", func(t *testing.T) {
		k, u := selectPrimaryArtifactOutput(nil, baseURL, pid, "m3u8")
		if k != "" || u != "" {
			t.Errorf("nil outputs should yield empty, got (%q,%q)", k, u)
		}
	})

	t.Run("preferred key wins and key is lowercased", func(t *testing.T) {
		// mp4 precedence is HTTP > MP4 > HLS; HTTP present so it wins over HLS.
		outputs := map[string]any{
			"HLS":  "https://edge/view/$/index.m3u8",
			"HTTP": "https://edge/view/$/video.mp4",
		}
		k, u := selectPrimaryArtifactOutput(outputs, baseURL, pid, "mp4")
		if k != "http" {
			t.Errorf("key = %q, want http (precedence + lowercase)", k)
		}
		if u != "https://edge/view/abc123/video.mp4" {
			t.Errorf("url = %q, want $-expanded mp4 url", u)
		}
	})

	t.Run("skips keys that resolve empty", func(t *testing.T) {
		// HLS value is empty → ResolveTemplateURL returns "" → fall through to DASH.
		outputs := map[string]any{
			"HLS":  "",
			"DASH": "https://edge/view/$/manifest.mpd",
		}
		k, u := selectPrimaryArtifactOutput(outputs, baseURL, pid, "m3u8")
		if k != "dash" || u != "https://edge/view/abc123/manifest.mpd" {
			t.Errorf("got (%q,%q), want (dash, expanded mpd)", k, u)
		}
	})

	t.Run("no matching key", func(t *testing.T) {
		outputs := map[string]any{"RTMP": "rtmp://edge/$/live"}
		k, u := selectPrimaryArtifactOutput(outputs, baseURL, pid, "m3u8")
		if k != "" || u != "" {
			t.Errorf("no preferred key present should yield empty, got (%q,%q)", k, u)
		}
	})
}
