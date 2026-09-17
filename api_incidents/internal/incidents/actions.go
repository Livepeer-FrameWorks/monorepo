package incidents

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"frameworks/api_incidents/internal/database/lookoutdb"

	"github.com/google/uuid"
)

const (
	defaultPageSize = 50
	maxPageSize     = 200
)

// IncidentDetail is an incident with its alerts and timeline.
type IncidentDetail struct {
	Incident lookoutdb.LookoutIncident
	Alerts   []lookoutdb.LookoutIncidentAlert
	Events   []lookoutdb.LookoutIncidentEvent
}

// ListFilter narrows ListIncidents. Scope and TenantID only apply to
// unrestricted callers; restricted callers always list their tenant.
type ListFilter struct {
	Scope     string
	TenantID  string
	Statuses  []string
	ClusterID string
	First     int
	After     string
}

// ListPage is one forward page of incidents, newest first.
type ListPage struct {
	Incidents   []lookoutdb.LookoutIncident
	TotalCount  int
	HasNextPage bool
	StartCursor string
	EndCursor   string
	// FiringAlertCounts maps incident ID to its firing alerts; incidents
	// without firing alerts are absent.
	FiringAlertCounts map[string]int
}

// List returns incidents visible to the caller.
func (s *Service) List(ctx context.Context, access Access, filter ListFilter) (ListPage, error) {
	pageSize := filter.First
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	for _, st := range filter.Statuses {
		if st != StatusFiring && st != StatusAcknowledged && st != StatusResolved {
			return ListPage{}, fmt.Errorf("%w: unknown status %q", ErrInvalidArgument, st)
		}
	}
	afterAt, afterID, err := decodeCursor(filter.After)
	if err != nil {
		return ListPage{}, err
	}
	statuses := filter.Statuses
	if statuses == nil {
		statuses = []string{}
	}
	q := lookoutdb.New(s.DB)

	var rows []lookoutdb.LookoutIncident
	var total int32
	if access.Unrestricted {
		scope := strings.TrimSpace(filter.Scope)
		if scope != "" && scope != ScopePlatform && scope != ScopeTenant {
			return ListPage{}, fmt.Errorf("%w: unknown scope %q", ErrInvalidArgument, scope)
		}
		tenantFilter := sql.NullString{}
		if filter.TenantID != "" {
			if _, parseErr := uuid.Parse(filter.TenantID); parseErr != nil {
				return ListPage{}, fmt.Errorf("%w: tenant_id must be a UUID", ErrInvalidArgument)
			}
			tenantFilter = nullString(filter.TenantID)
		}
		rows, err = q.ListIncidents(ctx, lookoutdb.ListIncidentsParams{
			Scope:          nullString(scope),
			TenantID:       tenantFilter,
			Statuses:       statuses,
			ClusterID:      nullString(filter.ClusterID),
			AfterCreatedAt: afterAt,
			AfterID:        afterID,
			RowLimit:       int32(pageSize + 1),
		})
		if err == nil {
			total, err = q.CountIncidents(ctx, lookoutdb.CountIncidentsParams{
				Scope:     nullString(scope),
				TenantID:  tenantFilter,
				Statuses:  statuses,
				ClusterID: nullString(filter.ClusterID),
			})
		}
	} else {
		tenantID, tenantErr := restrictedTenant(access)
		if tenantErr != nil {
			return ListPage{}, tenantErr
		}
		if filter.Scope == ScopePlatform {
			return ListPage{}, ErrPermissionDenied
		}
		rows, err = q.ListTenantIncidents(ctx, lookoutdb.ListTenantIncidentsParams{
			TenantID:       tenantID,
			Statuses:       statuses,
			ClusterID:      nullString(filter.ClusterID),
			AfterCreatedAt: afterAt,
			AfterID:        afterID,
			RowLimit:       int32(pageSize + 1),
		})
		if err == nil {
			total, err = q.CountTenantIncidents(ctx, lookoutdb.CountTenantIncidentsParams{
				TenantID:  tenantID,
				Statuses:  statuses,
				ClusterID: nullString(filter.ClusterID),
			})
		}
	}
	if err != nil {
		return ListPage{}, fmt.Errorf("list incidents: %w", err)
	}

	page := ListPage{TotalCount: int(total)}
	if len(rows) > pageSize {
		page.HasNextPage = true
		rows = rows[:pageSize]
	}
	page.Incidents = rows
	page.FiringAlertCounts = map[string]int{}
	if len(rows) > 0 {
		page.StartCursor = encodeCursor(rows[0])
		page.EndCursor = encodeCursor(rows[len(rows)-1])
		ids := make([]string, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, row.ID)
		}
		countTenant := sql.NullString{}
		if !access.Unrestricted {
			countTenant = nullString(access.TenantID)
		}
		counts, countErr := q.CountFiringAlertsForIncidents(ctx, lookoutdb.CountFiringAlertsForIncidentsParams{IncidentIds: ids, TenantID: countTenant})
		if countErr != nil {
			return ListPage{}, fmt.Errorf("count firing alerts: %w", countErr)
		}
		for _, count := range counts {
			page.FiringAlertCounts[count.IncidentID] = int(count.Firing)
		}
	}
	return page, nil
}

