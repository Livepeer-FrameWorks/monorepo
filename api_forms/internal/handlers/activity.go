package handlers

import (
	"context"
	"time"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const activityEmitTimeout = time.Second

// ServiceEventSender is the Decklog client slice Steward needs.
type ServiceEventSender interface {
	SendServiceEventContext(ctx context.Context, event *ipcpb.ServiceEvent) error
}

// DecklogActivityEmitter emits payload-free platform events. Contact content,
// subscriber identity, and request metadata never enter the event backbone.
type DecklogActivityEmitter struct {
	Client ServiceEventSender
}

func (e *DecklogActivityEmitter) EmitActivity(ctx context.Context, eventType string) error {
	if e == nil || e.Client == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, activityEmitTimeout)
	defer cancel()
	return e.Client.SendServiceEventContext(ctx, &ipcpb.ServiceEvent{
		EventType:    eventType,
		Timestamp:    timestamppb.Now(),
		Source:       "steward",
		ResourceType: "marketing",
	})
}
