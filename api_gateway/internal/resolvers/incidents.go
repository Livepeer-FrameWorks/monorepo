package resolvers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/demo"
	"frameworks/api_gateway/internal/middleware"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/authz"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	lookoutpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/lookout"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	incidentsDefaultPageSize = 50
	incidentsMaxPageSize     = 200
)

var errIncidentsUnavailable = errors.New("incidents are unavailable")

// Lookout decides visibility from the call identity: a service call with tenant
// metadata sees only that tenant's incidents, a tenantless service call sees
// every scope. The gateway therefore never forwards the user JWT to Lookout
// (an operator's JWT would widen a tenant request); it authorizes here and
// sends either the tenant or, for platform operators, no tenant. The user ID
// is kept so the incident timeline records who acted.
func withIncidentActor(ctx, callCtx context.Context) context.Context {
	if userID := userIDFromContext(ctx); userID != "" {
		return context.WithValue(callCtx, ctxkeys.KeyUserID, userID)
	}
	return callCtx
}

func (r *Resolver) tenantIncidentCtx(ctx context.Context, manage bool) (context.Context, error) {
	permission, action := "infrastructure:read", authz.ActionReadPrivateInfrastructure
	if manage {
		permission, action = "infrastructure:write", authz.ActionManageEdgeCluster
	}
	tenantID := tenantIDFromContext(ctx)
	if err := middleware.RequireTenantAction(ctx, permission, action, tenantID); err != nil {
		return nil, err
	}
	if tenantID == "" {
		return nil, fmt.Errorf("incident access requires a tenant")
	}
	return withIncidentActor(ctx, platformTenantCtx(ctx, tenantID)), nil
}

// incidentCtx lets platform operators reach any incident (the admin incident
// page uses the same detail and action fields); other callers are restricted
// to their tenant.
func (r *Resolver) incidentCtx(ctx context.Context, surface string, manage bool) (context.Context, error) {
	if r.RequirePlatformOperator(ctx) == nil {
		r.auditPlatformRead(ctx, surface, "", true)
		return withIncidentActor(ctx, platformServiceCtx(ctx)), nil
	}
	return r.tenantIncidentCtx(ctx, manage)
}

func incidentPagination(page *model.ConnectionInput) (*commonpb.CursorPaginationRequest, error) {
	req := &commonpb.CursorPaginationRequest{First: incidentsDefaultPageSize}
	if page == nil {
		return req, nil
	}
	if page.Last != nil || page.Before != nil {
		return nil, fmt.Errorf("incidents support forward pagination only")
	}
	if page.First != nil && *page.First > 0 {
		req.First = int32(min(*page.First, incidentsMaxPageSize))
	}
	if page.After != nil {
		after := *page.After
		req.After = &after
	}
	return req, nil
}

func incidentStatusesToProto(statuses []model.IncidentStatus) []lookoutpb.IncidentStatus {
	out := make([]lookoutpb.IncidentStatus, 0, len(statuses))
	for _, st := range statuses {
		switch st {
		case model.IncidentStatusFiring:
			out = append(out, lookoutpb.IncidentStatus_INCIDENT_STATUS_FIRING)
		case model.IncidentStatusAcknowledged:
			out = append(out, lookoutpb.IncidentStatus_INCIDENT_STATUS_ACKNOWLEDGED)
		case model.IncidentStatusResolved:
			out = append(out, lookoutpb.IncidentStatus_INCIDENT_STATUS_RESOLVED)
		}
	}
	return out
}

// DoIncidentsConnection lists incidents on clusters the caller's tenant owns.
// Platform operators get their own tenant here too; the cross-tenant list is
// platform.incidents.
func (r *Resolver) DoIncidentsConnection(ctx context.Context, page *model.ConnectionInput, filter *model.IncidentFilterInput) (*model.IncidentsConnection, error) {
	if middleware.IsDemoMode(ctx) {
		return incidentsConnectionFromModels(demo.GenerateIncidents()), nil
	}
	callCtx, err := r.tenantIncidentCtx(ctx, false)
	if err != nil {
		return nil, err
	}
	if r.Clients.Lookout == nil {
		return nil, errIncidentsUnavailable
	}
	pagination, err := incidentPagination(page)
	if err != nil {
		return nil, err
	}
	req := &lookoutpb.ListIncidentsRequest{
		Scope:      lookoutpb.IncidentScope_INCIDENT_SCOPE_TENANT,
		Pagination: pagination,
	}
	if filter != nil {
		req.Statuses = incidentStatusesToProto(filter.Statuses)
		req.ClusterId = strings.TrimSpace(deref(filter.ClusterID))
	}
	resp, err := r.Clients.Lookout.ListIncidents(callCtx, req)
	if err != nil {
		return nil, fmt.Errorf("list incidents: %w", err)
	}
	return incidentsConnectionFromProto(resp), nil
}

