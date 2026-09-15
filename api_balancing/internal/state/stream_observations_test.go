package state

import (
	"encoding/json"
	"testing"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
)

func processObservationFixture() *ipcpb.MistStreamProcessObservation {
	return &ipcpb.MistStreamProcessObservation{
		RuntimeName: "live+stream", BufferPid: proto.Int64(90), SourcePidsKnown: true, SourcePids: []int64{21, 23},
		FirstMediaMs: proto.Uint64(0), LastMediaMs: proto.Uint64(1000), ReadStartedUnixMillis: 100, ReadCompletedUnixMillis: 110,
	}
}

func TestStreamObservationsRemainDetachedAndEphemeral(t *testing.T) {
	sm := NewStreamStateManager()
	sm.UpdateNodeStats("stream", "node", 0, 1, 0, 0, true)
	before := sm.GetStreamInstances("stream")["node"]
	process := processObservationFixture()
	buffer := StreamBufferObservation{RuntimeName: "live+stream", BufferPID: 90, State: "FULL", EventID: "event", EventUnixMillis: 105}
	if !sm.ObserveStreamProcesses("stream", "node", process) || !sm.ObserveStreamBuffer("stream", "node", buffer) {
		t.Fatal("valid observations rejected")
	}
	process.SourcePids[0] = 999
	*process.BufferPid = 999
	for _, snapshot := range []StreamInstanceState{sm.GetStreamInstances("stream")["node"], sm.GetAllStreamInstances()["stream"]["node"]} {
		if snapshot.ProcessObservation.GetBufferPid() != 90 || snapshot.ProcessObservation.SourcePids[0] != 21 || snapshot.BufferObservation.BufferPID != 90 {
			t.Fatal("retained caller-owned memory")
		}
		if !snapshot.LastUpdate.Equal(before.LastUpdate) || snapshot.Status != before.Status || snapshot.Replicated != before.Replicated {
			t.Fatal("observations changed operational liveness or source status")
		}
		snapshot.ProcessObservation.SourcePids[0] = 999
		snapshot.BufferObservation.BufferPID = 999
	}
	snapshot := sm.GetStreamInstances("stream")["node"]
	if snapshot.ProcessObservation.SourcePids[0] != 21 || snapshot.BufferObservation.BufferPID != 90 {
		t.Fatal("snapshots share observation storage")
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var restored StreamInstanceState
	if err = json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.ProcessObservation != nil || restored.BufferObservation != nil {
		t.Fatal("restart restored old process evidence")
	}
	sm.SetOffline("stream", "node")
	snapshot = sm.GetStreamInstances("stream")["node"]
	if snapshot.ProcessObservation != nil || snapshot.BufferObservation != nil || sm.ObserveStreamProcesses("stream", "node", processObservationFixture()) || sm.ObserveStreamBuffer("stream", "node", buffer) {
		t.Fatal("offline instance retained or accepted process evidence")
	}
	sm.UpdateNodeStats("stream", "node", 0, 1, 0, 0, true)
	if !sm.ObserveStreamProcesses("stream", "node", processObservationFixture()) || !sm.ObserveStreamBuffer("stream", "node", buffer) {
		t.Fatal("new active instance rejected observations")
	}
	sm.ReconcileNodeStreamPresence("node", nil)
	snapshot = sm.GetStreamInstances("stream")["node"]
	if snapshot.ProcessObservation != nil || snapshot.BufferObservation != nil {
		t.Fatal("inventory reconciliation retained vanished process evidence")
	}
}

func TestStreamObservationsRejectStaleAndInvalidSamples(t *testing.T) {
	sm := NewStreamStateManager()
	if sm.ObserveStreamProcesses("stream", "missing", processObservationFixture()) {
		t.Fatal("observation created a stream instance")
	}
	sm.UpdateNodeStats("stream", "node", 0, 1, 0, 0, false)
	if !sm.ObserveStreamProcesses("stream", "node", processObservationFixture()) {
		t.Fatal("initial sample rejected")
	}
	for name, change := range map[string]func(*ipcpb.MistStreamProcessObservation){
		"different stream":        func(o *ipcpb.MistStreamProcessObservation) { o.RuntimeName = "live+other" },
		"no read window":          func(o *ipcpb.MistStreamProcessObservation) { o.ReadStartedUnixMillis = 0 },
		"backwards window":        func(o *ipcpb.MistStreamProcessObservation) { o.ReadCompletedUnixMillis = 99 },
		"unknown nonempty census": func(o *ipcpb.MistStreamProcessObservation) { o.SourcePidsKnown = false },
		"invalid buffer pid":      func(o *ipcpb.MistStreamProcessObservation) { o.BufferPid = proto.Int64(0) },
		"invalid source pid":      func(o *ipcpb.MistStreamProcessObservation) { o.SourcePids = []int64{0} },
		"duplicate source pid":    func(o *ipcpb.MistStreamProcessObservation) { o.SourcePids = []int64{21, 21} },
		"unsorted source pids":    func(o *ipcpb.MistStreamProcessObservation) { o.SourcePids = []int64{23, 21} },
	} {
		t.Run(name, func(t *testing.T) {
			observation := processObservationFixture()
			observation.ReadStartedUnixMillis = 120
			observation.ReadCompletedUnixMillis = 130
			change(observation)
			if sm.ObserveStreamProcesses("stream", "node", observation) {
				t.Fatal("invalid sample accepted")
			}
		})
	}
	if sm.ObserveStreamProcesses("stream", "node", processObservationFixture()) {
		t.Fatal("duplicate read accepted")
	}
	newer := processObservationFixture()
	newer.ReadStartedUnixMillis = 120
	newer.ReadCompletedUnixMillis = 130
	newer.SourcePidsKnown = false
	newer.SourcePids = nil
	if !sm.ObserveStreamProcesses("stream", "node", newer) || sm.ObserveStreamProcesses("stream", "node", processObservationFixture()) {
		t.Fatal("new unknown sample did not supersede older known census")
	}
	buffer := StreamBufferObservation{RuntimeName: "live+stream", BufferPID: 90, State: "FULL", EventID: "event", EventUnixMillis: 105}
	if !sm.ObserveStreamBuffer("stream", "node", buffer) || sm.ObserveStreamBuffer("stream", "node", buffer) {
		t.Fatal("buffer event replay was not fenced")
	}
	buffer.EventUnixMillis = 104
	if sm.ObserveStreamBuffer("stream", "node", buffer) {
		t.Fatal("older buffer event replaced current observation")
	}
	buffer.EventUnixMillis = 106
	buffer.State = "DRY"
	if !sm.ObserveStreamBuffer("stream", "node", buffer) || sm.GetStreamInstances("stream")["node"].BufferObservation.State != "DRY" {
		t.Fatal("DRY did not supersede FULL")
	}
}

// A periodic playability level restores readiness for a live instance without
// replacing Mist's native diagnostic state and is ordered
// against STREAM_BUFFER edges by time in both directions.
func TestObserveStreamPlayabilityLevelOrdersAgainstEdges(t *testing.T) {
	sm := NewStreamStateManager()
	sm.UpdateNodeStats("stream", "node", 0, 1, 0, 0, false)
	sm.streamInstances["stream"]["node"].BufferState = "DRY"
	sm.streams["stream"].BufferState = "DRY"

	if !sm.ObserveStreamPlayabilityLevel("stream", "node", true, 1_000) {
		t.Fatal("first level was not applied")
	}
	if inst := sm.streamInstances["stream"]["node"]; !inst.Playable || inst.BufferState != "DRY" || inst.BufferPlayableSampledUnixMillis != 1_000 {
		t.Fatalf("instance after level: %+v", inst)
	}
	if union := sm.streams["stream"]; union == nil || !union.Playable || union.BufferState != "DRY" {
		t.Fatalf("union after level: %+v", union)
	}

	// An older level cannot regress a newer one.
	if sm.ObserveStreamPlayabilityLevel("stream", "node", false, 900) {
		t.Fatal("older level was applied")
	}
	if !sm.streamInstances["stream"]["node"].Playable {
		t.Fatal("older level changed the state")
	}

	// A newer edge wins over the level, and a level older than that edge is ignored.
	if !sm.ObserveStreamBuffer("stream", "node", StreamBufferObservation{RuntimeName: "live+stream", BufferPID: 7, State: "DRY", EventID: "e1", EventUnixMillis: 2_000}) {
		t.Fatal("newer edge was not recorded")
	}
	if sm.ObserveStreamPlayabilityLevel("stream", "node", false, 1_500) {
		t.Fatal("level older than the edge was applied")
	}
	if !sm.PlayabilityLevelNewerThan("stream", "node", 500) || sm.PlayabilityLevelNewerThan("stream", "node", 1_000) {
		t.Fatal("PlayabilityLevelNewerThan must compare strictly against the applied level")
	}

	// A level newer than the edge applies and makes an even older edge stale.
	if !sm.ObserveStreamPlayabilityLevel("stream", "node", false, 3_000) {
		t.Fatal("level newer than the edge was not applied")
	}
	if sm.streamInstances["stream"]["node"].Playable || sm.streamInstances["stream"]["node"].BufferState != "DRY" {
		t.Fatal("playability level overwrote the native state or failed to apply")
	}
	if !sm.PlayabilityLevelNewerThan("stream", "node", 2_500) {
		t.Fatal("edge older than the newest level must be reported stale")
	}

	// Offline instances are never revived.
	sm.SetOffline("stream", "node")
	if sm.ObserveStreamPlayabilityLevel("stream", "node", true, 5_000) {
		t.Fatal("a level revived an offline instance")
	}
}

// After a Foghorn restart the live instance is rebuilt from node reports without
// identity; the resolved stream context restores it, and it never overwrites a
// tenant with another value or fabricates an instance.
func TestApplyStreamInstanceIdentityRestoresResolvedTenant(t *testing.T) {
	sm := NewStreamStateManager()
	if sm.ApplyStreamInstanceIdentity("stream", "node", "tenant-a") {
		t.Fatal("identity applied to an instance that does not exist")
	}
	sm.UpdateNodeStats("stream", "node", 0, 1, 0, 0, false)
	if !sm.ApplyStreamInstanceIdentity("stream", "node", "tenant-a") {
		t.Fatal("resolved tenant was not restored on a blank instance")
	}
	if inst := sm.streamInstances["stream"]["node"]; inst.TenantID != "tenant-a" {
		t.Fatalf("instance tenant = %q", inst.TenantID)
	}
	if union := sm.streams["stream"]; union == nil || union.TenantID != "tenant-a" {
		t.Fatalf("union tenant = %+v", union)
	}
	if sm.ApplyStreamInstanceIdentity("stream", "node", "tenant-a") {
		t.Fatal("re-applying the same tenant reported a change")
	}
	if sm.ApplyStreamInstanceIdentity("stream", "node", "  ") {
		t.Fatal("blank tenant was applied")
	}
}
