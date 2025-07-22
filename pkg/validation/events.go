package validation

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-playground/validator/v10"
)

// EventType represents the analytics event kind flowing through the pipeline
// Helmsman → Decklog (gRPC) → Kafka → Periscope-Ingest.
type EventType string

const (
	// Emitted by Helmsman on MistServer PUSH_REWRITE webhook
	EventStreamIngest EventType = "stream-ingest"
	// Emitted by Helmsman on MistServer DEFAULT_STREAM webhook
	EventStreamView EventType = "stream-view"
	// Emitted by Helmsman to reflect stream state transitions
	EventStreamLifecycle EventType = "stream-lifecycle"
	// Emitted by Helmsman on viewer connect/disconnect
	EventUserConnection EventType = "user-connection"
	// Emitted by Helmsman when a push target changes
	EventPushLifecycle EventType = "push-lifecycle"
	// Emitted by Helmsman recording hooks
	EventRecordingLifecycle EventType = "recording-lifecycle"
	// Emitted by Helmsman per-client polling from MistServer /api clients endpoint
	EventClientLifecycle EventType = "client-lifecycle"
	// Emitted by Helmsman node polling from MistServer /prometheus/json endpoint
	EventNodeLifecycle EventType = "node-lifecycle"
	// Emitted when MistServer LIVE_TRACK_LIST triggers
	EventTrackList EventType = "track-list"
	// Emitted by Foghorn (load balancer) decisions
	EventLoadBalancing EventType = "load-balancing"
	// Emitted by MistServer STREAM_BUFFER webhook
	EventStreamBuffer EventType = "stream-buffer"
	// Emitted by MistServer STREAM_END webhook
	EventStreamEnd EventType = "stream-end"
)

// BaseEvent is the normalized envelope for a single analytics event as received
// by Decklog over gRPC and validated before publishing to Kafka.
type BaseEvent struct {
	EventID       string                 `json:"event_id" validate:"required,uuid4"`
	EventType     EventType              `json:"event_type" validate:"required"`
	Timestamp     time.Time              `json:"timestamp" validate:"required"`
	Source        string                 `json:"source" validate:"required"`
	StreamID      *string                `json:"stream_id,omitempty" validate:"omitempty,uuid4"`
	UserID        *string                `json:"user_id,omitempty" validate:"omitempty,uuid4"`
	PlaybackID    *string                `json:"playback_id,omitempty"`
	InternalName  *string                `json:"internal_name,omitempty"`
	Region        string                 `json:"region" validate:"required"`
	NodeURL       *string                `json:"node_url,omitempty" validate:"omitempty,url"`
	Data          map[string]interface{} `json:"data" validate:"required"`
	SchemaVersion string                 `json:"schema_version" validate:"required"`
}

// BatchedEvents matches Decklog's gRPC batch envelope. All contained events are
// validated syntactically and then semantically per type below.
type BatchedEvents struct {
	BatchID   string      `json:"batch_id" validate:"required,uuid4"`
	Source    string      `json:"source" validate:"required"`
	Timestamp time.Time   `json:"timestamp" validate:"required"`
	Events    []BaseEvent `json:"events" validate:"required,min=1,max=100,dive"`
}

// EventValidator performs structural and event-type-specific validation for
// Decklog before events are accepted and published to Kafka.
type EventValidator struct {
	validator *validator.Validate
}

// NewEventValidator constructs an EventValidator with standard struct validation.
func NewEventValidator() *EventValidator {
	return &EventValidator{
		validator: validator.New(),
	}
}

// ValidateBatch checks the batch envelope and applies per-type validation to
// each event, failing fast on the first invalid entry.
func (v *EventValidator) ValidateBatch(batch *BatchedEvents) error {
	if err := v.validator.Struct(batch); err != nil {
		return fmt.Errorf("batch validation failed: %w", err)
	}

	for i, event := range batch.Events {
		if err := v.validateEventData(event); err != nil {
			return fmt.Errorf("event %d validation failed: %w", i, err)
		}
	}

	return nil
}

