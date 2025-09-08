package mist

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"
)

// TriggerType represents the type of MistServer trigger
type TriggerType string

const (
	TriggerPushRewrite   TriggerType = "PUSH_REWRITE"
	TriggerDefaultStream TriggerType = "DEFAULT_STREAM"
	TriggerStreamSource  TriggerType = "STREAM_SOURCE"
	TriggerPushOutStart  TriggerType = "PUSH_OUT_START"
	TriggerPushEnd       TriggerType = "PUSH_END"
	TriggerStreamBuffer  TriggerType = "STREAM_BUFFER"
	TriggerStreamEnd     TriggerType = "STREAM_END"
	TriggerUserNew       TriggerType = "USER_NEW"
	TriggerUserEnd       TriggerType = "USER_END"
	TriggerLiveTrackList TriggerType = "LIVE_TRACK_LIST"
	TriggerLiveBandwidth TriggerType = "LIVE_BANDWIDTH"
	TriggerRecordingEnd  TriggerType = "RECORDING_END"
	// Polled-from-Helmsman trigger types.
	TriggerStreamLifecycle TriggerType = "STREAM_LIFECYCLE_UPDATE"
	TriggerClientLifecycle TriggerType = "CLIENT_LIFECYCLE_UPDATE"
	TriggerNodeLifecycle   TriggerType = "NODE_LIFECYCLE_UPDATE"
)

