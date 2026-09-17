// Package grpcserver exposes Lookout incidents over gRPC.
package grpcserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"frameworks/api_incidents/internal/database/lookoutdb"
	"frameworks/api_incidents/internal/incidents"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	lookoutpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/lookout"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// IncidentService is the incident behaviour the gRPC API needs.
type IncidentService interface {
	List(ctx context.Context, access incidents.Access, filter incidents.ListFilter) (incidents.ListPage, error)
	Get(ctx context.Context, access incidents.Access, incidentID string) (incidents.IncidentDetail, error)
	Acknowledge(ctx context.Context, access incidents.Access, incidentID string) (lookoutdb.LookoutIncident, error)
	Assign(ctx context.Context, access incidents.Access, incidentID, assigneeUserID string) (lookoutdb.LookoutIncident, error)
	Resolve(ctx context.Context, access incidents.Access, incidentID string) (lookoutdb.LookoutIncident, error)
	AddNote(ctx context.Context, access incidents.Access, incidentID, body string) (lookoutdb.LookoutIncident, error)
	AttachInvestigation(ctx context.Context, incidentID, tenantID, reportID string) (lookoutdb.LookoutIncident, error)
	FiringAlertCount(ctx context.Context, inc lookoutdb.LookoutIncident) (int, error)
}

// Server implements lookoutpb.LookoutServiceServer.
type Server struct {
	lookoutpb.UnimplementedLookoutServiceServer
	Incidents IncidentService
	Logger    logging.Logger
}

// accessFromContext derives incident authority from the authenticated call.
// Platform operators are unrestricted; a tenant context (JWT or service-token
// metadata) restricts the caller to that tenant; a service-token call without
// tenant context is an internal caller and is unrestricted.
func accessFromContext(ctx context.Context) (incidents.Access, error) {
	userID := ctxkeys.GetUserID(ctx)
	if ctxkeys.IsPlatformOperator(ctx) {
		return incidents.Access{Unrestricted: true, ActorUserID: userID}, nil
	}
	if tenantID := strings.TrimSpace(ctxkeys.GetTenantID(ctx)); tenantID != "" {
		return incidents.Access{TenantID: tenantID, ActorUserID: userID}, nil
	}
	if middleware.IsServiceCall(ctx) {
		return incidents.Access{Unrestricted: true, ActorUserID: userID}, nil
	}
	return incidents.Access{}, status.Error(codes.PermissionDenied, "incident access requires a tenant or platform operator context")
}

func (s *Server) ListIncidents(ctx context.Context, req *lookoutpb.ListIncidentsRequest) (*lookoutpb.ListIncidentsResponse, error) {
	access, err := accessFromContext(ctx)
	if err != nil {
		return nil, err
	}
	pagination := req.GetPagination()
	if pagination.GetLast() > 0 || pagination.GetBefore() != "" {
		return nil, status.Error(codes.InvalidArgument, "backward pagination is not supported")
	}
	filter := incidents.ListFilter{
		Scope:     scopeFromProto(req.GetScope()),
		TenantID:  strings.TrimSpace(req.GetTenantId()),
		ClusterID: strings.TrimSpace(req.GetClusterId()),
		First:     int(pagination.GetFirst()),
		After:     pagination.GetAfter(),
	}
	for _, st := range req.GetStatuses() {
		if value := statusFromProto(st); value != "" {
			filter.Statuses = append(filter.Statuses, value)
		}
	}
	page, err := s.Incidents.List(ctx, access, filter)
	if err != nil {
		return nil, s.toStatus(err)
	}
	resp := &lookoutpb.ListIncidentsResponse{
		Incidents: make([]*lookoutpb.Incident, 0, len(page.Incidents)),
		Pagination: &pb.CursorPaginationResponse{
			TotalCount:      int32(page.TotalCount),
			HasNextPage:     page.HasNextPage,
			HasPreviousPage: pagination.GetAfter() != "",
		},
	}
	if page.StartCursor != "" {
		resp.Pagination.StartCursor = &page.StartCursor
		resp.Pagination.EndCursor = &page.EndCursor
	}
	for _, inc := range page.Incidents {
		resp.Incidents = append(resp.Incidents, incidentToProto(inc, page.FiringAlertCounts[inc.ID]))
	}
	return resp, nil
}

