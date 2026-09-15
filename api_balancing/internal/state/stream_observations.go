package state

import (
	"encoding/json"
	"strings"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
)

// StreamBufferObservation describes the buffer emitter, not the publisher or
// physical pull. It is not a generation-bound readiness attestation.
type StreamBufferObservation struct {
	RuntimeName     string `json:"runtime_name"`
	BufferPID       int64  `json:"buffer_pid"`
	State           string `json:"state"`
	EventID         string `json:"event_id"`
	EventUnixMillis int64  `json:"event_unix_millis"`
}

func cloneProcessObservation(observation *ipcpb.MistStreamProcessObservation) *ipcpb.MistStreamProcessObservation {
	if observation == nil {
		return nil
	}
	if cloned, ok := proto.Clone(observation).(*ipcpb.MistStreamProcessObservation); ok {
		return cloned
	}
	return nil
}

func cloneBufferObservation(observation *StreamBufferObservation) *StreamBufferObservation {
	if observation == nil {
		return nil
	}
	copy := *observation
	return &copy
}

// ObserveStreamProcesses retains the original read window without refreshing
// operational liveness or inferring source ownership from numeric PIDs. These
// observations are process-local: rehydration requires a fresh node report.
func (sm *StreamStateManager) ObserveStreamProcesses(internalName, nodeID string, observation *ipcpb.MistStreamProcessObservation) bool {
	if nodeID == "" || observation == nil || observation.GetRuntimeName() == "" || mist.ExtractInternalName(observation.GetRuntimeName()) != internalName ||
		observation.GetReadStartedUnixMillis() <= 0 || observation.GetReadCompletedUnixMillis() < observation.GetReadStartedUnixMillis() {
		return false
	}
	if !observation.GetSourcePidsKnown() && len(observation.GetSourcePids()) > 0 {
		return false
	}
	for i, pid := range observation.GetSourcePids() {
		if pid <= 0 || (i > 0 && pid <= observation.SourcePids[i-1]) {
			return false
		}
	}
	if observation.BufferPid != nil && observation.GetBufferPid() <= 0 {
		return false
	}
	sm.mu.Lock()
	instance := sm.streamInstances[internalName][nodeID]
	if instance == nil || instance.Status == "offline" || (instance.ProcessObservation != nil &&
		instance.ProcessObservation.GetReadStartedUnixMillis() >= observation.GetReadStartedUnixMillis()) {
		sm.mu.Unlock()
		return false
	}
	instance.ProcessObservation = cloneProcessObservation(observation)
	sm.mu.Unlock()
	return true
}

func (sm *StreamStateManager) ObserveStreamBuffer(internalName, nodeID string, observation StreamBufferObservation) bool {
	if nodeID == "" || observation.RuntimeName == "" || mist.ExtractInternalName(observation.RuntimeName) != internalName ||
		observation.BufferPID <= 0 || observation.EventID == "" || observation.EventUnixMillis <= 0 {
		return false
	}
	switch observation.State {
	case "FULL", "DRY", "RECOVER", "EMPTY":
	default:
		return false
	}
	sm.mu.Lock()
	instance := sm.streamInstances[internalName][nodeID]
	if instance == nil || instance.Status == "offline" || (instance.BufferObservation != nil &&
		instance.BufferObservation.EventUnixMillis >= observation.EventUnixMillis) {
		sm.mu.Unlock()
		return false
	}
	instance.BufferObservation = &observation
	sm.mu.Unlock()
	return true
}

