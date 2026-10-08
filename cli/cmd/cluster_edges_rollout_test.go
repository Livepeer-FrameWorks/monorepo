package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

type fakeEdgeRolloutSource struct {
	exp      edgeRolloutExpectation
	enrolled []string
	statuses []*foghorncontrolpb.NodeUpdateStatus
	reads    int
}

func (f *fakeEdgeRolloutSource) Expectation(context.Context, string) (edgeRolloutExpectation, error) {
	f.reads++
	return f.exp, nil
}

func (f *fakeEdgeRolloutSource) EnrolledEdges(context.Context, string) ([]string, error) {
	return f.enrolled, nil
}

func (f *fakeEdgeRolloutSource) NodeStatuses(context.Context, string) ([]*foghorncontrolpb.NodeUpdateStatus, error) {
	return f.statuses, nil
}

func edgeStatus(nodeID, phase, lastError string, versions map[string]string) *foghorncontrolpb.NodeUpdateStatus {
	status := &foghorncontrolpb.NodeUpdateStatus{
		NodeId:           nodeID,
		ClusterId:        "media-eu",
		TargetRelease:    "stable:v0.3.11",
		Phase:            phase,
		LastError:        lastError,
		Connected:        true,
		AutomaticUpdates: true,
		OperationalMode:  "normal",
	}
	for component, version := range versions {
		status.ComponentVersions = append(status.ComponentVersions, &foghorncontrolpb.NodeComponentVersion{Component: component, Version: version})
	}
	return status
}

func v0311Expectation() edgeRolloutExpectation {
	return edgeRolloutExpectation{
		ClusterID:     "media-eu",
		TargetRelease: "stable:v0.3.11",
		Components:    map[string]string{"helmsman": "v0.3.11", "mist": "v3.10.1"},
	}
}

// Production v0.3.11: `release apply` printed OK after syncing the edge
// target while the edges had only moved Helmsman. A node whose Helmsman is at
// the target but whose Mist is behind has not converged, and nothing may
// report the rollout as OK.
func TestEdgeRolloutHelmsmanAtTargetMistBehindIsNotConverged(t *testing.T) {
	src := &fakeEdgeRolloutSource{
		exp:      v0311Expectation(),
		enrolled: []string{"edge-1"},
		statuses: []*foghorncontrolpb.NodeUpdateStatus{
			edgeStatus("edge-1", "idle", "", map[string]string{"helmsman": "v0.3.11", "mist": "v3.10.0"}),
		},
	}
	var out bytes.Buffer
	outcome := waitEdgeRollout(context.Background(), &out, src, []string{"media-eu"}, 0, time.Millisecond)
	if err := reportEdgeRollout(&out, outcome, 0); err != nil {
		t.Fatalf("an unconverged node without a failure must not fail the command: %v", err)
	}
	text := out.String()
	if outcome.Converged() {
		t.Fatalf("outcome reported converged:\n%s", text)
	}
	if !strings.Contains(text, "edge-1  not converged") || !strings.Contains(text, "mist v3.10.0->v3.10.1") {
		t.Fatalf("node line does not show the behind Mist:\n%s", text)
	}
	if strings.Contains(text, "[OK]") || strings.Contains(text, "All edges run the target release") {
		t.Fatalf("unconverged edges were reported OK:\n%s", text)
	}
	if !strings.Contains(text, "EDGES NOT CONVERGED") || !strings.Contains(text, edgeRolloutStatusCommand) {
		t.Fatalf("missing the loud warning with the follow-up command:\n%s", text)
	}
}

