package resolvers

import (
	"fmt"
	"math"
	"time"

	pb "frameworks/pkg/proto"

	"google.golang.org/protobuf/types/known/timestamppb"
)

func mapSignalmanRoutingEvent(event *pb.SignalmanEvent) *pb.RoutingEvent {
	if event == nil || event.Data == nil {
		return nil
	}

	data := event.Data.GetLoadBalancing()
	if data == nil {
		return nil
	}

	timestamp := event.Timestamp
	if timestamp == nil {
		timestamp = timestamppb.New(time.Now())
	}

	streamID := ""
	if data.StreamId != nil {
		streamID = *data.StreamId
	}

	eventID := fmt.Sprintf("live:lb:%s:%d", streamID, timestamp.AsTime().UnixNano())

	routing := &pb.RoutingEvent{
		Id:           eventID,
		Timestamp:    timestamp,
		StreamId:     streamID,
		SelectedNode: data.SelectedNode,
		NodeId:       data.SelectedNodeId,
		Status:       data.Status,
		CandidatesCount: func() int32 {
			if data.CandidatesCount != nil {
				return int32(*data.CandidatesCount)
			}
			return 0
		}(),
		LatencyMs: func() float32 {
			if data.LatencyMs != nil {
				return *data.LatencyMs
			}
			return 0
		}(),
		ClientBucket:    data.ClientBucket,
		NodeBucket:      data.NodeBucket,
		StreamTenantId:  data.StreamTenantId,
		ClusterId:       data.ClusterId,
		EventType:       data.EventType,
		Source:          data.Source,
		RoutingDistance: data.RoutingDistanceKm,
		RemoteClusterId: data.RemoteClusterId,
	}

	if data.TenantId != nil {
		routing.TenantId = *data.TenantId
	}

	if data.ClientCountry != "" {
		c := data.ClientCountry
		routing.ClientCountry = &c
	}
	if data.Latitude != 0 {
		v := data.Latitude
		routing.ClientLatitude = &v
	}
	if data.Longitude != 0 {
		v := data.Longitude
		routing.ClientLongitude = &v
	}
	if data.NodeLatitude != 0 {
		v := data.NodeLatitude
		routing.NodeLatitude = &v
	}
	if data.NodeLongitude != 0 {
		v := data.NodeLongitude
		routing.NodeLongitude = &v
	}
	if data.NodeName != "" {
		v := data.NodeName
		routing.NodeName = &v
	}
	if data.Score != 0 {
		score := int32(math.Min(float64(data.Score), float64(math.MaxInt32)))
		routing.Score = &score
	}
	if data.Details != "" {
		v := data.Details
		routing.Details = &v
	}

	return routing
}
