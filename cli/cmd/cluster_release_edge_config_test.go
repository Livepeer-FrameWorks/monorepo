package cmd

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/provisioner"

	"github.com/spf13/cobra"
)

const releaseEdgeConfigTestManifest = `version: v1
type: edge
root_domain: example.test
pool_domain: edge.example.test
cluster_id: media-eu
channel: candidate
mode: native
nodes:
  - name: edge-a
    ssh: root@10.0.0.1
  - name: edge-b
    ssh: root@10.0.0.2
  - name: edge-fresh
    ssh: root@10.0.0.3
`

// releaseEdgeStageHarness fakes the edge release target, the edge rollout
// wait, and the hosts behind the edge manifest: every node but edge-fresh is
// enrolled, and Inspect reports what each node's check-mode run would change.
type releaseEdgeStageHarness struct {
	converged   bool
	inspections map[string]provisioner.EdgeInspection
	log         *rolloutLog
	pinned      []string
	versions    map[string]string
}

func (h *releaseEdgeStageHarness) install(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	h.log = &rolloutLog{}
	h.versions = map[string]string{}
	origSync, origVerify := releaseSyncEdgeTargetFn, releaseVerifyEdgeRolloutFn
	origDetect, origPrepare := edgeManifestDetectEnrollmentFn, edgeManifestPrepareNodeFn
	t.Cleanup(func() {
		releaseSyncEdgeTargetFn, releaseVerifyEdgeRolloutFn = origSync, origVerify
		edgeManifestDetectEnrollmentFn, edgeManifestPrepareNodeFn = origDetect, origPrepare
	})
	releaseSyncEdgeTargetFn = func(_ *cobra.Command, _ *resolvedCluster, channel, version string, _ map[string]string) error {
		h.pinned = append(h.pinned, channel+"/"+version)
		return nil
	}
	releaseVerifyEdgeRolloutFn = func(*cobra.Command, *resolvedCluster, time.Duration) (bool, error) {
		h.log.add("edges-report-release")
		return h.converged, nil
	}
	edgeManifestDetectEnrollmentFn = func(_ context.Context, sshTarget, _ string) (*provisioner.EdgeEnrollment, error) {
		if sshTarget == "root@10.0.0.3" {
			return nil, nil
		}
		return &provisioner.EdgeEnrollment{NodeID: sshTarget}, nil
	}
	edgeManifestPrepareNodeFn = func(n inventory.EdgeNode, version string, _ func() (edgeRolloutNode, error)) (edgeRolloutNode, error) {
		h.versions[n.Name] = version
		node, _ := liveNode(h.log, n.Name, h.inspections[n.Name])
		return node, nil
	}
}

func (h *releaseEdgeStageHarness) run(t *testing.T, rc *resolvedCluster, opts releaseApplyOptions) (string, error) {
	t.Helper()
	cmd := newClusterReleaseApplyCmd()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetContext(context.Background())
	err := runReleaseEdgeStage(cmd, rc, opts, "candidate", "v0.3.13-rc7", "v0.3.13-rc7")
	return out.String(), err
}