// FiringAlertCount returns how many alerts of an incident the caller already
// loaded are still firing.
func (s *Service) FiringAlertCount(ctx context.Context, inc lookoutdb.LookoutIncident) (int, error) {
	firing, err := lookoutdb.New(s.DB).CountFiringIncidentAlerts(ctx, lookoutdb.CountFiringIncidentAlertsParams{IncidentID: inc.ID, TenantID: inc.TenantID})
	if err != nil {
		return 0, fmt.Errorf("count firing alerts: %w", err)
	}
	return int(firing), nil
}

// Get returns one incident with its alerts and timeline.
func (s *Service) Get(ctx context.Context, access Access, incidentID string) (IncidentDetail, error) {
	q := lookoutdb.New(s.DB)
	inc, err := s.load(ctx, q, access, incidentID)
	if err != nil {
		return IncidentDetail{}, err
	}
	alerts, err := q.ListIncidentAlerts(ctx, lookoutdb.ListIncidentAlertsParams{IncidentID: inc.ID, TenantID: inc.TenantID})
	if err != nil {
		return IncidentDetail{}, fmt.Errorf("list incident alerts: %w", err)
	}
	events, err := q.ListIncidentEvents(ctx, lookoutdb.ListIncidentEventsParams{IncidentID: inc.ID, TenantID: inc.TenantID})
	if err != nil {
		return IncidentDetail{}, fmt.Errorf("list incident events: %w", err)
	}
	return IncidentDetail{Incident: inc, Alerts: alerts, Events: events}, nil
}

// Acknowledge marks a firing incident acknowledged; it stays open.
func (s *Service) Acknowledge(ctx context.Context, access Access, incidentID string) (lookoutdb.LookoutIncident, error) {
	return s.mutate(ctx, access, incidentID, func(q *lookoutdb.Queries, inc lookoutdb.LookoutIncident) (lookoutdb.LookoutIncident, string, error) {
		switch inc.Status {
		case StatusAcknowledged:
			return inc, "", nil
		case StatusResolved:
			return inc, "", fmt.Errorf("%w: incident is resolved", ErrInvalidState)
		}
		updated, err := q.AcknowledgeIncident(ctx, lookoutdb.AcknowledgeIncidentParams{
			ActorUserID: uuidParam(access.ActorUserID),
			ID:          inc.ID,
			TenantID:    inc.TenantID,
		})
		if err != nil {
			return inc, "", fmt.Errorf("acknowledge incident: %w", err)
		}
		if _, err := s.insertEvent(ctx, q, inc.ID, EventAcknowledged, access.ActorUserID, nil); err != nil {
			return inc, "", err
		}
		return updated, EventAcknowledged, nil
	})
}

// Assign sets or clears the assignee of an open incident.
func (s *Service) Assign(ctx context.Context, access Access, incidentID, assigneeUserID string) (lookoutdb.LookoutIncident, error) {
	assigneeUserID = strings.TrimSpace(assigneeUserID)
	if assigneeUserID != "" {
		if _, err := uuid.Parse(assigneeUserID); err != nil {
			return lookoutdb.LookoutIncident{}, fmt.Errorf("%w: assignee_user_id must be a UUID", ErrInvalidArgument)
		}
	}
	return s.mutate(ctx, access, incidentID, func(q *lookoutdb.Queries, inc lookoutdb.LookoutIncident) (lookoutdb.LookoutIncident, string, error) {
		if inc.Status == StatusResolved {
			return inc, "", fmt.Errorf("%w: incident is resolved", ErrInvalidState)
		}
		if strings.EqualFold(inc.AssignedTo.String, assigneeUserID) && inc.AssignedTo.Valid == (assigneeUserID != "") {
			return inc, "", nil
		}
		updated, err := q.AssignIncident(ctx, lookoutdb.AssignIncidentParams{
			AssignedTo: uuidParam(assigneeUserID),
			ID:         inc.ID,
			TenantID:   inc.TenantID,
		})
		if err != nil {
			return inc, "", fmt.Errorf("assign incident: %w", err)
		}
		if _, err := s.insertEvent(ctx, q, inc.ID, EventAssigned, access.ActorUserID, map[string]any{"assigned_to": assigneeUserID}); err != nil {
			return inc, "", err
		}
		return updated, EventAssigned, nil
	})
}