// ParseTriggerToProtobuf parses raw MistServer trigger payload and returns a protobuf MistTrigger
func ParseTriggerToProtobuf(triggerType TriggerType, rawPayload []byte, nodeID string, logger logging.Logger) (*pb.MistTrigger, error) {
	// Parse parameters from newline-separated format
	payloadStr := strings.TrimSpace(string(rawPayload))
	params := []string{}
	if payloadStr != "" {
		params = strings.Split(payloadStr, "\n")
	}

	mistTrigger := &pb.MistTrigger{
		TriggerType: string(triggerType),
		NodeId:      nodeID,
		Timestamp:   time.Now().Unix(),
		Blocking:    triggerType.IsBlocking(),
	}

	switch triggerType {
	case TriggerPushRewrite:
		if len(params) < 3 {
			return nil, fmt.Errorf("PUSH_REWRITE requires 3 parameters, got %d", len(params))
		}
		mistTrigger.TriggerPayload = &pb.MistTrigger_PushRewrite{
			PushRewrite: &pb.PushRewriteTrigger{
				PushUrl:    params[0],
				Hostname:   params[1],
				StreamName: params[2],
			},
		}

	case TriggerDefaultStream:
		if len(params) < 2 {
			return nil, fmt.Errorf("DEFAULT_STREAM requires at least 2 parameters, got %d", len(params))
		}
		// Map to ViewerResolveTrigger (viewer-side resolve)
		trigger := &pb.ViewerResolveTrigger{
			DefaultStream:   params[0],
			RequestedStream: params[1],
		}
		if len(params) > 2 {
			trigger.ViewerHost = params[2]
		}
		if len(params) > 3 {
			trigger.OutputType = params[3]
		}
		if len(params) > 4 {
			trigger.RequestUrl = params[4]
		}
		mistTrigger.TriggerPayload = &pb.MistTrigger_ViewerResolve{
			ViewerResolve: trigger,
		}

	case TriggerStreamSource:
		if len(params) < 1 {
			return nil, fmt.Errorf("STREAM_SOURCE requires at least 1 parameter, got %d", len(params))
		}
		mistTrigger.TriggerPayload = &pb.MistTrigger_StreamSource{
			StreamSource: &pb.StreamSourceTrigger{
				StreamName: params[0],
			},
		}

	case TriggerPushOutStart:
		if len(params) < 2 {
			return nil, fmt.Errorf("PUSH_OUT_START requires 2 parameters, got %d", len(params))
		}
		mistTrigger.TriggerPayload = &pb.MistTrigger_PushOutStart{
			PushOutStart: &pb.PushOutStartTrigger{
				StreamName: params[0],
				PushTarget: params[1],
			},
		}

	case TriggerPushEnd:
		if len(params) < 6 {
			return nil, fmt.Errorf("PUSH_END requires 6 parameters, got %d", len(params))
		}
		trigger := &pb.PushEndTrigger{
			StreamName:      params[1],
			TargetUriBefore: params[2],
			TargetUriAfter:  params[3],
			LogMessages:     params[4],
			PushStatus:      params[5],
		}
		if pushID, err := strconv.ParseInt(params[0], 10, 64); err == nil {
			trigger.PushId = pushID
		}
		mistTrigger.TriggerPayload = &pb.MistTrigger_PushEnd{
			PushEnd: trigger,
		}

	case TriggerUserNew:
		if len(params) < 6 {
			return nil, fmt.Errorf("USER_NEW requires 6 parameters, got %d", len(params))
		}
		mistTrigger.TriggerPayload = &pb.MistTrigger_ViewerConnect{
			ViewerConnect: &pb.ViewerConnectTrigger{
				StreamName:   params[0],
				Host:         params[1],
				ConnectionId: params[2],
				Connector:    params[3],
				RequestUrl:   params[4],
				SessionId:    params[5],
			},
		}

	case TriggerUserEnd:
		if len(params) < 8 {
			return nil, fmt.Errorf("USER_END requires 8 parameters, got %d", len(params))
		}
		trigger := &pb.ViewerDisconnectTrigger{
			SessionId:  params[0],
			StreamName: params[1],
			Connector:  params[2],
			Host:       params[3],
		}

		if duration, err := strconv.ParseInt(params[4], 10, 64); err == nil {
			trigger.Duration = duration
		}
		if upBytes, err := strconv.ParseInt(params[5], 10, 64); err == nil {
			trigger.UpBytes = upBytes
		}
		if downBytes, err := strconv.ParseInt(params[6], 10, 64); err == nil {
			trigger.DownBytes = downBytes
		}
		if len(params) > 7 {
			trigger.Tags = params[7]
		}
		mistTrigger.TriggerPayload = &pb.MistTrigger_ViewerDisconnect{
			ViewerDisconnect: trigger,
		}

	case TriggerStreamBuffer:
		if len(params) < 2 {
			return nil, fmt.Errorf("STREAM_BUFFER requires at least 2 parameters, got %d", len(params))
		}
		trigger := &pb.StreamBufferTrigger{
			StreamName:  params[0],
			BufferState: params[1],
		}

		// Parse JSON track details if present (params[2])
		if len(params) > 2 && params[2] != "" {
			var tracksData map[string]interface{}
			if err := json.Unmarshal([]byte(params[2]), &tracksData); err != nil {
				logger.WithFields(logging.Fields{
					"error": err.Error(),
					"json":  params[2],
				}).Warn("Failed to parse STREAM_BUFFER track JSON")
			} else {
				trigger.Tracks = parseTracksFromJSON(tracksData)
			}
		}

		mistTrigger.TriggerPayload = &pb.MistTrigger_StreamBuffer{
			StreamBuffer: trigger,
		}

	case TriggerStreamEnd:
		if len(params) < 1 {
			return nil, fmt.Errorf("STREAM_END requires at least 1 parameter, got %d", len(params))
		}
		trigger := &pb.StreamEndTrigger{
			StreamName: params[0],
		}

		if len(params) >= 7 {
			if downloadedBytes, err := strconv.ParseInt(params[1], 10, 64); err == nil {
				trigger.DownloadedBytes = &downloadedBytes
			}
			if uploadedBytes, err := strconv.ParseInt(params[2], 10, 64); err == nil {
				trigger.UploadedBytes = &uploadedBytes
			}
			if totalViewers, err := strconv.ParseInt(params[3], 10, 64); err == nil {
				trigger.TotalViewers = &totalViewers
			}
			if totalInputs, err := strconv.ParseInt(params[4], 10, 64); err == nil {
				trigger.TotalInputs = &totalInputs
			}
			if totalOutputs, err := strconv.ParseInt(params[5], 10, 64); err == nil {
				trigger.TotalOutputs = &totalOutputs
			}
			if viewerSeconds, err := strconv.ParseInt(params[6], 10, 64); err == nil {
				trigger.ViewerSeconds = &viewerSeconds
			}
		}
		mistTrigger.TriggerPayload = &pb.MistTrigger_StreamEnd{
			StreamEnd: trigger,
		}

	case TriggerLiveTrackList:
		if len(params) < 2 {
			return nil, fmt.Errorf("LIVE_TRACK_LIST requires 2 parameters, got %d", len(params))
		}
		trigger := &pb.StreamTrackListTrigger{
			StreamName: params[0],
		}

		// Parse JSON track list if present (params[1])
		if len(params) > 1 && params[1] != "" {
			var tracksData map[string]interface{}
			if err := json.Unmarshal([]byte(params[1]), &tracksData); err != nil {
				logger.WithFields(logging.Fields{
					"error": err.Error(),
					"json":  params[1],
				}).Warn("Failed to parse LIVE_TRACK_LIST JSON")
			} else {
				trigger.Tracks = parseTracksFromJSON(tracksData)
			}
		}

		mistTrigger.TriggerPayload = &pb.MistTrigger_TrackList{
			TrackList: trigger,
		}

	case TriggerLiveBandwidth:
		if len(params) < 2 {
			return nil, fmt.Errorf("LIVE_BANDWIDTH requires 2 parameters, got %d", len(params))
		}
		trigger := &pb.StreamBandwidthTrigger{
			StreamName: params[0],
		}
		if currentBytesPerSecond, err := strconv.ParseInt(params[1], 10, 64); err == nil {
			trigger.CurrentBytesPerSecond = currentBytesPerSecond
		}
		mistTrigger.TriggerPayload = &pb.MistTrigger_StreamBandwidth{
			StreamBandwidth: trigger,
		}

	case TriggerRecordingEnd:
		if len(params) < 8 {
			return nil, fmt.Errorf("RECORDING_END requires at least 8 parameters, got %d", len(params))
		}
		trigger := &pb.RecordingCompleteTrigger{
			StreamName:     params[0],
			FilePath:       params[1],
			OutputProtocol: params[2],
		}

		if bytesWritten, err := strconv.ParseInt(params[3], 10, 64); err == nil {
			trigger.BytesWritten = bytesWritten
		}
		if secondsWriting, err := strconv.ParseInt(params[4], 10, 64); err == nil {
			trigger.SecondsWriting = secondsWriting
		}
		if timeStarted, err := strconv.ParseInt(params[5], 10, 64); err == nil {
			trigger.TimeStarted = timeStarted
		}
		if timeEnded, err := strconv.ParseInt(params[6], 10, 64); err == nil {
			trigger.TimeEnded = timeEnded
		}
		if len(params) > 7 {
			if mediaDurationMs, err := strconv.ParseInt(params[7], 10, 64); err == nil {
				trigger.MediaDurationMs = mediaDurationMs
			}
		}
		mistTrigger.TriggerPayload = &pb.MistTrigger_RecordingComplete{
			RecordingComplete: trigger,
		}

	case TriggerStreamLifecycle:
		// For analytics triggers, directly unmarshal JSON to protobuf
		var trigger pb.StreamLifecycleUpdate
		if err := json.Unmarshal(rawPayload, &trigger); err != nil {
			return nil, fmt.Errorf("failed to parse STREAM_LIFECYCLE_UPDATE JSON: %w", err)
		}
		mistTrigger.TriggerPayload = &pb.MistTrigger_StreamLifecycleUpdate{
			StreamLifecycleUpdate: &trigger,
		}

	case TriggerClientLifecycle:
		var trigger pb.ClientLifecycleUpdate
		if err := json.Unmarshal(rawPayload, &trigger); err != nil {
			return nil, fmt.Errorf("failed to parse CLIENT_LIFECYCLE_UPDATE JSON: %w", err)
		}
		mistTrigger.TriggerPayload = &pb.MistTrigger_ClientLifecycleUpdate{
			ClientLifecycleUpdate: &trigger,
		}

	case TriggerNodeLifecycle:
		var trigger pb.NodeLifecycleUpdate
		if err := json.Unmarshal(rawPayload, &trigger); err != nil {
			return nil, fmt.Errorf("failed to parse NODE_LIFECYCLE_UPDATE JSON: %w", err)
		}
		mistTrigger.TriggerPayload = &pb.MistTrigger_NodeLifecycleUpdate{
			NodeLifecycleUpdate: &trigger,
		}

	default:
		return nil, fmt.Errorf("unknown trigger type: %s", triggerType)
	}

	return mistTrigger, nil
}

