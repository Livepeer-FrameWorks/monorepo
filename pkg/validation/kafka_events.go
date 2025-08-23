package validation

import (
	"encoding/json"
	"time"
)

// KafkaEvent represents the canonical event structure for Kafka messages
// This is the single source of truth for event schema shared between producers and consumers
type KafkaEvent struct {
	ID            string    `json:"id"`
	Type          string    `json:"type"`
	EventID       string    `json:"event_id"`   // Duplicate for downstream compatibility
	EventType     string    `json:"event_type"` // Duplicate for downstream compatibility
	Timestamp     time.Time `json:"timestamp"`
	Source        string    `json:"source"`
	SchemaVersion string    `json:"schema_version"`
	Data          EventData `json:"data"`
}

// EventData contains the typed payload for each event type
// Only one field will be populated based on the event type
type EventData struct {
	// Stream lifecycle events
	StreamIngest    *StreamIngestPayload    `json:"stream_ingest,omitempty"`
	StreamView      *StreamViewPayload      `json:"stream_view,omitempty"`
	StreamLifecycle *StreamLifecyclePayload `json:"stream_lifecycle,omitempty"`

	// User and client events
	UserConnection  *UserConnectionPayload  `json:"user_connection,omitempty"`
	ClientLifecycle *ClientLifecyclePayload `json:"client_lifecycle,omitempty"`

	// Content events
	TrackList     *TrackListPayload     `json:"track_list,omitempty"`
	Recording     *RecordingPayload     `json:"recording,omitempty"`
	PushLifecycle *PushLifecyclePayload `json:"push_lifecycle,omitempty"`

	// Infrastructure events
	NodeLifecycle      *NodeLifecyclePayload      `json:"node_lifecycle,omitempty"`
	BandwidthThreshold *BandwidthThresholdPayload `json:"bandwidth_threshold,omitempty"`
	LoadBalancing      *LoadBalancingPayload      `json:"load_balancing,omitempty"`
}

// LoadBalancingPayload represents load balancer routing decisions from Foghorn
type LoadBalancingPayload struct {
	StreamID          string  `json:"stream_id,omitempty"`
	TenantID          string  `json:"tenant_id"`
	SelectedNode      string  `json:"selected_node"`
	SelectedNodeID    string  `json:"selected_node_id,omitempty"`
	Latitude          float64 `json:"latitude"`
	Longitude         float64 `json:"longitude"`
	Status            string  `json:"status"`
	Details           string  `json:"details"`
	Score             uint64  `json:"score"`
	ClientIP          string  `json:"client_ip"`
	ClientCountry     string  `json:"client_country"`
	NodeLatitude      float64 `json:"node_latitude,omitempty"`
	NodeLongitude     float64 `json:"node_longitude,omitempty"`
	NodeName          string  `json:"node_name,omitempty"`
	RoutingDistanceKm float64 `json:"routing_distance_km,omitempty"`
}

// KafkaEventBatch represents a batch of events for Kafka publishing
type KafkaEventBatch struct {
	BatchID  string       `json:"batch_id"`
	Source   string       `json:"source"`
	TenantID string       `json:"tenant_id"`
	Events   []KafkaEvent `json:"events"`
}

// ConvertBaseEventToKafka converts a validation.BaseEvent to KafkaEvent
func ConvertBaseEventToKafka(baseEvent *BaseEvent) *KafkaEvent {
	event := &KafkaEvent{
		ID:            baseEvent.EventID,
		Type:          string(baseEvent.EventType),
		EventID:       baseEvent.EventID,
		EventType:     string(baseEvent.EventType),
		Timestamp:     baseEvent.Timestamp,
		Source:        baseEvent.Source,
		SchemaVersion: baseEvent.SchemaVersion,
	}

	// Populate typed data based on event type
	switch baseEvent.EventType {
	case EventStreamIngest:
		event.Data.StreamIngest = baseEvent.StreamIngest
	case EventStreamView:
		event.Data.StreamView = baseEvent.StreamView
	case EventStreamLifecycle:
		event.Data.StreamLifecycle = baseEvent.StreamLifecycle
	case EventStreamBuffer:
		// StreamBuffer events use StreamLifecyclePayload
		event.Data.StreamLifecycle = baseEvent.StreamLifecycle
	case EventStreamEnd:
		// StreamEnd events use StreamLifecyclePayload
		event.Data.StreamLifecycle = baseEvent.StreamLifecycle
	case EventUserConnection:
		event.Data.UserConnection = baseEvent.UserConnection
	case EventClientLifecycle:
		event.Data.ClientLifecycle = baseEvent.ClientLifecycle
	case EventTrackList:
		event.Data.TrackList = baseEvent.TrackList
	case EventRecordingLifecycle:
		event.Data.Recording = baseEvent.Recording
	case EventPushLifecycle:
		event.Data.PushLifecycle = baseEvent.PushLifecycle
	case EventNodeLifecycle:
		event.Data.NodeLifecycle = baseEvent.NodeLifecycle
	case EventBandwidthThreshold:
		event.Data.BandwidthThreshold = baseEvent.BandwidthThreshold
	case EventLoadBalancing:
		// LoadBalancing events need to be constructed from legacy data
		// This will be populated by the caller when converting from protobuf
	}

	return event
}

