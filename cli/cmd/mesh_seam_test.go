package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// fakeMeshStatusQM is a hand-written stand-in for the Quartermaster surface
// that `mesh status` renders from.
type fakeMeshStatusQM struct {
	listResp *quartermasterpb.ListNodesResponse
	listErr  error

	gotClusterID string
	gotNodeType  string
	gotRegion    string
	calls        int
}

func (f *fakeMeshStatusQM) ListNodes(_ context.Context, clusterID, nodeType, region string, _ *commonpb.CursorPaginationRequest) (*quartermasterpb.ListNodesResponse, error) {
	f.calls++
	f.gotClusterID = clusterID
	f.gotNodeType = nodeType
	f.gotRegion = region
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.listResp, nil
}

func meshHealthyNode() *quartermasterpb.InfrastructureNode {
	return &quartermasterpb.InfrastructureNode{
		Id:            "node-a",
		NodeName:      "edge-1",
		ClusterId:     "prod",
		NodeType:      "edge",
		InternalIp:    strptr("10.0.0.5"),
		WireguardIp:   strptr("100.64.0.5"),
		LastHeartbeat: timestamppb.New(time.Now()),
	}
}

func meshStaleNode() *quartermasterpb.InfrastructureNode {
	return &quartermasterpb.InfrastructureNode{
		Id:        "node-b",
		NodeName:  "edge-2",
		ClusterId: "prod",
		NodeType:  "ingest",
		// no IPs, no heartbeat -> dashes + Offline
	}
}

func TestRunMeshStatusText(t *testing.T) {
	qm := &fakeMeshStatusQM{listResp: &quartermasterpb.ListNodesResponse{
		Nodes: []*quartermasterpb.InfrastructureNode{meshStaleNode(), meshHealthyNode()},
	}}
	var buf bytes.Buffer
	if err := runMeshStatus(context.Background(), &buf, qm, nil, false); err != nil {
		t.Fatalf("runMeshStatus: %v", err)
	}
	out := buf.String()

	// Topology was fetched with no filters.
	if qm.calls != 1 || qm.gotClusterID != "" || qm.gotNodeType != "" || qm.gotRegion != "" {
		t.Fatalf("unexpected ListNodes args: calls=%d cluster=%q type=%q region=%q", qm.calls, qm.gotClusterID, qm.gotNodeType, qm.gotRegion)
	}
	// QM-only header (no manifest cross-reference) must NOT carry audit columns.
	if !strings.Contains(out, "NODE ID\tROLE") && !strings.Contains(out, "NODE ID") {
		t.Fatalf("missing base header in:\n%s", out)
	}
	if strings.Contains(out, "KEY-MATCH") || strings.Contains(out, "ORIGIN") {
		t.Fatalf("did not expect audit columns without manifest:\n%s", out)
	}
	// Node count surfaced and both nodes rendered.
	if !strings.Contains(out, "(2 nodes)") {
		t.Fatalf("missing node count in:\n%s", out)
	}
	for _, want := range []string{"node-a", "10.0.0.5", "100.64.0.5", "Healthy", "node-b", "Offline"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q in:\n%s", want, out)
		}
	}
	// Stale node (no IPs) renders dashes.
	if !strings.Contains(out, "-") {
		t.Fatalf("expected dash placeholders for missing IPs:\n%s", out)
	}
	// Sorted by Id: node-a before node-b.
	if strings.Index(out, "node-a") > strings.Index(out, "node-b") {
		t.Fatalf("nodes not sorted by id:\n%s", out)
	}
}

func TestRunMeshStatusWithAuditColumns(t *testing.T) {
	qm := &fakeMeshStatusQM{listResp: &quartermasterpb.ListNodesResponse{
		Nodes: []*quartermasterpb.InfrastructureNode{meshHealthyNode()},
	}}
	audit := map[statusKey]auditRow{
		{nodeName: "edge-1", clusterID: "prod"}: {origin: "gitops", severity: auditOK, revision: "rev-7"},
	}
	var buf bytes.Buffer
	if err := runMeshStatus(context.Background(), &buf, qm, audit, false); err != nil {
		t.Fatalf("runMeshStatus: %v", err)
	}
	out := buf.String()

	// Audit cross-reference adds the extended header + matched row values.
	for _, want := range []string{"ORIGIN", "KEY-MATCH", "REVISION", "gitops", statusKeyMatch(auditOK)} {
		if !strings.Contains(out, want) {
			t.Fatalf("audit output missing %q in:\n%s", want, out)
		}
	}
}

