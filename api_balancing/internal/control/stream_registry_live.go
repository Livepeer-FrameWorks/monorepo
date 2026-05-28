package control

import (
	"frameworks/api_balancing/internal/state"
)

// streamStateLivePresence adapts state.StreamStateManager to the registry's
// LivePresence interface. Kept as a thin shim so the state package stays
// agnostic of the registry.
type streamStateLivePresence struct {
	sm *state.StreamStateManager
}

// NewLivePresence returns a LivePresence backed by the given
// StreamStateManager. Pass nil to disable read-through.
func NewLivePresence(sm *state.StreamStateManager) LivePresence {
	if sm == nil {
		return nil
	}
	return &streamStateLivePresence{sm: sm}
}

// LiveSourceNodes returns node IDs currently buffering the source plus a
// boolean indicating whether any are live. The semantic for "live" is
// at least one instance with BufferState == "FULL"; this matches the
// load balancer's notion of "actively serveable".
func (a *streamStateLivePresence) LiveSourceNodes(internalName string) ([]string, bool) {
	if a == nil || a.sm == nil || internalName == "" {
		return nil, false
	}
	instances := a.sm.GetStreamInstances(internalName)
	if len(instances) == 0 {
		return nil, false
	}
	nodes := make([]string, 0, len(instances))
	anyLive := false
	for nodeID, inst := range instances {
		if nodeID == "" {
			continue
		}
		nodes = append(nodes, nodeID)
		if inst.BufferState == "FULL" {
			anyLive = true
		}
	}
	return nodes, anyLive
}
