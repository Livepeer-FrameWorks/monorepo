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

func strptr(s string) *string { return &s }
func i32ptr(v int32) *int32   { return &v }

// fakeServicesQM is a hand-written stand-in for the Quartermaster surface used
// by `services health` / `services discover`.
type fakeServicesQM struct {
	getResp  *quartermasterpb.ListServicesHealthResponse
	getErr   error
	listResp *quartermasterpb.ListServicesHealthResponse
	listErr  error
	discResp *quartermasterpb.ServiceDiscoveryResponse
	discErr  error

	gotDiscType string
}

func (f *fakeServicesQM) GetServiceHealth(_ context.Context, _ string) (*quartermasterpb.ListServicesHealthResponse, error) {
	return f.getResp, f.getErr
}

func (f *fakeServicesQM) ListServicesHealth(_ context.Context, _ *commonpb.CursorPaginationRequest) (*quartermasterpb.ListServicesHealthResponse, error) {
	return f.listResp, f.listErr
}

func (f *fakeServicesQM) DiscoverServices(_ context.Context, svcType, _ string, _ *commonpb.CursorPaginationRequest) (*quartermasterpb.ServiceDiscoveryResponse, error) {
	f.gotDiscType = svcType
	return f.discResp, f.discErr
}

func TestRunServicesDiscoverText(t *testing.T) {
	var buf bytes.Buffer
	qc := &fakeServicesQM{discResp: &quartermasterpb.ServiceDiscoveryResponse{
		Instances: []*quartermasterpb.ServiceInstance{
			{InstanceId: "b2", ServiceId: "helmsman", ClusterId: "c1", Version: strptr("v1"), Port: i32ptr(18001), HealthStatus: "ok"},
			{InstanceId: "a1", ServiceId: "helmsman", ClusterId: "c1", HealthStatus: "degraded"},
		},
	}}
	if err := runServicesDiscover(context.Background(), &buf, qc, "helmsman", "", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Discovered 2 instance(s) of helmsman") {
		t.Errorf("missing summary line:\n%s", out)
	}
	// Sorted by instance id: a1 before b2.
	if strings.Index(out, "inst=a1") > strings.Index(out, "inst=b2") {
		t.Errorf("instances not sorted by id:\n%s", out)
	}
	if qc.gotDiscType != "helmsman" {
		t.Errorf("DiscoverServices called with type %q", qc.gotDiscType)
	}
}

func TestRunServicesDiscoverJSON(t *testing.T) {
	var buf bytes.Buffer
	qc := &fakeServicesQM{discResp: &quartermasterpb.ServiceDiscoveryResponse{
		Instances: []*quartermasterpb.ServiceInstance{{InstanceId: "a1", ServiceId: "helmsman"}},
	}}
	if err := runServicesDiscover(context.Background(), &buf, qc, "helmsman", "", true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !json.Valid(buf.Bytes()) {
		t.Fatalf("JSON output is not valid:\n%s", buf.String())
	}
}

func TestRunServicesDiscoverError(t *testing.T) {
	qc := &fakeServicesQM{discErr: errors.New("rpc down")}
	if err := runServicesDiscover(context.Background(), &bytes.Buffer{}, qc, "helmsman", "", false); err == nil {
		t.Fatal("expected error to propagate")
	}
}

func healthResp(insts ...*quartermasterpb.ServiceInstanceHealth) *quartermasterpb.ListServicesHealthResponse {
	return &quartermasterpb.ListServicesHealthResponse{Instances: insts}
}

// With no filter, runServicesHealth lists all instances and renders a heading
// plus one status-classified line per instance.
func TestRunServicesHealthAll(t *testing.T) {
	var buf bytes.Buffer
	qc := &fakeServicesQM{listResp: healthResp(
		&quartermasterpb.ServiceInstanceHealth{ServiceId: "commodore", InstanceId: "i1", Status: "ok", Host: strptr("10.0.0.1"), Port: 19001, HealthEndpoint: strptr("/health")},
		&quartermasterpb.ServiceInstanceHealth{ServiceId: "foghorn", InstanceId: "i2", Status: "down"},
	)}
	if err := runServicesHealth(context.Background(), &buf, qc, "", "", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"Service Health (2 instances)", "commodore", "foghorn", "10.0.0.1:19001"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q\n---\n%s", want, out)
		}
	}
}

// A service-id filter routes through GetServiceHealth.
func TestRunServicesHealthByID(t *testing.T) {
	var buf bytes.Buffer
	qc := &fakeServicesQM{getResp: healthResp(
		&quartermasterpb.ServiceInstanceHealth{ServiceId: "commodore", InstanceId: "i1", Status: "healthy"},
	)}
	if err := runServicesHealth(context.Background(), &buf, qc, "commodore", "", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "commodore") {
		t.Errorf("expected commodore in output:\n%s", buf.String())
	}
}

// A type filter discovers service IDs, then fetches health per ID.
func TestRunServicesHealthByType(t *testing.T) {
	var buf bytes.Buffer
	qc := &fakeServicesQM{
		discResp: &quartermasterpb.ServiceDiscoveryResponse{
			Instances: []*quartermasterpb.ServiceInstance{{ServiceId: "commodore", InstanceId: "i1"}},
		},
		getResp: healthResp(&quartermasterpb.ServiceInstanceHealth{ServiceId: "commodore", InstanceId: "i1", Status: "ok"}),
	}
	if err := runServicesHealth(context.Background(), &buf, qc, "", "commodore", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if qc.gotDiscType != "commodore" {
		t.Errorf("expected discovery by type commodore, got %q", qc.gotDiscType)
	}
	if !strings.Contains(buf.String(), "commodore") {
		t.Errorf("expected health output for commodore:\n%s", buf.String())
	}
}

func TestRunServicesHealthJSON(t *testing.T) {
	var buf bytes.Buffer
	qc := &fakeServicesQM{listResp: healthResp(
		&quartermasterpb.ServiceInstanceHealth{ServiceId: "commodore", InstanceId: "i1", Status: "ok"},
	)}
	if err := runServicesHealth(context.Background(), &buf, qc, "", "", true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !json.Valid(buf.Bytes()) {
		t.Fatalf("JSON output is not valid:\n%s", buf.String())
	}
}

func TestRunServicesHealthError(t *testing.T) {
	qc := &fakeServicesQM{listErr: errors.New("unavailable")}
	if err := runServicesHealth(context.Background(), &bytes.Buffer{}, qc, "", "", false); err == nil {
		t.Fatal("expected error to propagate")
	}
}
