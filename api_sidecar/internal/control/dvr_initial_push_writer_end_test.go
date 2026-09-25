package control

import (
	"sync"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// endsFirstPushFakeMist accepts every PushStart, but the first push's writer
// ends at once: it never shows in the push list and Mist reports its end
// (PUSH_END). Later pushes stay up.
type endsFirstPushFakeMist struct {
	dm *DVRManager

	mu         sync.Mutex
	startCalls int
	startedAt  []time.Time
	live       bool
	stream     string
	target     string
}

func (f *endsFirstPushFakeMist) PushStart(streamName, targetURI string) error {
	f.mu.Lock()
	f.startCalls++
	first := f.startCalls == 1
	f.startedAt = append(f.startedAt, time.Now())
	f.stream, f.target = streamName, targetURI
	if !first {
		f.live = true
	}
	f.mu.Unlock()
	if first {
		f.dm.noteWriterEnded(streamName, time.Now(), targetURI)
	}
	return nil
}

func (f *endsFirstPushFakeMist) PushStop(int) error { return nil }

func (f *endsFirstPushFakeMist) PushList() ([]mist.PushInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.live {
		return []mist.PushInfo{}, nil
	}
	return []mist.PushInfo{{ID: 93, StreamName: f.stream, TargetURI: f.target}}, nil
}

// A push whose writer Mist reports ended before it ever appeared must be
// re-issued on the next confirmation pass, not waited on for the whole
// startup window. Runs through StartRecording (which holds the manager lock
// while confirming) at production timings.
func TestStartRecordingReissuesPushAfterReportedWriterEnd(t *testing.T) {
	clearConn()
	const (
		dvrHash      = "hash-writer-end"
		streamID     = "stream-writer-end"
		internalName = "internal-writer-end"
	)
	dm := &DVRManager{
		logger: logging.NewLogger(), jobs: make(map[string]*DVRJob), storagePath: t.TempDir(),
		diskCheck: func(string, uint64) error { return nil },
	}
	fake := &endsFirstPushFakeMist{dm: dm}
	dm.mistClient = fake

	done := make(chan error, 1)
	begin := time.Now()
	go func() {
		done <- dm.StartRecording(dvrHash, streamID, internalName, "live+"+internalName, "", &ipcpb.DVRConfig{}, nil)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("StartRecording: %v", err)
		}
	case <-time.After(6 * time.Second):
		t.Fatalf("no confirmed re-push within 6s of a reported writer end (initial push window is %s)", initialPushRetryFor)
	}

	dm.mutex.RLock()
	job := dm.jobs[dvrHash]
	dm.mutex.RUnlock()
	if job == nil || job.PushID != 93 {
		t.Fatalf("want the re-issued push confirmed (id 93), got %+v", job)
	}
	_ = dm.StopRecording(dvrHash)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.startCalls != 2 {
		t.Fatalf("want exactly one re-issued PushStart, got %d starts", fake.startCalls)
	}
	if gap := fake.startedAt[1].Sub(begin); gap > 5*time.Second {
		t.Fatalf("re-push came %s after the start; want within 5s", gap)
	}
}

// Without a reported end the accepted push is never re-issued: an unconfirmed
// push may still be writing.
func TestEnsureInitialPushDoesNotReissueWithoutWriterEnd(t *testing.T) {
	useFastInitialPushRetry(t)
	fake := &fakeMistClient{pushListItems: []mist.PushInfo{}}
	dm := &DVRManager{logger: logging.NewLogger(), jobs: make(map[string]*DVRJob), mistClient: fake}
	snap := pushIdentity{streamName: "live+no-end", targetURI: "/dvr/s/hash-no-end/x.m3u8", dvrHash: "hash-no-end"}
	_, outcome, _ := dm.ensureInitialPush(snap, logging.NewLogger())
	if outcome != dvrPushAcceptedUnconfirmed {
		t.Fatalf("want accepted-unconfirmed, got %d", outcome)
	}
	if fake.pushStartCalls != 1 {
		t.Fatalf("want exactly one PushStart without a writer end, got %d", fake.pushStartCalls)
	}
}
