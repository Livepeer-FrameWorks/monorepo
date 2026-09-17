package clientstest

import (
	"context"

	"frameworks/api_gateway/internal/clients"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/lookout"
	lookoutpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/lookout"
)

// FakeLookout stubs the Lookout incident client.
type FakeLookout struct {
	lookout.Interface

	ListIncidentsFn       func(ctx context.Context, req *lookoutpb.ListIncidentsRequest) (*lookoutpb.ListIncidentsResponse, error)
	GetIncidentFn         func(ctx context.Context, incidentID string) (*lookoutpb.GetIncidentResponse, error)
	AcknowledgeIncidentFn func(ctx context.Context, incidentID string) (*lookoutpb.IncidentMutationResponse, error)
	AssignIncidentFn      func(ctx context.Context, incidentID, assigneeUserID string) (*lookoutpb.IncidentMutationResponse, error)
	ResolveIncidentFn     func(ctx context.Context, incidentID string) (*lookoutpb.IncidentMutationResponse, error)
	AddIncidentNoteFn     func(ctx context.Context, incidentID, body string) (*lookoutpb.IncidentMutationResponse, error)
}

// WithLookout wires a FakeLookout into the ServiceClients.
func WithLookout(f *FakeLookout) func(*clients.ServiceClients) {
	return func(sc *clients.ServiceClients) { sc.Lookout = f }
}

func (f *FakeLookout) Close() error { return nil }

func (f *FakeLookout) ListIncidents(ctx context.Context, req *lookoutpb.ListIncidentsRequest) (*lookoutpb.ListIncidentsResponse, error) {
	if f.ListIncidentsFn == nil {
		panic("clientstest: FakeLookout.ListIncidents not stubbed")
	}
	return f.ListIncidentsFn(ctx, req)
}

func (f *FakeLookout) GetIncident(ctx context.Context, incidentID string) (*lookoutpb.GetIncidentResponse, error) {
	if f.GetIncidentFn == nil {
		panic("clientstest: FakeLookout.GetIncident not stubbed")
	}
	return f.GetIncidentFn(ctx, incidentID)
}

func (f *FakeLookout) AcknowledgeIncident(ctx context.Context, incidentID string) (*lookoutpb.IncidentMutationResponse, error) {
	if f.AcknowledgeIncidentFn == nil {
		panic("clientstest: FakeLookout.AcknowledgeIncident not stubbed")
	}
	return f.AcknowledgeIncidentFn(ctx, incidentID)
}

func (f *FakeLookout) AssignIncident(ctx context.Context, incidentID, assigneeUserID string) (*lookoutpb.IncidentMutationResponse, error) {
	if f.AssignIncidentFn == nil {
		panic("clientstest: FakeLookout.AssignIncident not stubbed")
	}
	return f.AssignIncidentFn(ctx, incidentID, assigneeUserID)
}

func (f *FakeLookout) ResolveIncident(ctx context.Context, incidentID string) (*lookoutpb.IncidentMutationResponse, error) {
	if f.ResolveIncidentFn == nil {
		panic("clientstest: FakeLookout.ResolveIncident not stubbed")
	}
	return f.ResolveIncidentFn(ctx, incidentID)
}

func (f *FakeLookout) AddIncidentNote(ctx context.Context, incidentID, body string) (*lookoutpb.IncidentMutationResponse, error) {
	if f.AddIncidentNoteFn == nil {
		panic("clientstest: FakeLookout.AddIncidentNote not stubbed")
	}
	return f.AddIncidentNoteFn(ctx, incidentID, body)
}