// Resolve manually resolves an open incident. Alertmanager repeats of the
// alerts it contained do not reopen it.
func (s *Service) Resolve(ctx context.Context, access Access, incidentID string) (lookoutdb.LookoutIncident, error) {
	return s.mutate(ctx, access, incidentID, func(q *lookoutdb.Queries, inc lookoutdb.LookoutIncident) (lookoutdb.LookoutIncident, string, error) {
		if inc.Status == StatusResolved {
			return inc, "", nil
		}
		updated, err := q.ManuallyResolveIncident(ctx, lookoutdb.ManuallyResolveIncidentParams{
			ActorUserID: uuidParam(access.ActorUserID),
			ID:          inc.ID,
			TenantID:    inc.TenantID,
		})
		if err != nil {
			return inc, "", fmt.Errorf("resolve incident: %w", err)
		}
		row, err := s.insertEvent(ctx, q, inc.ID, EventResolved, access.ActorUserID, map[string]any{"resolution": ResolutionManual})
		if err != nil {
			return inc, "", err
		}
		if err := s.enqueueOperatorNotification(ctx, q, updated, row.ID, DeliveryEventResolved); err != nil {
			return inc, "", err
		}
		return updated, EventResolved, nil
	})
}

// AddNote appends a note to the incident timeline.
func (s *Service) AddNote(ctx context.Context, access Access, incidentID, body string) (lookoutdb.LookoutIncident, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return lookoutdb.LookoutIncident{}, fmt.Errorf("%w: note body is required", ErrInvalidArgument)
	}
	if len(body) > maxNoteLength {
		return lookoutdb.LookoutIncident{}, fmt.Errorf("%w: note body exceeds %d bytes", ErrInvalidArgument, maxNoteLength)
	}
	return s.mutate(ctx, access, incidentID, func(q *lookoutdb.Queries, inc lookoutdb.LookoutIncident) (lookoutdb.LookoutIncident, string, error) {
		if _, err := s.insertEvent(ctx, q, inc.ID, EventNote, access.ActorUserID, map[string]any{"note": body}); err != nil {
			return inc, "", err
		}
		updated, err := touch(ctx, q, inc)
		if err != nil {
			return inc, "", err
		}
		return updated, EventNote, nil
	})
}

// AttachInvestigation links a Skipper report to a tenant-scope incident.
// Attaching the same report again changes nothing.
func (s *Service) AttachInvestigation(ctx context.Context, incidentID, tenantID, reportID string) (lookoutdb.LookoutIncident, error) {
	tenantID = strings.TrimSpace(tenantID)
	reportID = strings.TrimSpace(reportID)
	if _, err := uuid.Parse(tenantID); err != nil {
		return lookoutdb.LookoutIncident{}, fmt.Errorf("%w: tenant_id must be a UUID", ErrInvalidArgument)
	}
	if reportID == "" {
		return lookoutdb.LookoutIncident{}, fmt.Errorf("%w: report_id is required", ErrInvalidArgument)
	}
	return s.mutate(ctx, Access{TenantID: tenantID}, incidentID, func(q *lookoutdb.Queries, inc lookoutdb.LookoutIncident) (lookoutdb.LookoutIncident, string, error) {
		body := fmt.Sprintf(`{"report_id":%q}`, reportID)
		inserted, err := q.InsertInvestigationAttachedEvent(ctx, lookoutdb.InsertInvestigationAttachedEventParams{
			IncidentID: inc.ID,
			Body:       []byte(body),
		})
		if err != nil {
			return inc, "", fmt.Errorf("attach investigation: %w", err)
		}
		if inserted == 0 {
			return inc, "", nil
		}
		updated, err := touch(ctx, q, inc)
		if err != nil {
			return inc, "", err
		}
		return updated, EventInvestigationAttached, nil
	})
}

