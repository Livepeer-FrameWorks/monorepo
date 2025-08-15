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
		{"stream-ingest missing internal_name", baseEvent(EventStreamIngest, map[string]interface{}{"stream_key": "k", "protocol": "rtmp", "push_url": "u"}), false},
		{"stream-ingest ok", func() BaseEvent {
			e := baseEvent(EventStreamIngest, map[string]interface{}{"stream_key": "k", "protocol": "rtmp", "push_url": "u"})
			s := "ix"
			u := uuid.NewString()
			e.InternalName = &s
			e.UserID = &u
			return e
		}(), true},
		{"stream-view missing playback", baseEvent(EventStreamView, map[string]interface{}{"internal_name": "x"}), false},
		{"load-balancing ok", func() BaseEvent {
			e := baseEvent(EventLoadBalancing, map[string]interface{}{"status": "success", "selected_node": "n"})
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
