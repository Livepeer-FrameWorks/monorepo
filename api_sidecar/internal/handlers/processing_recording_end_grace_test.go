package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/sirupsen/logrus"
)

// recordingGraceHarness drives one processing job kind (VOD, clip, chapter)
// through its real handler against a fake Mist controller, up to the point
// where the recording push has started.
type recordingGraceHarness struct {
	streamName  string
	outputPath  string
	pushStarted atomic.Bool
	nuked       atomic.Bool
	results     chan *ipcpb.ProcessingJobResult
}

func setRecordingEndGraceForTest(t *testing.T, grace time.Duration) {
	t.Helper()
	previous := processingRecordingEndGrace
	processingRecordingEndGrace = grace
	t.Cleanup(func() { processingRecordingEndGrace = previous })
}

func startRecordingGraceJob(t *testing.T, kind, artifactHash string) *recordingGraceHarness {
	t.Helper()
	isolateProcessingOverrideState(t)
	hx := &recordingGraceHarness{
		streamName: "processing+" + artifactHash,
		results:    make(chan *ipcpb.ProcessingJobResult, 4),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/source.mkv":
			_, _ = w.Write(fakeMistCutBody())
			return
		case strings.HasPrefix(r.URL.Path, "/json_vod+"):
			// Booting the published VOD stream writes its DTSH sidecar.
			_ = os.WriteFile(hx.outputPath+".dtsh", validDTSHBytes(), 0o644)
			_, _ = w.Write([]byte("{}"))
			return
		case strings.HasPrefix(r.URL.Path, "/json_"):
			_, _ = w.Write([]byte("{}"))
			return
		}
		body, _ := io.ReadAll(r.Body)
		request := string(body) + r.URL.RawQuery
		if strings.Contains(request, "push_start") {
			hx.pushStarted.Store(true)
		}
		if strings.Contains(request, "nuke_stream") {
			hx.nuked.Store(true)
		}
		active := map[string]any{}
		if !hx.nuked.Load() {
			active[hx.streamName] = map[string]any{
				"health": map[string]any{
					"video_H264_640x360_15fps_0": map[string]any{"codec": "H264", "id": 0.0, "idx": 0.0, "width": 640.0, "height": 360.0},
					"audio_AAC_1ch_48000hz_1":    map[string]any{"codec": "AAC", "id": 1.0, "idx": 1.0, "channels": 1.0, "rate": 48000.0},
				},
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"authorize": map[string]any{"status": "OK"}, "active_streams": active})
	}))
	t.Cleanup(srv.Close)

	storage := t.TempDir()
	h := NewProcessingJobHandler(logrus.New(), srv.URL, storage, t.TempDir())
	send := func(msg *ipcpb.ControlMessage) {
		if result := msg.GetProcessingJobResult(); result != nil {
			hx.results <- result
		}
	}
	// The handler must return before the fake Mist closes and before later
	// tests change package timing state it reads.
	handlerDone := make(chan struct{})
	run := func(handle func(*ipcpb.ProcessingJobRequest, func(*ipcpb.ControlMessage)), req *ipcpb.ProcessingJobRequest) {
		go func() {
			defer close(handlerDone)
			handle(req, send)
		}()
	}
	t.Cleanup(func() {
		select {
		case <-handlerDone:
		case <-time.After(60 * time.Second):
			t.Error("processing handler did not return")
		}
	})

	switch kind {
	case "vod":
		hx.outputPath = filepath.Join(storage, "vod", artifactHash+".mkv")
		req := &ipcpb.ProcessingJobRequest{
			JobId:         "vod-" + artifactHash,
			JobType:       "process",
			ArtifactHash:  artifactHash,
			SourceUrl:     srv.URL + "/source.mkv",
			ProcessesJson: "[]",
		}
		run(h.Handle, req)
	case "clip":
		hx.outputPath = filepath.Join(storage, "clips", "clipstream", artifactHash+".mkv")
		req := &ipcpb.ProcessingJobRequest{
			JobId:             "clip-" + artifactHash,
			JobType:           "clip",
			ArtifactHash:      artifactHash,
			SourceUrl:         srv.URL + "/source.mkv",
			ProcessesJson:     "[]",
			OutputRuntimeName: "vod+" + artifactHash,
			Params:            map[string]string{"output_stream_name": "clipstream"},
		}
		run(h.handleClip, req)
	case "chapter":
		base := time.Now().Add(-time.Minute).UnixMilli()
		hx.outputPath = filepath.Join(storage, "vod", artifactHash+".mkv")
		req := &ipcpb.ProcessingJobRequest{
			JobId:           "chapter-finalize-v2-1-chapter-" + artifactHash,
			JobType:         "dvr_chapter_finalize",
			ArtifactHash:    artifactHash,
			SourceDvrHash:   "dvr-" + artifactHash,
			SourceChapterId: "chapter-" + artifactHash,
			ProcessesJson:   "[]",
			SourceSegments: []*ipcpb.DVRChapterSegmentRef{
				{SegmentName: "seg_1.ts", MediaStartMs: base, MediaEndMs: base + 10000, DurationMs: 10000, PresignedRecoveryUrl: srv.URL + "/source.mkv"},
			},
		}
		run(h.Handle, req)
	default:
		t.Fatalf("unknown job kind %q", kind)
	}

	deadline := time.Now().Add(20 * time.Second)
	for !hx.pushStarted.Load() {
		if time.Now().After(deadline) {
			t.Fatalf("%s job never started its recording push", kind)
		}
		select {
		case result := <-hx.results:
			t.Fatalf("%s job ended before its recording push: %q (%s)", kind, result.GetStatus(), result.GetError())
		case <-time.After(20 * time.Millisecond):
		}
	}
	return hx
}