func TestEdgeRolloutFailedNodeFailsWithLastError(t *testing.T) {
	src := &fakeEdgeRolloutSource{
		exp:      v0311Expectation(),
		enrolled: []string{"edge-1", "edge-2", "edge-3"},
		statuses: []*foghorncontrolpb.NodeUpdateStatus{
			edgeStatus("edge-1", "failed", "helmsman did not reconnect after self-update", map[string]string{"helmsman": "v0.3.10", "mist": "v3.10.0"}),
			edgeStatus("edge-2", "idle", "", map[string]string{"helmsman": "v0.3.11", "mist": "v3.10.1"}),
			edgeStatus("edge-3", "idle", "", map[string]string{"helmsman": "v0.3.10", "mist": "v3.10.0"}),
		},
	}
	var out bytes.Buffer
	outcome := waitEdgeRollout(context.Background(), &out, src, []string{"media-eu"}, time.Hour, time.Millisecond)
	if src.reads != 1 {
		t.Fatalf("a failed node must end the wait even while other nodes are pending; reads=%d", src.reads)
	}
	if err := reportEdgeRollout(&out, outcome, time.Hour); err == nil {
		t.Fatalf("a failed node must fail the command:\n%s", out.String())
	}
	text := out.String()
	if !strings.Contains(text, `edge-1  failed phase=failed`) || !strings.Contains(text, `last_error="helmsman did not reconnect after self-update"`) {
		t.Fatalf("failed node line missing reason:\n%s", text)
	}
	if !strings.Contains(text, "edge-2  converged") {
		t.Fatalf("converged node line missing:\n%s", text)
	}
}

// Foghorn evicts a node that stays disconnected, so an enrolled edge absent
// from its report has not converged.
func TestEdgeRolloutEnrolledEdgeMissingFromFoghornIsNotConverged(t *testing.T) {
	src := &fakeEdgeRolloutSource{
		exp:      v0311Expectation(),
		enrolled: []string{"edge-1", "edge-2"},
		statuses: []*foghorncontrolpb.NodeUpdateStatus{
			edgeStatus("edge-1", "idle", "", map[string]string{"helmsman": "v0.3.11", "mist": "v3.10.1"}),
		},
	}
	var out bytes.Buffer
	outcome := waitEdgeRollout(context.Background(), &out, src, []string{"media-eu"}, 0, time.Millisecond)
	if outcome.Converged() {
		t.Fatal("missing enrolled edge counted as converged")
	}
	if err := reportEdgeRollout(&out, outcome, 0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "edge-2  not connected to Foghorn") {
		t.Fatalf("missing node line:\n%s", out.String())
	}
}

func TestEdgeRolloutAllConvergedReportsOK(t *testing.T) {
	src := &fakeEdgeRolloutSource{
		exp:      v0311Expectation(),
		enrolled: []string{"edge-1"},
		statuses: []*foghorncontrolpb.NodeUpdateStatus{
			edgeStatus("edge-1", "idle", "", map[string]string{"helmsman": "v0.3.11", "mist": "v3.10.1"}),
		},
	}
	var out bytes.Buffer
	outcome := waitEdgeRollout(context.Background(), &out, src, []string{"media-eu"}, time.Hour, time.Millisecond)
	if err := reportEdgeRollout(&out, outcome, time.Hour); err != nil {
		t.Fatal(err)
	}
	if !outcome.Converged() || !strings.Contains(out.String(), "All edges run the target release") {
		t.Fatalf("converged fleet not reported:\n%s", out.String())
	}
}

func TestEdgeReleaseExpectationReadsTargetRelease(t *testing.T) {
	qm := &fakeEdgeReleaseQM{
		getResp: &quartermasterpb.ClusterReleaseTargetResponse{Target: &quartermasterpb.ClusterReleaseTarget{ClusterId: "media-eu", Channel: "stable", TargetVersion: "v0.3.11"}},
		listResp: &quartermasterpb.ListEdgeReleasesResponse{Releases: []*quartermasterpb.EdgeRelease{{
			Channel: "stable", Version: "v0.3.11",
			ComponentsJson: `{"helmsman":{"version":"v0.3.11"},"mist":{"version":"v3.10.1"},"config_schema":{"version":"3"}}`,
		}}},
	}
	exp, err := edgeReleaseExpectation(context.Background(), qm, "media-eu")
	if err != nil {
		t.Fatal(err)
	}
	if exp.TargetRelease != "stable:v0.3.11" || len(exp.Components) != 2 || exp.Components["mist"] != "v3.10.1" {
		t.Fatalf("expectation = %+v", exp)
	}
}
