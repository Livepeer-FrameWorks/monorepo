//go:build schema_verify

package grpcserver

import (
	"context"
	"testing"
	"time"

	"frameworks/api_incidents/internal/incidents"
	"frameworks/api_incidents/internal/lookouttest"

	lookoutpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/lookout"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type staticOwners map[string]incidents.Scope

func (s staticOwners) LookupOwner(_ context.Context, clusterID string) (incidents.ClusterOwner, error) {
	if scope, ok := s[clusterID]; ok {
		return incidents.ClusterOwner{Scope: scope, UpdatedAt: time.Date(2026, 9, 15, 9, 0, 0, 0, time.UTC)}, nil
	}
	return incidents.ClusterOwner{Scope: incidents.Scope{Kind: incidents.ScopePlatform}}, nil
}

func ingestFor(t *testing.T, svc *incidents.Service, groupKey, cluster string) string {
	t.Helper()
	result, err := svc.IngestAlertmanager(context.Background(), incidents.AlertmanagerWebhook{
		GroupKey:    groupKey,
		GroupLabels: map[string]string{"alertname": "EdgeDown", "cluster": cluster},
		Alerts: []incidents.AlertmanagerAlert{{
			Status:      "firing",
			Labels:      map[string]string{"alertname": "EdgeDown", "severity": "warning"},
			StartsAt:    time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC),
			Fingerprint: groupKey,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return result.IncidentID
}

func TestLookoutGRPCAuthorization_RealPG(t *testing.T) {
	db := lookouttest.StartPostgres(t)
	svc := &incidents.Service{
		DB:     db,
		Owners: staticOwners{"tenant-a-cluster": {Kind: incidents.ScopeTenant, TenantID: tenantA}},
	}
	platformID := ingestFor(t, svc, "grpc-platform", "platform-cluster")
	tenantID := ingestFor(t, svc, "grpc-tenant", "tenant-a-cluster")
	srv := &Server{Incidents: svc}

	tenantACtx := jwtContext(tenantA, userA, false)
	tenantBCtx := jwtContext(tenantB, userA, false)
	operatorCtx := jwtContext(tenantB, userA, true)

	expectCode := func(label string, err error, want codes.Code) {
		t.Helper()
		if status.Code(err) != want {
			t.Fatalf("%s: err = %v, want %s", label, err, want)
		}
	}

	_, err := srv.GetIncident(tenantBCtx, &lookoutpb.GetIncidentRequest{IncidentId: tenantID})
	expectCode("tenant B reads tenant A incident", err, codes.NotFound)
	_, err = srv.GetIncident(tenantACtx, &lookoutpb.GetIncidentRequest{IncidentId: platformID})
	expectCode("tenant reads platform incident", err, codes.NotFound)
	_, err = srv.ListIncidents(tenantACtx, &lookoutpb.ListIncidentsRequest{Scope: lookoutpb.IncidentScope_INCIDENT_SCOPE_PLATFORM})
	expectCode("tenant lists platform scope", err, codes.PermissionDenied)
	_, err = srv.AcknowledgeIncident(tenantBCtx, &lookoutpb.AcknowledgeIncidentRequest{IncidentId: tenantID})
	expectCode("tenant B acknowledges tenant A incident", err, codes.NotFound)
	_, err = srv.ResolveIncident(tenantACtx, &lookoutpb.ResolveIncidentRequest{IncidentId: platformID})
	expectCode("tenant resolves platform incident", err, codes.NotFound)
	_, err = srv.AttachInvestigation(tenantACtx, &lookoutpb.AttachInvestigationRequest{IncidentId: tenantID, TenantId: tenantA, ReportId: "r"})
	expectCode("tenant JWT attaches investigation", err, codes.PermissionDenied)

	own, err := srv.ListIncidents(tenantACtx, &lookoutpb.ListIncidentsRequest{})
	if err != nil || len(own.GetIncidents()) != 1 || own.GetIncidents()[0].GetId() != tenantID || own.GetIncidents()[0].GetScope() != lookoutpb.IncidentScope_INCIDENT_SCOPE_TENANT {
		t.Fatalf("tenant list = %v, %v", own, err)
	}
	if _, err := srv.GetIncident(tenantACtx, &lookoutpb.GetIncidentRequest{IncidentId: tenantID}); err != nil {
		t.Fatalf("tenant reads own incident: %v", err)
	}
	all, err := srv.ListIncidents(operatorCtx, &lookoutpb.ListIncidentsRequest{})
	if err != nil || len(all.GetIncidents()) != 2 || all.GetPagination().GetTotalCount() != 2 {
		t.Fatalf("operator list = %v, %v", all, err)
	}
	if _, err := srv.GetIncident(operatorCtx, &lookoutpb.GetIncidentRequest{IncidentId: platformID}); err != nil {
		t.Fatalf("operator reads platform incident: %v", err)
	}

	const reportID = "30000000-0000-0000-0000-000000000001"
	if _, err := srv.AttachInvestigation(serviceContext(""), &lookoutpb.AttachInvestigationRequest{IncidentId: tenantID, TenantId: tenantA, ReportId: reportID}); err != nil {
		t.Fatalf("service attach: %v", err)
	}
	detail, err := srv.GetIncident(tenantACtx, &lookoutpb.GetIncidentRequest{IncidentId: tenantID})
	if err != nil {
		t.Fatal(err)
	}
	last := detail.GetTimeline()[len(detail.GetTimeline())-1]
	if last.GetKind() != lookoutpb.IncidentEventKind_INCIDENT_EVENT_KIND_INVESTIGATION_ATTACHED || last.GetReportId() != reportID {
		t.Fatalf("timeline tail = %v", last)
	}
}