func (hx *recordingGraceHarness) pushEnd(status string) {
	SignalProcessingPushEnd(ProcessingPushEndEvent{
		StreamName:  hx.streamName,
		PushID:      7,
		TargetAfter: hx.outputPath,
		PushStatus:  status,
		LogMessages: `["output terminated"]`,
	})
}

// cleanRecordingEnd is a RECORDING_END that passes every recording check for
// the harness output, a 10 s 640x360 recording.
func (hx *recordingGraceHarness) cleanRecordingEnd() ProcessingRecordingEndEvent {
	return ProcessingRecordingEndEvent{
		StreamName:      hx.streamName,
		FilePath:        hx.outputPath,
		BytesWritten:    4096,
		MediaDurationMs: 10000,
		TimeStarted:     time.Now().Unix(),
		ExitReason:      "CLEAN_EOF",
		Tracks:          []processingMetaVideoTrack{{codec: "H264", width: 640, height: 360, firstms: 0, lastms: 10000}},
		FullTracks:      []*ipcpb.StreamTrack{{TrackName: "video_H264_640x360_15fps_0", TrackType: "video", Codec: "H264"}},
	}
}

func (hx *recordingGraceHarness) awaitResult(t *testing.T, within time.Duration) *ipcpb.ProcessingJobResult {
	t.Helper()
	select {
	case result := <-hx.results:
		return result
	case <-time.After(within):
		t.Fatalf("no processing result within %s", within)
		return nil
	}
}

const recordingOutputVanishedPrefix = "recording output ended without reporting its end"

var recordingGraceKinds = []string{"vod", "clip", "chapter"}