// validateEventData dispatches to the specific validator per event type.
func (v *EventValidator) validateEventData(event BaseEvent) error {
	switch event.EventType {
	case EventStreamIngest:
		return v.validateStreamIngestEvent(event)
	case EventStreamView:
		return v.validateStreamViewEvent(event)
	case EventStreamLifecycle:
		return v.validateStreamLifecycleEvent(event)
	case EventUserConnection:
		return v.validateUserConnectionEvent(event)
	case EventClientLifecycle:
		return v.validateClientLifecycleEvent(event)
	case EventPushLifecycle:
		return v.validatePushLifecycleEvent(event)
	case EventRecordingLifecycle:
		return v.validateRecordingLifecycleEvent(event)
	case EventNodeLifecycle:
		return v.validateNodeLifecycleEvent(event)
	case EventTrackList:
		return v.validateTrackListEvent(event)
	case EventStreamBuffer:
		return v.validateStreamBufferEvent(event)
	case EventStreamEnd:
		return v.validateStreamEndEvent(event)
	case EventLoadBalancing:
		return v.validateLoadBalancingEvent(event)
	default:
		return fmt.Errorf("unknown event type: %s", event.EventType)
	}
}

// validateStreamIngestEvent validates MistServer PUSH_REWRITE → Helmsman webhook
// events. Security-sensitive: requires stream_key (used to resolve internal_name).
func (v *EventValidator) validateStreamIngestEvent(event BaseEvent) error {
	if event.InternalName == nil {
		return fmt.Errorf("internal_name is required for stream-ingest events")
	}
	if event.UserID == nil {
		return fmt.Errorf("user_id is required for stream-ingest events")
	}

	requiredFields := []string{"stream_key", "protocol", "push_url"}
	for _, field := range requiredFields {
		if _, exists := event.Data[field]; !exists {
			return fmt.Errorf("missing required field in data: %s", field)
		}
	}

	return nil
}

// validateStreamViewEvent validates MistServer DEFAULT_STREAM → Helmsman webhook
// events that map playback_id to internal_name for viewer access control.
func (v *EventValidator) validateStreamViewEvent(event BaseEvent) error {
	if event.PlaybackID == nil {
		return fmt.Errorf("playback_id is required for stream-view events")
	}
	if event.InternalName == nil {
		return fmt.Errorf("internal_name is required for stream-view events")
	}

	return nil
}

// validateStreamLifecycleEvent validates Helmsman lifecycle transitions.
// Two origins:
// - Webhooks (STREAM_BUFFER, STREAM_END) → require subtype in data.event_type
// - Monitor (active streams snapshot) → no subtype required; accept status
func (v *EventValidator) validateStreamLifecycleEvent(event BaseEvent) error {
	if event.InternalName == nil {
		return fmt.Errorf("internal_name is required for stream-lifecycle events")
	}

	// If explicit subtype provided, validate per subtype rules (webhooks only)
	if etRaw, ok := event.Data["event_type"]; ok {
		et, _ := etRaw.(string)
		switch et {
		case "stream-buffer":
			bs, ok := event.Data["buffer_state"].(string)
			if !ok || bs == "" {
				return fmt.Errorf("buffer_state is required for stream-buffer events")
			}
			valid := map[string]bool{"FULL": true, "EMPTY": true, "DRY": true, "RECOVER": true}
			if !valid[bs] {
				return fmt.Errorf("invalid buffer_state: %s", bs)
			}
			return nil
		case "stream-end":
			return nil
		default:
			return fmt.Errorf("unknown stream-lifecycle subtype: %s", et)
		}
	}

	// No subtype → monitor emission: require status only
	if _, ok := event.Data["status"]; !ok {
		return fmt.Errorf("status is required for monitor stream-lifecycle events")
	}
	return nil
}

// validateUserConnectionEvent validates viewer connect/disconnect events emitted
// by Helmsman with stream context (USER_NEW / USER_END).
func (v *EventValidator) validateUserConnectionEvent(event BaseEvent) error {
	if event.InternalName == nil {
		return fmt.Errorf("internal_name is required for user-connection events")
	}

	action, exists := event.Data["action"]
	if !exists {
		return fmt.Errorf("missing required field in data: action")
	}

	actionStr, ok := action.(string)
	if !ok {
		return fmt.Errorf("action field must be a string")
	}

	if actionStr != "connect" && actionStr != "disconnect" {
		return fmt.Errorf("invalid action: %s", actionStr)
	}

	return nil
}