func (s *Server) GetIncident(ctx context.Context, req *lookoutpb.GetIncidentRequest) (*lookoutpb.GetIncidentResponse, error) {
	access, err := accessFromContext(ctx)
	if err != nil {
		return nil, err
	}
	detail, err := s.Incidents.Get(ctx, access, req.GetIncidentId())
	if err != nil {
		return nil, s.toStatus(err)
	}
	firing := 0
	for _, alert := range detail.Alerts {
		if alert.Status == "firing" {
			firing++
		}
	}
	resp := &lookoutpb.GetIncidentResponse{
		Incident: incidentToProto(detail.Incident, firing),
		Alerts:   make([]*lookoutpb.IncidentAlert, 0, len(detail.Alerts)),
		Timeline: make([]*lookoutpb.IncidentTimelineEvent, 0, len(detail.Events)),
	}
	for _, alert := range detail.Alerts {
		resp.Alerts = append(resp.Alerts, alertToProto(alert))
	}
	for _, event := range detail.Events {
		resp.Timeline = append(resp.Timeline, eventToProto(event))
	}
	return resp, nil
}

func (s *Server) AcknowledgeIncident(ctx context.Context, req *lookoutpb.AcknowledgeIncidentRequest) (*lookoutpb.IncidentMutationResponse, error) {
	access, err := accessFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.mutationResponse(ctx)(s.Incidents.Acknowledge(ctx, access, req.GetIncidentId()))
}

func (s *Server) AssignIncident(ctx context.Context, req *lookoutpb.AssignIncidentRequest) (*lookoutpb.IncidentMutationResponse, error) {
	access, err := accessFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.mutationResponse(ctx)(s.Incidents.Assign(ctx, access, req.GetIncidentId(), req.GetAssigneeUserId()))
}

func (s *Server) ResolveIncident(ctx context.Context, req *lookoutpb.ResolveIncidentRequest) (*lookoutpb.IncidentMutationResponse, error) {
	access, err := accessFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.mutationResponse(ctx)(s.Incidents.Resolve(ctx, access, req.GetIncidentId()))
}

func (s *Server) AddIncidentNote(ctx context.Context, req *lookoutpb.AddIncidentNoteRequest) (*lookoutpb.IncidentMutationResponse, error) {
	access, err := accessFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.mutationResponse(ctx)(s.Incidents.AddNote(ctx, access, req.GetIncidentId(), req.GetBody()))
}

// AttachInvestigation is restricted to service-token callers (Skipper). The
// auth interceptor also rejects JWT callers for this method.
func (s *Server) AttachInvestigation(ctx context.Context, req *lookoutpb.AttachInvestigationRequest) (*lookoutpb.IncidentMutationResponse, error) {
	if !middleware.IsServiceCall(ctx) {
		return nil, status.Error(codes.PermissionDenied, "AttachInvestigation is service-only")
	}
	tenantID := strings.TrimSpace(req.GetTenantId())
	if ctxTenant := strings.TrimSpace(ctxkeys.GetTenantID(ctx)); ctxTenant != "" && ctxTenant != tenantID {
		return nil, status.Error(codes.PermissionDenied, "tenant_id does not match the caller's tenant")
	}
	return s.mutationResponse(ctx)(s.Incidents.AttachInvestigation(ctx, req.GetIncidentId(), tenantID, req.GetReportId()))
}

func (s *Server) mutationResponse(ctx context.Context) func(lookoutdb.LookoutIncident, error) (*lookoutpb.IncidentMutationResponse, error) {
	return func(inc lookoutdb.LookoutIncident, err error) (*lookoutpb.IncidentMutationResponse, error) {
		if err != nil {
			return nil, s.toStatus(err)
		}
		firing, err := s.Incidents.FiringAlertCount(ctx, inc)
		if err != nil {
			return nil, s.toStatus(err)
		}
		return &lookoutpb.IncidentMutationResponse{Incident: incidentToProto(inc, firing)}, nil
	}
}

