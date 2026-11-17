package control

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestRecordingWriterEndPersistsWhileManagerLockIsHeld(t *testing.T) {
	const runtime = "live+stream_fw1p_84ed1d3113d2468db7b5ef132d4e3ee9"
	dm := newDVRManagerWithMist(t, &handoffMist{})
	dir := filepath.Join(dm.storagePath, "dvr", "stream-id", "recording")
	if err := os.MkdirAll(filepath.Join(dir, "segments"), 0755); err != nil {
		t.Fatal(err)
	}
	confirmed := time.Now().Add(-time.Second).UnixMilli()
	if err := writeDVRJobMeta(dir, dvrJobMeta{InternalName: "stream", SourceRuntimeName: runtime, WriterConfirmedAtMillis: confirmed}); err != nil {
		t.Fatal(err)
	}
	dm.mutex.Lock()
	done := make(chan struct{})
	go func() {
		dm.noteWriterEnded(runtime, time.Now(), filepath.Join(dir, "segments", "last.ts"))
		close(done)
	}()
	select {
	case <-done:
		dm.mutex.Unlock()
	case <-time.After(time.Second):
		dm.mutex.Unlock()
		<-done
		t.Fatal("writer end deadlocked with recording startup")
	}
	if !dvrWriterEndedOnDisk(dir, runtime, confirmed) {
		t.Fatal("writer end proof did not survive independently of the job map")
	}
	if dvrWriterEndedOnDisk(dir, runtime, time.Now().Add(time.Second).UnixMilli()) {
		t.Fatal("an older writer end retired a later writer")
	}
}

func TestRecordingHandoffRequiresWriterExitFromSameController(t *testing.T) {
	old := mist.PushInfo{ID: 42, StreamName: "old", TargetURI: "target"}
	handoff := dvrSourceHandoff{OldRuntime: old.StreamName, OldTarget: old.TargetURI, OldWriterID: old.ID, ControllerID: "controller"}
	for _, test := range []struct {
		name     string
		snapshot mist.PushSnapshot
		exited   bool
	}{
		{"stop only dispatched", mist.PushSnapshot{ControllerID: "controller", Pushes: []mist.PushInfo{old}}, false},
		{"controller replaced", mist.PushSnapshot{ControllerID: "new-controller"}, false},
		{"same process id reused", mist.PushSnapshot{ControllerID: "controller", Pushes: []mist.PushInfo{{ID: 42, StreamName: "other", TargetURI: "other"}}}, false},
		{"writer exited", mist.PushSnapshot{ControllerID: "controller"}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := dvrOldWriterExited(handoff, test.snapshot); got != test.exited {
				t.Fatalf("writer exit proof=%v want %v", got, test.exited)
			}
		})
	}
}

type handoffMist struct {
	pushes            []mist.PushInfo
	starts, stops     int
	ambiguous         bool
	rejectAmbiguously bool
}

func (m *handoffMist) PushList() ([]mist.PushInfo, error) {
	return append([]mist.PushInfo(nil), m.pushes...), nil
}
func (m *handoffMist) GetPushSnapshot(context.Context) (mist.PushSnapshot, error) {
	return mist.PushSnapshot{ControllerID: "controller", Pushes: append([]mist.PushInfo(nil), m.pushes...)}, nil
}
func (m *handoffMist) PushStop(id int) error {
	m.stops++
	for i, p := range m.pushes {
		if p.ID == id {
			m.pushes = append(m.pushes[:i], m.pushes[i+1:]...)
			break
		}
	}
	return nil
}
func (m *handoffMist) PushStart(stream, target string) error {
	m.starts++
	if m.rejectAmbiguously {
		return mist.ErrMistAmbiguous
	}
	if len(m.pushes) != 0 {
		return errors.New("new writer overlaps old writer")
	}
	m.pushes = append(m.pushes, mist.PushInfo{ID: 43, StreamName: stream, TargetURI: target})
	if m.ambiguous {
		return mist.ErrMistAmbiguous
	}
	return nil
}

func TestRecordingHandoffLostReplyDoesNotStartSecondWriter(t *testing.T) {
	const old = "live+stream_fw1p_84ed1d3113d2468db7b5ef132d4e3ee9"
	const next = "live+stream_fw1p_faed1d3113d2468db7b5ef132d4e3ee9"
	client := &handoffMist{ambiguous: true}
	dm := newDVRManagerWithMist(t, client)
	dir := t.TempDir()
	job := &DVRJob{DVRHash: "recording", InternalName: "stream", SourceRuntimeName: old, StreamName: old, PushID: 42, OutputDir: dir,
		ManifestPath: filepath.Join(dir, "recording.m3u8"), Config: &ipcpb.DVRConfig{SegmentDuration: 2}, StartTime: time.Now(), Status: "recording", Logger: logging.NewLogger()}
	job.TargetURI = dvrTargetURI(dir, job.DVRHash, job.Config, old)
	client.pushes = []mist.PushInfo{{ID: 42, StreamName: old, TargetURI: job.TargetURI}}
	job.writerControllerID = "controller"
	if err := writeDVRJobMeta(dir, dvrMetaForJob(job)); err != nil {
		t.Fatal(err)
	}
	dm.jobs[job.DVRHash] = job
	req := &ipcpb.DVRStartRequest{DvrHash: job.DVRHash, InternalName: job.InternalName, SourceRuntimeName: next, PreviousSourceRuntimeName: old, Config: job.Config}
	if err := dm.RebindRecording(req, nil); !errors.Is(err, mist.ErrMistAmbiguous) {
		t.Fatalf("lost reply=%v", err)
	}
	meta, ok := readDVRJobMeta(dir)
	if !ok || meta.Handoff == nil || !meta.Handoff.OldEnded || !meta.Handoff.NewIssued {
		t.Fatalf("handoff lost durable recovery state: %+v", meta)
	}
	if err := dm.RebindRecording(req, nil); err != nil {
		t.Fatal(err)
	}
	if client.starts != 1 || client.stops != 1 || job.SourceRuntimeName != next || job.Status != "recording" {
		t.Fatalf("handoff did not converge once: starts=%d stops=%d job=%+v", client.starts, client.stops, job)
	}
	meta, ok = readDVRJobMeta(dir)
	if !ok || meta.Handoff != nil || meta.SourceRuntimeName != next {
		t.Fatalf("completed handoff descriptor=%+v", meta)
	}
}

