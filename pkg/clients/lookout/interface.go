package lookout

import (
	"context"

	lookoutpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/lookout"
)

// Interface is the full method surface of the concrete client so callers can
// inject fakes in tests.
type Interface interface {
	Close() error
	ListIncidents(ctx context.Context, req *lookoutpb.ListIncidentsRequest) (*lookoutpb.ListIncidentsResponse, error)
	GetIncident(ctx context.Context, incidentID string) (*lookoutpb.GetIncidentResponse, error)
	AcknowledgeIncident(ctx context.Context, incidentID string) (*lookoutpb.IncidentMutationResponse, error)
	AssignIncident(ctx context.Context, incidentID, assigneeUserID string) (*lookoutpb.IncidentMutationResponse, error)
	ResolveIncident(ctx context.Context, incidentID string) (*lookoutpb.IncidentMutationResponse, error)
	AddIncidentNote(ctx context.Context, incidentID, body string) (*lookoutpb.IncidentMutationResponse, error)
	AttachInvestigation(ctx context.Context, incidentID, tenantID, reportID string) (*lookoutpb.IncidentMutationResponse, error)
}

var _ Interface = (*GRPCClient)(nil)
