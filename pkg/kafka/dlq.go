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
	KeyBase64   string            `json:"key_base64,omitempty"`
	ValueBase64 string            `json:"value_base64"`
	Headers     map[string]string `json:"headers,omitempty"`
	Error       string            `json:"error"`
	Consumer    string            `json:"consumer"`
}

// EncodeDLQMessage serializes a Kafka message into a DLQ-safe payload.
func EncodeDLQMessage(msg Message, err error, consumer string) ([]byte, error) {
	payload := DLQPayload{
		Topic:       msg.Topic,
		Partition:   msg.Partition,
		Offset:      msg.Offset,
		Timestamp:   msg.Timestamp,
		ValueBase64: base64.StdEncoding.EncodeToString(msg.Value),
		Headers:     msg.Headers,
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
