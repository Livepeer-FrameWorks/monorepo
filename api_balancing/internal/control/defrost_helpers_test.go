package control

import (
	"testing"

	"frameworks/api_balancing/internal/state"
	"frameworks/pkg/logging"
)

type mockLoadBalancer struct {
	nodes map[string]state.NodeState
}

func (m *mockLoadBalancer) GetNodes() map[string]state.NodeState {
	return m.nodes
}

func (m *mockLoadBalancer) GetNodeByID(nodeID string) (string, error) {
	return "", nil
}

func (m *mockLoadBalancer) GetNodeIDByHost(host string) string {
	return ""
}

func TestPickStorageNodeID(t *testing.T) {
	original := loadBalancerInstance
	defer func() { loadBalancerInstance = original }()

	loadBalancerInstance = nil
	if _, err := pickStorageNodeID(); err == nil {
		t.Fatal("expected error when load balancer is nil")
	}

	loadBalancerInstance = &mockLoadBalancer{
		nodes: map[string]state.NodeState{
			"node-1": {NodeID: "node-1", CapStorage: true, IsHealthy: true},
		},
	}
	id, err := pickStorageNodeID()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "node-1" {
		t.Fatalf("expected node-1, got %q", id)
	}

	loadBalancerInstance = &mockLoadBalancer{
		nodes: map[string]state.NodeState{
			"node-2": {NodeID: "node-2", CapStorage: false, IsHealthy: true},
			"node-3": {NodeID: "node-3", CapStorage: true, IsHealthy: false},
		},
	}
	if _, err := pickStorageNodeID(); err == nil {
		t.Fatal("expected error when no storage nodes available")
	}
}

func TestPickStorageNodeIDPublic(t *testing.T) {
	original := loadBalancerInstance
	defer func() { loadBalancerInstance = original }()

	loadBalancerInstance = &mockLoadBalancer{
		nodes: map[string]state.NodeState{
			"node-storage": {NodeID: "node-storage", CapStorage: true, IsHealthy: true},
		},
	}
	id, err := PickStorageNodeIDPublic()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "node-storage" {
		t.Fatalf("expected node-storage, got %q", id)
	}
}

func TestControlLogger(t *testing.T) {
	originalRegistry := registry
	defer func() { registry = originalRegistry }()

	registry = nil
	if got := controlLogger(); got == nil {
		t.Fatal("expected fallback logger when registry is nil")
	}

	exp := logging.NewLoggerWithService("test-control")
	registry = &Registry{log: exp}
	if got := controlLogger(); got != exp {
		t.Fatal("expected registry logger to be returned")
	}
}