func (s *Server) toStatus(err error) error {
	switch {
	case errors.Is(err, incidents.ErrNotFound):
		return status.Error(codes.NotFound, "incident not found")
	case errors.Is(err, incidents.ErrPermissionDenied):
		return status.Error(codes.PermissionDenied, "incident access denied")
	case errors.Is(err, incidents.ErrInvalidArgument):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, incidents.ErrInvalidState):
		return status.Error(codes.FailedPrecondition, err.Error())
	default:
		if s.Logger != nil {
			s.Logger.WithError(err).Error("Lookout incident request failed")
		}
		return status.Error(codes.Internal, "incident request failed")
	}
}

func scopeFromProto(scope lookoutpb.IncidentScope) string {
	switch scope {
	case lookoutpb.IncidentScope_INCIDENT_SCOPE_PLATFORM:
		return incidents.ScopePlatform
	case lookoutpb.IncidentScope_INCIDENT_SCOPE_TENANT:
		return incidents.ScopeTenant
	default:
		return ""
	}
}

func scopeToProto(scope string) lookoutpb.IncidentScope {
	switch scope {
	case incidents.ScopePlatform:
		return lookoutpb.IncidentScope_INCIDENT_SCOPE_PLATFORM
	case incidents.ScopeTenant:
		return lookoutpb.IncidentScope_INCIDENT_SCOPE_TENANT
	default:
		return lookoutpb.IncidentScope_INCIDENT_SCOPE_UNSPECIFIED
	}
}

func statusFromProto(st lookoutpb.IncidentStatus) string {
	switch st {
	case lookoutpb.IncidentStatus_INCIDENT_STATUS_FIRING:
		return incidents.StatusFiring
	case lookoutpb.IncidentStatus_INCIDENT_STATUS_ACKNOWLEDGED:
		return incidents.StatusAcknowledged
	case lookoutpb.IncidentStatus_INCIDENT_STATUS_RESOLVED:
		return incidents.StatusResolved
	default:
		return ""
	}
}

func statusToProto(st string) lookoutpb.IncidentStatus {
	switch st {
	case incidents.StatusFiring:
		return lookoutpb.IncidentStatus_INCIDENT_STATUS_FIRING
	case incidents.StatusAcknowledged:
		return lookoutpb.IncidentStatus_INCIDENT_STATUS_ACKNOWLEDGED
	case incidents.StatusResolved:
		return lookoutpb.IncidentStatus_INCIDENT_STATUS_RESOLVED
	default:
		return lookoutpb.IncidentStatus_INCIDENT_STATUS_UNSPECIFIED
	}
}

func resolutionToProto(resolution string) lookoutpb.IncidentResolution {
	switch resolution {
	case incidents.ResolutionAuto:
		return lookoutpb.IncidentResolution_INCIDENT_RESOLUTION_AUTO
	case incidents.ResolutionManual:
		return lookoutpb.IncidentResolution_INCIDENT_RESOLUTION_MANUAL
	default:
		return lookoutpb.IncidentResolution_INCIDENT_RESOLUTION_UNSPECIFIED
	}
}

func eventKindToProto(kind string) lookoutpb.IncidentEventKind {
	switch kind {
	case incidents.EventAlertFiring:
		return lookoutpb.IncidentEventKind_INCIDENT_EVENT_KIND_ALERT_FIRING
	case incidents.EventAlertResolved:
		return lookoutpb.IncidentEventKind_INCIDENT_EVENT_KIND_ALERT_RESOLVED
	case incidents.EventAcknowledged:
		return lookoutpb.IncidentEventKind_INCIDENT_EVENT_KIND_ACKNOWLEDGED
	case incidents.EventAssigned:
		return lookoutpb.IncidentEventKind_INCIDENT_EVENT_KIND_ASSIGNED
	case incidents.EventNote:
		return lookoutpb.IncidentEventKind_INCIDENT_EVENT_KIND_NOTE
	case incidents.EventResolved:
		return lookoutpb.IncidentEventKind_INCIDENT_EVENT_KIND_RESOLVED
	case incidents.EventInvestigationAttached:
		return lookoutpb.IncidentEventKind_INCIDENT_EVENT_KIND_INVESTIGATION_ATTACHED
	case incidents.EventNotified:
		return lookoutpb.IncidentEventKind_INCIDENT_EVENT_KIND_NOTIFIED
	case incidents.EventScopeChanged:
		return lookoutpb.IncidentEventKind_INCIDENT_EVENT_KIND_SCOPE_CHANGED
	default:
		return lookoutpb.IncidentEventKind_INCIDENT_EVENT_KIND_UNSPECIFIED
	}
}

