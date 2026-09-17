// Package incidents implements Lookout's incident state machine: Alertmanager
// ingestion, operator and tenant actions, and the delivery outbox writes that
// accompany each transition.
package incidents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"frameworks/api_incidents/internal/database/lookoutdb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/serviceevents"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var (
	ErrNotFound         = errors.New("incident not found")
	ErrInvalidState     = errors.New("incident state does not allow this action")
	ErrInvalidArgument  = errors.New("invalid incident request")
	ErrPermissionDenied = errors.New("incident access denied")
)

// Incident statuses and resolutions stored in lookout.incidents.
const (
	StatusFiring       = "firing"
	StatusAcknowledged = "acknowledged"
	StatusResolved     = "resolved"

	ResolutionAuto   = "auto"
	ResolutionManual = "manual"
)

// Timeline kinds stored in lookout.incident_events.kind.
const (
	EventAlertFiring           = "alert_firing"
	EventAlertResolved         = "alert_resolved"
	EventAcknowledged          = "acknowledged"
	EventAssigned              = "assigned"
	EventNote                  = "note"
	EventResolved              = "resolved"
	EventInvestigationAttached = "investigation_attached"
	EventNotified              = "notified"
	EventScopeChanged          = "scope_changed"
)

// Realtime change names carried in ipc IncidentEvent.change.
const (
	changeOpened = "opened"
	// changeScopeChanged is sent to the tenant an incident moved to and to the
	// tenant it moved away from.
	changeScopeChanged = "scope_changed"

	realtimeEventType = "incident_updated"
	maxNoteLength     = 10000
)

// Access is the caller's authority over incidents.
type Access struct {
	// Unrestricted callers (platform operators and tenantless service calls)
	// read and act on every scope.
	Unrestricted bool
	// TenantID restricts a caller to its own tenant-scope incidents.
	TenantID string
	// ActorUserID is recorded on timeline events and acknowledgement/resolution
	// columns when it is a valid user ID.
	ActorUserID string
}

// RealtimePublisher sends tenant and platform operator incident updates
// through Decklog.
type RealtimePublisher interface {
	SendServiceEvent(event *ipcpb.ServiceEvent) error
}

// Service owns incident state transitions.
type Service struct {
	DB       *sql.DB
	Owners   OwnerLookup
	Router   OperatorRouter
	Realtime RealtimePublisher
	Logger   logging.Logger
	Metrics  *Metrics
	Now      func() time.Time

	// afterClusterScopeLock runs inside the ingest transaction once the
	// cluster's scope row is share-locked; tests use it to interleave a
	// concurrent ownership reconciliation.
	afterClusterScopeLock func(clusterID string)
}

type realtimeChange struct {
	incident lookoutdb.LookoutIncident
	change   string
	// recipientTenantID overrides the incident's own tenant as the recipient.
	// It is set only for the tenant a rescoped incident moved away from, which
	// already had visibility of the incident.
	recipientTenantID string
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) inTx(ctx context.Context, fn func(*lookoutdb.Queries) error) error {
	return database.WithRetryablePostgresTx(ctx, s.DB, nil, func(tx *sql.Tx) error {
		return fn(lookoutdb.New(tx))
	})
}

// publish emits realtime updates after commit, to two audiences:
//   - Tenants: an incident_updated event for each tenant recipient. A
//     platform-scope incident has none, and must never be sent with an empty
//     tenant, because Signalman delivers tenantless system events to every
//     tenant subscriber.
//   - Platform operators: one tenantless platform_incident_updated event per
//     incident and change, whatever the scope. A rescope lists the same
//     incident once per tenant recipient; operators see it once.
func (s *Service) publish(changes []realtimeChange) {
	if s.Realtime == nil {
		return
	}
	for _, c := range changes {
		inc := c.incident
		tenantID := c.recipientTenantID
		if tenantID == "" {
			if inc.Scope != ScopeTenant || !inc.TenantID.Valid {
				continue
			}
			tenantID = inc.TenantID.String
		}
		s.sendRealtime(realtimeEventType, tenantID, tenantID, inc, c.change)
	}
	type operatorKey struct{ incidentID, change string }
	sent := make(map[operatorKey]bool, len(changes))
	for _, c := range changes {
		key := operatorKey{c.incident.ID, c.change}
		if sent[key] {
			continue
		}
		sent[key] = true
		owner := ""
		if c.incident.Scope == ScopeTenant && c.incident.TenantID.Valid {
			owner = c.incident.TenantID.String
		}
		s.sendRealtime(serviceevents.PlatformIncidentUpdated, "", owner, c.incident, c.change)
	}
}

