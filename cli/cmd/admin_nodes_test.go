package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

type fakeAdminNodesClient struct {
	listResp *quartermasterpb.ListNodesResponse
	listErr  error

	createReq  *quartermasterpb.CreateNodeRequest
	createResp *quartermasterpb.NodeResponse
	createErr  error

	hardwareReq   *quartermasterpb.UpdateNodeHardwareRequest
	hardwareCalls int
	hardwareErr   error

	statusResp *quartermasterpb.GetServicePoolStatusResponse
	statusErr  error

	addResp *quartermasterpb.AddToServicePoolResponse
	addErr  error

	drainResp *quartermasterpb.DrainServiceInstanceResponse
	drainErr  error

	assignReq   *quartermasterpb.AssignServiceToClusterRequest
	assignCalls int
	assignErr   error

	unassignReq   *quartermasterpb.UnassignServiceFromClusterRequest
	unassignCalls int
	unassignErr   error
}

func (f *fakeAdminNodesClient) ListNodes(_ context.Context, _, _, _ string, _ *commonpb.CursorPaginationRequest) (*quartermasterpb.ListNodesResponse, error) {
	return f.listResp, f.listErr
}

func (f *fakeAdminNodesClient) CreateNode(_ context.Context, req *quartermasterpb.CreateNodeRequest) (*quartermasterpb.NodeResponse, error) {
	f.createReq = req
	if f.createErr != nil {
		return nil, f.createErr
	}
	return f.createResp, nil
}

func (f *fakeAdminNodesClient) UpdateNodeHardware(_ context.Context, req *quartermasterpb.UpdateNodeHardwareRequest) error {
	f.hardwareCalls++
	f.hardwareReq = req
	return f.hardwareErr
}

func (f *fakeAdminNodesClient) GetServicePoolStatus(_ context.Context, _ string) (*quartermasterpb.GetServicePoolStatusResponse, error) {
	return f.statusResp, f.statusErr
}

func (f *fakeAdminNodesClient) AddToServicePool(_ context.Context, _ *quartermasterpb.AddToServicePoolRequest) (*quartermasterpb.AddToServicePoolResponse, error) {
	return f.addResp, f.addErr
}

func (f *fakeAdminNodesClient) DrainServiceInstance(_ context.Context, _ *quartermasterpb.DrainServiceInstanceRequest) (*quartermasterpb.DrainServiceInstanceResponse, error) {
	return f.drainResp, f.drainErr
}

func (f *fakeAdminNodesClient) AssignServiceToCluster(_ context.Context, req *quartermasterpb.AssignServiceToClusterRequest) error {
	f.assignCalls++
	f.assignReq = req
	return f.assignErr
}

func (f *fakeAdminNodesClient) UnassignServiceFromCluster(_ context.Context, req *quartermasterpb.UnassignServiceFromClusterRequest) error {
	f.unassignCalls++
	f.unassignReq = req
	return f.unassignErr
}

func TestRunNodesList(t *testing.T) {
	fake := &fakeAdminNodesClient{listResp: &quartermasterpb.ListNodesResponse{
		Nodes: []*quartermasterpb.InfrastructureNode{
			{NodeId: "n-1", NodeName: "edge-1", NodeType: "edge", ClusterId: "c-1"},
		},
	}}
	var buf bytes.Buffer
	if err := runNodesList(context.Background(), &buf, fake, "", "", "", "", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{"Nodes (1)", "edge-1", "n-1", "type=edge", "cluster=c-1"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q:\n%s", want, buf.String())
		}
	}

	var jbuf bytes.Buffer
	if err := runNodesList(context.Background(), &jbuf, fake, "", "", "", "", true); err != nil {
		t.Fatalf("json: %v", err)
	}
	if !json.Valid(jbuf.Bytes()) {
		t.Errorf("not valid JSON: %s", jbuf.String())
	}

	errFake := &fakeAdminNodesClient{listErr: errors.New("rpc down")}
	if err := runNodesList(context.Background(), &bytes.Buffer{}, errFake, "", "", "", "", false); err == nil {
		t.Fatal("expected error to propagate")
	}
}