// DoPlatformIncidents lists incidents across scopes for platform operators.
func (r *Resolver) DoPlatformIncidents(ctx context.Context, page *model.ConnectionInput, filter *model.PlatformIncidentFilterInput) (*model.IncidentsConnection, error) {
	targetTenant := ""
	if filter != nil {
		targetTenant = strings.TrimSpace(deref(filter.TenantID))
	}
	if err := r.platformGate(ctx, "platform.incidents", targetTenant); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		return incidentsConnectionFromModels(demo.GenerateIncidents()), nil
	}
	if r.Clients.Lookout == nil {
		return nil, errIncidentsUnavailable
	}
	pagination, err := incidentPagination(page)
	if err != nil {
		return nil, err
	}
	req := &lookoutpb.ListIncidentsRequest{Pagination: pagination, TenantId: targetTenant}
	if filter != nil {
		req.Statuses = incidentStatusesToProto(filter.Statuses)
		req.ClusterId = strings.TrimSpace(deref(filter.ClusterID))
		if filter.Scope != nil {
			switch *filter.Scope {
			case model.IncidentScopePlatform:
				req.Scope = lookoutpb.IncidentScope_INCIDENT_SCOPE_PLATFORM
			case model.IncidentScopeTenant:
				req.Scope = lookoutpb.IncidentScope_INCIDENT_SCOPE_TENANT
			}
		}
	}
	resp, err := r.Clients.Lookout.ListIncidents(platformServiceCtx(ctx), req)
	if err != nil {
		return nil, fmt.Errorf("list platform incidents: %w", err)
	}
	return incidentsConnectionFromProto(resp), nil
}

// DoIncident returns one incident with alerts and timeline, or nil when the
// caller cannot see it.
func (r *Resolver) DoIncident(ctx context.Context, id string) (*model.IncidentDetail, error) {
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateIncidentDetail(id), nil
	}
	callCtx, err := r.incidentCtx(ctx, "incident", false)
	if err != nil {
		return nil, err
	}
	if r.Clients.Lookout == nil {
		return nil, errIncidentsUnavailable
	}
	resp, err := r.Clients.Lookout.GetIncident(callCtx, strings.TrimSpace(id))
	if err != nil {
		switch status.Code(err) {
		case codes.NotFound, codes.InvalidArgument:
			return nil, nil
		}
		return nil, fmt.Errorf("get incident: %w", err)
	}
	return incidentDetailFromProto(resp), nil
}

func (r *Resolver) DoAcknowledgeIncident(ctx context.Context, id string) (model.IncidentMutationResult, error) {
	return r.incidentMutation(ctx, "acknowledgeIncident", func(callCtx context.Context) (*lookoutpb.IncidentMutationResponse, error) {
		return r.Clients.Lookout.AcknowledgeIncident(callCtx, strings.TrimSpace(id))
	})
}

// DoAssignIncident assigns an incident to the caller or clears the assignment.
// No API lists a tenant's members, so another user cannot be verified as a
// member of the incident's tenant; taking an incident yourself always is.
func (r *Resolver) DoAssignIncident(ctx context.Context, id string, assigneeUserID *string) (model.IncidentMutationResult, error) {
	assignee := strings.TrimSpace(deref(assigneeUserID))
	if assignee != "" && !middleware.IsDemoMode(ctx) && assignee != userIDFromContext(ctx) {
		return &model.ValidationError{Message: "An incident can only be assigned to yourself or unassigned.", Field: strPtr("assigneeUserId")}, nil
	}
	return r.incidentMutation(ctx, "assignIncident", func(callCtx context.Context) (*lookoutpb.IncidentMutationResponse, error) {
		return r.Clients.Lookout.AssignIncident(callCtx, strings.TrimSpace(id), assignee)
	})
}

