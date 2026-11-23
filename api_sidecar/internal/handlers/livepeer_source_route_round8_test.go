package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestSourceProbeLeavesLivepeerRefusalToProcessor(t *testing.T) {
	for _, source := range []struct {
		codec    string
		rotation int
	}{{"H264", 90}, {"VP9", 0}, {"H264", 0}, {"H264", 360}} {
		t.Run(fmt.Sprintf("%s-%d", source.codec, source.rotation), func(t *testing.T) {
			isolateProcessingOverrideState(t)
			const stream, job = "processing+mist-source-verdict", "source-verdict"
			if err := setProcessingProcessOverride(stream, processingSourceProbePolicy, job, time.Now().Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			var booted atomic.Bool
			var nukes atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/json_") {
					booted.Store(true)
					_, _ = w.Write([]byte("{}"))
					return
				}
				body, _ := io.ReadAll(r.Body)
				if strings.Contains(string(body)+r.URL.RawQuery, "nuke_stream") {
					booted.Store(false)
					nukes.Add(1)
				}
				active := map[string]any{}
				if booted.Load() {
					active[stream] = map[string]any{"lastms": 60000.0, "health": map[string]any{"video_0": map[string]any{"codec": source.codec, "id": 0.0, "idx": 0.0, "width": 1920.0, "height": 1080.0, "rotation": float64(source.rotation)}}}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"authorize": map[string]any{"status": "OK"}, "active_streams": active})
			}))
			defer server.Close()
			h := &ProcessingJobHandler{logger: discardLog().Logger, mistServerURL: server.URL, storagePath: t.TempDir()}
			h.memorySnapshot = func() (uint64, uint64, error) { return 16 * 1024 * memoryMiB, 16 * 1024 * memoryMiB, nil }
			client := mist.NewClient(discardLog().Logger, mist.ClientConfig{BaseURL: server.URL})
			policy := `[{"process":"Livepeer","target_profiles":[{"name":"720p","height":720,"bitrate":3000000}]}]`
			got, _, err := h.prepareProcessingMemory(discardLog(), client, &ipcpb.ProcessingJobRequest{JobId: job, ArtifactHash: "mist-source-verdict"}, stream, "", policy, nil, func(*ipcpb.ControlMessage) {})
			if err != nil || !mist.HasLivepeerProcesses(got) {
				t.Fatalf("pre-start source predicate bypassed Mist refusal: policy=%s err=%v", got, err)
			}
			if nukes.Load() != 0 {
				t.Fatalf("admitted job restarted its source after the metadata probe: nukes=%d", nukes.Load())
			}
		})
	}
}

func TestCodecRoutingUsesStructuredExitReason(t *testing.T) {
	if !expectedSourceCodecRoute("Livepeer", "SOURCE_CODEC_UNSUPPORTED", "localized explanation") {
		t.Fatal("structured codec refusal was not routed")
	}
	if expectedSourceCodecRoute("Livepeer", "FORMAT_SPECIFIC", "SOURCE_CODEC_UNSUPPORTED: Livepeer transport cannot carry VP9") {
		t.Fatal("message text was treated as a codec refusal")
	}
}

func TestLiveProcessReplacementRunsMemoryAdmissionBeforeAnswer(t *testing.T) {
	reports, wg := stubDegradedReports(t)
	wg.Add(1)
	defer wg.Wait()
	oldLogger, oldAdmission := logger, admitLiveProcessReplacement
	logger = discardLog().Logger
	t.Cleanup(func() { logger, admitLiveProcessReplacement = oldLogger, oldAdmission })
	called := false
	admitLiveProcessReplacement = func(evt ProcessReplaceEvent, policy string) (string, error) {
		called = true
		return "", fmt.Errorf("waiting for processing memory")
	}
	code, answer := callProcessReplace(t, strings.Join([]string{"live+memory-admission", "Livepeer", failedLivepeerConfig, "2", "SOURCE_ROTATED", "requires local processing"}, "\n"))
	if !called || code != http.StatusOK || answer != noProcessReplacement {
		t.Fatalf("live replacement bypassed memory admission: called=%t code=%d answer=%s", called, code, answer)
	}
	wg.Wait()
	got := reports()
	if len(got) != 1 || got[0].ShortReason != "LOCAL_ADMISSION_REFUSED" || !strings.Contains(got[0].Reason, "waiting for processing memory") || got[0].ReplacementCount != 0 {
		t.Fatalf("admission failure misreported as source routing: %+v", got)
	}
}

