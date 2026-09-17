// Package serviceeventoutbox writes Quartermaster service events into the
// transactional outbox. Both the gRPC handlers and the bootstrap reconciler
// use it, so a state change and its event commit together on every write path.
package serviceeventoutbox

import (
	"context"
	"errors"
	"fmt"

	"frameworks/api_tenants/internal/database/quartermasterdb"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/serviceevents"
	"google.golang.org/protobuf/encoding/protojson"
)

// Enqueue writes the outbox row inside the caller's transaction, so the event
// becomes durable exactly when the state mutation commits.
func Enqueue(ctx context.Context, exec quartermasterdb.DBTX, event *ipcpb.ServiceEvent) (string, error) {
	if event == nil {
		return "", errors.New("nil service event")
	}
	scope, err := Scope(event)
	if err != nil {
		return "", err
	}
	payload, err := protojson.Marshal(event)
	if err != nil {
		return "", fmt.Errorf("marshal service event: %w", err)
	}
	id, err := quartermasterdb.New(exec).EnqueueServiceEvent(ctx, quartermasterdb.EnqueueServiceEventParams{
		EventType: event.GetEventType(), TenantID: event.GetTenantId(), Scope: scope, UserID: event.GetUserId(),
		ResourceType: event.GetResourceType(), ResourceID: event.GetResourceId(), Payload: string(payload),
	})
	if err != nil {
		return "", fmt.Errorf("insert service event outbox row: %w", err)
	}
	return id, nil
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
