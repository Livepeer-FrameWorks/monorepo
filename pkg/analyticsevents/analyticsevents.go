// Package analyticsevents names the analytics_events event types Decklog
// publishes. Decklog derives an event's type here, and Periscope Ingest's
// contract test checks every routed type against its dispatch table, so a
// payload nothing consumes cannot reach Kafka unnoticed.
package analyticsevents

import (
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// TriggerEventType returns the analytics_events event_type of a MistTrigger
// sent through Decklog's SendEvent. It reports false for an empty payload and
// for one no analytics_events consumer handles (UnroutedTriggerPayloads).
func TriggerEventType(trigger *ipcpb.MistTrigger) (string, bool) {
	switch trigger.GetTriggerPayload().(type) {
	case *ipcpb.MistTrigger_PushRewrite:
		return "push_rewrite", true
	case *ipcpb.MistTrigger_PlayRewrite:
		return "play_rewrite", true
	case *ipcpb.MistTrigger_StreamSource:
		return "stream_source", true
	case *ipcpb.MistTrigger_PushOutStart:
		return "push_out_start", true
	case *ipcpb.MistTrigger_PushEnd:
		return "push_end", true
	case *ipcpb.MistTrigger_RestreamStatus:
		if trigger.GetTriggerType() == string(mist.TriggerRestreamStatusFinal) {
			return "restream_status_final", true
		}
		return "restream_status", true
	case *ipcpb.MistTrigger_ViewerConnect:
		return "viewer_connect", true
	case *ipcpb.MistTrigger_ViewerDisconnect:
		return "viewer_disconnect", true
	case *ipcpb.MistTrigger_StreamBuffer:
		return "stream_buffer", true
	case *ipcpb.MistTrigger_StreamEnd:
		return "stream_end", true
	case *ipcpb.MistTrigger_TrackList:
		return "stream_track_list", true
	case *ipcpb.MistTrigger_RecordingComplete:
		return "recording_complete", true
	case *ipcpb.MistTrigger_RecordingSegment:
		return "recording_segment", true
	case *ipcpb.MistTrigger_StreamLifecycleUpdate:
		return "stream_lifecycle_update", true
	case *ipcpb.MistTrigger_ClientLifecycleBatch:
		return "client_lifecycle_batch", true
	case *ipcpb.MistTrigger_PlaybackBootTrace:
		return "playback_boot", true
	case *ipcpb.MistTrigger_PlaybackSessionQoe:
		return "playback_session_qoe", true
	case *ipcpb.MistTrigger_NodeLifecycleUpdate:
		return "node_lifecycle_update", true
	case *ipcpb.MistTrigger_LoadBalancingData:
		return "load_balancing", true
	case *ipcpb.MistTrigger_ClipLifecycleData:
		return "clip_lifecycle", true
	case *ipcpb.MistTrigger_DvrLifecycleData:
		return "dvr_lifecycle", true
	case *ipcpb.MistTrigger_StorageLifecycleData:
		return "storage_lifecycle", true
	case *ipcpb.MistTrigger_ProcessBilling:
		return "process_billing", true
	case *ipcpb.MistTrigger_RawMistWebhook:
		return "raw_mist_webhook", true
	case *ipcpb.MistTrigger_StorageSnapshot:
		return "storage_snapshot", true
	case *ipcpb.MistTrigger_VodLifecycleData:
		return "vod_lifecycle", true
	case *ipcpb.MistTrigger_FederationEventData:
		return "federation_event", true
	}
	return "", false
}

// UnroutedTriggerPayloads names, by MistTrigger oneof field, each payload
// that never reaches analytics_events and why. Decklog acknowledges these
// without publishing, so a sender from an older release that still forwards
// one does not fail during a mixed-version rollout.
var UnroutedTriggerPayloads = map[string]string{
	"client_lifecycle_update":    "Foghorn folds per-viewer updates into client_lifecycle_batch",
	"stream_process":             "Foghorn answers STREAM_PROCESS inline; it records no analytics fact",
	"connection_play":            "Foghorn answers CONN_PLAY inline; viewer_connect records the session",
	"ingest_runtime_absent":      "Foghorn forwards it as the stream_lifecycle_update it carries",
	"ingest_admission_abandoned": "Foghorn ends the minted ingest session; stream state comes from stream_lifecycle_update",
	"push_input_close":           "source presence owned by Foghorn's ingest admission; stream_end and stream_lifecycle_update record the stream going offline",
	"api_request_batch":          "API usage travels on service_events",
	"message_lifecycle_data":     "messaging events travel on service_events",
}

// UnroutedTriggerPayload returns the oneof field name of a trigger whose
// payload is declared in UnroutedTriggerPayloads. It reports false for a
// routed payload, an empty one, or one this build does not know.
func UnroutedTriggerPayload(trigger *ipcpb.MistTrigger) (string, bool) {
	m := trigger.ProtoReflect()
	field := m.WhichOneof(m.Descriptor().Oneofs().ByName("trigger_payload"))
	if field == nil {
		return "", false
	}
	name := string(field.Name())
	_, unrouted := UnroutedTriggerPayloads[name]
	return name, unrouted
}

// GatewayTelemetryEventType returns the analytics_events event_type of a
// Livepeer gateway telemetry event, and false for an empty payload.
func GatewayTelemetryEventType(event *ipcpb.GatewayTelemetryEvent) (string, bool) {
	switch event.GetPayload().(type) {
	case *ipcpb.GatewayTelemetryEvent_Discovery:
		return "orchestrator_discovery_observed", true
	case *ipcpb.GatewayTelemetryEvent_State:
		return "orchestrator_state_update", true
	case *ipcpb.GatewayTelemetryEvent_Transcode:
		return "orchestrator_transcode_outcome", true
	case *ipcpb.GatewayTelemetryEvent_Ai:
		return "orchestrator_ai_outcome", true
	}
	return "", false
}
