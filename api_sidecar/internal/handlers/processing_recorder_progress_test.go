package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
)

// fakeRecordingMist answers Mist API calls for a processing stream whose
// source has fully arrived (lastms frozen at the chapter end) while its
// recording push reports its own position in push_list.
type fakeRecordingMist struct {
	mu            sync.Mutex
	stream        string
	target        string
	pushID        int
	sourceLastMs  float64
	recorderMs    float64
	recorderBytes float64
	listPush      bool
}

func (f *fakeRecordingMist) set(recorderMs, recorderBytes float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recorderMs, f.recorderBytes = recorderMs, recorderBytes
}

func (f *fakeRecordingMist) serve(t *testing.T) *mist.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		body := map[string]any{
			"authorize": map[string]any{"status": "OK"},
			"active_streams": map[string]any{
				f.stream: map[string]any{"lastms": f.sourceLastMs},
			},
		}
		pushes := []any{}
		if f.listPush {
			pushes = append(pushes, []any{
				float64(f.pushID), f.stream, f.target, f.target, []any{},
				map[string]any{"mediatime": f.recorderMs, "bytes": f.recorderBytes, "active_ms": float64(0)},
			})
		}
		body["push_list"] = pushes
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return mist.NewClient(logging.NewLogger(), mist.ClientConfig{BaseURL: srv.URL})
}

func newFakeRecordingMist(t *testing.T) (*fakeRecordingMist, string) {
	outputPath := filepath.Join(t.TempDir(), "chapter.mkv")
	return &fakeRecordingMist{
		stream:       "processing+chapter",
		target:       processingMuxTargetURIWithSelectors(outputPath, "source", "all"),
		pushID:       4242,
		sourceLastMs: 1799979,
		listPush:     true,
	}, outputPath
}

const recorderProgressTick = 5 * time.Second

// Staging rc6: a 30-minute chapter's source was fully buffered (lastms
// 1799979) while its recorder, the slowest reader holding the feed back, was
// still writing. An attempt whose recorder keeps advancing is not stalled,
// however long it trails the source.
func TestRecorderStallWatchKeepsRecorderAdvancingBehindArrivedSource(t *testing.T) {
	fake, outputPath := newFakeRecordingMist(t)
	client := fake.serve(t)
	start := time.Unix(1_760_000_000, 0)
	watch := newRecorderStallWatch(start)
	recorderMs, recorderBytes := 600_000.0, 50_000_000.0
	for tick := 1; tick <= 60; tick++ {
		recorderMs += 2000
		recorderBytes += 400_000
		fake.set(recorderMs, recorderBytes)
		sample := sampleProcessingRecorderProgress(client, fake.stream, fake.target, fake.pushID, outputPath)
		known, stalledFor := watch.observe(start.Add(time.Duration(tick)*recorderProgressTick), sample)
		if stalledFor >= processingRecorderStallWindow {
			t.Fatalf("tick %d (%s in): advancing recorder at %.0fms judged stalled for %s", tick, time.Duration(tick)*recorderProgressTick, recorderMs, stalledFor)
		}
		if known.mediaMs != int64(recorderMs) {
			t.Fatalf("tick %d: reported position %dms, want the recorder's %.0fms, not the source's", tick, known.mediaMs, recorderMs)
		}
	}
}

// A recorder that stops writing fails within the stall window, even though
// the source finished arriving long ago.
func TestRecorderStallWatchFailsRecorderThatStopsAdvancing(t *testing.T) {
	fake, outputPath := newFakeRecordingMist(t)
	client := fake.serve(t)
	start := time.Unix(1_760_000_000, 0)
	watch := newRecorderStallWatch(start)
	fake.set(900_000, 80_000_000)
	stalledAt := 0
	for tick := 1; tick <= 60 && stalledAt == 0; tick++ {
		// Every other read misses the push, as a push_list that has not
		// listed it or a failed API call does; a missed read is no advance.
		fake.mu.Lock()
		fake.listPush = tick%2 == 1
		fake.mu.Unlock()
		sample := sampleProcessingRecorderProgress(client, fake.stream, fake.target, fake.pushID, outputPath)
		if _, stalledFor := watch.observe(start.Add(time.Duration(tick)*recorderProgressTick), sample); stalledFor >= processingRecorderStallWindow {
			stalledAt = tick
		}
	}
	// Tick 1 is the recorder's last advance (its first report); the window
	// runs out one window later.
	want := 1 + int(processingRecorderStallWindow/recorderProgressTick)
	if stalledAt != want {
		t.Fatalf("stopped recorder judged stalled at tick %d, want tick %d (%s after its last write)", stalledAt, want, processingRecorderStallWindow)
	}
}

// The output file growing on disk is recorder progress even when Mist has not
// listed the push's status.
func TestRecorderStallWatchCountsOutputFileGrowth(t *testing.T) {
	fake, outputPath := newFakeRecordingMist(t)
	fake.listPush = false
	client := fake.serve(t)
	start := time.Unix(1_760_000_000, 0)
	watch := newRecorderStallWatch(start)
	f, err := os.Create(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	chunk := make([]byte, 1024)
	for tick := 1; tick <= 60; tick++ {
		if _, err := f.Write(chunk); err != nil {
			t.Fatal(err)
		}
		sample := sampleProcessingRecorderProgress(client, fake.stream, fake.target, 0, outputPath)
		if _, stalledFor := watch.observe(start.Add(time.Duration(tick)*recorderProgressTick), sample); stalledFor >= processingRecorderStallWindow {
			t.Fatalf("tick %d: growing output file judged stalled for %s", tick, stalledFor)
		}
	}
}
