package orchestrator

import (
	"context"
	"testing"

	"frameworks/api_balancing/internal/state"
)

// platformAliases turns a node platform key into the artifact-map keys a
// release may use. "os/arch" must yield BOTH the slash form and the hyphen
// form because release artifacts are sometimes keyed "linux-amd64"; anything
// that isn't a clean 2-part key collapses to a single literal lookup (so an
// underscore-joined platform silently matches nothing rather than aliasing).
func TestPlatformAliases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want []string
	}{
		{"linux/amd64", []string{"linux/amd64", "linux-amd64"}},
		{"darwin/arm64", []string{"darwin/arm64", "darwin-arm64"}},
		{"linux", []string{"linux"}},
		{"a/b/c", []string{"a/b/c"}},
		{"", []string{""}},
		{"linux_amd64", []string{"linux_amd64"}},
	}
	for _, tc := range tests {
		got := platformAliases(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("platformAliases(%q) = %v, want %v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("platformAliases(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
			}
		}
	}
}

// nodePlatformKey lowercases and trims OS/Arch into "os/arch"; a node missing
// either field has no derivable platform and must return "" (so it is excluded
// from automatic rollout rather than matching a bogus artifact key).
func TestNodePlatformKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		node *state.NodeState
		want string
	}{
		{"lowercases and trims", &state.NodeState{OS: " LINUX ", Arch: "AMD64"}, "linux/amd64"},
		{"missing arch", &state.NodeState{OS: "linux"}, ""},
		{"missing os", &state.NodeState{Arch: "amd64"}, ""},
		{"nil node", nil, ""},
	}
	for _, tc := range tests {
		if got := nodePlatformKey(tc.node); got != tc.want {
			t.Errorf("%s: nodePlatformKey = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// The DeployMode gate accepts "native" and "container" (the single edge
// image applies in-place component updates exactly like native)
// case-insensitively, tolerating surrounding whitespace; everything else
// (the retired multi-container docker mode, empty, fenced operational
// modes, underivable platform) must be rejected so automatic release
// updates only ever touch healthy, normal-mode, convergeable nodes.
func TestNodeAllowsAutomaticReleaseUpdate_DeployModeNormalization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		node *state.NodeState
		want bool
	}{
		{"uppercase native", &state.NodeState{DeployMode: "NATIVE", OS: "linux", Arch: "amd64"}, true},
		{"padded native", &state.NodeState{DeployMode: "  native  ", OS: "linux", Arch: "amd64"}, true},
		{"container", &state.NodeState{DeployMode: "container", OS: "linux", Arch: "amd64"}, true},
		{"uppercase container", &state.NodeState{DeployMode: "CONTAINER", OS: "linux", Arch: "arm64"}, true},
		{"empty deploy mode", &state.NodeState{DeployMode: "", OS: "linux", Arch: "amd64"}, false},
		{"legacy docker", &state.NodeState{DeployMode: "docker", OS: "linux", Arch: "amd64"}, false},
		{"native but no platform", &state.NodeState{DeployMode: "native"}, false},
		{"container but no platform", &state.NodeState{DeployMode: "container"}, false},
		{"native but draining", &state.NodeState{DeployMode: "native", OS: "linux", Arch: "amd64", OperationalMode: state.NodeModeDraining}, false},
		{"container but draining", &state.NodeState{DeployMode: "container", OS: "linux", Arch: "amd64", OperationalMode: state.NodeModeDraining}, false},
		{"nil node", nil, false},
	}
	for _, tc := range tests {
		if got := nodeAllowsAutomaticReleaseUpdate(tc.node); got != tc.want {
			t.Errorf("%s: nodeAllowsAutomaticReleaseUpdate = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// When a component carries no per-platform Artifacts map, the bare
// ArtifactURL/Checksum is a legacy single-binary release that only applies to
// linux/amd64. A node of any other platform must NOT receive it (ok=false),
// otherwise a darwin node would try to run a linux binary.
func TestReleaseComponentForNode_BareURLFallbackIsLinuxAmd64Only(t *testing.T) {
	t.Parallel()

	component := releaseComponent{
		Version:     "v1.2.3",
		ArtifactURL: "https://example.test/binary.tgz",
		Checksum:    "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
	}

	if selected, ok := releaseComponentForNode(component, &state.NodeState{OS: "linux", Arch: "amd64"}); !ok || selected.ArtifactURL != component.ArtifactURL {
		t.Errorf("linux/amd64 should accept the bare artifact; got ok=%v url=%q", ok, selected.ArtifactURL)
	}
	if _, ok := releaseComponentForNode(component, &state.NodeState{OS: "darwin", Arch: "arm64"}); ok {
		t.Error("non-linux/amd64 node must not accept a bare (linux) artifact")
	}
	if _, ok := releaseComponentForNode(component, &state.NodeState{}); ok {
		t.Error("node with no derivable platform must return ok=false")
	}
}

// desiredComponentsFromExpected normalizes (lowercase+trim) component names,
// drops entries whose version is empty/whitespace, and returns a slice sorted
// by component so reconcile diffs are deterministic.
func TestDesiredComponentsFromExpected_NormalizesAndSorts(t *testing.T) {
	t.Parallel()

	got := desiredComponentsFromExpected(map[string]string{
		" Helmsman ": "v2",
		"MIST":       "v1",
		"foghorn":    "  ", // whitespace-only version -> skipped
	})
	if len(got) != 2 {
		t.Fatalf("want 2 components (foghorn dropped), got %d: %+v", len(got), got)
	}
	if got[0].GetComponent() != "helmsman" || got[1].GetComponent() != "mist" {
		t.Fatalf("want sorted [helmsman, mist], got [%s, %s]", got[0].GetComponent(), got[1].GetComponent())
	}
}

// rolloutBudget's batch/canary math is isolated here by passing an empty node
// set: with no nodes, activeUpdateCount and completedTargetCount are both 0
// regardless of DB state. The canary gate caps the first wave to CanaryCount
// (until a completion lands), and the budget is never negative. The
// "completion unlocks the full batch" branch needs node phases in the DB and
// is intentionally out of scope for this pure-math unit.
func TestRolloutBudget_BatchAndCanaryMath(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	var noNodes []*state.NodeState

	tests := []struct {
		name string
		plan rolloutPlan
		want int
	}{
		{"non-canary uses full batch", rolloutPlan{BatchSize: 5}, 5},
		{"canary caps first wave", rolloutPlan{BatchSize: 5, Canary: true, CanaryCount: 1}, 1},
		{"canary >= batch does not cap", rolloutPlan{BatchSize: 5, Canary: true, CanaryCount: 5}, 5},
		{"canary count larger than batch stays at batch", rolloutPlan{BatchSize: 5, Canary: true, CanaryCount: 10}, 5},
		{"zero batch yields zero, never negative", rolloutPlan{BatchSize: 0}, 0},
	}
	for _, tc := range tests {
		if got := rolloutBudget(ctx, noNodes, "rel-target", tc.plan); got != tc.want {
			t.Errorf("%s: rolloutBudget = %d, want %d", tc.name, got, tc.want)
		}
	}
}