// A recording output killed mid-push never sends RECORDING_END; Mist's
// controller still reports the push end with its stats object. The attempt is
// reported retryable once the grace window passes, after the processing
// stream has been stopped, instead of waiting out the stall timeout.
func TestRecordingOutputEndedWithoutRecordingEndAfterPushSuccessIsRetryable(t *testing.T) {
	for _, kind := range recordingGraceKinds {
		t.Run(kind, func(t *testing.T) {
			setRecordingEndGraceForTest(t, 300*time.Millisecond)
			hx := startRecordingGraceJob(t, kind, "gracesuccess"+kind)
			// A RECORDING_END from a retired push does not end the wait.
			stale := hx.cleanRecordingEnd()
			stale.TimeStarted = time.Now().Add(-time.Hour).Unix()
			SignalProcessingRecordingEnd(stale)
			hx.pushEnd(`{"active_seconds":4,"bytes":1234}`)

			result := hx.awaitResult(t, 30*time.Second)
			if result.GetStatus() != processingResultRetryable {
				t.Fatalf("status = %q (%s), want %q", result.GetStatus(), result.GetError(), processingResultRetryable)
			}
			if !strings.HasPrefix(result.GetError(), recordingOutputVanishedPrefix) || !strings.Contains(result.GetError(), "push status:") {
				t.Fatalf("error = %q, want the vanished-output reason with the push status", result.GetError())
			}
			if !hx.nuked.Load() {
				t.Fatal("the retryable result was reported before the processing stream was stopped")
			}
		})
	}
}

// A push that ends with a failure status and no RECORDING_END is an output
// that terminated abnormally: retryable, not a permanent failure.
func TestRecordingOutputEndedWithoutRecordingEndAfterPushFailureIsRetryable(t *testing.T) {
	for _, kind := range recordingGraceKinds {
		t.Run(kind, func(t *testing.T) {
			setRecordingEndGraceForTest(t, 300*time.Millisecond)
			hx := startRecordingGraceJob(t, kind, "gracefailure"+kind)
			hx.pushEnd("null")

			result := hx.awaitResult(t, 30*time.Second)
			if result.GetStatus() != processingResultRetryable {
				t.Fatalf("status = %q (%s), want %q", result.GetStatus(), result.GetError(), processingResultRetryable)
			}
			if !strings.HasPrefix(result.GetError(), recordingOutputVanishedPrefix) || !strings.Contains(result.GetError(), "push status: null") {
				t.Fatalf("error = %q, want the vanished-output reason with the push status", result.GetError())
			}
			if !hx.nuked.Load() {
				t.Fatal("the retryable result was reported before the processing stream was stopped")
			}
		})
	}
}

// RECORDING_END and PUSH_END are separate asynchronous triggers; a
// RECORDING_END that lands shortly after PUSH_END is the recording's end, and
// the job proceeds exactly as if it had arrived first.
func TestLateRecordingEndWithinGraceProceedsNormally(t *testing.T) {
	for _, kind := range recordingGraceKinds {
		t.Run(kind, func(t *testing.T) {
			setRecordingEndGraceForTest(t, 10*time.Second)
			hx := startRecordingGraceJob(t, kind, "gracelate"+kind)
			hx.pushEnd(`{"active_seconds":10}`)
			time.Sleep(300 * time.Millisecond)

			if err := os.WriteFile(hx.outputPath, []byte("mkv bytes"), 0o644); err != nil {
				t.Fatal(err)
			}
			SignalProcessingRecordingEnd(hx.cleanRecordingEnd())

			result := hx.awaitResult(t, 30*time.Second)
			if result.GetStatus() != "completed" {
				t.Fatalf("status = %q (%s), want completed", result.GetStatus(), result.GetError())
			}
			if hx.nuked.Load() {
				t.Fatal("a completed recording tore down its processing stream as a failure")
			}
		})
	}
}

// A failed push whose recording did report its end stays a failed attempt.
func TestPushFailureWithRecordingEndStaysFailed(t *testing.T) {
	for _, kind := range recordingGraceKinds {
		t.Run(kind, func(t *testing.T) {
			setRecordingEndGraceForTest(t, 10*time.Second)
			hx := startRecordingGraceJob(t, kind, "gracepushfail"+kind)
			hx.pushEnd("null")
			time.Sleep(200 * time.Millisecond)
			SignalProcessingRecordingEnd(hx.cleanRecordingEnd())

			result := hx.awaitResult(t, 30*time.Second)
			if result.GetStatus() != "failed" {
				t.Fatalf("status = %q (%s), want failed", result.GetStatus(), result.GetError())
			}
			if strings.HasPrefix(result.GetError(), recordingOutputVanishedPrefix) {
				t.Fatalf("error = %q: a recording that reported its end is not a vanished output", result.GetError())
			}
		})
	}
}
