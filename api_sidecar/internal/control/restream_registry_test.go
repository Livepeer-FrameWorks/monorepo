package control

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestInstallRestreamDesiredStateClassifiesSupersededRevision(t *testing.T) {
	const streamName = "live+registry-revision"
	restreamRegistry.Lock()
	delete(restreamRegistry.streams, streamName)
	restreamRegistry.Unlock()
	t.Cleanup(func() {
		restreamRegistry.Lock()
		delete(restreamRegistry.streams, streamName)
		restreamRegistry.Unlock()
	})

	request := func(revision int64, uri string) *ipcpb.ActivatePushTargets {
		return &ipcpb.ActivatePushTargets{
			StreamName: streamName, SourceGeneration: "generation-a", TargetRevision: revision,
			Targets: []*ipcpb.PushTargetSpec{{TargetId: "target-a", TargetUri: uri}},
		}
	}
	if _, result := installRestreamDesiredState(request(7, "rtmp://example.com/new")); result != restreamInstallApplied {
		t.Fatalf("revision 7 result=%v, want applied", result)
	}
	state, result := installRestreamDesiredState(request(6, "rtmp://example.com/old"))
	if result != restreamInstallSuperseded {
		t.Fatalf("revision 6 result=%v, want superseded", result)
	}
	if state.targetRevision != 7 {
		t.Fatalf("superseded install replaced authoritative state: revision=%d", state.targetRevision)
	}
}

func TestRestreamRegistryRejectsRetiredControlEpochAtSameRevision(t *testing.T) {
	const streamName = "live+registry-control-epoch"
	previousConnection := getConnection()
	oldConnection := &streamConn{epoch: "control-old"}
	newConnection := &streamConn{epoch: "control-new"}
	activeConn.Store(newConnection)
	restreamRegistry.Lock()
	delete(restreamRegistry.streams, streamName)
	restreamRegistry.Unlock()
	t.Cleanup(func() {
		activeConn.Store(previousConnection)
		restreamRegistry.Lock()
		delete(restreamRegistry.streams, streamName)
		restreamRegistry.Unlock()
	})

	request := func(attempt, uri string) *ipcpb.ActivatePushTargets {
		return &ipcpb.ActivatePushTargets{
			StreamName: streamName, SourceGeneration: "generation-a", TargetRevision: 7,
			ActivationAttempt: attempt,
			Targets:           []*ipcpb.PushTargetSpec{{TargetId: "target-a", TargetUri: uri}},
		}
	}
	if _, result := installRestreamDesiredStateForControlEpoch(request("attempt-new", "rtmp://example.com/new"), newConnection.epoch, 2); result != restreamInstallApplied {
		t.Fatalf("new control epoch result=%v, want applied", result)
	}
	state, result := installRestreamDesiredStateForControlEpoch(request("attempt-old", "rtmp://example.com/old"), oldConnection.epoch, 1)
	if result != restreamInstallSuperseded {
		t.Fatalf("retired control epoch result=%v, want superseded", result)
	}
	current := state
	if current.sourceGeneration == "" {
		restreamRegistry.RLock()
		current = restreamRegistry.streams[streamName]
		restreamRegistry.RUnlock()
	}
	if target := current.byID["target-a"]; target.activationAttempt != "attempt-new" || target.targetURI != "rtmp://example.com/new" {
		t.Fatalf("retired control epoch replaced current state: %+v", target)
	}
}

func TestRestreamRegistryRejectsEarlierDispatchAtSameAttemptRevision(t *testing.T) {
	const streamName = "live+registry-dispatch-sequence"
	previousConnection := getConnection()
	connection := &streamConn{epoch: "control-current"}
	activeConn.Store(connection)
	restreamRegistry.Lock()
	delete(restreamRegistry.streams, streamName)
	restreamRegistry.Unlock()
	t.Cleanup(func() {
		activeConn.Store(previousConnection)
		restreamRegistry.Lock()
		delete(restreamRegistry.streams, streamName)
		restreamRegistry.Unlock()
	})

	request := func(attempt, uri string) *ipcpb.ActivatePushTargets {
		return &ipcpb.ActivatePushTargets{
			StreamName: streamName, SourceGeneration: "generation-a", TargetRevision: 7,
			ActivationAttempt: attempt,
			Targets:           []*ipcpb.PushTargetSpec{{TargetId: "target-a", TargetUri: uri}},
		}
	}
	if _, result := installRestreamDesiredStateForControlEpoch(request("attempt-new", "rtmp://example.com/new"), connection.epoch, 2); result != restreamInstallApplied {
		t.Fatalf("later dispatch result=%v, want applied", result)
	}
	state, result := installRestreamDesiredStateForControlEpoch(request("attempt-old", "rtmp://example.com/old"), connection.epoch, 1)
	if result != restreamInstallSuperseded {
		t.Fatalf("earlier dispatch result=%v, want superseded", result)
	}
	if target := state.byID["target-a"]; target.activationAttempt != "attempt-new" || target.targetURI != "rtmp://example.com/new" {
		t.Fatalf("earlier dispatch replaced current state: %+v", target)
	}
}

