package state

import (
	"encoding/json"
)

// PlaybackAuthorityChange names a signed authority one replica applied: a
// media object (by internal name) or a tenant.
type PlaybackAuthorityChange struct {
	Kind         string `json:"kind"`
	InternalName string `json:"internal_name,omitempty"`
	TenantID     string `json:"tenant_id,omitempty"`
}

// SetPlaybackAuthorityChangeHandler installs what runs when a peer replica
// announces an applied authority. It runs on the changelog reader and must
// not block.
func (sm *StreamStateManager) SetPlaybackAuthorityChangeHandler(handler func(PlaybackAuthorityChange)) {
	sm.mu.Lock()
	sm.playbackAuthorityHandler = handler
	sm.mu.Unlock()
}

// AnnouncePlaybackAuthorityChange tells the other replicas of the cell that
// this one applied an authority. Only the replica that applies an authority
// observes it, while any replica may hold the control stream of an edge
// holding the stream's playback grant.
func (sm *StreamStateManager) AnnouncePlaybackAuthorityChange(change PlaybackAuthorityChange) {
	if sm.redisStore == nil {
		return
	}
	payload, err := json.Marshal(change)
	if err != nil {
		return
	}
	key := change.InternalName
	if key == "" {
		key = "tenant:" + change.TenantID
	}
	sm.publishStateChange(StateChange{
		InstanceID: sm.instanceID, Entity: StateEntityPlaybackAuthority, Operation: StateOpUpsert,
		StreamName: key, Payload: payload,
	})
}

func (sm *StreamStateManager) notifyPlaybackAuthorityChange(change StateChange) {
	var announced PlaybackAuthorityChange
	if err := json.Unmarshal(change.Payload, &announced); err != nil {
		if stateLogger != nil {
			stateLogger.WithError(err).Warn("Ignoring an unreadable playback authority announcement")
		}
		return
	}
	sm.mu.RLock()
	handler := sm.playbackAuthorityHandler
	sm.mu.RUnlock()
	if handler != nil {
		handler(announced)
	}
}
