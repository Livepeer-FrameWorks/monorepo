package resolvers

import (
	"fmt"

	pb "frameworks/pkg/proto"

	"google.golang.org/protobuf/types/known/timestamppb"
)

func mapSignalmanConnectionEvent(event *pb.SignalmanEvent) *pb.ConnectionEvent {
	if event == nil || event.Data == nil {
		return nil
	}

	ts := event.Timestamp
	if ts == nil {
		ts = timestamppb.Now()
	}

	switch event.EventType {
	case pb.EventType_EVENT_TYPE_VIEWER_CONNECT:
		vc := event.Data.GetViewerConnect()
		if vc == nil {
			return nil
		}
		streamID := vc.GetStreamId()
		return &pb.ConnectionEvent{
			EventId:        buildLiveConnectionEventID(streamID, vc.GetSessionId(), ts),
			Timestamp:      ts,
			TenantId:       event.GetTenantId(),
			StreamId:       streamID,
			SessionId:      vc.GetSessionId(),
			ConnectionAddr: vc.GetHost(),
			Connector:      vc.GetConnector(),
			NodeId:         vc.GetNodeId(),
			CountryCode:    vc.GetClientCountry(),
			City:           vc.GetClientCity(),
			Latitude:       vc.GetClientLatitude(),
			Longitude:      vc.GetClientLongitude(),
			EventType:      "connect",
			ClientBucket:   vc.GetClientBucket(),
			NodeBucket:     vc.GetNodeBucket(),
		}

	case pb.EventType_EVENT_TYPE_VIEWER_DISCONNECT:
		vd := event.Data.GetViewerDisconnect()
		if vd == nil {
			return nil
		}
		streamID := vd.GetStreamId()
		duration := uint32(0)
		if vd.GetSecondsConnected() > 0 {
			duration = uint32(vd.GetSecondsConnected())
		} else if vd.GetDuration() > 0 {
			duration = uint32(vd.GetDuration())
		}
		bytesTransferred := uint64(0)
		if vd.GetUpBytes() > 0 {
			bytesTransferred += uint64(vd.GetUpBytes())
		}
		if vd.GetDownBytes() > 0 {
			bytesTransferred += uint64(vd.GetDownBytes())
		}

		return &pb.ConnectionEvent{
			EventId:                buildLiveConnectionEventID(streamID, vd.GetSessionId(), ts),
			Timestamp:              ts,
			TenantId:               event.GetTenantId(),
			StreamId:               streamID,
			SessionId:              vd.GetSessionId(),
			ConnectionAddr:         vd.GetHost(),
			Connector:              vd.GetConnector(),
			NodeId:                 vd.GetNodeId(),
			CountryCode:            vd.GetCountryCode(),
			City:                   vd.GetCity(),
			Latitude:               vd.GetLatitude(),
			Longitude:              vd.GetLongitude(),
			EventType:              "disconnect",
			ClientBucket:           vd.GetClientBucket(),
			NodeBucket:             vd.GetNodeBucket(),
			SessionDurationSeconds: duration,
			BytesTransferred:       bytesTransferred,
		}
	}

	return nil
}

func buildLiveConnectionEventID(streamID, sessionID string, ts *timestamppb.Timestamp) string {
	parts := streamID
	if sessionID != "" {
		parts = fmt.Sprintf("%s:%s", streamID, sessionID)
	}
	return fmt.Sprintf("live:conn:%s:%d", parts, ts.AsTime().UnixNano())
}
