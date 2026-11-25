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

// BeginLegacyPublisherRuntime marks the node that was admitted a publisher under the stream's unbound
// name as having the input, so the balancer picks it before its first STREAM_BUFFER. Such an
// admission happens during a control-plane outage, when no runtime binding can be minted. When the
// node last held a bound runtime, the unbound name is another physical buffer, begun like an
// isolated runtime: the ended buffer's readiness does not carry over, its late callbacks are
// refused, and the new buffer's own callbacks are accepted.
func (sm *StreamStateManager) BeginLegacyPublisherRuntime(internal, node, tenant string) {
	if internal == "" || node == "" {
		return
	}
	runtime := "live+" + internal
	sm.mu.Lock()
	old := sm.streamInstances[internal][node]
	if old == nil || old.RuntimeName == "" || old.RuntimeName == runtime {
		sm.mu.Unlock()
		sm.SetStreamInstanceInputs(internal, node, 1)
		return
	}
	if sm.streamInstances[internal] == nil {
		sm.streamInstances[internal] = make(map[string]*StreamInstanceState)
	}
	inst := &StreamInstanceState{NodeID: node, TenantID: tenant, RuntimeName: runtime, Status: "waiting", Inputs: 1, LastUpdate: time.Now()}
	inst.Viewers, inst.BytesUp, inst.BytesDown, inst.TotalConnections = old.Viewers, old.BytesUp, old.BytesDown, old.TotalConnections
	sm.streamInstances[internal][node] = inst
	raw, err := json.Marshal(inst)
	sm.mu.Unlock()
	if err == nil {
		sm.persistStreamInstanceWriteThrough(internal, node, raw)
	}
}

// runtimeObservationMatches reports whether a callback for runtime belongs to the physical buffer
// the instance projects. An instance without a runtime name predates runtime isolation and accepts
// every callback; one with a name, bound or the unbound publisher name, accepts its own only.
func runtimeObservationMatches(inst *StreamInstanceState, runtime string) bool {
	if inst == nil || inst.RuntimeName == "" {
		return true
	}
	return inst.RuntimeName == runtime
}