func TestRecordingHandoffCrashBeforeIssuanceSurfacesPartialAndNeverReissues(t *testing.T) {
	const old = "live+stream_fw1p_84ed1d3113d2468db7b5ef132d4e3ee9"
	const next = "live+stream_fw1p_faed1d3113d2468db7b5ef132d4e3ee9"
	client := &handoffMist{}
	dm := newDVRManagerWithMist(t, client)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "segments"), 0755); err != nil {
		t.Fatal(err)
	}
	job := &DVRJob{DVRHash: "recording", InternalName: "stream", SourceRuntimeName: old, StreamName: old, OutputDir: dir,
		ManifestPath: filepath.Join(dir, "recording.m3u8"), Config: &ipcpb.DVRConfig{}, StartTime: time.Now(), Status: "rebinding", Logger: logging.NewLogger(),
		handoff: &dvrSourceHandoff{OldRuntime: old, OldEnded: true, NewRuntime: next, NewIssued: true, NewIssuerID: "previous-process"}}
	if err := writeDVRJobMeta(dir, dvrMetaForJob(job)); err != nil {
		t.Fatal(err)
	}
	dm.jobs[job.DVRHash] = job
	var result *ipcpb.DVRStopped
	req := &ipcpb.DVRStartRequest{DvrHash: job.DVRHash, InternalName: "stream", SourceRuntimeName: next, PreviousSourceRuntimeName: old}
	if err := dm.RebindRecording(req, func(msg *ipcpb.ControlMessage) { result = msg.GetDvrStopped() }); err != nil {
		t.Fatal(err)
	}
	if client.starts != 0 || result == nil || !result.GetWriterOutcomeUnknown() || result.GetSourceRuntimeName() != next || job.Status != "finalizing" {
		t.Fatalf("ambiguous restart silently resumed or stranded recording: starts=%d report=%+v status=%s", client.starts, result, job.Status)
	}
	meta, ok := readDVRJobMeta(dir)
	if !ok || meta.Handoff == nil || !meta.Handoff.Cancelled {
		t.Fatal("interrupted handoff was not durably cancelled")
	}
}

func TestRecordingHandoffUnknownNativeResultDoesNotRequireSidecarRestart(t *testing.T) {
	const old = "live+stream_fw1p_84ed1d3113d2468db7b5ef132d4e3ee9"
	const next = "live+stream_fw1p_faed1d3113d2468db7b5ef132d4e3ee9"
	client := &handoffMist{rejectAmbiguously: true}
	dm := newDVRManagerWithMist(t, client)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "segments"), 0755); err != nil {
		t.Fatal(err)
	}
	job := &DVRJob{DVRHash: "recording", InternalName: "stream", SourceRuntimeName: old, StreamName: old, OutputDir: dir,
		ManifestPath: filepath.Join(dir, "recording.m3u8"), Config: &ipcpb.DVRConfig{}, StartTime: time.Now(), Status: "rebinding", Logger: logging.NewLogger(),
		handoff: &dvrSourceHandoff{OldRuntime: old, OldEnded: true, NewRuntime: next}}
	if err := writeDVRJobMeta(dir, dvrMetaForJob(job)); err != nil {
		t.Fatal(err)
	}
	dm.jobs[job.DVRHash] = job
	var result *ipcpb.DVRStopped
	req := &ipcpb.DVRStartRequest{DvrHash: job.DVRHash, InternalName: "stream", SourceRuntimeName: next, PreviousSourceRuntimeName: old}
	send := func(msg *ipcpb.ControlMessage) { result = msg.GetDvrStopped() }
	if err := dm.RebindRecording(req, send); !errors.Is(err, mist.ErrMistAmbiguous) {
		t.Fatalf("unknown native result=%v", err)
	}
	if err := dm.RebindRecording(req, send); err != nil {
		t.Fatalf("recording remains stranded until sidecar restart: %v", err)
	}
	if client.starts != 1 || result == nil || !result.GetWriterOutcomeUnknown() || job.Status != "finalizing" {
		t.Fatalf("unknown result reopened or stranded the writer: starts=%d report=%+v status=%s", client.starts, result, job.Status)
	}
}