func (r *Resolver) DoResolveIncident(ctx context.Context, id string) (model.IncidentMutationResult, error) {
	return r.incidentMutation(ctx, "resolveIncident", func(callCtx context.Context) (*lookoutpb.IncidentMutationResponse, error) {
		return r.Clients.Lookout.ResolveIncident(callCtx, strings.TrimSpace(id))
	})
}

func (r *Resolver) DoAddIncidentNote(ctx context.Context, id, body string) (model.IncidentMutationResult, error) {
	return r.incidentMutation(ctx, "addIncidentNote", func(callCtx context.Context) (*lookoutpb.IncidentMutationResponse, error) {
		return r.Clients.Lookout.AddIncidentNote(callCtx, strings.TrimSpace(id), body)
	})
}

func (r *Resolver) incidentMutation(ctx context.Context, surface string, call func(context.Context) (*lookoutpb.IncidentMutationResponse, error)) (model.IncidentMutationResult, error) {
	if middleware.IsDemoMode(ctx) {
		return nil, errDemoUnavailable("Incident changes")
	}
	callCtx, err := r.incidentCtx(ctx, surface, true)
	if err != nil {
		return nil, err
	}
	if r.Clients.Lookout == nil {
		return nil, errIncidentsUnavailable
	}
	resp, err := call(callCtx)
	if err != nil {
		if vErr := mapInvalidArgument(err); vErr != nil {
			return vErr, nil
		}
		if vErr := mapFailedPrecondition(err); vErr != nil {
			return vErr, nil
		}
		if nfErr := mapNotFound(err); nfErr != nil {
			return nfErr, nil
		}
		if authErr := mapPermissionDenied(err); authErr != nil {
			return authErr, nil
		}
		return nil, fmt.Errorf("%s: %w", surface, err)
	}
	inc := incidentFromProto(resp.GetIncident())
	if inc == nil {
		return nil, fmt.Errorf("%s: empty incident response", surface)
	}
	return inc, nil
}

// DoIncidentUpdates streams incident changes. Platform operators receive every
// incident they can reach through the operator API (all scopes and tenants),
// the same visibility incidentCtx grants; other callers receive their tenant's
// incidents only. The operator grant is checked here, when the subscription
// starts, and Signalman admits the operator channel only on the Gateway's
// tenantless service stream.
func (r *Resolver) DoIncidentUpdates(ctx context.Context) (<-chan *model.IncidentUpdatedEvent, error) {
	if middleware.IsDemoMode(ctx) {
		ch := make(chan *model.IncidentUpdatedEvent)
		go func() {
			<-ctx.Done()
			close(ch)
		}()
		return ch, nil
	}
	user, err := middleware.RequireAuth(ctx)
	if err != nil {
		return nil, fmt.Errorf("authentication required for incident subscriptions: %w", err)
	}
	if r.RequirePlatformOperator(ctx) == nil {
		r.auditPlatformRead(ctx, "liveIncidentUpdates", "", true)
		return r.SubManager.SubscribeToPlatformIncidents(ctx, ConnectionConfig{
			UserID:   user.UserID,
			TenantID: user.TenantID,
		})
	}
	if err := middleware.RequireTenantAction(ctx, "infrastructure:read", authz.ActionReadPrivateInfrastructure, user.TenantID); err != nil {
		return nil, err
	}
	if user.TenantID == "" {
		return nil, fmt.Errorf("incident subscriptions require a tenant")
	}
	return r.SubManager.SubscribeToIncidents(ctx, ConnectionConfig{
		UserID:   user.UserID,
		TenantID: user.TenantID,
		JWT:      ctxkeys.GetJWTToken(ctx),
	})
}

func incidentsConnectionFromProto(resp *lookoutpb.ListIncidentsResponse) *model.IncidentsConnection {
	nodes := make([]*model.Incident, 0, len(resp.GetIncidents()))
	for _, inc := range resp.GetIncidents() {
		if converted := incidentFromProto(inc); converted != nil {
			nodes = append(nodes, converted)
		}
	}
	conn := incidentsConnectionFromModels(nodes)
	pag := resp.GetPagination()
	conn.TotalCount = int(pag.GetTotalCount())
	conn.PageInfo.HasNextPage = pag.GetHasNextPage()
	conn.PageInfo.HasPreviousPage = pag.GetHasPreviousPage()
	if pag.GetStartCursor() != "" {
		start := pag.GetStartCursor()
		conn.PageInfo.StartCursor = &start
	}
	if pag.GetEndCursor() != "" {
		end := pag.GetEndCursor()
		conn.PageInfo.EndCursor = &end
	}
	return conn
}