func TestResolveRestreamTargetFencesReusedURIByMistPushID(t *testing.T) {
	const (
		streamName = "live+registry-reused-uri"
		targetURI  = "rtmp://example.com/live/same-destination"
	)
	restreamRegistry.Lock()
	delete(restreamRegistry.streams, streamName)
	restreamRegistry.Unlock()
	t.Cleanup(func() {
		restreamRegistry.Lock()
		delete(restreamRegistry.streams, streamName)
		restreamRegistry.Unlock()
	})

	request := func(generation string, revision int64) *ipcpb.ActivatePushTargets {
		return &ipcpb.ActivatePushTargets{
			TenantId: "tenant-a", StreamId: "stream-a", StreamName: streamName,
			SourceGeneration: generation, TargetRevision: revision,
			Targets: []*ipcpb.PushTargetSpec{{TargetId: "target-a", Platform: "youtube", TargetUri: targetURI}},
		}
	}
	if _, result := installRestreamDesiredState(request("generation-a", 1)); result != restreamInstallApplied {
		t.Fatalf("generation-a install=%v", result)
	}
	bindRestreamPushIDs(streamName, "generation-a", 1, []mist.PushInfo{{ID: 7, StreamName: streamName, TargetURI: targetURI}})
	if _, result := installRestreamDesiredState(request("generation-b", 2)); result != restreamInstallApplied {
		t.Fatalf("generation-b install=%v", result)
	}
	bindRestreamPushIDs(streamName, "generation-b", 2, []mist.PushInfo{{ID: 8, StreamName: streamName, TargetURI: targetURI}})

	oldReport, ok := ResolveRestreamTarget(streamName, 7, targetURI, "")
	if !ok || oldReport.GetSourceGeneration() != "generation-a" || oldReport.GetTargetRevision() != 1 {
		t.Fatalf("old push resolved to %+v, ok=%v", oldReport, ok)
	}
	newReport, ok := ResolveRestreamTarget(streamName, 8, targetURI, "")
	if !ok || newReport.GetSourceGeneration() != "generation-b" || newReport.GetTargetRevision() != 2 {
		t.Fatalf("new push resolved to %+v, ok=%v", newReport, ok)
	}
	if report, ok := ResolveRestreamTarget(streamName, 9, targetURI, ""); ok || report != nil {
		t.Fatalf("unknown non-zero push ID rebound by URI: %+v", report)
	}
}

func TestUnchangedPushIDStaysCurrentDuringRevisionInstall(t *testing.T) {
	const (
		streamName = "live+registry-unchanged"
		targetURI  = "rtmp://example.com/live/unchanged"
	)
	restreamRegistry.Lock()
	delete(restreamRegistry.streams, streamName)
	restreamRegistry.Unlock()
	t.Cleanup(func() {
		restreamRegistry.Lock()
		delete(restreamRegistry.streams, streamName)
		restreamRegistry.Unlock()
	})
	request := func(revision int64) *ipcpb.ActivatePushTargets {
		return &ipcpb.ActivatePushTargets{
			TenantId: "tenant-a", StreamId: "stream-a", StreamName: streamName,
			SourceGeneration: "generation-a", TargetRevision: revision,
			Targets: []*ipcpb.PushTargetSpec{{TargetId: "target-a", Platform: "youtube", TargetUri: targetURI}},
		}
	}
	if _, result := installRestreamDesiredState(request(1)); result != restreamInstallApplied {
		t.Fatalf("revision 1 install=%v", result)
	}
	bindRestreamPushIDs(streamName, "generation-a", 1, []mist.PushInfo{{ID: 7, StreamName: streamName, TargetURI: targetURI}})
	if _, result := installRestreamDesiredState(request(2)); result != restreamInstallApplied {
		t.Fatalf("revision 2 install=%v", result)
	}

	report, ok := ResolveRestreamTarget(streamName, 7, targetURI, "")
	if !ok || report.GetTargetRevision() != 2 || report.GetSourceGeneration() != "generation-a" {
		t.Fatalf("unchanged push ID was retired during revision install: report=%+v ok=%v", report, ok)
	}
}

func TestRestreamRegistryEchoesActivationAttemptOnRuntimeReport(t *testing.T) {
	const streamName = "live+registry-attempt"
	restreamRegistry.Lock()
	delete(restreamRegistry.streams, streamName)
	restreamRegistry.Unlock()
	t.Cleanup(func() {
		restreamRegistry.Lock()
		delete(restreamRegistry.streams, streamName)
		restreamRegistry.Unlock()
	})
	request := &ipcpb.ActivatePushTargets{
		TenantId: "tenant-a", StreamId: "stream-a", StreamName: streamName,
		SourceGeneration: "generation-a", TargetRevision: 7, ActivationAttempt: "attempt-a",
		Targets: []*ipcpb.PushTargetSpec{{TargetId: "target-a", TargetUri: "rtmp://example.test/live/key"}},
	}
	if _, result := installRestreamDesiredState(request); result != restreamInstallApplied {
		t.Fatalf("install result=%v", result)
	}
	reports := restreamTargetReports(streamName, "generation-a")
	if len(reports) != 1 || reports[0].GetActivationAttempt() != "attempt-a" {
		t.Fatalf("runtime report did not echo activation attempt: %+v", reports)
	}
}