func TestRunMeshStatusAuditMiss(t *testing.T) {
	// Node present in QM but absent from the manifest index -> no-manifest-row.
	qm := &fakeMeshStatusQM{listResp: &quartermasterpb.ListNodesResponse{
		Nodes: []*quartermasterpb.InfrastructureNode{meshHealthyNode()},
	}}
	audit := map[statusKey]auditRow{
		{nodeName: "someone-else", clusterID: "prod"}: {origin: "gitops", severity: auditOK},
	}
	var buf bytes.Buffer
	if err := runMeshStatus(context.Background(), &buf, qm, audit, false); err != nil {
		t.Fatalf("runMeshStatus: %v", err)
	}
	if !strings.Contains(buf.String(), "no-manifest-row") {
		t.Fatalf("expected no-manifest-row marker in:\n%s", buf.String())
	}
}

func TestRunMeshStatusJSON(t *testing.T) {
	qm := &fakeMeshStatusQM{listResp: &quartermasterpb.ListNodesResponse{
		Nodes: []*quartermasterpb.InfrastructureNode{meshHealthyNode()},
	}}
	var buf bytes.Buffer
	if err := runMeshStatus(context.Background(), &buf, qm, nil, true); err != nil {
		t.Fatalf("runMeshStatus json: %v", err)
	}
	// JSON mode must not emit the human progress chrome.
	if strings.Contains(buf.String(), "Fetching topology") {
		t.Fatalf("json mode leaked progress text:\n%s", buf.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rows); err != nil {
		t.Fatalf("decode json: %v\n%s", err, buf.String())
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	if rows[0]["id"] != "node-a" || rows[0]["cluster_id"] != "prod" {
		t.Fatalf("unexpected json row: %+v", rows[0])
	}
	if rows[0]["agent_status"] != "Healthy" {
		t.Fatalf("want Healthy agent_status, got %v", rows[0]["agent_status"])
	}
}

func TestRunMeshStatusEmpty(t *testing.T) {
	qm := &fakeMeshStatusQM{listResp: &quartermasterpb.ListNodesResponse{}}
	var buf bytes.Buffer
	if err := runMeshStatus(context.Background(), &buf, qm, nil, false); err != nil {
		t.Fatalf("runMeshStatus empty: %v", err)
	}
	if !strings.Contains(buf.String(), "(0 nodes)") {
		t.Fatalf("expected zero-node count in:\n%s", buf.String())
	}
}

func TestRunMeshStatusError(t *testing.T) {
	qm := &fakeMeshStatusQM{listErr: errors.New("rpc down")}
	var buf bytes.Buffer
	err := runMeshStatus(context.Background(), &buf, qm, nil, false)
	if err == nil {
		t.Fatal("expected error from failed ListNodes")
	}
	if !strings.Contains(err.Error(), "failed to get nodes") {
		t.Fatalf("error not wrapped: %v", err)
	}
	// Failure marker emitted in text mode.
	if !strings.Contains(buf.String(), "❌") {
		t.Fatalf("expected failure marker in:\n%s", buf.String())
	}
}

func TestRunMeshStatusErrorJSONQuiet(t *testing.T) {
	qm := &fakeMeshStatusQM{listErr: errors.New("rpc down")}
	var buf bytes.Buffer
	if err := runMeshStatus(context.Background(), &buf, qm, nil, true); err == nil {
		t.Fatal("expected error")
	}
	// JSON mode stays quiet on the wire even on failure.
	if strings.Contains(buf.String(), "❌") || strings.Contains(buf.String(), "Fetching topology") {
		t.Fatalf("json mode leaked chrome on error:\n%s", buf.String())
	}
}
