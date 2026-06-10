package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	fwcfg "frameworks/cli/internal/config"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

type fakeClusterNodesListQM struct {
	resp         *quartermasterpb.ListNodesResponse
	err          error
	calls        int
	gotClusterID string
	gotNodeType  string
	gotRegion    string
}

func (f *fakeClusterNodesListQM) ListNodes(_ context.Context, clusterID, nodeType, region string, _ *commonpb.CursorPaginationRequest) (*quartermasterpb.ListNodesResponse, error) {
	f.calls++
	f.gotClusterID = clusterID
	f.gotNodeType = nodeType
	f.gotRegion = region
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func clusterNodesListSeamNodes() []*quartermasterpb.InfrastructureNode {
	return []*quartermasterpb.InfrastructureNode{
		{NodeId: "n-1", NodeName: "edge-eu", NodeType: "edge", ClusterId: "c-1"},
		{NodeId: "n-2", NodeName: "edge-us", NodeType: "edge", ClusterId: "c-1"},
	}
}

func TestRunClusterNodesList_TextNoHealth(t *testing.T) {
	qm := &fakeClusterNodesListQM{resp: &quartermasterpb.ListNodesResponse{Nodes: clusterNodesListSeamNodes()}}
	var buf bytes.Buffer
	// loadHealth nil → no health dial, health columns render as "-".
	if err := runClusterNodesList(context.Background(), &buf, qm, fwcfg.Context{}, "c-1", "edge", "us-east", false, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Request forwarding: filters are passed through verbatim.
	if qm.calls != 1 || qm.gotClusterID != "c-1" || qm.gotNodeType != "edge" || qm.gotRegion != "us-east" {
		t.Fatalf("ListNodes args wrong: calls=%d cluster=%q type=%q region=%q", qm.calls, qm.gotClusterID, qm.gotNodeType, qm.gotRegion)
	}
	out := buf.String()
	if !strings.Contains(out, "Cluster nodes (2)") {
		t.Errorf("expected heading with count, got %q", out)
	}
	if !strings.Contains(out, "node=n-1") || !strings.Contains(out, "node=n-2") {
		t.Errorf("expected both node ids rendered, got %q", out)
	}
	// No health → mode/streams/versions all dash.
	if !strings.Contains(out, "mode=- streams=- versions=-") {
		t.Errorf("expected dash health columns without loader, got %q", out)
	}
}

func TestRunClusterNodesList_TextWithHealth(t *testing.T) {
	qm := &fakeClusterNodesListQM{resp: &quartermasterpb.ListNodesResponse{Nodes: clusterNodesListSeamNodes()}}
	var gotNodes int
	loadHealth := func(nodes []*quartermasterpb.InfrastructureNode) map[string]*foghorncontrolpb.GetNodeHealthResponse {
		gotNodes = len(nodes)
		return map[string]*foghorncontrolpb.GetNodeHealthResponse{
			"n-1": {
				OperationalMode: "active",
				ActiveStreams:   3,
				ComponentVersions: []*foghorncontrolpb.NodeComponentVersion{
					{Component: "helmsman", Version: "v1.2.3"},
				},
			},
		}
	}
	var buf bytes.Buffer
	if err := runClusterNodesList(context.Background(), &buf, qm, fwcfg.Context{}, "c-1", "edge", "", false, loadHealth); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotNodes != 2 {
		t.Errorf("loader should see all returned nodes, got %d", gotNodes)
	}
	out := buf.String()
	// n-1 has health, n-2 does not — assert both branches render.
	if !strings.Contains(out, "mode=active streams=3 versions=helmsman=v1.2.3") {
		t.Errorf("expected n-1 health columns, got %q", out)
	}
	if !strings.Contains(out, "node=n-2 type=edge cluster=c-1 mode=- streams=- versions=-") {
		t.Errorf("expected n-2 to render dash health, got %q", out)
	}
}

func TestRunClusterNodesList_Empty(t *testing.T) {
	qm := &fakeClusterNodesListQM{resp: &quartermasterpb.ListNodesResponse{}}
	healthCalled := false
	loadHealth := func(nodes []*quartermasterpb.InfrastructureNode) map[string]*foghorncontrolpb.GetNodeHealthResponse {
		healthCalled = true
		return nil
	}
	var buf bytes.Buffer
	if err := runClusterNodesList(context.Background(), &buf, qm, fwcfg.Context{}, "c-1", "edge", "", false, loadHealth); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "Cluster nodes (0)") {
		t.Errorf("expected zero-count heading, got %q", buf.String())
	}
	// Loader still runs (with an empty slice) but yields no per-node output.
	if !healthCalled {
		t.Error("loader should be invoked even with zero nodes on the text path")
	}
}

func TestRunClusterNodesList_JSON(t *testing.T) {
	qm := &fakeClusterNodesListQM{resp: &quartermasterpb.ListNodesResponse{Nodes: clusterNodesListSeamNodes()}}
	healthCalled := false
	loadHealth := func(nodes []*quartermasterpb.InfrastructureNode) map[string]*foghorncontrolpb.GetNodeHealthResponse {
		healthCalled = true
		return nil
	}
	var buf bytes.Buffer
	// JSON short-circuits before health is loaded.
	if err := runClusterNodesList(context.Background(), &buf, qm, fwcfg.Context{}, "c-1", "edge", "", true, loadHealth); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if healthCalled {
		t.Error("JSON path must not invoke the health loader")
	}
	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v (%q)", err, buf.String())
	}
	if !strings.Contains(buf.String(), "n-1") {
		t.Errorf("expected node id in JSON, got %q", buf.String())
	}
}

func TestRunClusterNodesList_Error(t *testing.T) {
	qm := &fakeClusterNodesListQM{err: errors.New("rpc down")}
	var buf bytes.Buffer
	if err := runClusterNodesList(context.Background(), &buf, qm, fwcfg.Context{}, "c-1", "edge", "", false, nil); err == nil {
		t.Fatal("expected ListNodes error to propagate")
	}
	if buf.Len() != 0 {
		t.Errorf("no output expected on error, got %q", buf.String())
	}
}