func TestLiveReplacementSharesDurableLedgerWithoutOverridingWholePolicy(t *testing.T) {
	isolateProcessingOverrideState(t)
	previous := logger
	logger = discardLog().Logger
	t.Cleanup(func() { logger = previous })
	total := uint64(3*1024) * memoryMiB
	snapshot := func() (uint64, uint64, error) { return total, total, nil }
	source := mist.SourceMediaInfo{Width: 3840, Height: 2160}
	policy := fourKLadder(t)
	admitted, err := reserveLiveReplacement("live+reserved", policy, source, nil, snapshot)
	if err != nil || admitted == "" {
		t.Fatalf("first live ladder not admitted: %s %v", admitted, err)
	}
	if _, override := getProcessingProcessOverride("live+reserved"); override {
		t.Fatal("partial live ladder overwrote complete STREAM_PROCESS policy")
	}
	if err = setProcessingProcessOverride("processing+waiting", processingSourceProbePolicy, "waiting", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	bytes, err := processingMemoryEstimate(admitted, source)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := reserveProcessingMemory("processing+waiting", "waiting", policy, bytes, total, total); err != nil || ok {
		t.Fatalf("upload ignored running live encoders: admitted=%v err=%v", ok, err)
	}
	reconcileExpiredProcessingOverrides(map[string]any{"live+reserved": map[string]any{}}, time.Now())
	processingProcessOverridesMu.Lock()
	retained := processingOverrideRecords["live+reserved"].MemoryBytes
	processingProcessOverridesMu.Unlock()
	if retained != bytes {
		t.Fatalf("active live reservation lost: got=%d want=%d", retained, bytes)
	}
}

func TestLiveReplacementReportsAdmittedConfigCount(t *testing.T) {
	_, _ = stubDegradedReports(t)
	old := admitLiveProcessReplacement
	admitLiveProcessReplacement = func(_ ProcessReplaceEvent, _ string) (string, error) {
		return `[{"process":"AV","codec":"H264","resolution":"x180"}]`, nil
	}
	t.Cleanup(func() { admitLiveProcessReplacement = old })
	const stream = "live+admitted-count"
	events := RegisterProcessReplaceListener(stream)
	defer UnregisterProcessReplaceListener(stream)
	_, body := callProcessReplace(t, strings.Join([]string{stream, "Livepeer", failedLivepeerConfig, "2", "SOURCE_ROTATED", "upright required"}, "\n"))
	var configs []map[string]any
	if err := json.Unmarshal([]byte(body), &configs); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-events:
		if event.ReplacementCount != len(configs) {
			t.Fatalf("reported %d replacements but admitted %d", event.ReplacementCount, len(configs))
		}
	default:
		t.Fatal("replacement was not routed")
	}
}

func TestRefusedAdmissionStopsProbedSourceBeforeQueuing(t *testing.T) {
	isolateProcessingOverrideState(t)
	const stream = "processing+refused-probe"
	var active atomic.Bool
	var boots, nukes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/json_") {
			active.Store(true)
			boots.Add(1)
			_, _ = w.Write([]byte("{}"))
			return
		}
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body)+r.URL.RawQuery, "nuke_stream") {
			active.Store(false)
			nukes.Add(1)
		}
		streams := map[string]any{}
		if active.Load() {
			streams[stream] = map[string]any{"lastms": 60000.0, "health": map[string]any{"video_0": map[string]any{"codec": "H264", "id": 0.0, "idx": 0.0, "width": 3840.0, "height": 2160.0}}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"authorize": map[string]any{"status": "OK"}, "active_streams": streams})
	}))
	defer server.Close()
	h := &ProcessingJobHandler{logger: discardLog().Logger, mistServerURL: server.URL, storagePath: t.TempDir(), memorySnapshot: func() (uint64, uint64, error) { return 512 * memoryMiB, 512 * memoryMiB, nil }}
	var refused bool
	h.Handle(&ipcpb.ProcessingJobRequest{JobId: "refused-probe", ArtifactHash: "refused-probe", SourceUrl: server.URL + "/source.mp4", JobType: "process", ProcessesJson: `[{"process":"AV","codec":"H264","resolution":"0x2160"}]`, DeadlineUnixMs: time.Now().Add(time.Minute).UnixMilli()}, func(message *ipcpb.ControlMessage) {
		result := message.GetProcessingJobResult()
		if result != nil && result.GetOutputs()[mist.ProcessingRequiredMemoryBytes] != "" && result.GetStatus() != "cache_update" {
			refused = true
			if active.Load() || nukes.Load() != 1 {
				t.Error("queued capacity refusal left its source probe running")
			}
		}
	})
	if !refused || boots.Load() != 1 || nukes.Load() != 1 {
		t.Fatalf("wanted one probe and one cleanup before capacity deferral: refused=%t boots=%d nukes=%d", refused, boots.Load(), nukes.Load())
	}
	if policy, _ := getProcessingProcessOverride(stream); policy != "" {
		t.Fatalf("capacity deferral retained its probing policy: %s", policy)
	}
}
