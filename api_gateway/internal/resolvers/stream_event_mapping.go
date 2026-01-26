package resolvers

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/pkg/globalid"
	pb "frameworks/pkg/proto"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func mapPeriscopeStreamEvent(event *pb.StreamEvent) *model.StreamEvent {
	if event == nil {
		return nil
	}

	timestamp := time.Now()
	if event.Timestamp != nil {
		timestamp = event.Timestamp.AsTime()
	}

	details := redactInternalJSONPtr(event.EventData)
	nodeID := stringPtrOrNil(event.NodeId)

	eventID := event.EventId
	if eventID == "" {
		eventID = fmt.Sprintf("hist:%s:%d", event.StreamId, timestamp.UnixNano())
	}

	return &model.StreamEvent{
		Id:       globalid.Encode(globalid.TypeStreamEvent, fmt.Sprintf("%s|%s|%d", event.StreamId, eventID, timestamp.UnixNano())),
		EventId:   eventID,
		StreamId:  event.StreamId,
		NodeId:    nodeID,
		Type:      mapStreamEventType(event.EventType),
		Status:    mapStreamStatus(event.Status),
		Timestamp: timestamp,
		Details:   details,
		Payload:   protoPayloadJSON(event.EventPayload),
		Source:    model.StreamEventSourceHistorical,

		BufferState:    event.BufferState,
		HasIssues:      event.HasIssues,
		TrackCount:     intPtrFromInt32(event.TrackCount),
		QualityTier:    event.QualityTier,
		PrimaryWidth:   intPtrFromInt32(event.PrimaryWidth),
		PrimaryHeight:  intPtrFromInt32(event.PrimaryHeight),
		PrimaryFps:     float64PtrFromFloat32(event.PrimaryFps),
		PrimaryCodec:   event.PrimaryCodec,
		PrimaryBitrate: intPtrFromInt32(event.PrimaryBitrate),
		DownloadedBytes: float64PtrFromUint64(event.DownloadedBytes),
		UploadedBytes:   float64PtrFromUint64(event.UploadedBytes),
		TotalViewers:    intPtrFromUint32(event.TotalViewers),
		TotalInputs:     intPtrFromInt32(event.TotalInputs),
		TotalOutputs:    intPtrFromInt32(event.TotalOutputs),
		ViewerSeconds:   float64PtrFromUint64(event.ViewerSeconds),

		RequestUrl:  event.RequestUrl,
		Protocol:    event.Protocol,
		Latitude:    event.Latitude,
		Longitude:   event.Longitude,
		Location:    event.Location,
		CountryCode: event.CountryCode,
		City:        event.City,
	}
}

