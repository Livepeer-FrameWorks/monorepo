package handlers

import (
	"encoding/json"
	"fmt"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	nukeCount   atomic.Int32
	pushCount   atomic.Int32
	results     chan *ipcpb.ProcessingJobResult
	pushTarget  atomic.Value
}

// harnessPushID is the id the fake Mist lists for the job's recording push.
const harnessPushID = 7

func setRecordingEndGraceForTest(t *testing.T, grace time.Duration) {
	t.Helper()
	previous := processingRecordingEndGrace
	processingRecordingEndGrace = grace
	t.Cleanup(func() { processingRecordingEndGrace = previous })
}

type recordingGeometry struct {
	width, height int
	rotation      int
	noAudio       bool
	completedEnd  float64
	sourceEnd     float64
	processes     string
	fallbackPhase string
}

func startRecordingGraceJob(t *testing.T, kind, artifactHash string, geometry ...recordingGeometry) *recordingGraceHarness {
	t.Helper()
	isolateProcessingOverrideState(t)
	hx := &recordingGraceHarness{
		streamName: "processing+" + artifactHash,
		results:    make(chan *ipcpb.ProcessingJobResult, 4),
	}
	shape := recordingGeometry{width: 640, height: 360, sourceEnd: 10000}
	if len(geometry) > 0 {
		shape = geometry[0]
	}
	var booted atomic.Bool
	var fallbackSignalled atomic.Bool
	var startupListed sync.Once
	startupListedCh := make(chan struct{})
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
		case r.URL.Path == "/json_"+hx.streamName+".js":
			// Like Mist, a /json_ request is a viewer that boots the stream.
			booted.Store(true)
			_, _ = w.Write([]byte("{}"))
			return
		case strings.HasPrefix(r.URL.Path, "/json_"):
			_, _ = w.Write([]byte("{}"))
			return
		}
		body, _ := io.ReadAll(r.Body)
		request := string(body) + r.URL.RawQuery
		if strings.Contains(request, "push_start") {
			var command struct {
				PushStart struct {
					Target string `json:"target"`
				} `json:"push_start"`
			}
			if json.Unmarshal([]byte(r.URL.Query().Get("command")), &command) == nil && command.PushStart.Target != "" {
				hx.pushTarget.Store(command.PushStart.Target)
			}
			hx.pushStarted.Store(true)
			if hx.pushCount.Add(1) == 1 && shape.fallbackPhase == "push" {
				RouteProcessExit(ProcessExitEvent{StreamName: hx.streamName, ProcessType: "Livepeer", ExitCode: 2, BootCount: 1, Status: "unrecoverable", Reason: "fixture gateway refused source"})
			}
		}
		if strings.Contains(request, "nuke_stream") {
			hx.nuked.Store(true)
			hx.nukeCount.Add(1)
			booted.Store(false)
		}
		// The processing stream is active only between a boot and a nuke, as
		// in Mist; a job boots it only after claiming it, so the handler's
		// startup orphan sweep never stops the job's own stream.
		active := map[string]any{}
		if booted.Load() {
			active[hx.streamName] = map[string]any{
				"lastms": shape.sourceEnd,
				"health": map[string]any{
					"video_H264_640x360_15fps_0": map[string]any{"codec": "H264", "id": 0.0, "idx": 0.0, "width": float64(shape.width), "height": float64(shape.height), "rotation": float64(shape.rotation)},
					"audio_AAC_1ch_48000hz_1":    map[string]any{"codec": "AAC", "id": 1.0, "idx": 1.0, "channels": 1.0, "rate": 48000.0},
				},
			}
			if shape.fallbackPhase == "readiness" && hx.nukeCount.Load() == 0 {
				processingProcessOverridesMu.Lock()
				policy := processingOverrideRecords[hx.streamName].ProcessesJSON
				processingProcessOverridesMu.Unlock()
				if mist.HasLivepeerProcesses(policy) {
					active[hx.streamName].(map[string]any)["health"] = map[string]any{}
					if fallbackSignalled.CompareAndSwap(false, true) {
						RouteProcessExit(ProcessExitEvent{StreamName: hx.streamName, ProcessType: "Livepeer", ExitCode: 2, BootCount: 1, Status: "unrecoverable", Reason: "fixture gateway refused source during readiness"})
					}
				}
			}
			if shape.noAudio {
				delete(active[hx.streamName].(map[string]any)["health"].(map[string]any), "audio_AAC_1ch_48000hz_1")
			}
			if hx.pushStarted.Load() && shape.completedEnd > 0 {
				active[hx.streamName].(map[string]any)["lastms"] = shape.completedEnd
			}
		}
		response := map[string]any{"authorize": map[string]any{"status": "OK"}, "active_streams": active}
		// Like Mist, push_list names the started recording push by its id.
		if target, ok := hx.pushTarget.Load().(string); ok && strings.Contains(request, "push_list") {
			response["push_list"] = []any{[]any{float64(harnessPushID), hx.streamName, target, target}}
		}
		_ = json.NewEncoder(w).Encode(response)
		if strings.Contains(request, "active_streams") {
			startupListed.Do(func() { close(startupListedCh) })
		}
	}))
	t.Cleanup(srv.Close)

	storage := t.TempDir()
	h := NewProcessingJobHandler(logrus.New(), srv.URL, storage, t.TempDir())
	// The job is dispatched only after the handler's startup orphan sweep has
	// listed Mist's streams, the order a restarted Helmsman sees before Foghorn
	// re-dispatches, so every run exercises the same interleaving.
	select {
	case <-startupListedCh:
	case <-time.After(20 * time.Second):
		t.Fatal("the handler never listed Mist streams for its startup orphan sweep")
	}
	send := func(msg *ipcpb.ControlMessage) {
		if result := msg.GetProcessingJobResult(); result != nil && result.GetStatus() != "cache_update" {
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
		// A finalized chapter's DTSH generation runs on after its result and reads the same
		// package timing state.
		h.stopBackgroundWork()
	})

	switch kind {
	case "vod":
		hx.outputPath = filepath.Join(storage, "vod", artifactHash+".mkv")
		req := &ipcpb.ProcessingJobRequest{
			JobId:        "vod-" + artifactHash,
			JobType:      "process",
			ArtifactHash: artifactHash,
			SourceUrl:    srv.URL + "/source.mkv",
			ProcessesJson: func() string {
				if shape.processes != "" {
					return shape.processes
				}
				return "[]"
			}(),
		}
		if shape.processes != "" {
			req.ProcessesJson = shape.processes
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
	targetPushes := int32(1)
	if shape.fallbackPhase == "push" {
		targetPushes = 2
	}
	for hx.pushCount.Load() < targetPushes {
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
		PushID:      harnessPushID,
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

// PUSH_END is delivered by stream name. After a Helmsman restart the orphan
// sweep kills the previous attempt's push, and that push's PUSH_END reaches
// the new attempt once it has started its own push. The retired push's end is
// not this attempt's recording output vanishing: the attempt keeps running and
// completes on its own push's end.
func TestRetiredPushEndDoesNotEndTheAttempt(t *testing.T) {
	for _, kind := range recordingGraceKinds {
		t.Run(kind, func(t *testing.T) {
			setRecordingEndGraceForTest(t, 300*time.Millisecond)
			hx := startRecordingGraceJob(t, kind, "retiredpush"+kind)
			SignalProcessingPushEnd(ProcessingPushEndEvent{
				StreamName:  hx.streamName,
				PushID:      harnessPushID - 1,
				TargetAfter: hx.outputPath,
				PushStatus:  `{"active_ms":0,"active_seconds":0,"current_target":"","media_tx":0,"mediatime":0}`,
			})
			select {
			case result := <-hx.results:
				t.Fatalf("a retired push's PUSH_END ended the attempt: %q (%s)", result.GetStatus(), result.GetError())
			case <-time.After(1500 * time.Millisecond):
			}

			if err := os.WriteFile(hx.outputPath, []byte("mkv bytes"), 0o644); err != nil {
				t.Fatal(err)
			}
			SignalProcessingRecordingEnd(hx.cleanRecordingEnd())
			hx.pushEnd(`{"active_seconds":10}`)
			result := hx.awaitResult(t, 30*time.Second)
			if result.GetStatus() != "completed" {
				t.Fatalf("status = %q (%s), want completed", result.GetStatus(), result.GetError())
			}
		})
	}
}

func TestLocalVODCompletesForOffLadderSource(t *testing.T) {
	for _, dims := range [][2]int{{2560, 1440}, {3840, 2160}, {1920, 1200}, {960, 540}, {1080, 1080}} {
		t.Run(fmt.Sprintf("%dx%d", dims[0], dims[1]), func(t *testing.T) {
			hx := startRecordingGraceJob(t, "vod", "offladder", recordingGeometry{width: dims[0], height: dims[1], sourceEnd: 10000,
				processes: `[{"process":"AV","codec":"H264","resolution":"0x360"}]`})
			if err := os.WriteFile(hx.outputPath, []byte("mkv bytes"), 0o644); err != nil {
				t.Fatal(err)
			}
			evt := hx.cleanRecordingEnd()
			evt.Tracks = []processingMetaVideoTrack{renditionTrack(2, 360*dims[0]/dims[1], 360, 10000)}
			SignalProcessingRecordingEnd(evt)
			hx.pushEnd(`{"active_seconds":10}`)
			result := hx.awaitResult(t, 30*time.Second)
			if result.GetStatus() != "completed" {
				t.Fatalf("status = %q (%s), want completed", result.GetStatus(), result.GetError())
			}
		})
	}
}

func TestLocalVODRefusesMissingOrTruncatedRung(t *testing.T) {
	for _, truncated := range []bool{false, true} {
		t.Run(fmt.Sprint(truncated), func(t *testing.T) {
			hx := startRecordingGraceJob(t, "vod", "incompletelocal", recordingGeometry{width: 1280, height: 720, sourceEnd: 1000, completedEnd: 10000, noAudio: true,
				processes: `[{"process":"AV","codec":"H264","resolution":"0x360"},{"process":"AV","codec":"H264","resolution":"0x720"}]`})
			if err := os.WriteFile(hx.outputPath, []byte("mkv bytes"), 0o644); err != nil {
				t.Fatal(err)
			}
			evt := hx.cleanRecordingEnd()
			evt.Tracks = []processingMetaVideoTrack{renditionTrack(2, 1280, 720, 10000)}
			if truncated {
				evt.Tracks = append(evt.Tracks, renditionTrack(3, 640, 360, 2000))
			}
			SignalProcessingRecordingEnd(evt)
			hx.pushEnd(`{"active_seconds":10}`)
			result := hx.awaitResult(t, 30*time.Second)
			if result.GetStatus() != "failed" || !strings.Contains(result.GetError(), "renditions incomplete") {
				t.Fatalf("status = %q (%s), want failed/incomplete", result.GetStatus(), result.GetError())
			}
		})
	}
}

func TestSmallRotatedVODRunsSourceSizeAV(t *testing.T) {
	hx := startRecordingGraceJob(t, "vod", "smallrotated", recordingGeometry{width: 320, height: 180, rotation: 90, sourceEnd: 10000,
		processes: `[{"process":"AV","codec":"H264","resolution":"x360","track_inhibit":"video=<640x360"}]`})
	policy, ok := getProcessingProcessOverride(hx.streamName)
	expected, err := mist.ExpectedVideoRenditions(policy, mist.SourceMediaInfo{Width: 320, Height: 180, Rotation: 90})
	if !ok || err != nil || len(expected) != 1 || expected[0].Width != 180 || expected[0].Height != 320 {
		t.Errorf("source-size policy = %s, expected %v, error %v", policy, expected, err)
	}
	if err := os.WriteFile(hx.outputPath, []byte("mkv bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	evt := hx.cleanRecordingEnd()
	evt.Tracks = []processingMetaVideoTrack{renditionTrack(2, 180, 320, 10000)}
	SignalProcessingRecordingEnd(evt)
	hx.pushEnd(`{"active_seconds":10}`)
	result := hx.awaitResult(t, 30*time.Second)
	if result.GetStatus() != "completed" {
		t.Fatalf("status = %q (%s)", result.GetStatus(), result.GetError())
	}
}

func TestLocalVODRefusesUnknownSourceEnd(t *testing.T) {
	hx := startRecordingGraceJob(t, "vod", "unknownsourceend", recordingGeometry{width: 1280, height: 720,
		processes: `[{"process":"AV","codec":"H264","resolution":"x360"}]`})
	if err := os.WriteFile(hx.outputPath, []byte("mkv bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	evt := hx.cleanRecordingEnd()
	evt.Tracks = []processingMetaVideoTrack{renditionTrack(2, 640, 360, 10000)}
	SignalProcessingRecordingEnd(evt)
	hx.pushEnd(`{"active_seconds":10}`)
	result := hx.awaitResult(t, 30*time.Second)
	if result.GetStatus() != "failed" || !strings.Contains(result.GetError(), "renditions incomplete") {
		t.Fatalf("status = %q (%s), want incomplete", result.GetStatus(), result.GetError())
	}
}
