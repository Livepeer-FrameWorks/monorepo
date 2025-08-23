package validation

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestValidateBatch_TableDriven(t *testing.T) {
	cases := []struct {
		name string
		evt  BaseEvent
		ok   bool
	}{
		{"stream-ingest missing internal_name", func() BaseEvent {
			e := baseEvent(EventStreamIngest)
			e.StreamIngest = &StreamIngestPayload{
				StreamKey: "k",
				Protocol:  "rtmp",
				PushURL:   "u",
			}
			return e
		}(), false},
		{"stream-ingest ok", func() BaseEvent {
			e := baseEvent(EventStreamIngest)
			e.StreamIngest = &StreamIngestPayload{
				StreamKey:    "k",
				Protocol:     "rtmp",
				PushURL:      "u",
				InternalName: "ix",
			}
			s := "ix"
			u := uuid.NewString()
			e.InternalName = &s
			e.UserID = &u
			return e
		}(), true},
		{"stream-view missing playback", func() BaseEvent {
			e := baseEvent(EventStreamView)
			e.StreamView = &StreamViewPayload{
				InternalName: "x",
			}
			return e
		}(), false},
		{"load-balancing ok", func() BaseEvent {
			e := baseEvent(EventLoadBalancing)
			e.LoadBalancing = &LoadBalancingPayload{
				Status:       "success",
				SelectedNode: "n",
			}
			s := "x"
			e.InternalName = &s
			return e
		}(), true},
	}
	v := NewEventValidator()
	for _, tc := range cases {
		batch := &BatchedEvents{BatchID: uuid.NewString(), Source: "test", Timestamp: time.Now(), Events: []BaseEvent{tc.evt}}
		err := v.ValidateBatch(batch)
		if tc.ok && err != nil {
			t.Fatalf("%s unexpected error: %v", tc.name, err)
		}
		if !tc.ok && err == nil {
			t.Fatalf("%s expected error", tc.name)
		}
	}
}
