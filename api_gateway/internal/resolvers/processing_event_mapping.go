package resolvers

import (
	"fmt"
	"time"

	pb "frameworks/pkg/proto"

	"google.golang.org/protobuf/types/known/timestamppb"
)

func mapSignalmanProcessingEvent(event *pb.SignalmanEvent) *pb.ProcessingUsageRecord {
	if event == nil || event.Data == nil {
		return nil
	}

	data := event.Data.GetProcessBilling()
	if data == nil {
		return nil
	}

	timestamp := time.Now()
	if event.Timestamp != nil {
		timestamp = event.Timestamp.AsTime()
	} else if data.GetTimestamp() > 0 {
		timestamp = time.Unix(data.GetTimestamp(), 0)
	}

	streamID := data.GetStreamId()
	eventID := fmt.Sprintf("live:%s:%s:%d", streamID, data.GetNodeId(), timestamp.UnixNano())

	tenantID := ""
	if data.TenantId != nil {
		tenantID = data.GetTenantId()
	} else if event.TenantId != nil && *event.TenantId != "" {
		tenantID = *event.TenantId
	}

	return &pb.ProcessingUsageRecord{
		Id:                  eventID,
		Timestamp:           timestamppb.New(timestamp),
		TenantId:            tenantID,
		NodeId:              data.GetNodeId(),
		StreamId:            streamID,
		ProcessType:         data.GetProcessType(),
		DurationMs:          data.GetDurationMs(),
		InputCodec:          data.InputCodec,
		OutputCodec:         data.OutputCodec,
		TrackType:           data.TrackType,
		SegmentNumber:       data.SegmentNumber,
		Width:               data.Width,
		Height:              data.Height,
		RenditionCount:      data.RenditionCount,
		BroadcasterUrl:      data.BroadcasterUrl,
		UploadTimeUs:        data.UploadTimeUs,
		LivepeerSessionId:   data.LivepeerSessionId,
		SegmentStartMs:      data.SegmentStartMs,
		InputBytes:          data.InputBytes,
		OutputBytesTotal:    data.OutputBytesTotal,
		AttemptCount:        data.AttemptCount,
		TurnaroundMs:        data.TurnaroundMs,
		SpeedFactor:         data.SpeedFactor,
		RenditionsJson:      data.RenditionsJson,
		InputFrames:         data.InputFrames,
		OutputFrames:        data.OutputFrames,
		DecodeUsPerFrame:    data.DecodeUsPerFrame,
		TransformUsPerFrame: data.TransformUsPerFrame,
		EncodeUsPerFrame:    data.EncodeUsPerFrame,
		IsFinal:             data.IsFinal,
		InputFramesDelta:    data.InputFramesDelta,
		OutputFramesDelta:   data.OutputFramesDelta,
		InputBytesDelta:     data.InputBytesDelta,
		OutputBytesDelta:    data.OutputBytesDelta,
		InputWidth:          data.InputWidth,
		InputHeight:         data.InputHeight,
		OutputWidth:         data.OutputWidth,
		OutputHeight:        data.OutputHeight,
		InputFpks:           data.InputFpks,
		OutputFpsMeasured:   data.OutputFpsMeasured,
		SampleRate:          data.SampleRate,
		Channels:            data.Channels,
		SourceTimestampMs:   data.SourceTimestampMs,
		SinkTimestampMs:     data.SinkTimestampMs,
		SourceAdvancedMs:    data.SourceAdvancedMs,
		SinkAdvancedMs:      data.SinkAdvancedMs,
		RtfIn:               data.RtfIn,
		RtfOut:              data.RtfOut,
		PipelineLagMs:       data.PipelineLagMs,
		OutputBitrateBps:    data.OutputBitrateBps,
	}
}