func mapSignalmanStreamEvent(event *pb.SignalmanEvent) *model.StreamEvent {
	if event == nil || event.Data == nil {
		return nil
	}

	timestamp := time.Now()
	if event.Timestamp != nil {
		timestamp = event.Timestamp.AsTime()
	}

	switch event.EventType {
	case pb.EventType_EVENT_TYPE_PUSH_REWRITE:
		data := event.Data.GetPushRewrite()
		if data == nil {
			return nil
		}
		streamID := data.GetStreamId()
		nodeID := stringPtrOrNil(data.GetNodeId())
		status := model.StreamStatusLive
		eventID := buildLiveEventID(streamID, model.StreamEventTypeStreamStart, timestamp, nodeID)
		return &model.StreamEvent{
			Id:        globalid.Encode(globalid.TypeStreamEvent, fmt.Sprintf("%s|%s|%d", streamID, eventID, timestamp.UnixNano())),
			EventId:   eventID,
			StreamId:  streamID,
			NodeId:    nodeID,
			Type:      model.StreamEventTypeStreamStart,
			Status:    &status,
			Timestamp: timestamp,
			Payload:   protoPayloadJSON(data),
			Source:    model.StreamEventSourceLive,
		}

	case pb.EventType_EVENT_TYPE_STREAM_LIFECYCLE_UPDATE:
		data := event.Data.GetStreamLifecycle()
		if data == nil {
			return nil
		}
		nodeID := stringPtrOrNil(data.GetNodeId())
		streamID := data.GetStreamId()
		eventID := buildLiveEventID(streamID, model.StreamEventTypeStreamLifecycleUpdate, timestamp, nodeID)
		return &model.StreamEvent{
			Id:        globalid.Encode(globalid.TypeStreamEvent, fmt.Sprintf("%s|%s|%d", streamID, eventID, timestamp.UnixNano())),
			EventId:   eventID,
			StreamId:  streamID,
			NodeId:    nodeID,
			Type:      model.StreamEventTypeStreamLifecycleUpdate,
			Status:    mapStreamStatus(data.Status),
			Timestamp: timestamp,
			Payload:   protoPayloadJSON(data),
			Source:    model.StreamEventSourceLive,
		}

	case pb.EventType_EVENT_TYPE_STREAM_END:
		data := event.Data.GetStreamEnd()
		if data == nil {
			return nil
		}
		nodeID := stringPtrOrNil(data.GetNodeId())
		status := model.StreamStatusEnded
		streamID := data.GetStreamId()
		eventID := buildLiveEventID(streamID, model.StreamEventTypeStreamEnd, timestamp, nodeID)
		return &model.StreamEvent{
			Id:        globalid.Encode(globalid.TypeStreamEvent, fmt.Sprintf("%s|%s|%d", streamID, eventID, timestamp.UnixNano())),
			EventId:   eventID,
			StreamId:  streamID,
			NodeId:    nodeID,
			Type:      model.StreamEventTypeStreamEnd,
			Status:    &status,
			Timestamp: timestamp,
			Payload:   protoPayloadJSON(data),
			Source:    model.StreamEventSourceLive,
		}

	case pb.EventType_EVENT_TYPE_STREAM_BUFFER:
		data := event.Data.GetStreamBuffer()
		if data == nil {
			return nil
		}
		streamID := data.GetStreamId()
		eventID := buildLiveEventID(streamID, model.StreamEventTypeBufferUpdate, timestamp, nil)
		return &model.StreamEvent{
			Id:        globalid.Encode(globalid.TypeStreamEvent, fmt.Sprintf("%s|%s|%d", streamID, eventID, timestamp.UnixNano())),
			EventId:   eventID,
			StreamId:  streamID,
			Type:      model.StreamEventTypeBufferUpdate,
			Timestamp: timestamp,
			Payload:   protoPayloadJSON(data),
			Source:    model.StreamEventSourceLive,
		}

	case pb.EventType_EVENT_TYPE_STREAM_TRACK_LIST:
		data := event.Data.GetTrackList()
		if data == nil {
			return nil
		}
		streamID := data.GetStreamId()
		eventID := buildLiveEventID(streamID, model.StreamEventTypeTrackListUpdate, timestamp, nil)
		return &model.StreamEvent{
			Id:        globalid.Encode(globalid.TypeStreamEvent, fmt.Sprintf("%s|%s|%d", streamID, eventID, timestamp.UnixNano())),
			EventId:   eventID,
			StreamId:  streamID,
			Type:      model.StreamEventTypeTrackListUpdate,
			Timestamp: timestamp,
			Payload:   protoPayloadJSON(data),
			Source:    model.StreamEventSourceLive,
		}

	case pb.EventType_EVENT_TYPE_PLAY_REWRITE:
		data := event.Data.GetPlayRewrite()
		if data == nil {
			return nil
		}
		streamID := data.GetStreamId()
		eventID := buildLiveEventID(streamID, model.StreamEventTypePlayRewrite, timestamp, nil)
		return &model.StreamEvent{
			Id:        globalid.Encode(globalid.TypeStreamEvent, fmt.Sprintf("%s|%s|%d", streamID, eventID, timestamp.UnixNano())),
			EventId:   eventID,
			StreamId:  streamID,
			Type:      model.StreamEventTypePlayRewrite,
			Timestamp: timestamp,
			Payload:   protoPayloadJSON(data),
			Source:    model.StreamEventSourceLive,
		}

	case pb.EventType_EVENT_TYPE_STREAM_SOURCE:
		data := event.Data.GetStreamSource()
		if data == nil {
			return nil
		}
		streamID := data.GetStreamId()
		eventID := buildLiveEventID(streamID, model.StreamEventTypeStreamSource, timestamp, nil)
		return &model.StreamEvent{
			Id:        globalid.Encode(globalid.TypeStreamEvent, fmt.Sprintf("%s|%s|%d", streamID, eventID, timestamp.UnixNano())),
			EventId:   eventID,
			StreamId:  streamID,
			Type:      model.StreamEventTypeStreamSource,
			Timestamp: timestamp,
			Payload:   protoPayloadJSON(data),
			Source:    model.StreamEventSourceLive,
		}
	}

	return nil
}

