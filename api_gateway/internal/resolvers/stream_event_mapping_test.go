package resolvers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	signalmanpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/signalman"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestProtoPayloadJSONRedactsInternalFields(t *testing.T) {
	msg := &ipcpb.StreamLifecycleUpdate{
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
	event := &periscopepb.StreamEvent{
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
	event := &signalmanpb.SignalmanEvent{EventType: signalmanpb.EventType_EVENT_TYPE_STREAM_END}
	if got := mapSignalmanStreamEvent(event); got != nil {
		t.Fatalf("expected nil when event data missing, got %#v", got)
	}

	event = &signalmanpb.SignalmanEvent{
		EventType: signalmanpb.EventType_EVENT_TYPE_STREAM_LIFECYCLE_UPDATE,
		Data:      &signalmanpb.EventData{},
	}
	if got := mapSignalmanStreamEvent(event); got != nil {
		t.Fatalf("expected nil when payload missing, got %#v", got)
	}
}

func TestMapSignalmanStreamEventMappingRules(t *testing.T) {
	// Intent: each Signalman proto event type must project to the correct
	// GraphQL StreamEvent Type/Status, always with Source=LIVE. These are the
	// load-bearing mapping rules; a wrong arm silently mislabels live events.
	ts := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	tspb := timestamppb.New(ts)

	liveStatus := model.StreamStatusLive
	endedStatus := model.StreamStatusEnded

	tests := []struct {
		name       string
		event      *signalmanpb.SignalmanEvent
		wantType   model.StreamEventType
		wantStatus *model.StreamStatus
		wantNodeID *string
	}{
		{
			name: "push rewrite becomes stream start, live",
			event: &signalmanpb.SignalmanEvent{
				EventType: signalmanpb.EventType_EVENT_TYPE_PUSH_REWRITE,
				Timestamp: tspb,
				Data: &signalmanpb.EventData{Payload: &signalmanpb.EventData_PushRewrite{
					PushRewrite: &ipcpb.PushRewriteTrigger{StreamId: ptrString("stream-1"), NodeId: ptrString("node-7")},
				}},
			},
			wantType:   model.StreamEventTypeStreamStart,
			wantStatus: &liveStatus,
			wantNodeID: ptrString("node-7"),
		},
		{
			name: "lifecycle update carries mapped status",
			event: &signalmanpb.SignalmanEvent{
				EventType: signalmanpb.EventType_EVENT_TYPE_STREAM_LIFECYCLE_UPDATE,
				Timestamp: tspb,
				Data: &signalmanpb.EventData{Payload: &signalmanpb.EventData_StreamLifecycle{
					StreamLifecycle: &ipcpb.StreamLifecycleUpdate{StreamId: ptrString("stream-1"), NodeId: "node-7", Status: "live"},
				}},
			},
			wantType:   model.StreamEventTypeStreamLifecycleUpdate,
			wantStatus: &liveStatus,
			wantNodeID: ptrString("node-7"),
		},
		{
			name: "stream end becomes ended",
			event: &signalmanpb.SignalmanEvent{
				EventType: signalmanpb.EventType_EVENT_TYPE_STREAM_END,
				Timestamp: tspb,
				Data: &signalmanpb.EventData{Payload: &signalmanpb.EventData_StreamEnd{
					StreamEnd: &ipcpb.StreamEndTrigger{StreamId: ptrString("stream-1"), NodeId: ptrString("node-7")},
				}},
			},
			wantType:   model.StreamEventTypeStreamEnd,
			wantStatus: &endedStatus,
			wantNodeID: ptrString("node-7"),
		},
		{
			name: "buffer update has no status",
			event: &signalmanpb.SignalmanEvent{
				EventType: signalmanpb.EventType_EVENT_TYPE_STREAM_BUFFER,
				Timestamp: tspb,
				Data: &signalmanpb.EventData{Payload: &signalmanpb.EventData_StreamBuffer{
					StreamBuffer: &ipcpb.StreamBufferTrigger{StreamId: ptrString("stream-1")},
				}},
			},
			wantType:   model.StreamEventTypeBufferUpdate,
			wantStatus: nil,
		},
		{
			name: "track list update",
			event: &signalmanpb.SignalmanEvent{
				EventType: signalmanpb.EventType_EVENT_TYPE_STREAM_TRACK_LIST,
				Timestamp: tspb,
				Data: &signalmanpb.EventData{Payload: &signalmanpb.EventData_TrackList{
					TrackList: &ipcpb.StreamTrackListTrigger{StreamId: ptrString("stream-1")},
				}},
			},
			wantType:   model.StreamEventTypeTrackListUpdate,
			wantStatus: nil,
		},
		{
			name: "play rewrite",
			event: &signalmanpb.SignalmanEvent{
				EventType: signalmanpb.EventType_EVENT_TYPE_PLAY_REWRITE,
				Timestamp: tspb,
				Data: &signalmanpb.EventData{Payload: &signalmanpb.EventData_PlayRewrite{
					PlayRewrite: &ipcpb.ViewerResolveTrigger{StreamId: ptrString("stream-1")},
				}},
			},
			wantType:   model.StreamEventTypePlayRewrite,
			wantStatus: nil,
		},
		{
			name: "stream source",
			event: &signalmanpb.SignalmanEvent{
				EventType: signalmanpb.EventType_EVENT_TYPE_STREAM_SOURCE,
				Timestamp: tspb,
				Data: &signalmanpb.EventData{Payload: &signalmanpb.EventData_StreamSource{
					StreamSource: &ipcpb.StreamSourceTrigger{StreamId: ptrString("stream-1")},
				}},
			},
			wantType:   model.StreamEventTypeStreamSource,
			wantStatus: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapSignalmanStreamEvent(tt.event)
			if got == nil {
				t.Fatalf("expected mapped event, got nil")
			}
			if got.Type != tt.wantType {
				t.Fatalf("Type = %q, want %q", got.Type, tt.wantType)
			}
			if got.Source != model.StreamEventSourceLive {
				t.Fatalf("Source = %q, want LIVE", got.Source)
			}
			if got.StreamId != "stream-1" {
				t.Fatalf("StreamId = %q, want stream-1", got.StreamId)
			}
			if !got.Timestamp.Equal(ts) {
				t.Fatalf("Timestamp = %v, want %v", got.Timestamp, ts)
			}
			if got.EventId == "" {
				t.Fatal("EventId must be set")
			}
			switch {
			case tt.wantStatus == nil && got.Status != nil:
				t.Fatalf("Status = %v, want nil", *got.Status)
			case tt.wantStatus != nil && got.Status == nil:
				t.Fatalf("Status = nil, want %v", *tt.wantStatus)
			case tt.wantStatus != nil && *got.Status != *tt.wantStatus:
				t.Fatalf("Status = %v, want %v", *got.Status, *tt.wantStatus)
			}
			switch {
			case tt.wantNodeID == nil && got.NodeId != nil:
				t.Fatalf("NodeId = %v, want nil", *got.NodeId)
			case tt.wantNodeID != nil && (got.NodeId == nil || *got.NodeId != *tt.wantNodeID):
				t.Fatalf("NodeId = %v, want %v", got.NodeId, *tt.wantNodeID)
			}
		})
	}
}