func TestRunNodeCreate(t *testing.T) {
	fake := &fakeAdminNodesClient{createResp: &quartermasterpb.NodeResponse{
		Node: &quartermasterpb.InfrastructureNode{NodeId: "n-1", NodeName: "edge-1"},
	}}
	req := &quartermasterpb.CreateNodeRequest{NodeId: "n-1"}
	var buf bytes.Buffer
	if err := runNodeCreate(context.Background(), &buf, fake, "", req, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.createReq != req {
		t.Error("request not forwarded unchanged")
	}
	if !strings.Contains(buf.String(), "Created node edge-1 (id=n-1)") {
		t.Errorf("missing success line: %q", buf.String())
	}

	var jbuf bytes.Buffer
	if err := runNodeCreate(context.Background(), &jbuf, fake, "", req, true); err != nil {
		t.Fatalf("json: %v", err)
	}
	if !json.Valid(jbuf.Bytes()) {
		t.Errorf("not valid JSON: %s", jbuf.String())
	}

	errFake := &fakeAdminNodesClient{createErr: errors.New("boom")}
	if err := runNodeCreate(context.Background(), &bytes.Buffer{}, errFake, "", req, false); err == nil {
		t.Fatal("expected create error to propagate")
	}
}

func TestRunNodeHardware(t *testing.T) {
	fake := &fakeAdminNodesClient{}
	req := &quartermasterpb.UpdateNodeHardwareRequest{NodeId: "n-9"}
	var buf bytes.Buffer
	if err := runNodeHardware(context.Background(), &buf, fake, "", req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.hardwareCalls != 1 || fake.hardwareReq != req {
		t.Errorf("hardware calls=%d forwarded=%v", fake.hardwareCalls, fake.hardwareReq == req)
	}
	if !strings.Contains(buf.String(), "Updated node hardware for n-9") {
		t.Errorf("missing success line: %q", buf.String())
	}

	errFake := &fakeAdminNodesClient{hardwareErr: errors.New("boom")}
	if err := runNodeHardware(context.Background(), &bytes.Buffer{}, errFake, "", req); err == nil {
		t.Fatal("expected hardware error to propagate")
	}
}

func TestRunServicePoolStatus(t *testing.T) {
	fake := &fakeAdminNodesClient{statusResp: &quartermasterpb.GetServicePoolStatusResponse{
		Total: 3, Assigned: 2, Unassigned: 1,
		Clusters: []*quartermasterpb.ServicePoolClusterEntry{
			{ClusterId: "", InstanceCount: 1, Instances: []*quartermasterpb.ServiceInstance{
				{Id: "i-1", Host: bootstrapStrPtr("h-1"), Port: int32Ptr(18008), Status: "healthy"},
			}},
			{ClusterId: "c-1", InstanceCount: 2},
		},
		Assignments: []*quartermasterpb.ServiceInstanceAssignment{
			{InstanceId: "i-2", ClusterId: "c-1", IsActive: true},
			{InstanceId: "i-3", ClusterId: "c-1", IsActive: false},
		},
	}}
	var buf bytes.Buffer
	if err := runServicePoolStatus(context.Background(), &buf, fake, "", "foghorn", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"foghorn service pool: 3 total, 2 assigned, 1 unassigned",
		"(pool) (1 instances)", // empty cluster id renders as "(pool)"
		"h-1:18008",
		"id=i-1",
		"Assignments (many-to-many)",
		"instance=i-2 → cluster=c-1  active",
		"instance=i-3 → cluster=c-1  inactive",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}

	var jbuf bytes.Buffer
	if err := runServicePoolStatus(context.Background(), &jbuf, fake, "", "foghorn", true); err != nil {
		t.Fatalf("json: %v", err)
	}
	if !json.Valid(jbuf.Bytes()) {
		t.Errorf("not valid JSON: %s", jbuf.String())
	}

	errFake := &fakeAdminNodesClient{statusErr: errors.New("rpc down")}
	if err := runServicePoolStatus(context.Background(), &bytes.Buffer{}, errFake, "", "foghorn", false); err == nil {
		t.Fatal("expected status error to propagate")
	}
}

func TestRunServicePoolAdd(t *testing.T) {
	fake := &fakeAdminNodesClient{addResp: &quartermasterpb.AddToServicePoolResponse{Released: 2}}
	var buf bytes.Buffer
	if err := runServicePoolAdd(context.Background(), &buf, fake, "", &quartermasterpb.AddToServicePoolRequest{ServiceType: "foghorn"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "Released 2 foghorn instance(s) to pool") {
		t.Errorf("missing success line: %q", buf.String())
	}

	errFake := &fakeAdminNodesClient{addErr: errors.New("boom")}
	if err := runServicePoolAdd(context.Background(), &bytes.Buffer{}, errFake, "", &quartermasterpb.AddToServicePoolRequest{}); err == nil {
		t.Fatal("expected add error to propagate")
	}
}

func TestRunServicePoolDrain_AlreadyInPoolVsDrained(t *testing.T) {
	// previous cluster empty/"pool" → "already in pool"; otherwise → drained.
	for _, prev := range []string{"", "pool"} {
		fake := &fakeAdminNodesClient{drainResp: &quartermasterpb.DrainServiceInstanceResponse{PreviousClusterId: prev}}
		var buf bytes.Buffer
		if err := runServicePoolDrain(context.Background(), &buf, fake, "", &quartermasterpb.DrainServiceInstanceRequest{InstanceId: "i-1"}); err != nil {
			t.Fatalf("prev=%q: %v", prev, err)
		}
		if !strings.Contains(buf.String(), "was already in pool") {
			t.Errorf("prev=%q: expected already-in-pool message: %q", prev, buf.String())
		}
	}

	fake := &fakeAdminNodesClient{drainResp: &quartermasterpb.DrainServiceInstanceResponse{PreviousClusterId: "c-1"}}
	var buf bytes.Buffer
	if err := runServicePoolDrain(context.Background(), &buf, fake, "", &quartermasterpb.DrainServiceInstanceRequest{InstanceId: "i-1"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "Drained instance i-1 from cluster c-1 → pool") {
		t.Errorf("expected drained message: %q", buf.String())
	}

	errFake := &fakeAdminNodesClient{drainErr: errors.New("boom")}
	if err := runServicePoolDrain(context.Background(), &bytes.Buffer{}, errFake, "", &quartermasterpb.DrainServiceInstanceRequest{}); err == nil {
		t.Fatal("expected drain error to propagate")
	}
}

func TestRunServicePoolAssign_ExplicitVsLeastLoaded(t *testing.T) {
	// Explicit instance IDs → exact-count message.
	fake := &fakeAdminNodesClient{}
	var buf bytes.Buffer
	if err := runServicePoolAssign(context.Background(), &buf, fake, "", &quartermasterpb.AssignServiceToClusterRequest{
		ClusterId: "c-1", ServiceType: "foghorn", InstanceIds: []string{"i-1", "i-2"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "Assigned 2 foghorn instance(s) to cluster c-1") || strings.Contains(buf.String(), "least-loaded") {
		t.Errorf("explicit-ids message wrong: %q", buf.String())
	}

	// Count-only → least-loaded message.
	fake2 := &fakeAdminNodesClient{}
	var buf2 bytes.Buffer
	if err := runServicePoolAssign(context.Background(), &buf2, fake2, "", &quartermasterpb.AssignServiceToClusterRequest{
		ClusterId: "c-1", ServiceType: "foghorn", Count: 3,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf2.String(), "Assigned 3 foghorn instance(s) to cluster c-1 (least-loaded)") {
		t.Errorf("least-loaded message wrong: %q", buf2.String())
	}

	errFake := &fakeAdminNodesClient{assignErr: errors.New("boom")}
	if err := runServicePoolAssign(context.Background(), &bytes.Buffer{}, errFake, "", &quartermasterpb.AssignServiceToClusterRequest{}); err == nil {
		t.Fatal("expected assign error to propagate")
	}
}

func TestRunServicePoolUnassign(t *testing.T) {
	fake := &fakeAdminNodesClient{}
	req := &quartermasterpb.UnassignServiceFromClusterRequest{ClusterId: "c-1", ServiceType: "foghorn", InstanceIds: []string{"i-1"}}
	var buf bytes.Buffer
	if err := runServicePoolUnassign(context.Background(), &buf, fake, "", req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.unassignCalls != 1 || fake.unassignReq != req {
		t.Errorf("unassign calls=%d forwarded=%v", fake.unassignCalls, fake.unassignReq == req)
	}
	if !strings.Contains(buf.String(), "Unassigned 1 foghorn instance(s) from cluster c-1") {
		t.Errorf("missing success line: %q", buf.String())
	}

	errFake := &fakeAdminNodesClient{unassignErr: errors.New("boom")}
	if err := runServicePoolUnassign(context.Background(), &bytes.Buffer{}, errFake, "", req); err == nil {
		t.Fatal("expected unassign error to propagate")
	}
}