func mapStreamEventType(value string) model.StreamEventType {
	switch strings.ToUpper(value) {
	case "STREAM_LIFECYCLE":
		return model.StreamEventTypeStreamLifecycleUpdate
	case "STREAM_LIFECYCLE_UPDATE":
		return model.StreamEventTypeStreamLifecycleUpdate
	case "STREAM_START":
		return model.StreamEventTypeStreamStart
	case "STREAM_END":
		return model.StreamEventTypeStreamEnd
	case "STREAM_BUFFER":
		return model.StreamEventTypeBufferUpdate
	case "BUFFER_UPDATE":
		return model.StreamEventTypeBufferUpdate
	case "TRACK_LIST_UPDATE":
		return model.StreamEventTypeTrackListUpdate
	case "PLAY_REWRITE":
		return model.StreamEventTypePlayRewrite
	case "STREAM_SOURCE":
		return model.StreamEventTypeStreamSource
	default:
		return model.StreamEventTypeStreamLifecycleUpdate
	}
}

func mapStreamStatus(value string) *model.StreamStatus {
	if value == "" {
		return nil
	}

	switch strings.ToUpper(value) {
	case "LIVE":
		v := model.StreamStatusLive
		return &v
	case "RECORDING":
		v := model.StreamStatusRecording
		return &v
	case "ENDED":
		v := model.StreamStatusEnded
		return &v
	case "OFFLINE":
		v := model.StreamStatusOffline
		return &v
	default:
		v := model.StreamStatusOffline
		return &v
	}
}

func buildLiveEventID(streamID string, eventType model.StreamEventType, ts time.Time, nodeID *string) string {
	if nodeID != nil && *nodeID != "" {
		return fmt.Sprintf("live:%s:%s:%d:%s", streamID, eventType, ts.UnixNano(), *nodeID)
	}
	return fmt.Sprintf("live:%s:%s:%d", streamID, eventType, ts.UnixNano())
}

func protoPayloadJSON(msg proto.Message) *string {
	if msg == nil {
		return nil
	}
	raw, err := protojson.Marshal(msg)
	if err != nil || len(raw) == 0 {
		return nil
	}
	payload := redactInternalJSON(string(raw))
	return &payload
}

func redactInternalJSONPtr(raw string) *string {
	if raw == "" {
		return nil
	}
	redacted := redactInternalJSON(raw)
	return &redacted
}

func stringPtrOrNil(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func intPtrFromInt32(value *int32) *int {
	if value == nil {
		return nil
	}
	v := int(*value)
	return &v
}

func intPtrFromUint32(value *uint32) *int {
	if value == nil {
		return nil
	}
	v := int(*value)
	return &v
}

func float64PtrFromFloat32(value *float32) *float64 {
	if value == nil {
		return nil
	}
	v := float64(*value)
	return &v
}

func float64PtrFromUint64(value *uint64) *float64 {
	if value == nil {
		return nil
	}
	v := float64(*value)
	return &v
}

func redactInternalJSON(raw string) string {
	if raw == "" {
		return raw
	}
	var payload any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return raw
	}
	redactInternalFields(&payload)
	redacted, err := json.Marshal(payload)
	if err != nil {
		return raw
	}
	return string(redacted)
}

func redactInternalFields(value *any) {
	switch v := (*value).(type) {
	case map[string]any:
		delete(v, "internalName")
		delete(v, "internal_name")
		delete(v, "streamName")
		delete(v, "stream_name")
		delete(v, "resolvedInternalName")
		delete(v, "resolved_internal_name")
		for key, child := range v {
			childValue := child
			redactInternalFields(&childValue)
			v[key] = childValue
		}
	case []any:
		for i := range v {
			childValue := v[i]
			redactInternalFields(&childValue)
			v[i] = childValue
		}
	}
}
