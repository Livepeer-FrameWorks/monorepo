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
	if payloadHeaders != nil {
		tenantID = payloadHeaders["tenant_id"]
	}
	if tenantID == "" {
		if extractedTenant, ok := extractTenantIDFromJSON(msg.Value); ok {
			tenantID = extractedTenant
			payloadHeaders = ensureHeader(payloadHeaders, "tenant_id", extractedTenant)
		}
	}

	payload := DLQPayload{
		Topic:       msg.Topic,
		Partition:   msg.Partition,
		Offset:      msg.Offset,
		Timestamp:   msg.Timestamp,
		TenantID:    tenantID,
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

func extractTenantIDFromJSON(value []byte) (string, bool) {
	if len(value) == 0 {
		return "", false
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(value, &payload); err != nil {
		return "", false
	}
	if rawTenant, ok := payload["tenant_id"]; ok {
		if tenantID, ok := rawTenant.(string); ok && tenantID != "" {
			return tenantID, true
		}
	}
	return "", false
}
