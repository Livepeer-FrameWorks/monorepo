package kafka

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

// DLQPayload captures enough context to replay or inspect a failed Kafka message.
type DLQPayload struct {
	Topic       string            `json:"topic"`
	Partition   int32             `json:"partition"`
	Offset      int64             `json:"offset"`
	Timestamp   time.Time         `json:"timestamp"`
	TenantID    string            `json:"tenant_id,omitempty"`
	EventID     string            `json:"event_id,omitempty"`
	EventType   string            `json:"event_type,omitempty"`
	KeyBase64   string            `json:"key_base64,omitempty"`
	ValueBase64 string            `json:"value_base64"`
	Headers     map[string]string `json:"headers,omitempty"`
	Error       string            `json:"error"`
	Consumer    string            `json:"consumer"`
}

// EncodeDLQMessage serializes a Kafka message into a DLQ-safe payload.
func EncodeDLQMessage(msg Message, err error, consumer string) ([]byte, error) {
	payloadHeaders := msg.Headers
	tenantID := ""
	eventID := ""
	eventType := ""
	if payloadHeaders != nil {
		tenantID = payloadHeaders["tenant_id"]
		eventID = payloadHeaders["event_id"]
		eventType = payloadHeaders["event_type"]
	}

	if tenantID == "" || eventID == "" || eventType == "" {
		metadata, ok := extractMessageMetadataFromJSON(msg.Value)
		if ok {
			if tenantID == "" {
				tenantID = metadata.TenantID
				payloadHeaders = ensureHeader(payloadHeaders, "tenant_id", metadata.TenantID)
			}
			if eventID == "" {
				eventID = metadata.EventID
				payloadHeaders = ensureHeader(payloadHeaders, "event_id", metadata.EventID)
			}
			if eventType == "" {
				eventType = metadata.EventType
				payloadHeaders = ensureHeader(payloadHeaders, "event_type", metadata.EventType)
			}
		}
	}

	payload := DLQPayload{
		Topic:       msg.Topic,
		Partition:   msg.Partition,
		Offset:      msg.Offset,
		Timestamp:   msg.Timestamp,
		TenantID:    tenantID,
		EventID:     eventID,
		EventType:   eventType,
		ValueBase64: base64.StdEncoding.EncodeToString(msg.Value),
		Headers:     payloadHeaders,
		Consumer:    consumer,
	}

	if len(msg.Key) > 0 {
		payload.KeyBase64 = base64.StdEncoding.EncodeToString(msg.Key)
	}

	if err != nil {
		payload.Error = err.Error()
	}

	b, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		return nil, fmt.Errorf("marshal dlq payload: %w", marshalErr)
	}

	return b, nil
}

func ensureHeader(headers map[string]string, key string, value string) map[string]string {
	if value == "" {
		return headers
	}
	if headers == nil {
		return map[string]string{key: value}
	}
	if existing, ok := headers[key]; ok && existing != "" {
		return headers
	}
	clone := make(map[string]string, len(headers)+1)
	for k, v := range headers {
		clone[k] = v
	}
	clone[key] = value
	return clone
}

type messageMetadata struct {
	TenantID  string
	EventID   string
	EventType string
}

func extractMessageMetadataFromJSON(value []byte) (messageMetadata, bool) {
	if len(value) == 0 {
		return messageMetadata{}, false
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(value, &payload); err != nil {
		return messageMetadata{}, false
	}

	meta := messageMetadata{}
	if rawTenant, ok := payload["tenant_id"]; ok {
		if tenantID, ok := rawTenant.(string); ok {
			meta.TenantID = tenantID
		}
	}
	if rawEventID, ok := payload["event_id"]; ok {
		if id, ok := rawEventID.(string); ok {
			meta.EventID = id
		}
	}
	if rawEventType, ok := payload["event_type"]; ok {
		if eventType, ok := rawEventType.(string); ok {
			meta.EventType = eventType
		}
	}

	return meta, meta.TenantID != "" || meta.EventID != "" || meta.EventType != ""
}