// ObserveStreamPlayabilityLevel applies the readiness carried on a node's periodic
// stream report, sampled from Mist's API at sampledUnixMillis. A level is
// authoritative only when it is newer than the last applied STREAM_BUFFER edge
// and the last applied level; an offline instance is never revived by a level,
// the live report's stats update revives it first. It returns whether the level
// was applied; state is persisted only when it changed.
func (sm *StreamStateManager) ObserveStreamPlayabilityLevel(internalName, nodeID string, playable bool, sampledUnixMillis int64) bool {
	if internalName == "" || nodeID == "" || sampledUnixMillis <= 0 {
		return false
	}
	sm.mu.Lock()
	instance := sm.streamInstances[internalName][nodeID]
	if instance == nil || instance.Status == "offline" || instance.BufferPlayableSampledUnixMillis >= sampledUnixMillis ||
		(instance.BufferObservation != nil && instance.BufferObservation.EventUnixMillis >= sampledUnixMillis) {
		sm.mu.Unlock()
		return false
	}
	instance.BufferPlayableSampledUnixMillis = sampledUnixMillis
	changed := instance.Playable != playable
	instance.Playable = playable
	union := sm.streams[internalName]
	var streamPayload []byte
	if union != nil {
		unionPlayable := sm.anyPlayableInstanceLocked(internalName)
		if union.Playable != unionPlayable {
			union.Playable = unionPlayable
			changed = true
		}
		streamPayload = marshalStateOrNil(union)
	}
	instPayload := marshalStateOrNil(instance)
	sm.mu.Unlock()
	if changed {
		if streamPayload != nil {
			sm.persistStreamWriteThrough(internalName, streamPayload)
		}
		if instPayload != nil {
			sm.persistStreamInstanceWriteThrough(internalName, nodeID, instPayload)
		}
	}
	return true
}

// marshalStateOrNil encodes a state value for write-through, returning nil when
// it cannot be encoded so the caller skips the write instead of persisting an
// empty payload over good state.
func marshalStateOrNil(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		// Callers hold sm.mu without a deferred unlock, so a nil logger here
		// would panic with the lock held and wedge every later state call.
		if stateLogger != nil {
			stateLogger.WithError(err).Warn("Dropping state write-through: value could not be encoded")
		}
		return nil
	}
	return encoded
}

// PlayabilityLevelNewerThan reports whether a periodic level newer than eventUnixMillis
// has been applied for the instance, in which case a STREAM_BUFFER edge with that
// event time is stale and must not be applied.
func (sm *StreamStateManager) PlayabilityLevelNewerThan(internalName, nodeID string, eventUnixMillis int64) bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	instance := sm.streamInstances[internalName][nodeID]
	return instance != nil && instance.BufferPlayableSampledUnixMillis > eventUnixMillis
}

// ApplyStreamInstanceIdentity stamps the resolved owner tenant on a node's stream
// instance and the stream union. A restarted Foghorn rebuilds live stream state
// from node reports, which carry no verified identity; the publisher claim needs
// the instance to name the same tenant as the registry, so the lifecycle path
// restores it from the resolved stream context (never from a node-asserted
// tenant). It follows applyIdentity: a resolved tenant overwrites nothing else.
func (sm *StreamStateManager) ApplyStreamInstanceIdentity(internalName, nodeID, tenantID string) bool {
	tenantID = strings.TrimSpace(tenantID)
	if internalName == "" || nodeID == "" || tenantID == "" {
		return false
	}
	sm.mu.Lock()
	instance := sm.streamInstances[internalName][nodeID]
	if instance == nil {
		sm.mu.Unlock()
		return false
	}
	changed := instance.TenantID != tenantID
	applyIdentity(&instance.TenantID, tenantID)
	var streamPayload []byte
	if union := sm.streams[internalName]; union != nil && union.TenantID != tenantID {
		applyIdentity(&union.TenantID, tenantID)
		changed = true
		streamPayload = marshalStateOrNil(union)
	}
	instPayload := marshalStateOrNil(instance)
	sm.mu.Unlock()
	if changed {
		if streamPayload != nil {
			sm.persistStreamWriteThrough(internalName, streamPayload)
		}
		if instPayload != nil {
			sm.persistStreamInstanceWriteThrough(internalName, nodeID, instPayload)
		}
	}
	return changed
}
