package control

import (
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

const finalSegmentManifestHead = "#EXTM3U\n#EXT-X-PROGRAM-DATE-TIME:2026-10-12T20:49:21.424Z\n#EXTINF:6.000,\nsegments/49_33.ts\n"
const finalSegmentManifestTail = "#EXTINF:4.344,\nsegments/49_34.ts\n"

// ledgerRecorder answers every RecordDVRSegment request (rejecting it, so no
// upload follows) and remembers which segments were offered to the ledger.
type ledgerRecorder struct {
	mu       sync.Mutex
	recorded []string
}

func (l *ledgerRecorder) serve(t *testing.T, stream *fakeControlStream, done <-chan struct{}) {
	t.Helper()
	go func() {
		for {
			select {
			case msg := <-stream.sendCh:
				if req := msg.GetRecordDvrSegmentRequest(); req != nil {
					l.mu.Lock()
					l.recorded = append(l.recorded, req.GetSegmentName())
					l.mu.Unlock()
					handleRecordDVRSegmentResponse(&ipcpb.RecordDVRSegmentResponse{RequestId: req.GetRequestId(), Reason: "test"})
				}
			case <-done:
				return
			}
		}
	}()
}

func (l *ledgerRecorder) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.recorded)
}

func finalSegmentJob(t *testing.T, ledger *ledgerRecorder, stoppedWith chan<- []string) *DVRJob {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "segments"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"49_33.ts", "49_34.ts"} {
		if err := os.WriteFile(filepath.Join(dir, "segments", name), []byte("ts"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &DVRJob{
		DVRHash:          "dvrhash",
		PushID:           42,
		StreamName:       "live+test",
		TargetURI:        filepath.Join(dir, "dvrhash.m3u8"),
		OutputDir:        dir,
		ManifestPath:     filepath.Join(dir, "dvrhash.m3u8"),
		Status:           "recording",
		SegmentCount:     34,
		MaxRetries:       10,
		Logger:           logging.NewLogger(),
		SyncedSegments:   map[string]bool{"49_33.ts": true},
		UnmarkedSegments: map[string]uint64{},
		SendFunc: func(msg *ipcpb.ControlMessage) {
			if msg.GetDvrStopped() != nil {
				stoppedWith <- ledger.snapshot()
			}
		},
	}
}

func awaitStopped(t *testing.T, stoppedWith <-chan []string) []string {
	t.Helper()
	select {
	case recorded := <-stoppedWith:
		return recorded
	case <-time.After(5 * time.Second):
		t.Fatal("no DVRStopped report")
		return nil
	}
}

// Foghorn rejects every segment once DVRStopped claims the recording, so the
// writer's final segment must be offered to the ledger before the report.
// Scenario 04 lost its 4.344 s tail segment when Mist finished the push and
// the monitor reported completion while that segment's RECORDING_SEGMENT
// sync was still in flight.
func TestPushGoneCompletionLedgersTheFinalSegmentFirst(t *testing.T) {
	stream := connectFake(t)
	done := make(chan struct{})
	defer close(done)
	ledger := &ledgerRecorder{}
	ledger.serve(t, stream, done)

	stoppedWith := make(chan []string, 1)
	job := finalSegmentJob(t, ledger, stoppedWith)
	if err := os.WriteFile(job.ManifestPath, []byte(finalSegmentManifestHead+finalSegmentManifestTail), 0o644); err != nil {
		t.Fatal(err)
	}
	dm := newDVRManagerWithMist(t, &fakeMistClient{pushListItems: []mist.PushInfo{}})
	dm.jobs[job.DVRHash] = job

	dm.maintainPushStatus(job)

	if recorded := awaitStopped(t, stoppedWith); !slices.Contains(recorded, "49_34.ts") {
		t.Fatalf("DVRStopped sent before the final segment reached the ledger (offered: %v)", recorded)
	}
}

// exitingWriterMist lists the stopped push for a few polls, then drops it and
// only then lets the final segment appear in the playlist, as Mist does when
// a stopped writer closes its last segment on exit.
type exitingWriterMist struct {
	mu        sync.Mutex
	stopped   bool
	listed    int
	push      mist.PushInfo
	onExit    func()
	exitAfter int
}

func (m *exitingWriterMist) PushStart(string, string) error { return nil }

func (m *exitingWriterMist) PushStop(int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopped = true
	return nil
}

func (m *exitingWriterMist) PushList() ([]mist.PushInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.stopped {
		return []mist.PushInfo{m.push}, nil
	}
	m.listed++
	if m.listed < m.exitAfter {
		return []mist.PushInfo{m.push}, nil
	}
	if m.onExit != nil {
		m.onExit()
		m.onExit = nil
	}
	return nil, nil
}

// A Foghorn stop lands while Mist is still writing: PushStop only asks the
// writer to finish, and its final segment reaches the playlist as it exits.
// The final sync must run after that exit.
func TestStopRecordingLedgersTheSegmentTheStoppedWriterCloses(t *testing.T) {
	stream := connectFake(t)
	done := make(chan struct{})
	defer close(done)
	ledger := &ledgerRecorder{}
	ledger.serve(t, stream, done)

	stoppedWith := make(chan []string, 1)
	job := finalSegmentJob(t, ledger, stoppedWith)
	if err := os.WriteFile(job.ManifestPath, []byte(finalSegmentManifestHead), 0o644); err != nil {
		t.Fatal(err)
	}
	mc := &exitingWriterMist{
		push:      mist.PushInfo{ID: 42, StreamName: job.StreamName, TargetURI: job.TargetURI},
		exitAfter: 3,
		onExit: func() {
			if err := os.WriteFile(job.ManifestPath, []byte(finalSegmentManifestHead+finalSegmentManifestTail), 0o644); err != nil {
				t.Error(err)
			}
		},
	}
	dm := newDVRManagerWithMist(t, mc)
	dm.sleepFn = func(time.Duration) {}
	dm.jobs[job.DVRHash] = job

	if err := dm.StopRecordingWithSender(job.DVRHash, nil); err != nil {
		t.Fatalf("stop: %v", err)
	}

	if recorded := awaitStopped(t, stoppedWith); !slices.Contains(recorded, "49_34.ts") {
		t.Fatalf("DVRStopped sent before the stopped writer's final segment reached the ledger (offered: %v)", recorded)
	}
}

// Reconciliation and the RECORDING_SEGMENT path must time a segment alike:
// Foghorn refuses a second report of a segment whose timing differs, and the
// chapter remux places segments by these starts.
func TestReconciliationTimesTheFinalSegmentLikeTheTrigger(t *testing.T) {
	stream := connectFake(t)
	done := make(chan struct{})
	defer close(done)

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "segments"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "segments", "56_34.ts"), []byte("ts"), 0o644); err != nil {
		t.Fatal(err)
	}
	const manifest = "#EXTM3U\n#EXT-X-PROGRAM-DATE-TIME:2026-09-26T13:56:31.491Z\n#EXTINF:6.000,\nsegments/56_33.ts\n#EXT-X-PROGRAM-DATE-TIME:2026-09-26T13:56:35.473Z\n#EXTINF:4.344,\nsegments/56_34.ts\n"
	manifestPath := filepath.Join(dir, "dvrhash.m3u8")
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	wantStart, _, _, ok := dvrSegmentWallClock(manifest, "56_34.ts")
	if !ok {
		t.Fatal("trigger timing unanchored")
	}

	got := make(chan int64, 1)
	go func() {
		for {
			select {
			case msg := <-stream.sendCh:
				if req := msg.GetRecordDvrSegmentRequest(); req != nil {
					if req.GetSegmentName() == "56_34.ts" {
						got <- req.GetMediaStartMs()
					}
					handleRecordDVRSegmentResponse(&ipcpb.RecordDVRSegmentResponse{RequestId: req.GetRequestId(), Reason: "test"})
				}
			case <-done:
				return
			}
		}
	}()

	dm := newDVRManagerWithMist(t, &fakeMistClient{})
	dm.syncNewSegments(&DVRJob{
		DVRHash: "dvrhash", OutputDir: dir, ManifestPath: manifestPath, Logger: logging.NewLogger(),
		SyncedSegments: map[string]bool{"56_33.ts": true}, UnmarkedSegments: map[string]uint64{},
	})
	select {
	case start := <-got:
		if start != wantStart {
			t.Fatalf("reconciliation start %d, trigger start %d", start, wantStart)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("final segment not reconciled")
	}
}