// ToJSON marshals the KafkaEvent to JSON bytes for Kafka publishing
func (e *KafkaEvent) ToJSON() ([]byte, error) {
	return json.Marshal(e)
}

// ConvertKafkaEventBatchToBaseEventBatch converts KafkaEventBatch to BatchedEvents for validation
func ConvertKafkaEventBatchToBaseEventBatch(kafkaBatch *KafkaEventBatch) *BatchedEvents {
	batch := &BatchedEvents{
		BatchID:   kafkaBatch.BatchID,
		Source:    kafkaBatch.Source,
		Timestamp: time.Now().UTC(),
		Events:    make([]BaseEvent, len(kafkaBatch.Events)),
	}

	for i, kafkaEvent := range kafkaBatch.Events {
		baseEvent := BaseEvent{
			EventID:       kafkaEvent.EventID,
			EventType:     EventType(kafkaEvent.EventType),
			Timestamp:     kafkaEvent.Timestamp,
			Source:        kafkaEvent.Source,
			SchemaVersion: kafkaEvent.SchemaVersion,
		}

		// Convert typed data back to BaseEvent fields
		switch EventType(kafkaEvent.EventType) {
		case EventStreamIngest:
			baseEvent.StreamIngest = kafkaEvent.Data.StreamIngest
			if kafkaEvent.Data.StreamIngest != nil {
				baseEvent.InternalName = &kafkaEvent.Data.StreamIngest.InternalName
			}
		case EventStreamView:
			baseEvent.StreamView = kafkaEvent.Data.StreamView
			if kafkaEvent.Data.StreamView != nil {
				baseEvent.InternalName = &kafkaEvent.Data.StreamView.InternalName
				baseEvent.PlaybackID = &kafkaEvent.Data.StreamView.PlaybackID
			}
		case EventStreamLifecycle:
			baseEvent.StreamLifecycle = kafkaEvent.Data.StreamLifecycle
			if kafkaEvent.Data.StreamLifecycle != nil {
				baseEvent.InternalName = &kafkaEvent.Data.StreamLifecycle.InternalName
			}
		case EventStreamBuffer:
			// StreamBuffer events use StreamLifecyclePayload
			baseEvent.StreamLifecycle = kafkaEvent.Data.StreamLifecycle
			if kafkaEvent.Data.StreamLifecycle != nil {
				baseEvent.InternalName = &kafkaEvent.Data.StreamLifecycle.InternalName
			}
		case EventStreamEnd:
			// StreamEnd events use StreamLifecyclePayload
			baseEvent.StreamLifecycle = kafkaEvent.Data.StreamLifecycle
			if kafkaEvent.Data.StreamLifecycle != nil {
				baseEvent.InternalName = &kafkaEvent.Data.StreamLifecycle.InternalName
			}
		case EventUserConnection:
			baseEvent.UserConnection = kafkaEvent.Data.UserConnection
			if kafkaEvent.Data.UserConnection != nil {
				baseEvent.InternalName = &kafkaEvent.Data.UserConnection.InternalName
			}
		case EventClientLifecycle:
			baseEvent.ClientLifecycle = kafkaEvent.Data.ClientLifecycle
			if kafkaEvent.Data.ClientLifecycle != nil {
				baseEvent.InternalName = &kafkaEvent.Data.ClientLifecycle.InternalName
			}
		case EventTrackList:
			baseEvent.TrackList = kafkaEvent.Data.TrackList
			if kafkaEvent.Data.TrackList != nil {
				baseEvent.InternalName = &kafkaEvent.Data.TrackList.InternalName
			}
		case EventRecordingLifecycle:
			baseEvent.Recording = kafkaEvent.Data.Recording
			if kafkaEvent.Data.Recording != nil {
				baseEvent.InternalName = &kafkaEvent.Data.Recording.InternalName
			}
		case EventPushLifecycle:
			baseEvent.PushLifecycle = kafkaEvent.Data.PushLifecycle
			if kafkaEvent.Data.PushLifecycle != nil {
				baseEvent.InternalName = &kafkaEvent.Data.PushLifecycle.InternalName
			}
		case EventNodeLifecycle:
			baseEvent.NodeLifecycle = kafkaEvent.Data.NodeLifecycle
		case EventBandwidthThreshold:
			baseEvent.BandwidthThreshold = kafkaEvent.Data.BandwidthThreshold
			if kafkaEvent.Data.BandwidthThreshold != nil {
				baseEvent.InternalName = &kafkaEvent.Data.BandwidthThreshold.InternalName
			}
		}

		batch.Events[i] = baseEvent
	}

	return batch
}
