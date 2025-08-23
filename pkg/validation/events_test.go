package validation

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func baseEvent(t EventType) BaseEvent {
	return BaseEvent{
		EventID:       uuid.NewString(),
		EventType:     t,
		Timestamp:     time.Now(),
		Source:        "test",
		SchemaVersion: "1.0",
	}
}

func TestValidate_NodeLifecycle_MissingNodeID(t *testing.T) {
	v := NewEventValidator()
	evt := baseEvent(EventNodeLifecycle)
	evt.NodeLifecycle = &NodeLifecyclePayload{
		IsHealthy: true,
		// Missing NodeID - should cause validation error
	}
	batch := &BatchedEvents{BatchID: uuid.NewString(), Source: "test", Timestamp: time.Now(), Events: []BaseEvent{evt}}
	if err := v.ValidateBatch(batch); err == nil {
		t.Fatalf("expected error for missing node_id")
	}
}

func TestValidate_LoadBalancing_OK(t *testing.T) {
	v := NewEventValidator()
	evt := baseEvent(EventLoadBalancing)
	evt.LoadBalancing = &LoadBalancingPayload{
		Status:       "success",
		SelectedNode: "n1",
	}
	s := "x"
	evt.InternalName = &s
	batch := &BatchedEvents{BatchID: uuid.NewString(), Source: "test", Timestamp: time.Now(), Events: []BaseEvent{evt}}
	if err := v.ValidateBatch(batch); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
