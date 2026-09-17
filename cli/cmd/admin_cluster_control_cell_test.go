package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type fakeControlCellClient struct {
	reassign      *quartermasterpb.ReassignClusterControlCellRequest
	statusCluster string
	resp          *quartermasterpb.ClusterControlCellReassignment
}

func (f *fakeControlCellClient) ReassignClusterControlCell(_ context.Context, req *quartermasterpb.ReassignClusterControlCellRequest) (*quartermasterpb.ClusterControlCellReassignment, error) {
	f.reassign = req
	return f.resp, nil
}

func (f *fakeControlCellClient) GetClusterControlCellReassignment(_ context.Context, clusterID string) (*quartermasterpb.ClusterControlCellReassignment, error) {
	f.statusCluster = clusterID
	return f.resp, nil
}

func TestRunClusterReassignControlCellSendsTargetAndTimeout(t *testing.T) {
	client := &fakeControlCellClient{resp: &quartermasterpb.ClusterControlCellReassignment{
		ClusterId: "private-eu", ControlCellId: "cell-us", PreviousControlCellId: "cell-eu", State: "switching",
		DeadlineAt: timestamppb.New(time.Date(2026, 9, 15, 12, 30, 0, 0, time.UTC)),
	}}
	var out bytes.Buffer
	if err := runClusterReassignControlCell(context.Background(), &out, client, "jwt", " private-eu ", " cell-us ", 45*time.Minute, false); err != nil {
		t.Fatal(err)
	}
	if client.reassign.GetClusterId() != "private-eu" || client.reassign.GetTargetControlCellId() != "cell-us" || client.reassign.GetTimeoutSeconds() != 2700 {
		t.Fatalf("request = %+v", client.reassign)
	}
	for _, want := range []string{"moving from control cell cell-eu to cell-us", "state:         switching", "deadline:      2026-09-15T12:30:00Z"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestRunClusterReassignControlCellRejectsShortTimeout(t *testing.T) {
	client := &fakeControlCellClient{}
	err := runClusterReassignControlCell(context.Background(), &bytes.Buffer{}, client, "jwt", "private-eu", "cell-us", 30*time.Second, false)
	if err == nil || !strings.Contains(err.Error(), "--timeout") {
		t.Fatalf("err = %v, want a --timeout error", err)
	}
	if client.reassign != nil {
		t.Fatal("short timeout reached Quartermaster")
	}
}

func TestRunClusterControlCellStatusPrintsFailureAndPendingEdges(t *testing.T) {
	client := &fakeControlCellClient{resp: &quartermasterpb.ClusterControlCellReassignment{
		ClusterId: "private-eu", ControlCellId: "cell-us", PreviousControlCellId: "cell-eu", State: "failed",
		Error: "1 edge node(s) still observed by another control cell at the deadline: edge-2", PendingNodeIds: []string{"edge-2"},
	}}
	var out bytes.Buffer
	if err := runClusterControlCellStatus(context.Background(), &out, client, "jwt", "private-eu", false); err != nil {
		t.Fatal(err)
	}
	if client.statusCluster != "private-eu" {
		t.Fatalf("status requested for %q", client.statusCluster)
	}
	for _, want := range []string{"state:         failed", "previous cell: cell-eu", "pending edges: edge-2", "at the deadline: edge-2"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestRunClusterControlCellStatusShowsIdleCluster(t *testing.T) {
	client := &fakeControlCellClient{resp: &quartermasterpb.ClusterControlCellReassignment{ClusterId: "private-eu", ControlCellId: "cell-eu"}}
	var out bytes.Buffer
	if err := runClusterControlCellStatus(context.Background(), &out, client, "jwt", "private-eu", false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "state:         idle") || strings.Contains(out.String(), "deadline") {
		t.Fatalf("idle output:\n%s", out.String())
	}
}
