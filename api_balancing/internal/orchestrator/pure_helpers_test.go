package orchestrator

import (
	"encoding/hex"
	"testing"

	"frameworks/api_balancing/internal/state"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// expectedComponentVersions folds a desired-component list into a name→version
// map for diffing against a node's installed set. It is deliberately lenient on
// junk: nil entries, blank names, and blank versions are dropped (a component
// with no version is "no opinion", not "install empty"), and names are
// lowercased so the diff is case-insensitive against the DB's component keys.
func TestExpectedComponentVersions(t *testing.T) {
	got := expectedComponentVersions([]*ipcpb.DesiredComponent{
		{Component: "MistServer", Version: "1.2.3"},
		{Component: "  helmsman ", Version: "  9.9 "},
		nil,                                 // dropped
		{Component: "foghorn", Version: ""}, // dropped: no version opinion
		{Component: "", Version: "1.0"},     // dropped: no name
		{Component: "  ", Version: "1.0"},   // dropped: blank name
	})

	want := map[string]string{
		"mistserver": "1.2.3",
		"helmsman":   "9.9",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("component %q = %q, want %q", k, got[k], v)
		}
	}

	if got := expectedComponentVersions(nil); len(got) != 0 {
		t.Errorf("nil input should yield empty map, got %v", got)
	}
}

// parseReleaseComponents unmarshals the release JSON blob carried on a target.
// Valid JSON round-trips into the keyed map; malformed JSON must return a
// wrapped error rather than a nil map the caller would treat as "no components"
// — a silent empty parse would skip a real rollout.
func TestParseReleaseComponents(t *testing.T) {
	comps, err := parseReleaseComponents(`{"mistserver":{"version":"1.0","artifact_url":"https://x/m","checksum":"sha256:ab"}}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	c, ok := comps["mistserver"]
	if !ok {
		t.Fatalf("mistserver missing from %v", comps)
	}
	if c.Version != "1.0" || c.ArtifactURL != "https://x/m" || c.Checksum != "sha256:ab" {
		t.Errorf("decoded component = %+v", c)
	}

	if _, err := parseReleaseComponents(`{not json`); err == nil {
		t.Error("malformed JSON must return an error, not a nil map")
	}
}

// nodesInCluster filters a cluster snapshot down to one cluster, tolerating nil
// entries. Unlike eligibleNodes it does NOT apply health/mode gating — it is the
// denominator for rollout-failure accounting, so an unhealthy or fenced node in
// the cluster must still be counted here.
func TestNodesInCluster(t *testing.T) {
	nodes := []*state.NodeState{
		{NodeID: "a", ClusterID: "c1"},
		{NodeID: "b", ClusterID: "c2"},
		nil,
		{NodeID: "c", ClusterID: "c1", IsStale: true}, // unhealthy but still in-cluster
	}
	got := nodesInCluster(nodes, "c1")
	if len(got) != 2 {
		t.Fatalf("got %d nodes, want 2: %+v", len(got), got)
	}
	for _, n := range got {
		if n.ClusterID != "c1" {
			t.Errorf("node %s leaked from cluster %s", n.NodeID, n.ClusterID)
		}
	}
	if got := nodesInCluster(nil, "c1"); len(got) != 0 {
		t.Errorf("nil snapshot should yield no nodes, got %v", got)
	}
}

// newCordonToken mints the opaque token a node must echo to prove it honoured a
// cordon. It must be a 32-byte value hex-encoded (64 chars) and effectively
// unique per call — a predictable or repeating token would let a stale node
// replay an old cordon acknowledgement.
func TestNewCordonToken(t *testing.T) {
	seen := make(map[string]struct{})
	for range 100 {
		tok, err := newCordonToken()
		if err != nil {
			t.Fatalf("newCordonToken: %v", err)
		}
		if len(tok) != 64 {
			t.Fatalf("token %q len = %d, want 64 hex chars", tok, len(tok))
		}
		if _, err := hex.DecodeString(tok); err != nil {
			t.Fatalf("token %q is not valid hex: %v", tok, err)
		}
		if _, dup := seen[tok]; dup {
			t.Fatalf("duplicate token minted: %q", tok)
		}
		seen[tok] = struct{}{}
	}
}
