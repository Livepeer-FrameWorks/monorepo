package delivery

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"frameworks/api_webhooks/internal/ledger"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"

	"google.golang.org/protobuf/encoding/protojson"
)

// body is the webhook request body. Field order is fixed so the signed bytes
// of the same event and version are identical on every attempt.
type body struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	APIVersion string          `json:"api_version"`
	CreatedAt  string          `json:"created_at"`
	Data       json.RawMessage `json:"data"`
}

// dataOptions renders payloads with the proto JSON field names the public
// catalog documents, and emits unset fields so every payload of a type has
// the same keys.
var dataOptions = protojson.MarshalOptions{EmitUnpopulated: true}

// ErrNoRenderer means this binary has no renderer for the endpoint's API
// version.
var ErrNoRenderer = errors.New("api version has no renderer")

// RenderFailureRetryable reports whether a Render error can clear on another
// replica: the event type or the API version is newer than this binary, as
// during a rolling upgrade. Every other render failure is a property of the
// stored event and fails the same way on every attempt.
func RenderFailureRetryable(err error) bool {
	return errors.Is(err, events.ErrUnknownType) || errors.Is(err, ErrNoRenderer)
}

// Render returns the request body of a stored event for the endpoint's pinned
// API version: {id, type, api_version, created_at, data}, where data is the
// stored public message rendered with protojson. There is no mapping step; a
// breaking payload change ships as a new public package the endpoint pins.
func Render(ev ledger.StoredEvent, apiVersion string) ([]byte, error) {
	if apiVersion != ledger.APIVersionV1 {
		return nil, fmt.Errorf("%w: %q", ErrNoRenderer, apiVersion)
	}
	spec, msg, err := events.Decode(ev.Type, ev.Payload)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", ev.Type, err)
	}
	if !spec.Public() {
		return nil, fmt.Errorf("%s is not a public event type", ev.Type)
	}
	if string(spec.MessageName) != ev.SchemaName {
		return nil, fmt.Errorf("%s is stored as %s, registered as %s", ev.Type, ev.SchemaName, spec.MessageName)
	}
	data, err := dataOptions.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("render %s: %w", ev.Type, err)
	}
	return marshalBody(ev.ID, ev.Type, apiVersion, ev.OccurredAt, data)
}

// RenderTest returns the body of a test delivery. Its data names the endpoint.
func RenderTest(deliveryID, endpointID, apiVersion string, at time.Time) ([]byte, error) {
	data, err := json.Marshal(map[string]string{"endpointId": endpointID})
	if err != nil {
		return nil, err
	}
	return marshalBody(deliveryID, ledger.TestEventType, apiVersion, at, data)
}

func marshalBody(id, eventType, apiVersion string, at time.Time, data []byte) ([]byte, error) {
	// protojson deliberately varies its whitespace; json.Marshal compacts a
	// RawMessage, so the body bytes do not depend on it.
	return json.Marshal(body{
		ID:         id,
		Type:       eventType,
		APIVersion: apiVersion,
		CreatedAt:  at.UTC().Format(time.RFC3339Nano),
		Data:       json.RawMessage(data),
	})
}