func incidentsConnectionFromModels(nodes []*model.Incident) *model.IncidentsConnection {
	edges := make([]*model.IncidentEdge, 0, len(nodes))
	for _, node := range nodes {
		edges = append(edges, &model.IncidentEdge{Cursor: node.ID, Node: node})
	}
	return &model.IncidentsConnection{
		Edges:      edges,
		Nodes:      nodes,
		PageInfo:   &model.PageInfo{},
		TotalCount: len(nodes),
	}
}

func incidentFromProto(inc *lookoutpb.Incident) *model.Incident {
	if inc == nil || inc.GetId() == "" {
		return nil
	}
	out := &model.Incident{
		ID:               inc.GetId(),
		Scope:            model.IncidentScopePlatform,
		TenantID:         optionalString(inc.GetTenantId()),
		ClusterID:        optionalString(inc.GetClusterId()),
		Region:           optionalString(inc.GetRegion()),
		Alertname:        inc.GetAlertname(),
		Severity:         inc.GetSeverity(),
		Status:           incidentStatusFromProto(inc.GetStatus()),
		Resolution:       incidentResolutionFromProto(inc.GetResolution()),
		Title:            inc.GetTitle(),
		Summary:          optionalString(inc.GetSummary()),
		FiringAlertCount: int(inc.GetFiringAlertCount()),
		StartedAt:        inc.GetStartedAt().AsTime(),
		LastAlertAt:      inc.GetLastAlertAt().AsTime(),
		AcknowledgedBy:   optionalString(inc.GetAcknowledgedBy()),
		AssignedTo:       optionalString(inc.GetAssignedTo()),
		ResolvedBy:       optionalString(inc.GetResolvedBy()),
		CreatedAt:        inc.GetCreatedAt().AsTime(),
		UpdatedAt:        inc.GetUpdatedAt().AsTime(),
	}
	if inc.GetScope() == lookoutpb.IncidentScope_INCIDENT_SCOPE_TENANT {
		out.Scope = model.IncidentScopeTenant
	}
	if inc.GetAcknowledgedAt() != nil {
		t := inc.GetAcknowledgedAt().AsTime()
		out.AcknowledgedAt = &t
	}
	if inc.GetResolvedAt() != nil {
		t := inc.GetResolvedAt().AsTime()
		out.ResolvedAt = &t
	}
	return out
}

func incidentDetailFromProto(resp *lookoutpb.GetIncidentResponse) *model.IncidentDetail {
	inc := incidentFromProto(resp.GetIncident())
	if inc == nil {
		return nil
	}
	detail := &model.IncidentDetail{
		Incident: inc,
		Alerts:   make([]*model.IncidentAlert, 0, len(resp.GetAlerts())),
		Timeline: make([]*model.IncidentTimelineEvent, 0, len(resp.GetTimeline())),
	}
	for _, alert := range resp.GetAlerts() {
		out := &model.IncidentAlert{
			Fingerprint:  alert.GetFingerprint(),
			Status:       alert.GetStatus(),
			Labels:       stringMapToAny(alert.GetLabels()),
			Annotations:  stringMapToAny(alert.GetAnnotations()),
			StartsAt:     alert.GetStartsAt().AsTime(),
			GeneratorURL: optionalString(alert.GetGeneratorUrl()),
		}
		if alert.GetEndsAt() != nil {
			t := alert.GetEndsAt().AsTime()
			out.EndsAt = &t
		}
		detail.Alerts = append(detail.Alerts, out)
	}
	for _, event := range resp.GetTimeline() {
		kind, ok := incidentEventKindFromProto(event.GetKind())
		if !ok {
			continue
		}
		detail.Timeline = append(detail.Timeline, &model.IncidentTimelineEvent{
			ID:               event.GetId(),
			Kind:             kind,
			ActorUserID:      optionalString(event.GetActorUserId()),
			CreatedAt:        event.GetCreatedAt().AsTime(),
			Note:             optionalString(event.GetNote()),
			AssignedTo:       optionalString(event.GetAssignedTo()),
			ReportID:         optionalString(event.GetReportId()),
			Channel:          optionalString(event.GetChannel()),
			Resolution:       incidentResolutionFromProto(event.GetResolution()),
			AlertFingerprint: optionalString(event.GetAlertFingerprint()),
			Alertname:        optionalString(event.GetAlertname()),
		})
	}
	return detail
}