// ExtractInternalName extracts internal name from stream name (handles wildcard format)
func ExtractInternalName(streamName string) string {
	// Extract internal name from wildcard stream (format: "live+{internal_name}")
	if plusIndex := strings.Index(streamName, "+"); plusIndex != -1 {
		return streamName[plusIndex+1:]
	}
	// For non-wildcard streams, use the stream name as-is
	return streamName
}

// IsBlocking returns whether the trigger type requires a blocking response
func (t TriggerType) IsBlocking() bool {
	switch t {
	case TriggerPushRewrite, TriggerDefaultStream, TriggerStreamSource, TriggerPushOutStart, TriggerUserNew:
		return true
	default:
		return false
	}
}

// parseTracksFromJSON converts MistServer track JSON data to protobuf StreamTrack messages
func parseTracksFromJSON(tracksData map[string]interface{}) []*pb.StreamTrack {
	var tracks []*pb.StreamTrack

	for trackName, trackData := range tracksData {
		trackMap, ok := trackData.(map[string]interface{})
		if !ok {
			continue
		}

		// Check if this looks like a track (has codec field)
		codec, hasCodec := trackMap["codec"].(string)
		if !hasCodec {
			continue
		}

		track := &pb.StreamTrack{
			TrackName: trackName,
			Codec:     codec,
		}

		// Extract bitrate
		if kbits, ok := trackMap["kbits"].(float64); ok {
			bitrateKbps := int32(kbits)
			bitrateBps := int64(kbits * 1000)
			track.BitrateKbps = &bitrateKbps
			track.BitrateBps = &bitrateBps
		}

		// Extract buffer and jitter
		if buffer, ok := trackMap["buffer"].(float64); ok {
			bufferInt := int32(buffer)
			track.Buffer = &bufferInt
		}
		if jitter, ok := trackMap["jitter"].(float64); ok {
			jitterInt := int32(jitter)
			track.Jitter = &jitterInt
		}

		// Determine track type and extract type-specific fields
		if strings.Contains(trackName, "video_") || codec == "H264" || codec == "H265" || codec == "AV1" {
			track.TrackType = "video"

			// Extract video-specific fields
			if width, ok := trackMap["width"].(float64); ok {
				widthInt := int32(width)
				track.Width = &widthInt
			}
			if height, ok := trackMap["height"].(float64); ok {
				heightInt := int32(height)
				track.Height = &heightInt
			}
			if fpks, ok := trackMap["fpks"].(float64); ok {
				fps := fpks / 1000 // fpks is frames per kilosecond
				track.Fps = &fps
			}
			if bframes, ok := trackMap["bframes"].(bool); ok {
				track.HasBframes = &bframes
			}

			// Create resolution string
			if track.Width != nil && track.Height != nil {
				resolution := fmt.Sprintf("%dx%d", *track.Width, *track.Height)
				track.Resolution = &resolution
			}

		} else if strings.Contains(trackName, "audio_") || codec == "AAC" || codec == "opus" || codec == "MP3" {
			track.TrackType = "audio"

			// Extract audio-specific fields
			if channels, ok := trackMap["channels"].(float64); ok {
				channelsInt := int32(channels)
				track.Channels = &channelsInt
			}
			if rate, ok := trackMap["rate"].(float64); ok {
				sampleRate := int32(rate)
				track.SampleRate = &sampleRate
			}

		} else if strings.Contains(trackName, "meta_") || codec == "JSON" {
			track.TrackType = "meta"
		} else {
			track.TrackType = "unknown"
		}

		// Extract frame timing info from keys if available
		if keys, ok := trackMap["keys"].(map[string]interface{}); ok {
			if frameMax, ok := keys["frames_max"].(float64); ok {
				framesMax := int32(frameMax)
				track.FramesMax = &framesMax
			}
			if frameMin, ok := keys["frames_min"].(float64); ok {
				framesMin := int32(frameMin)
				track.FramesMin = &framesMin
			}
		}

		tracks = append(tracks, track)
	}

	return tracks
}
