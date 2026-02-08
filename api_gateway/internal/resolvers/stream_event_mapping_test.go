package resolvers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/middleware"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"
)

func TestProtoPayloadJSONRedactsInternalFields(t *testing.T) {
	msg := &pb.StreamLifecycleUpdate{
		NodeId:       "node-1",
		InternalName: "secret-name",
	}

	payload := protoPayloadJSON(msg)
	if payload == nil {
		t.Fatal("expected payload, got nil")
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(*payload), &parsed); err != nil {
		t.Fatalf("expected JSON payload, got error: %v", err)
	}

	if _, exists := parsed["internalName"]; exists {
		t.Fatalf("expected internalName to be redacted, got %v", parsed["internalName"])
	}
	if got, ok := parsed["nodeId"].(string); !ok || got != "node-1" {
		t.Fatalf("expected nodeId to be preserved, got %v", parsed["nodeId"])
	}
}

func TestRedactInternalJSONMalformedPayload(t *testing.T) {
	malformed := "{not-json"
	if got := redactInternalJSON(malformed); got != malformed {
		t.Fatalf("expected malformed payload to be returned as-is, got %q", got)
	}
}

func TestMapPeriscopeStreamEventMalformedDetails(t *testing.T) {
	badDetails := "{not-json"
	event := &pb.StreamEvent{
		EventId:   "evt-1",
		StreamId:  "stream-1",
		EventType: "STREAM_LIFECYCLE",
		EventData: badDetails,
	}

	result := mapPeriscopeStreamEvent(event)
	if result == nil {
		t.Fatal("expected mapped event, got nil")
	}
	if result.Details == nil {
		t.Fatal("expected details to be set")
	}
	if *result.Details != badDetails {
		t.Fatalf("expected details to preserve malformed payload, got %q", *result.Details)
	}
}

func TestMapSignalmanStreamEventNilData(t *testing.T) {
	event := &pb.SignalmanEvent{EventType: pb.EventType_EVENT_TYPE_STREAM_END}
	if got := mapSignalmanStreamEvent(event); got != nil {
		t.Fatalf("expected nil when event data missing, got %#v", got)
	}

	event = &pb.SignalmanEvent{
		EventType: pb.EventType_EVENT_TYPE_STREAM_LIFECYCLE_UPDATE,
		Data:      &pb.EventData{},
	}
	if got := mapSignalmanStreamEvent(event); got != nil {
		t.Fatalf("expected nil when payload missing, got %#v", got)
	}
}

func TestCanViewSensitiveTenantDataContexts(t *testing.T) {
	resolver := &Resolver{}

	tests := []struct {
		name string
		ctx  context.Context
		want bool
	}{
		{
			name: "demo mode allows",
			ctx:  context.WithValue(context.Background(), ctxkeys.KeyDemoMode, true),
			want: true,
		},
		{
			name: "service token allows",
			ctx:  context.WithValue(context.Background(), ctxkeys.KeyServiceToken, "service-token"),
			want: true,
		},
		{
			name: "admin role allows",
			ctx: context.WithValue(
				context.Background(),
				ctxkeys.KeyUser,
				&middleware.UserContext{Role: "admin"},
			),
			want: true,
		},
		{
			name: "non-privileged role denied",
			ctx: context.WithValue(
				context.Background(),
				ctxkeys.KeyUser,
				&middleware.UserContext{Role: "viewer"},
			),
			want: false,
		},
		{
			name: "unauthenticated denied",
			ctx:  context.Background(),
			want: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			if got := resolver.CanViewSensitiveTenantData(tt.ctx); got != tt.want {
				t.Fatalf("expected %t, got %t", tt.want, got)
			}
		})
	}
}

func TestDoUpdateTenantRejectsMalformedSettings(t *testing.T) {
	resolver := &Resolver{Logger: logging.NewLogger()}
	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-1")

	input := model.UpdateTenantInput{Settings: ptrString("{bad-json")}
	_, err := resolver.DoUpdateTenant(ctx, input)
	if err == nil {
		t.Fatal("expected error for invalid settings JSON")
	}
	if !strings.Contains(err.Error(), "invalid settings JSON") {
		t.Fatalf("expected error to mention invalid settings JSON, got %q", err.Error())
	}
}