func writeReleaseEdgeManifest(t *testing.T) (dir, clusterPath, edgePath string) {
	t.Helper()
	dir = t.TempDir()
	clusterPath = filepath.Join(dir, "cluster.yaml")
	edgePath = filepath.Join(dir, "edge.yaml")
	if err := os.WriteFile(clusterPath, []byte("version: v1\ntype: cluster\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(edgePath, []byte(releaseEdgeConfigTestManifest), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, clusterPath, edgePath
}

// After `cluster release apply`, edges whose config an older CLI rendered
// must carry this CLI's config without a separate `edge provision`: once the
// fleet reports the release, every enrolled edge of the edge manifest is
// checked, the drifted ones are re-rendered one at a time against the
// release, and a node without drift is left alone.
func TestReleaseApplyReRendersEdgeConfigOlderThanTheCLI(t *testing.T) {
	_, _, edgePath := writeReleaseEdgeManifest(t)
	markerOnly := provisioner.ClassifyEdgeChanges([]string{"Write edge config marker"})
	h := &releaseEdgeStageHarness{converged: true, inspections: map[string]provisioner.EdgeInspection{
		"edge-a": markerOnly,
		"edge-b": inSync,
	}}
	h.install(t)

	out, err := h.run(t, &resolvedCluster{}, releaseApplyOptions{edgeManifest: edgePath})
	if err != nil {
		t.Fatalf("edge stage: %v\n%s", err, out)
	}
	if !slices.Equal(h.pinned, []string{"candidate/v0.3.13-rc7"}) {
		t.Fatalf("edge release target pins = %v, want exactly the release apply pin", h.pinned)
	}
	prechecksThen(t, h.log.snapshot()[1:], []string{"edge-a:inspect", "edge-b:inspect"}, []string{"edge-a:apply"})
	if got := h.log.snapshot()[0]; got != "edges-report-release" {
		t.Fatalf("first event = %q, want the edge config to wait for the fleet to report the release", got)
	}
	for _, name := range []string{"edge-a", "edge-b"} {
		if h.versions[name] != "v0.3.13-rc7" {
			t.Fatalf("%s rendered with %q, want the applied release v0.3.13-rc7", name, h.versions[name])
		}
	}
	if _, prepared := h.versions["edge-fresh"]; prepared {
		t.Fatal("release apply prepared a node that was never enrolled")
	}
	for _, want := range []string{"edge-a", "apply=1", "skip=1", "not enrolled", "edge-fresh", "Release v0.3.13-rc7 applied"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

// The edge manifest defaults to edge.yaml beside the cluster manifest.
func TestReleaseApplyEdgeConfigUsesSiblingEdgeManifest(t *testing.T) {
	_, clusterPath, edgePath := writeReleaseEdgeManifest(t)
	if got := releaseEdgeManifestPath(&resolvedCluster{ManifestPath: clusterPath}, ""); got != edgePath {
		t.Fatalf("edge manifest = %q, want %q", got, edgePath)
	}
	if got := releaseEdgeManifestPath(&resolvedCluster{ManifestPath: clusterPath}, "/elsewhere/edge.yaml"); got != "/elsewhere/edge.yaml" {
		t.Fatalf("edge manifest = %q, want the --edge-manifest value", got)
	}
}

// A fleet still rolling when the wait ends gets the exact command instead of
// a config render against binaries it does not run yet.
func TestReleaseApplyEdgeConfigWaitsForTheFleet(t *testing.T) {
	_, _, edgePath := writeReleaseEdgeManifest(t)
	h := &releaseEdgeStageHarness{converged: false}
	h.install(t)

	out, err := h.run(t, &resolvedCluster{}, releaseApplyOptions{edgeManifest: edgePath})
	if err != nil {
		t.Fatalf("edge stage: %v\n%s", err, out)
	}
	if events := h.log.snapshot(); !slices.Equal(events, []string{"edges-report-release"}) {
		t.Fatalf("events = %v, want no edge touched before the fleet reports the release", events)
	}
	if want := "frameworks edge provision --manifest " + edgePath + " --version v0.3.13-rc7"; !strings.Contains(out, want) {
		t.Fatalf("output missing the next command %q:\n%s", want, out)
	}
}

// Without an edge manifest release apply cannot reach the edges and says
// which command re-renders their config.
func TestReleaseApplyEdgeConfigWithoutEdgeManifestNamesTheCommand(t *testing.T) {
	h := &releaseEdgeStageHarness{converged: true}
	h.install(t)

	out, err := h.run(t, &resolvedCluster{ManifestPath: filepath.Join(t.TempDir(), "cluster.yaml")}, releaseApplyOptions{})
	if err != nil {
		t.Fatalf("edge stage: %v\n%s", err, out)
	}
	if len(h.versions) != 0 {
		t.Fatalf("prepared nodes %v without an edge manifest", h.versions)
	}
	if want := "frameworks edge provision --manifest <edge-manifest> --version v0.3.13-rc7"; !strings.Contains(out, want) {
		t.Fatalf("output missing %q:\n%s", want, out)
	}
}
