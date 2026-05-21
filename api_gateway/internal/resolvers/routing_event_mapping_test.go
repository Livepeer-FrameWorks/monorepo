package resolvers

import (
	"testing"
	"time"

	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"

	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestMapSignalmanRoutingEventPreservesZeroCoordinateWithBucket(t *testing.T) {
	streamID := "stream-1"
	event := &pb.SignalmanEvent{
		Timestamp: timestamppb.New(time.Unix(123, 0)),
		Data: &pb.EventData{
			Payload: &pb.EventData_LoadBalancing{
				LoadBalancing: &pb.LoadBalancingData{
					StreamId:      &streamID,
					Latitude:      0,
					Longitude:     4.9041,
					NodeLatitude:  52.3676,
					NodeLongitude: 0,
					ClientBucket:  &pb.GeoBucket{H3Index: 1, Resolution: 5},
					NodeBucket:    &pb.GeoBucket{H3Index: 2, Resolution: 5},
				},
			},
		},
	}

	got := mapSignalmanRoutingEvent(event)
	if got == nil {
		t.Fatal("expected routing event")
	}
	if got.ClientLatitude == nil || *got.ClientLatitude != 0 {
		t.Fatalf("expected client latitude 0, got %#v", got.ClientLatitude)
	}
	if got.ClientLongitude == nil || *got.ClientLongitude != 4.9041 {
		t.Fatalf("expected client longitude 4.9041, got %#v", got.ClientLongitude)
	}
	if got.NodeLatitude == nil || *got.NodeLatitude != 52.3676 {
		t.Fatalf("expected node latitude 52.3676, got %#v", got.NodeLatitude)
	}
	if got.NodeLongitude == nil || *got.NodeLongitude != 0 {
		t.Fatalf("expected node longitude 0, got %#v", got.NodeLongitude)
	}
}