// incidentUpdatedFromIPC maps the realtime projection; an unknown status means
// a newer Lookout and the event is skipped.
func incidentUpdatedFromIPC(event *ipcpb.IncidentEvent) *model.IncidentUpdatedEvent {
	if event == nil || event.GetIncidentId() == "" {
		return nil
	}
	var st model.IncidentStatus
	switch event.GetStatus() {
	case "firing":
		st = model.IncidentStatusFiring
	case "acknowledged":
		st = model.IncidentStatusAcknowledged
	case "resolved":
		st = model.IncidentStatusResolved
	default:
		return nil
	}
	return &model.IncidentUpdatedEvent{
		IncidentID: event.GetIncidentId(),
		ClusterID:  optionalString(event.GetClusterId()),
		Status:     st,
		Severity:   event.GetSeverity(),
		Title:      event.GetTitle(),
		Change:     event.GetChange(),
		UpdatedAt:  time.UnixMilli(event.GetUpdatedAtMs()).UTC(),
	}
}

func incidentStatusFromProto(st lookoutpb.IncidentStatus) model.IncidentStatus {
	switch st {
	case lookoutpb.IncidentStatus_INCIDENT_STATUS_ACKNOWLEDGED:
		return model.IncidentStatusAcknowledged
	case lookoutpb.IncidentStatus_INCIDENT_STATUS_RESOLVED:
		return model.IncidentStatusResolved
	default:
		return model.IncidentStatusFiring
	}
}

func incidentResolutionFromProto(resolution lookoutpb.IncidentResolution) *model.IncidentResolution {
	var out model.IncidentResolution
	switch resolution {
	case lookoutpb.IncidentResolution_INCIDENT_RESOLUTION_AUTO:
		out = model.IncidentResolutionAuto
	case lookoutpb.IncidentResolution_INCIDENT_RESOLUTION_MANUAL:
		out = model.IncidentResolutionManual
	default:
		return nil
	}
	return &out
}

func incidentEventKindFromProto(kind lookoutpb.IncidentEventKind) (model.IncidentEventKind, bool) {
	switch kind {
	case lookoutpb.IncidentEventKind_INCIDENT_EVENT_KIND_ALERT_FIRING:
		return model.IncidentEventKindAlertFiring, true
	case lookoutpb.IncidentEventKind_INCIDENT_EVENT_KIND_ALERT_RESOLVED:
		return model.IncidentEventKindAlertResolved, true
	case lookoutpb.IncidentEventKind_INCIDENT_EVENT_KIND_ACKNOWLEDGED:
		return model.IncidentEventKindAcknowledged, true
	case lookoutpb.IncidentEventKind_INCIDENT_EVENT_KIND_ASSIGNED:
		return model.IncidentEventKindAssigned, true
	case lookoutpb.IncidentEventKind_INCIDENT_EVENT_KIND_NOTE:
		return model.IncidentEventKindNote, true
	case lookoutpb.IncidentEventKind_INCIDENT_EVENT_KIND_RESOLVED:
		return model.IncidentEventKindResolved, true
	case lookoutpb.IncidentEventKind_INCIDENT_EVENT_KIND_INVESTIGATION_ATTACHED:
		return model.IncidentEventKindInvestigationAttached, true
	case lookoutpb.IncidentEventKind_INCIDENT_EVENT_KIND_NOTIFIED:
		return model.IncidentEventKindNotified, true
	case lookoutpb.IncidentEventKind_INCIDENT_EVENT_KIND_SCOPE_CHANGED:
		return model.IncidentEventKindScopeChanged, true
	default:
		return "", false
	}
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func stringMapToAny(values map[string]string) map[string]any {
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