// mutate loads the incident under the caller's access, locks it, and applies
// fn in one transaction. A non-empty change is published after commit.
func (s *Service) mutate(ctx context.Context, access Access, incidentID string, fn func(*lookoutdb.Queries, lookoutdb.LookoutIncident) (lookoutdb.LookoutIncident, string, error)) (lookoutdb.LookoutIncident, error) {
	var (
		result lookoutdb.LookoutIncident
		change string
	)
	err := s.inTx(ctx, func(q *lookoutdb.Queries) error {
		inc, err := s.load(ctx, q, access, incidentID)
		if err != nil {
			return err
		}
		inc, err = q.LockIncident(ctx, lookoutdb.LockIncidentParams{ID: inc.ID, TenantID: inc.TenantID})
		if err != nil {
			return fmt.Errorf("lock incident: %w", err)
		}
		result, change, err = fn(q, inc)
		return err
	})
	if err != nil {
		return lookoutdb.LookoutIncident{}, err
	}
	if change != "" {
		s.Metrics.observeTransition(result.Scope, change)
		s.publish([]realtimeChange{{incident: result, change: change}})
	}
	return result, nil
}

// load reads an incident the caller may see. Restricted callers only match
// their own tenant's incidents, so another tenant's or a platform incident is
// indistinguishable from a missing one.
func (s *Service) load(ctx context.Context, q *lookoutdb.Queries, access Access, incidentID string) (lookoutdb.LookoutIncident, error) {
	incidentID = strings.TrimSpace(incidentID)
	if _, err := uuid.Parse(incidentID); err != nil {
		return lookoutdb.LookoutIncident{}, fmt.Errorf("%w: incident_id must be a UUID", ErrInvalidArgument)
	}
	var (
		inc lookoutdb.LookoutIncident
		err error
	)
	if access.Unrestricted {
		inc, err = q.GetIncident(ctx, incidentID)
	} else {
		tenantID, tenantErr := restrictedTenant(access)
		if tenantErr != nil {
			return lookoutdb.LookoutIncident{}, tenantErr
		}
		inc, err = q.GetTenantIncident(ctx, lookoutdb.GetTenantIncidentParams{ID: incidentID, TenantID: tenantID})
	}
	if errors.Is(err, sql.ErrNoRows) {
		return lookoutdb.LookoutIncident{}, ErrNotFound
	}
	if err != nil {
		return lookoutdb.LookoutIncident{}, fmt.Errorf("get incident: %w", err)
	}
	return inc, nil
}

func restrictedTenant(access Access) (string, error) {
	tenantID := strings.TrimSpace(access.TenantID)
	if _, err := uuid.Parse(tenantID); err != nil {
		return "", ErrPermissionDenied
	}
	return tenantID, nil
}

func touch(ctx context.Context, q *lookoutdb.Queries, inc lookoutdb.LookoutIncident) (lookoutdb.LookoutIncident, error) {
	if err := q.TouchIncident(ctx, lookoutdb.TouchIncidentParams{ID: inc.ID, TenantID: inc.TenantID}); err != nil {
		return inc, fmt.Errorf("touch incident: %w", err)
	}
	updated, err := q.LockIncident(ctx, lookoutdb.LockIncidentParams{ID: inc.ID, TenantID: inc.TenantID})
	if err != nil {
		return inc, fmt.Errorf("reload incident: %w", err)
	}
	return updated, nil
}

func encodeCursor(inc lookoutdb.LookoutIncident) string {
	raw := inc.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + inc.ID
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(cursor string) (sql.NullTime, sql.NullString, error) {
	cursor = strings.TrimSpace(cursor)
	if cursor == "" {
		return sql.NullTime{}, sql.NullString{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return sql.NullTime{}, sql.NullString{}, fmt.Errorf("%w: malformed cursor", ErrInvalidArgument)
	}
	createdRaw, id, ok := strings.Cut(string(raw), "|")
	if !ok {
		return sql.NullTime{}, sql.NullString{}, fmt.Errorf("%w: malformed cursor", ErrInvalidArgument)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, createdRaw)
	if err != nil {
		return sql.NullTime{}, sql.NullString{}, fmt.Errorf("%w: malformed cursor", ErrInvalidArgument)
	}
	if _, err := uuid.Parse(id); err != nil {
		return sql.NullTime{}, sql.NullString{}, fmt.Errorf("%w: malformed cursor", ErrInvalidArgument)
	}
	return sql.NullTime{Time: createdAt, Valid: true}, sql.NullString{String: id, Valid: true}, nil
}
