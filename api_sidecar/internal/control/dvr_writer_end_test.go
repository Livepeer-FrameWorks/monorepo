package control

import (
	"sync"
	"testing"
	"time"

	"frameworks/api_sidecar/internal/appconfig/appconfigtest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// vanishingFirstPushMist accepts every PushStart, but the first push ends
// before Mist lists it (staging rc5: the DVR output attached to a buffer whose
// meta was not live yet and aborted within milliseconds). Later pushes stay up.
type vanishingFirstPushMist struct {
	mu         sync.Mutex
	startCalls int
	stream     string
	target     string
	listed     bool
}

func (m *vanishingFirstPushMist) PushStart(streamName, targetURI string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.startCalls++
	m.stream, m.target = streamName, targetURI
	m.listed = m.startCalls > 1
	return nil
}

func (m *vanishingFirstPushMist) PushStop(int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.listed = false
	return nil
}

func (m *vanishingFirstPushMist) PushList() ([]mist.PushInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.listed {
		return nil, nil
	}
	return []mist.PushInfo{{ID: 88, StreamName: m.stream, TargetURI: m.target}}, nil
}

func (m *vanishingFirstPushMist) starts() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.startCalls
}

func startVanishingDVR(t *testing.T, dvrHash string) (*DVRManager, *vanishingFirstPushMist, *DVRJob) {
	t.Helper()
	clearConn()
	appconfigtest.Setenv(t, "FRAMEWORKS_CONTROL_OUTBOX_DIR", t.TempDir())
	useFastInitialPushRetry(t)
	mc := &vanishingFirstPushMist{}
	dm := newDVRManagerWithMist(t, mc)
	if err := dm.StartRecording(dvrHash, "stream-1", "rec", "live+rec", "http://source", &ipcpb.DVRConfig{}, nil); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	dm.mutex.RLock()
	job := dm.jobs[dvrHash]
	dm.mutex.RUnlock()
	if job == nil || job.PushID != 0 || job.Status != "starting" {
		t.Fatalf("setup: want a quarantined starting job, got %+v", job)
	}
	return dm, mc, job
}

// Mist reporting the end of the job's own writer, with no segment written and
// no listed push, lifts the quarantine: the backoff recreate re-issues
// PushStart once and the job records on the new push.
func TestMaintainPushStatus_ReportedWriterEndRecreatesPush(t *testing.T) {
	const dvrHash = "hash-vanished-first-push"
	dm, mc, job := startVanishingDVR(t, dvrHash)

	dm.mutex.Lock()
	job.LastPushAttempt = time.Now().Add(-InitialRetryDelay - time.Second)
	issuedAt := job.LastPushAttempt
	target := job.TargetURI
	dm.mutex.Unlock()
	dm.noteWriterEnded("live+rec", issuedAt.Add(time.Millisecond), target)

	dm.maintainPushStatus(job)

	if got := mc.starts(); got != 2 {
		t.Fatalf("PushStart calls = %d, want the initial push plus one recreate", got)
	}
	dm.mutex.RLock()
	defer dm.mutex.RUnlock()
	if job.PushID != 88 || job.Status != "recording" {
		t.Fatalf("job after recreate: push_id=%d status=%q, want 88/recording", job.PushID, job.Status)
	}
}

// The quarantine still holds without Mist's word: an end reported before this
// push was issued belongs to an earlier writer, and a job that already wrote
// segments is not recreated from this path.
func TestMaintainPushStatus_WriterEndMustPostdateIssueAndPrecedeSegments(t *testing.T) {
	t.Run("stale end", func(t *testing.T) {
		dm, mc, job := startVanishingDVR(t, "hash-stale-end")
		dm.mutex.Lock()
		job.LastPushAttempt = time.Now().Add(-InitialRetryDelay - time.Second)
		issuedAt, target := job.LastPushAttempt, job.TargetURI
		dm.mutex.Unlock()
		dm.noteWriterEnded("live+rec", issuedAt.Add(-time.Second), target)

		dm.maintainPushStatus(job)
		if got := mc.starts(); got != 1 {
			t.Fatalf("PushStart calls = %d, a stale end must not lift the quarantine", got)
		}
	})
	t.Run("segments written", func(t *testing.T) {
		dm, mc, job := startVanishingDVR(t, "hash-with-segments")
		dm.mutex.Lock()
		job.LastPushAttempt = time.Now().Add(-InitialRetryDelay - time.Second)
		job.SegmentCount = 3
		issuedAt, target := job.LastPushAttempt, job.TargetURI
		dm.mutex.Unlock()
		dm.noteWriterEnded("live+rec", issuedAt.Add(time.Millisecond), target)

		dm.maintainPushStatus(job)
		if got := mc.starts(); got != 1 {
			t.Fatalf("PushStart calls = %d, a job with segments must not be recreated here", got)
		}
	})
}
