// Package serviceeventoutbox writes Quartermaster events into the
// transactional outboxes. Both the gRPC handlers and the bootstrap reconciler
// use it, so a state change and its events commit together on every write
// path: the domain event into quartermaster.domain_event_outbox and the legacy
// service event into quartermaster.service_event_outbox, under one event ID.
package serviceeventoutbox

import (
	"context"
	"errors"
	"fmt"

	"frameworks/api_tenants/internal/database/quartermasterdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events/outbox"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/serviceevents"
	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const (
	// Schema is the database schema holding both outboxes.
	Schema = "quartermaster"
	// Source is the CloudEvents source of Quartermaster's domain events.
	Source = "quartermaster"
)

// Domain is the domain event a service event is written with. TenantID is
// empty for platform-scoped types.
type Domain struct {
	Message     proto.Message
	TenantID    string
	AggregateID string
}

// Enqueue writes event and, when its type has a domain counterpart, the
// domain event derived from it, inside the caller's transaction, so both
// become durable exactly when the state mutation commits.
func Enqueue(ctx context.Context, exec quartermasterdb.DBTX, event *ipcpb.ServiceEvent, actor events.Actor) (string, error) {
	if event == nil {
		return "", errors.New("nil service event")
	}
	domain, err := DomainFor(event)
	if err != nil {
		return "", err
	}
	return EnqueueWithDomain(ctx, exec, event, domain, actor)
}

// EnqueueWithDomain writes event and domain (nil for a service event without a
// domain counterpart) inside the caller's transaction. With a domain event,
// the service event carries the domain event's ID; without one it keeps the
// caller's ID or gets a new UUIDv7. The ID is stored with the row, so every
// dispatch of it sends the same ID.
func EnqueueWithDomain(ctx context.Context, exec quartermasterdb.DBTX, event *ipcpb.ServiceEvent, domain *Domain, actor events.Actor) (string, error) {
	if event == nil {
		return "", errors.New("nil service event")
	}
	scope, err := Scope(event)
	if err != nil {
		return "", err
	}
	switch {
	case domain != nil:
		opts := []events.Option{events.WithActor(actor)}
		if event.GetTimestamp().IsValid() {
			opts = append(opts, events.WithTime(event.GetTimestamp().AsTime()))
		}
		ev, newErr := events.New(Source, domain.TenantID, domain.AggregateID, domain.Message, opts...)
		if newErr != nil {
			return "", fmt.Errorf("build domain event for %s: %w", event.GetEventType(), newErr)
		}
		if enqErr := outbox.Enqueue(ctx, exec, Schema, ev); enqErr != nil {
			return "", enqErr
		}
		event.EventId = ev.ID
	case event.GetEventId() != "":
		if _, parseErr := uuid.Parse(event.GetEventId()); parseErr != nil {
			return "", fmt.Errorf("service event %s: event_id %q is not a UUID", event.GetEventType(), event.GetEventId())
		}
	default:
		id, idErr := uuid.NewV7()
		if idErr != nil {
			return "", fmt.Errorf("generate event id: %w", idErr)
		}
		event.EventId = id.String()
	}
	payload, err := protojson.Marshal(event)
	if err != nil {
		return "", fmt.Errorf("marshal service event: %w", err)
	}
	id, err := quartermasterdb.New(exec).EnqueueServiceEvent(ctx, quartermasterdb.EnqueueServiceEventParams{
		EventID: event.GetEventId(), EventType: event.GetEventType(), TenantID: event.GetTenantId(), Scope: scope,
		UserID: event.GetUserId(), ResourceType: event.GetResourceType(), ResourceID: event.GetResourceId(),
		Payload: string(payload),
	})
	if err != nil {
		return "", fmt.Errorf("insert service event outbox row: %w", err)
	}
	return id, nil
}

// DomainFor returns the domain event a Quartermaster service event is
// published as, or nil when its type has none (cluster access audits).
// cluster_invite_created is refused: the invited tenant is not part of the
// service event, so its caller passes the domain event to EnqueueWithDomain.
func DomainFor(event *ipcpb.ServiceEvent) (*Domain, error) {
	tenant := event.GetTenantEvent()
	cluster := event.GetClusterEvent()
	tenantID := event.GetTenantId()
	switch event.GetEventType() {
	case "tenant_created":
		return &Domain{TenantID: tenantID, AggregateID: tenantID, Message: &internalv1.TenantCreated{
			TenantId: tenantID, Attribution: tenant.GetAttribution(),
		}}, nil
	case "tenant_updated":
		return &Domain{TenantID: tenantID, AggregateID: tenantID, Message: &internalv1.TenantUpdated{
			TenantId: tenantID, ChangedFields: tenant.GetChangedFields(),
		}}, nil
	case "tenant_deleted":
		return &Domain{TenantID: tenantID, AggregateID: tenantID, Message: &internalv1.TenantDeleted{TenantId: tenantID}}, nil
	case "tenant_cluster_assigned":
		return &Domain{TenantID: tenantID, AggregateID: tenantID, Message: &internalv1.TenantClusterAssigned{
			TenantId: tenantID, ClusterId: cluster.GetClusterId(),
		}}, nil
	case "tenant_cluster_unassigned":
		return &Domain{TenantID: tenantID, AggregateID: tenantID, Message: &internalv1.TenantClusterUnassigned{
			TenantId: tenantID, ClusterId: cluster.GetClusterId(),
		}}, nil
	case "cluster_created":
		return &Domain{AggregateID: cluster.GetClusterId(), Message: &internalv1.ClusterCreated{
			ClusterId: cluster.GetClusterId(), OwnerTenantId: tenantID,
		}}, nil
	case "cluster_updated":
		return &Domain{AggregateID: cluster.GetClusterId(), Message: &internalv1.ClusterUpdated{
			ClusterId: cluster.GetClusterId(), OwnerTenantId: tenantID,
			BeforeState: cluster.GetBeforeState(), AfterState: cluster.GetAfterState(),
		}}, nil
	case "cluster_invite_created":
		return nil, errors.New("cluster_invite_created needs its domain event passed explicitly")
	case "cluster_invite_revoked":
		return &Domain{TenantID: tenantID, AggregateID: cluster.GetInviteId(), Message: &internalv1.ClusterInviteRevoked{
			InviteId: cluster.GetInviteId(), ClusterId: cluster.GetClusterId(),
		}}, nil
	case "cluster_subscription_requested":
		return &Domain{TenantID: tenantID, AggregateID: cluster.GetSubscriptionId(), Message: &internalv1.ClusterSubscriptionRequested{
			SubscriptionId: cluster.GetSubscriptionId(), ClusterId: cluster.GetClusterId(),
		}}, nil
	case "cluster_subscription_approved":
		return &Domain{TenantID: tenantID, AggregateID: cluster.GetSubscriptionId(), Message: &internalv1.ClusterSubscriptionApproved{
			SubscriptionId: cluster.GetSubscriptionId(), ClusterId: cluster.GetClusterId(),
		}}, nil
	case "cluster_subscription_rejected":
		return &Domain{TenantID: tenantID, AggregateID: cluster.GetSubscriptionId(), Message: &internalv1.ClusterSubscriptionRejected{
			SubscriptionId: cluster.GetSubscriptionId(), ClusterId: cluster.GetClusterId(),
			RejectReason: internalv1.ClusterRejectReason(cluster.GetRejectReasonCode()), Reason: cluster.GetReason(),
		}}, nil
	}
	return nil, nil
}

// Scope classifies an outbox event. A tenantless event is accepted only for
// platform-scoped types; any other tenantless event is a producer bug and fails
// the caller's transaction.
func Scope(event *ipcpb.ServiceEvent) (string, error) {
	if event.GetTenantId() != "" {
		return "tenant", nil
	}
	if serviceevents.PlatformScoped(event.GetEventType()) {
		return "platform", nil
	}
	return "", fmt.Errorf("service event %s requires tenant_id", event.GetEventType())
}