func incidentToProto(inc lookoutdb.LookoutIncident, firingAlerts int) *lookoutpb.Incident {
	out := &lookoutpb.Incident{
		Id:               inc.ID,
		Scope:            scopeToProto(inc.Scope),
		TenantId:         inc.TenantID.String,
		ClusterId:        inc.ClusterID,
		Region:           inc.Region,
		GroupKey:         inc.GroupKey,
		Alertname:        inc.Alertname,
		Severity:         inc.Severity,
		Status:           statusToProto(inc.Status),
		Resolution:       resolutionToProto(inc.Resolution.String),
		Title:            inc.Title,
		Summary:          inc.Summary,
		StartedAt:        timestamppb.New(inc.StartedAt),
		LastAlertAt:      timestamppb.New(inc.LastAlertAt),
		AcknowledgedBy:   inc.AcknowledgedBy.String,
		AssignedTo:       inc.AssignedTo.String,
		ResolvedBy:       inc.ResolvedBy.String,
		CreatedAt:        timestamppb.New(inc.CreatedAt),
		UpdatedAt:        timestamppb.New(inc.UpdatedAt),
		FiringAlertCount: int32(firingAlerts),
	}
	if inc.AcknowledgedAt.Valid {
		out.AcknowledgedAt = timestamppb.New(inc.AcknowledgedAt.Time)
	}
	if inc.ResolvedAt.Valid {
		out.ResolvedAt = timestamppb.New(inc.ResolvedAt.Time)
	}
	return out
}

func alertToProto(alert lookoutdb.LookoutIncidentAlert) *lookoutpb.IncidentAlert {
	out := &lookoutpb.IncidentAlert{
		Fingerprint:  alert.Fingerprint,
		Status:       alert.Status,
		Labels:       decodeStringMap(alert.Labels),
		Annotations:  decodeStringMap(alert.Annotations),
		StartsAt:     timestamppb.New(alert.StartsAt),
		GeneratorUrl: alert.GeneratorUrl,
	}
	if alert.EndsAt.Valid {
		out.EndsAt = timestamppb.New(alert.EndsAt.Time)
	}
	return out
}

func eventToProto(event lookoutdb.LookoutIncidentEvent) *lookoutpb.IncidentTimelineEvent {
	out := &lookoutpb.IncidentTimelineEvent{
		Id:          event.ID,
		Kind:        eventKindToProto(event.Kind),
		ActorUserId: event.ActorUserID.String,
		CreatedAt:   timestamppb.New(event.CreatedAt),
	}
	// Bodies are written by Lookout as JSON objects; an unreadable body leaves
	// the kind-specific fields empty rather than failing the whole timeline.
	body := map[string]any{}
	if err := json.Unmarshal(event.Body, &body); err != nil {
		body = map[string]any{}
	}
	text := func(key string) string {
		if value, ok := body[key].(string); ok {
			return value
		}
		return ""
	}
	switch event.Kind {
	case incidents.EventNote:
		out.Note = text("note")
	case incidents.EventAssigned:
		out.AssignedTo = text("assigned_to")
	case incidents.EventInvestigationAttached:
		out.ReportId = text("report_id")
	case incidents.EventNotified:
		out.Channel = text("channel")
	case incidents.EventResolved:
		out.Resolution = resolutionToProto(text("resolution"))
	case incidents.EventAlertFiring, incidents.EventAlertResolved:
		out.AlertFingerprint = text("fingerprint")
		out.Alertname = text("alertname")
	}
	return out
}

func decodeStringMap(raw []byte) map[string]string {
	out := map[string]string{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]string{}
	}
	return out
}
