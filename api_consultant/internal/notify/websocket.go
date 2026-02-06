package notify

import (
	"context"
	"fmt"

	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const websocketEventType = "skipper_investigation"

type ServiceEventPublisher interface {
	SendServiceEvent(event *pb.ServiceEvent) error
}

type WebsocketNotifier struct {
	publisher ServiceEventPublisher
	logger    logging.Logger
	eventType string
}

func NewWebsocketNotifier(publisher ServiceEventPublisher, logger logging.Logger) *WebsocketNotifier {
	return &WebsocketNotifier{
		publisher: publisher,
		logger:    logger,
		eventType: websocketEventType,
	}
}

func (n *WebsocketNotifier) Notify(ctx context.Context, report Report) error {
	_ = ctx
	if n.publisher == nil {
		n.logger.WithField("tenant_id", report.TenantID).Warn("Websocket notifier not configured, skipping skipper investigation event")
		return nil
	}

	eventID := uuid.NewString()
	event := &pb.ServiceEvent{
		EventId:   eventID,
		EventType: n.eventType,
		Timestamp: timestamppb.Now(),
		Source:    "skipper",
		TenantId:  report.TenantID,
		ResourceType: func() string {
			if report.InvestigationID != "" {
				return "skipper_report"
			}
			return "skipper_investigation"
		}(),
		ResourceId: report.InvestigationID,
	}

	if err := n.publisher.SendServiceEvent(event); err != nil {
		return fmt.Errorf("send websocket service event: %w", err)
	}

	n.logger.WithFields(logging.Fields{
		"tenant_id": report.TenantID,
		"event_id":  eventID,
	}).Info("Skipper investigation websocket event sent")
	return nil
}
