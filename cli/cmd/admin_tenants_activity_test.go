package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/periscope"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"

	"google.golang.org/protobuf/types/known/timestamppb"
)

type fakeTenantsClient struct {
	resp *quartermasterpb.ListTenantsResponse
}

func (f *fakeTenantsClient) ListTenants(ctx context.Context, _ *commonpb.CursorPaginationRequest) (*quartermasterpb.ListTenantsResponse, error) {
	return f.resp, nil
}

type fakeActivityClient struct {
	resp      *periscopepb.ListTenantActivityResponse
	gotLimit  int32
	gotWindow time.Duration
}

func (f *fakeActivityClient) ListTenantActivity(ctx context.Context, timeRange *periscope.TimeRangeOpts, limit int32) (*periscopepb.ListTenantActivityResponse, error) {
	f.gotLimit = limit
	if timeRange != nil {
		f.gotWindow = timeRange.EndTime.Sub(timeRange.StartTime)
	}
	return f.resp, nil
}

func TestRunTenantsActivityJoinsNamesAndRendersTable(t *testing.T) {
	qm := &fakeTenantsClient{resp: &quartermasterpb.ListTenantsResponse{Tenants: []*quartermasterpb.Tenant{
		{Id: "tenant-1", Name: "Acme Streams", DeploymentTier: "pro"},
		{Id: "tenant-2", Name: "Idle Corp", DeploymentTier: "free"},
	}}}
	ps := &fakeActivityClient{resp: &periscopepb.ListTenantActivityResponse{Tenants: []*periscopepb.TenantActivity{
		{
			TenantId:       "tenant-1",
			LiveStreams:    2,
			CurrentViewers: 14,
			IngestHours:    40.5,
			ViewerHours:    120.25,
			EgressGb:       33.5,
			UniqueViewers:  77,
			ApiRequests:    900,
			LastStreamAt:   timestamppb.New(time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)),
		},
	}}}

	var out bytes.Buffer
	err := runTenantsActivity(context.Background(), &out, qm, ps, "jwt-token", 7*24*time.Hour, 25, false)
	if err != nil {
		t.Fatalf("runTenantsActivity: %v", err)
	}

	text := out.String()
	if !strings.Contains(text, "Acme Streams") {
		t.Fatalf("expected tenant name joined from quartermaster, got:\n%s", text)
	}
	if !strings.Contains(text, "pro") {
		t.Fatalf("expected tier column, got:\n%s", text)
	}
	if !strings.Contains(text, "2026-06-10") {
		t.Fatalf("expected last-stream date, got:\n%s", text)
	}
	// Idle Corp has no activity row; it is summarized, not listed.
	if !strings.Contains(text, "1 tenant(s) had no activity") {
		t.Fatalf("expected quiet-tenant summary, got:\n%s", text)
	}
	if ps.gotLimit != 25 {
		t.Fatalf("limit not forwarded: got %d", ps.gotLimit)
	}
	// Start/end are captured with separate time.Now() calls, so allow drift.
	if drift := ps.gotWindow - 7*24*time.Hour; drift < 0 || drift > time.Second {
		t.Fatalf("window not forwarded: got %s", ps.gotWindow)
	}
}

func TestRunTenantsActivityFallsBackToTenantID(t *testing.T) {
	qm := &fakeTenantsClient{resp: &quartermasterpb.ListTenantsResponse{}}
	ps := &fakeActivityClient{resp: &periscopepb.ListTenantActivityResponse{Tenants: []*periscopepb.TenantActivity{
		{TenantId: "00000000-0000-0000-0000-000000000042", IngestHours: 1},
	}}}

	var out bytes.Buffer
	if err := runTenantsActivity(context.Background(), &out, qm, ps, "", 24*time.Hour, 0, false); err != nil {
		t.Fatalf("runTenantsActivity: %v", err)
	}
	if !strings.Contains(out.String(), "00000000-0000-0000-0000-000000000042") {
		t.Fatalf("expected tenant_id fallback when quartermaster has no name, got:\n%s", out.String())
	}
}
