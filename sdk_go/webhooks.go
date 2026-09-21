package frameworks

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	standardwebhooks "github.com/standard-webhooks/standard-webhooks/libraries/go"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// webhookTolerance is how far webhook-timestamp may be from the receiver's
// clock, as in the Standard Webhooks libraries.
const webhookTolerance = 5 * time.Minute

// WebhookReceiver verifies and parses the webhook requests of one endpoint
// secret (whsec_...).
type WebhookReceiver struct {
	wh  *standardwebhooks.Webhook
	now func() time.Time
}

// NewWebhookReceiver returns a receiver for one endpoint secret.
func NewWebhookReceiver(secret string) (*WebhookReceiver, error) {
	wh, err := standardwebhooks.NewWebhook(secret)
	if err != nil {
		return nil, fmt.Errorf("frameworks: webhook secret: %w", err)
	}
	return &WebhookReceiver{wh: wh, now: time.Now}, nil
}

// Verify returns a *WebhookVerificationError unless headers sign payload
// with this secret within five minutes of now. Pass the raw request body:
// parsing and re-serializing JSON changes the bytes that were signed.
func (r *WebhookReceiver) Verify(payload []byte, headers http.Header) error {
	// The signature check is the official Standard Webhooks library's; only
	// the timestamp window is checked here, against the receiver's clock.
	if err := r.wh.VerifyIgnoringTimestamp(payload, headers); err != nil {
		return &WebhookVerificationError{ErrorInfo{Message: err.Error(), Cause: err}}
	}
	ts, err := strconv.ParseInt(headers.Get("webhook-timestamp"), 10, 64)
	if err != nil {
		return &WebhookVerificationError{ErrorInfo{Message: "invalid webhook-timestamp", Cause: err}}
	}
	now := r.now()
	sent := time.Unix(ts, 0)
	if now.Sub(sent) > webhookTolerance {
		return &WebhookVerificationError{ErrorInfo{Message: "webhook timestamp too old", Cause: standardwebhooks.ErrMessageTooOld}}
	}
	if sent.After(now.Add(webhookTolerance)) {
		return &WebhookVerificationError{ErrorInfo{Message: "webhook timestamp too new", Cause: standardwebhooks.ErrMessageTooNew}}
	}
	return nil
}

// Receive verifies payload and parses it.
func (r *WebhookReceiver) Receive(payload []byte, headers http.Header) (*WebhookEvent, error) {
	if err := r.Verify(payload, headers); err != nil {
		return nil, err
	}
	return ParseWebhookEvent(payload)
}

// WebhookEvent is one webhook body. When Known is true, Data holds the
// payload decoded into its generated message (e.g. *publicv1.ClipReady);
// otherwise the SDK does not know the type and RawData holds the payload as
// received.
type WebhookEvent struct {
	// ID equals the webhook-id header; deduplicate on it.
	ID string
	// Type is the event type, e.g. clip.ready.
	Type string
	// APIVersion is the payload version the endpoint pins, "v1".
	APIVersion string
	// CreatedAt is when the change committed, RFC 3339.
	CreatedAt string
	Known     bool
	Data      proto.Message
	RawData   json.RawMessage
}

// ParseWebhookEvent parses a webhook body without verifying it. An event
// type this SDK does not know is returned with Known false, never as an
// error, so a new event type never breaks an older SDK.
func ParseWebhookEvent(payload []byte) (*WebhookEvent, error) {
	var envelope struct {
		ID         string          `json:"id"`
		Type       string          `json:"type"`
		APIVersion string          `json:"api_version"`
		CreatedAt  string          `json:"created_at"`
		Data       json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, fmt.Errorf("frameworks: webhook body is not an event object: %w", err)
	}
	ev := &WebhookEvent{ID: envelope.ID, Type: envelope.Type, APIVersion: envelope.APIVersion, CreatedAt: envelope.CreatedAt, RawData: envelope.Data}
	newMessage, ok := publicEventMessages[envelope.Type]
	if !ok {
		return ev, nil
	}
	data := envelope.Data
	if len(data) == 0 || string(data) == "null" {
		data = []byte("{}")
	}
	msg := newMessage()
	// Fields and enum values a newer server adds are skipped.
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(data, msg); err != nil {
		return nil, fmt.Errorf("frameworks: decoding %s payload: %w", envelope.Type, err)
	}
	ev.Known = true
	ev.Data = msg
	return ev, nil
}
