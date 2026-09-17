package grpcserver

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_incidents/internal/database/lookoutdb"
	"frameworks/api_incidents/internal/incidents"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	lookoutpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/lookout"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	tenantA = "10000000-0000-0000-0000-00000000000a"
	tenantB = "10000000-0000-0000-0000-00000000000b"
	userA   = "20000000-0000-0000-0000-00000000000a"
)

func jwtContext(tenantID, userID string, operator bool) context.Context {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "jwt")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, tenantID)
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, userID)
	if operator {
		ctx = context.WithValue(ctx, ctxkeys.KeyPlatformOperator, true)
	}
	return ctx
}

func serviceContext(tenantID string) context.Context {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
	if tenantID != "" {
		ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, tenantID)
	}
	return ctx
}

type fakeIncidents struct {
	access       incidents.Access
	attachTenant string
	calls        int
	err          error
}

func (f *fakeIncidents) record(access incidents.Access) (lookoutdb.LookoutIncident, error) {
	f.calls++
	f.access = access
	return lookoutdb.LookoutIncident{ID: "i"}, f.err
}

func (f *fakeIncidents) List(_ context.Context, access incidents.Access, _ incidents.ListFilter) (incidents.ListPage, error) {
	_, err := f.record(access)
	return incidents.ListPage{}, err
}

func (f *fakeIncidents) Get(_ context.Context, access incidents.Access, _ string) (incidents.IncidentDetail, error) {
	inc, err := f.record(access)
	return incidents.IncidentDetail{Incident: inc}, err
}

func (f *fakeIncidents) Acknowledge(_ context.Context, access incidents.Access, _ string) (lookoutdb.LookoutIncident, error) {
	return f.record(access)
}

func (f *fakeIncidents) Assign(_ context.Context, access incidents.Access, _, _ string) (lookoutdb.LookoutIncident, error) {
	return f.record(access)
}

func (f *fakeIncidents) Resolve(_ context.Context, access incidents.Access, _ string) (lookoutdb.LookoutIncident, error) {
	return f.record(access)
}

func (f *fakeIncidents) AddNote(_ context.Context, access incidents.Access, _, _ string) (lookoutdb.LookoutIncident, error) {
	return f.record(access)
}

func (f *fakeIncidents) AttachInvestigation(_ context.Context, _, tenantID, _ string) (lookoutdb.LookoutIncident, error) {
	f.attachTenant = tenantID
	return f.record(incidents.Access{TenantID: tenantID})
}

func (f *fakeIncidents) FiringAlertCount(context.Context, lookoutdb.LookoutIncident) (int, error) {
	return 0, nil
}

func TestAccessFromContext(t *testing.T) {
	cases := []struct {
		name string
		ctx  context.Context
		want incidents.Access
		code codes.Code
	}{
		{name: "platform operator is unrestricted", ctx: jwtContext(tenantA, userA, true), want: incidents.Access{Unrestricted: true, ActorUserID: userA}},
		{name: "tenant user is restricted to its tenant", ctx: jwtContext(tenantA, userA, false), want: incidents.Access{TenantID: tenantA, ActorUserID: userA}},
		{name: "service call with tenant metadata acts as the tenant", ctx: serviceContext(tenantB), want: incidents.Access{TenantID: tenantB}},
		{name: "tenantless service call is unrestricted", ctx: serviceContext(""), want: incidents.Access{Unrestricted: true}},
		{name: "no identity is denied", ctx: context.Background(), code: codes.PermissionDenied},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := accessFromContext(tc.ctx)
			if tc.code != codes.OK {
				if status.Code(err) != tc.code {
					t.Fatalf("err = %v, want %s", err, tc.code)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("access = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

func TestAttachInvestigationIsServiceOnly(t *testing.T) {
	fake := &fakeIncidents{}
	srv := &Server{Incidents: fake}
	req := &lookoutpb.AttachInvestigationRequest{IncidentId: "i", TenantId: tenantA, ReportId: "r"}

	if _, err := srv.AttachInvestigation(jwtContext(tenantA, userA, true), req); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("operator JWT attach = %v, want PermissionDenied", err)
	}
	if _, err := srv.AttachInvestigation(serviceContext(tenantB), req); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("mismatched tenant metadata attach = %v, want PermissionDenied", err)
	}
	if fake.calls != 0 {
		t.Fatalf("service was called %d times for denied requests", fake.calls)
	}
	if _, err := srv.AttachInvestigation(serviceContext(""), req); err != nil || fake.attachTenant != tenantA {
		t.Fatalf("service attach = %v tenant=%q", err, fake.attachTenant)
	}
}

func TestRestrictedCallerAccessReachesService(t *testing.T) {
	fake := &fakeIncidents{}
	srv := &Server{Incidents: fake}
	if _, err := srv.AcknowledgeIncident(jwtContext(tenantA, userA, false), &lookoutpb.AcknowledgeIncidentRequest{IncidentId: "i"}); err != nil {
		t.Fatal(err)
	}
	if fake.access.Unrestricted || fake.access.TenantID != tenantA || fake.access.ActorUserID != userA {
		t.Fatalf("access = %+v", fake.access)
	}
}

func TestServiceErrorsMapToGRPCCodes(t *testing.T) {
	cases := map[error]codes.Code{
		incidents.ErrNotFound:          codes.NotFound,
		incidents.ErrPermissionDenied:  codes.PermissionDenied,
		incidents.ErrInvalidArgument:   codes.InvalidArgument,
		incidents.ErrInvalidState:      codes.FailedPrecondition,
		errors.New("database is down"): codes.Internal,
	}
	for serviceErr, want := range cases {
		srv := &Server{Incidents: &fakeIncidents{err: serviceErr}}
		_, err := srv.GetIncident(jwtContext(tenantA, userA, false), &lookoutpb.GetIncidentRequest{IncidentId: "i"})
		if status.Code(err) != want {
			t.Errorf("%v -> %v, want %s", serviceErr, err, want)
		}
	}
}

func TestListRejectsBackwardPagination(t *testing.T) {
	srv := &Server{Incidents: &fakeIncidents{}}
	_, err := srv.ListIncidents(jwtContext(tenantA, userA, false), &lookoutpb.ListIncidentsRequest{
		Pagination: &commonpb.CursorPaginationRequest{Last: 10},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("err = %v, want InvalidArgument", err)
	}
}