// validateClientLifecycleEvent validates per-client metrics collected by Helmsman
// from MistServer /api clients endpoint. Contains bandwidth and connection data.
func (v *EventValidator) validateClientLifecycleEvent(event BaseEvent) error {
	if event.InternalName == nil {
		return fmt.Errorf("internal_name is required for client-lifecycle events")
	}
	required := []string{"bandwidth_in", "bandwidth_out"}
	for _, f := range required {
		if _, ok := event.Data[f]; !ok {
			return fmt.Errorf("missing required metric in data: %s", f)
		}
	}
	return nil
}

// validatePushLifecycleEvent validates push start/end/status updates.
func (v *EventValidator) validatePushLifecycleEvent(event BaseEvent) error {
	if event.InternalName == nil {
		return fmt.Errorf("internal_name is required for push-lifecycle events")
	}
	return nil
}

// validateRecordingLifecycleEvent validates recording start/end updates.
func (v *EventValidator) validateRecordingLifecycleEvent(event BaseEvent) error {
	if event.InternalName == nil {
		return fmt.Errorf("internal_name is required for recording-lifecycle events")
	}
	return nil
}

// validateNodeLifecycleEvent validates node health metrics collected by Helmsman
// from MistServer /prometheus/json endpoint. Contains CPU, RAM, bandwidth data.
func (v *EventValidator) validateNodeLifecycleEvent(event BaseEvent) error {
	required := []string{"node_id", "is_healthy"}
	for _, field := range required {
		if _, exists := event.Data[field]; !exists {
			return fmt.Errorf("missing required field in data: %s", field)
		}
	}
	return nil
}

// validateTrackListEvent validates track list updates from MistServer LIVE_TRACK_LIST
func (v *EventValidator) validateTrackListEvent(event BaseEvent) error {
	if event.InternalName == nil {
		return fmt.Errorf("internal_name is required for track-list events")
	}
	if _, ok := event.Data["track_list"]; !ok {
		return fmt.Errorf("track_list is required for track-list events")
	}
	return nil
}

// validateLoadBalancingEvent validates Foghorn routing decisions for a stream.
func (v *EventValidator) validateLoadBalancingEvent(event BaseEvent) error {
	if event.InternalName == nil {
		return fmt.Errorf("internal_name is required for load-balancing events")
	}

	requiredFields := []string{"status", "selected_node"}
	for _, field := range requiredFields {
		if _, exists := event.Data[field]; !exists {
			return fmt.Errorf("missing required field in data: %s", field)
		}
	}

	status, exists := event.Data["status"]
	if !exists {
		return fmt.Errorf("missing required field in data: status")
	}
	statusStr, ok := status.(string)
	if !ok {
		return fmt.Errorf("status field must be a string")
	}

	validStatuses := []string{"success", "redirect", "failed"}
	for _, validStatus := range validStatuses {
		if statusStr == validStatus {
			return nil
		}
	}

	return fmt.Errorf("invalid status: %s", statusStr)
}

// validateStreamBufferEvent validates STREAM_BUFFER webhook events
func (v *EventValidator) validateStreamBufferEvent(event BaseEvent) error {
	if event.InternalName == nil {
		return fmt.Errorf("internal_name is required for stream-buffer events")
	}
	bs, ok := event.Data["buffer_state"].(string)
	if !ok || bs == "" {
		return fmt.Errorf("buffer_state is required for stream-buffer events")
	}
	valid := map[string]bool{"FULL": true, "EMPTY": true, "DRY": true, "RECOVER": true}
	if !valid[bs] {
		return fmt.Errorf("invalid buffer_state: %s", bs)
	}
	return nil
}

// validateStreamEndEvent validates STREAM_END webhook events
func (v *EventValidator) validateStreamEndEvent(event BaseEvent) error {
	if event.InternalName == nil {
		return fmt.Errorf("internal_name is required for stream-end events")
	}
	return nil
}

// EnrichEvent fills commonly missing metadata (timestamp, schema version) and
// merges provided enrichment fields into the event data when absent.
func (v *EventValidator) EnrichEvent(event *BaseEvent, enrichmentData map[string]interface{}) {
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}

	for key, value := range enrichmentData {
		if _, exists := event.Data[key]; !exists {
			event.Data[key] = value
		}
	}

	if event.SchemaVersion == "" {
		event.SchemaVersion = "1.0"
	}
}

// ToJSON serializes the event for Kafka production.
func (e *BaseEvent) ToJSON() ([]byte, error) {
	return json.Marshal(e)
}