// sendRealtime sends one incident change. envelopeTenant selects the audience;
// payloadTenant is the tenant the payload names.
func (s *Service) sendRealtime(eventType, envelopeTenant, payloadTenant string, inc lookoutdb.LookoutIncident, change string) {
	event := &ipcpb.ServiceEvent{
		EventType:    eventType,
		Timestamp:    timestamppb.New(s.now()),
		Source:       "lookout",
		TenantId:     envelopeTenant,
		ResourceType: "incident",
		ResourceId:   inc.ID,
		Payload: &ipcpb.ServiceEvent_IncidentEvent{IncidentEvent: &ipcpb.IncidentEvent{
			IncidentId:  inc.ID,
			TenantId:    payloadTenant,
			ClusterId:   inc.ClusterID,
			Status:      inc.Status,
			Severity:    inc.Severity,
			Title:       inc.Title,
			Change:      change,
			UpdatedAtMs: inc.UpdatedAt.UnixMilli(),
		}},
	}
	if err := s.Realtime.SendServiceEvent(event); err != nil {
		s.Metrics.observeRealtime("error")
		if s.Logger != nil {
			s.Logger.WithError(err).WithField("incident_id", inc.ID).WithField("event_type", eventType).Warn("Failed to publish incident update")
		}
		return
	}
	s.Metrics.observeRealtime("sent")
}

func (s *Service) insertEvent(ctx context.Context, q *lookoutdb.Queries, incidentID, kind, actorUserID string, body map[string]any) (lookoutdb.InsertIncidentEventRow, error) {
	if body == nil {
		body = map[string]any{}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return lookoutdb.InsertIncidentEventRow{}, fmt.Errorf("encode %s event: %w", kind, err)
	}
	row, err := q.InsertIncidentEvent(ctx, lookoutdb.InsertIncidentEventParams{
		IncidentID:  incidentID,
		Kind:        kind,
		ActorUserID: uuidParam(actorUserID),
		Body:        raw,
	})
	if err != nil {
		return lookoutdb.InsertIncidentEventRow{}, fmt.Errorf("insert %s event: %w", kind, err)
	}
	return row, nil
}

// enqueueOperatorNotification writes one outbox row per routed operator
// channel. Only platform-scope incidents notify operator channels.
func (s *Service) enqueueOperatorNotification(ctx context.Context, q *lookoutdb.Queries, inc lookoutdb.LookoutIncident, eventID, event string) error {
	if inc.Scope != ScopePlatform || s.Router == nil {
		return nil
	}
	channels := s.Router.ChannelsFor(inc.Severity)
	if len(channels) == 0 {
		return nil
	}
	payload := DeliveryPayload{
		IncidentID: inc.ID,
		ClusterID:  inc.ClusterID,
		Region:     inc.Region,
		Alertname:  inc.Alertname,
		Severity:   inc.Severity,
		Title:      inc.Title,
		Summary:    inc.Summary,
		Event:      event,
		StartedAt:  inc.StartedAt.UTC(),
	}
	if inc.Resolution.Valid {
		payload.Resolution = inc.Resolution.String
	}
	if inc.ResolvedAt.Valid {
		resolvedAt := inc.ResolvedAt.Time.UTC()
		payload.ResolvedAt = &resolvedAt
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode notification payload: %w", err)
	}
	for _, channel := range channels {
		if err := q.EnqueueDelivery(ctx, lookoutdb.EnqueueDeliveryParams{
			EventID:    eventID,
			IncidentID: inc.ID,
			Channel:    channel,
			Payload:    raw,
		}); err != nil {
			return fmt.Errorf("enqueue %s notification: %w", channel, err)
		}
	}
	return nil
}

// enqueueIncidentPublication queues the lookout.incidents record for a new
// tenant-scope incident.
func (s *Service) enqueueIncidentPublication(ctx context.Context, q *lookoutdb.Queries, inc lookoutdb.LookoutIncident, eventID string) error {
	if inc.Scope != ScopeTenant || !inc.TenantID.Valid {
		return nil
	}
	raw, err := incidentPublicationPayload(inc)
	if err != nil {
		return err
	}
	if err := q.EnqueueDelivery(ctx, lookoutdb.EnqueueDeliveryParams{
		EventID:    eventID,
		IncidentID: inc.ID,
		TenantID:   inc.TenantID,
		Channel:    ChannelKafka,
		Payload:    raw,
	}); err != nil {
		return fmt.Errorf("enqueue incident publication: %w", err)
	}
	return nil
}

func incidentPublicationPayload(inc lookoutdb.LookoutIncident) (json.RawMessage, error) {
	raw, err := json.Marshal(KafkaIncidentMessage{
		IncidentID: inc.ID,
		TenantID:   inc.TenantID.String,
		ClusterID:  inc.ClusterID,
		Severity:   inc.Severity,
		Summary:    inc.Summary,
	})
	if err != nil {
		return nil, fmt.Errorf("encode incident publication: %w", err)
	}
	return raw, nil
}

func nullString(value string) sql.NullString {
	value = strings.TrimSpace(value)
	return sql.NullString{String: value, Valid: value != ""}
}

// uuidParam yields NULL for values that are not UUIDs so optional actor and
// assignee columns never fail the ::uuid cast.
func uuidParam(value string) sql.NullString {
	value = strings.TrimSpace(value)
	if _, err := uuid.Parse(value); err != nil {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}
