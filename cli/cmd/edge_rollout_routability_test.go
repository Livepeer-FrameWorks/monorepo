package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	fwcfg "frameworks/cli/internal/config"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/metadata"
)

type peerHealthClient map[string]*foghorncontrolpb.GetNodeHealthResponse

func (c peerHealthClient) GetNodeHealth(_ context.Context, req *foghorncontrolpb.GetNodeHealthRequest) (*foghorncontrolpb.GetNodeHealthResponse, metadata.MD, error) {
	if h, ok := c[req.GetNodeId()]; ok {
		return h, nil, nil
	}
	return nil, nil, errors.New("node not found")
}

// A peer counts only when it is another active node that Foghorn reports
// healthy in normal mode; the node itself never counts.
func TestCountRoutablePeers(t *testing.T) {
	nodes := []*quartermasterpb.InfrastructureNode{
		{NodeId: "self", ClusterId: "media-eu"},
		{NodeId: "draining", ClusterId: "media-eu"},
		{NodeId: "unhealthy", ClusterId: "media-eu"},
		{NodeId: "retired", ClusterId: "media-eu", Status: "decommissioned"},
		{NodeId: "silent", ClusterId: "media-eu"},
		{NodeId: "ok", ClusterId: "media-eu", Status: "active"},
	}
	health := peerHealthClient{
		"self":      {IsHealthy: true, OperationalMode: "normal"},
		"draining":  {IsHealthy: true, OperationalMode: "draining"},
		"unhealthy": {IsHealthy: false, OperationalMode: "normal"},
		"retired":   {IsHealthy: true, OperationalMode: "normal"},
		"ok":        {IsHealthy: true, OperationalMode: "normal"},
	}
	dial := func(context.Context, string) (nodeHealthClient, func(), error) { return health, func() {}, nil }
	if n, err := countRoutablePeers(context.Background(), fwcfg.Context{}, nodes, "self", dial); err != nil || n != 1 {
		t.Fatalf("countRoutablePeers = %d, %v; want 1 (only %q)", n, err, "ok")
	}
	if n, err := countRoutablePeers(context.Background(), fwcfg.Context{}, nodes[:1], "self", dial); err != nil || n != 0 {
		t.Fatalf("single-node cluster = %d, %v; want 0", n, err)
	}
	refused := func(context.Context, string) (nodeHealthClient, func(), error) {
		return nil, nil, errors.New("connection refused")
	}
	if _, err := countRoutablePeers(context.Background(), fwcfg.Context{}, nodes, "self", refused); err == nil {
		t.Fatal("unreadable peer health must leave the count unknown")
	}
}

func TestParseControlStreamConnected(t *testing.T) {
	for metrics, want := range map[string]bool{
		"# TYPE helmsman_control_stream_connected gauge\nhelmsman_control_stream_connected 1\n": true,
		"helmsman_control_stream_connected 0\nstream_viewers{stream=\"a\"} 2\n":                 false,
	} {
		got, err := parseControlStreamConnected(metrics)
		if err != nil || got != want {
			t.Fatalf("parseControlStreamConnected(%q) = %v, %v; want %v", metrics, got, err, want)
		}
	}
	if _, err := parseControlStreamConnected("stream_viewers{stream=\"a\"} 2\n"); err == nil {
		t.Fatal("a missing gauge must be an error, not a disconnected stream")
	}
}

// After an apply the CLI polls Helmsman's control stream gauge, printing
// every poll, and gives up after the bound with the reason.
func TestEdgeDrainerAwaitControlConnected(t *testing.T) {
	h := &fakeHelmsman{control: []int{0, 0, 1}, metricsErrs: 1}
	var out strings.Builder
	d := testDrainer(h, edgeDrainDeadline)
	d.w = &out
	if err := d.awaitControlConnected(context.Background()); err != nil {
		t.Fatalf("awaitControlConnected = %v, want connected on the fourth read", err)
	}
	if got := strings.Count(out.String(), "waiting for Helmsman control stream to Foghorn"); got != 3 {
		t.Fatalf("got %d progress lines, want 3 (one unreadable, two disconnected):\n%s", got, out.String())
	}

	h = &fakeHelmsman{control: []int{0}}
	out.Reset()
	d = testDrainer(h, edgeDrainDeadline)
	d.w = &out
	if d.controlWait > 2*time.Minute {
		t.Fatalf("control stream wait = %s, want a bound of at most 2m", d.controlWait)
	}
	err := d.awaitControlConnected(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not connected after "+d.controlWait.String()) {
		t.Fatalf("awaitControlConnected = %v, want the bounded failure", err)
	}
	if got, want := strings.Count(out.String(), "waiting for Helmsman control stream"), int(d.controlWait/edgeControlStreamPoll)+1; got != want {
		t.Fatalf("got %d progress lines, want %d", got, want)
	}
}

// The summary names services whose unit/env change waits for a restart.
func TestSummarizeEdgeRolloutListsPendingRestarts(t *testing.T) {
	var out strings.Builder
	err := summarizeEdgeRollout(&out, []edgeRolloutResult{
		{Name: "edge-1", Action: edgeActionApply, PendingRestarts: []string{"frameworks-mistserver"}},
		{Name: "edge-2", Action: edgeActionInPlaceApply},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "restart pending: frameworks-mistserver") || !strings.Contains(out.String(), "apply-in-place=1") {
		t.Fatalf("summary = %q", out.String())
	}
}