func TestMapStreamStatus(t *testing.T) {
	// Intent: empty status means "no status" (nil), known values map through,
	// and crucially any UNKNOWN value falls back to OFFLINE — not nil — so a
	// garbled upstream status can never read as "no change".
	offline := model.StreamStatusOffline
	tests := []struct {
		in   string
		want *model.StreamStatus
	}{
		{"", nil},
		{"live", ptrStatus(model.StreamStatusLive)},
		{"LIVE", ptrStatus(model.StreamStatusLive)},
		{"recording", ptrStatus(model.StreamStatusRecording)},
		{"ended", ptrStatus(model.StreamStatusEnded)},
		{"offline", ptrStatus(model.StreamStatusOffline)},
		{"something-unexpected", &offline},
	}
	for _, tt := range tests {
		got := mapStreamStatus(tt.in)
		switch {
		case tt.want == nil && got != nil:
			t.Fatalf("mapStreamStatus(%q) = %v, want nil", tt.in, *got)
		case tt.want != nil && got == nil:
			t.Fatalf("mapStreamStatus(%q) = nil, want %v", tt.in, *tt.want)
		case tt.want != nil && *got != *tt.want:
			t.Fatalf("mapStreamStatus(%q) = %v, want %v", tt.in, *got, *tt.want)
		}
	}
}

func TestMapStreamEventType(t *testing.T) {
	// Intent: case-insensitive name mapping with a silent default — any
	// unrecognized string becomes STREAM_LIFECYCLE_UPDATE. Pinned so a renamed
	// upstream type surfaces as a test failure rather than silent misrouting.
	tests := []struct {
		in   string
		want model.StreamEventType
	}{
		{"stream_lifecycle", model.StreamEventTypeStreamLifecycleUpdate},
		{"STREAM_LIFECYCLE_UPDATE", model.StreamEventTypeStreamLifecycleUpdate},
		{"stream_start", model.StreamEventTypeStreamStart},
		{"STREAM_END", model.StreamEventTypeStreamEnd},
		{"buffer_update", model.StreamEventTypeBufferUpdate},
		{"track_list_update", model.StreamEventTypeTrackListUpdate},
		{"play_rewrite", model.StreamEventTypePlayRewrite},
		{"stream_source", model.StreamEventTypeStreamSource},
		{"totally-unknown", model.StreamEventTypeStreamLifecycleUpdate},
	}
	for _, tt := range tests {
		if got := mapStreamEventType(tt.in); got != tt.want {
			t.Fatalf("mapStreamEventType(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestBuildLiveEventID(t *testing.T) {
	// Intent: a present nodeID is appended to the event ID (so per-node events
	// stay distinct); an absent/empty nodeID yields the shorter form.
	ts := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	node := "node-7"
	withNode := buildLiveEventID("stream-1", model.StreamEventTypeStreamStart, ts, &node)
	if !strings.HasSuffix(withNode, ":node-7") {
		t.Fatalf("expected node suffix, got %q", withNode)
	}
	empty := ""
	withoutNode := buildLiveEventID("stream-1", model.StreamEventTypeStreamStart, ts, &empty)
	if strings.HasSuffix(withoutNode, ":node-7") || strings.HasSuffix(withoutNode, ":") {
		t.Fatalf("expected no node suffix for empty nodeID, got %q", withoutNode)
	}
	if withNode == withoutNode {
		t.Fatal("node presence must change the event ID")
	}
}

func ptrStatus(s model.StreamStatus) *model.StreamStatus { return &s }

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
