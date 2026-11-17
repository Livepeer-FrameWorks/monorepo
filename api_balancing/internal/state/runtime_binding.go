package state

import (
	"encoding/json"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/streamident"
)

// BeginIsolatedRuntime changes the physical buffer whose readiness this node
// projects. Viewer counters remain logical-stream totals; old buffer readiness
// and input ownership cannot make the new process appear playable.
func (sm *StreamStateManager) BeginIsolatedRuntime(internal, node, tenant, runtime string) {
	binding, ok := streamident.ParseRuntimeBinding(runtime)
	if !ok || binding.InternalName != internal || node == "" || tenant == "" {
		return
	}
	sm.mu.Lock()
	if sm.streamInstances[internal] == nil {
		sm.streamInstances[internal] = make(map[string]*StreamInstanceState)
	}
	old := sm.streamInstances[internal][node]
	if old != nil && old.RuntimeName == runtime {
		sm.mu.Unlock()
		return
	}
	inst := &StreamInstanceState{NodeID: node, TenantID: tenant, RuntimeName: runtime, Status: "waiting", LastUpdate: time.Now(), Replicated: binding.Role == streamident.RuntimeRelay}
	if old != nil {
		inst.Viewers, inst.BytesUp, inst.BytesDown, inst.TotalConnections = old.Viewers, old.BytesUp, old.BytesDown, old.TotalConnections
	}
	sm.streamInstances[internal][node] = inst
	raw, err := json.Marshal(inst)
	sm.mu.Unlock()
	if err == nil {
		sm.persistStreamInstanceWriteThrough(internal, node, raw)
	}
}

func runtimeObservationMatches(inst *StreamInstanceState, runtime string) bool {
	if inst == nil || inst.RuntimeName == "" {
		return true
	}
	if _, bound := streamident.ParseRuntimeBinding(inst.RuntimeName); bound {
		return inst.RuntimeName == runtime
	}
	return true
}
