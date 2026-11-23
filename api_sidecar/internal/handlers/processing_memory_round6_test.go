package handlers

import (
	"encoding/json"
	"errors"
	"github.com/sirupsen/logrus"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestFallbackStopsGenerationBeforeMeasuringMemory(t *testing.T) {
	isolateProcessingOverrideState(t)
	const stream, job = "processing+memory-retirement", "retirement"
	if err := setProcessingProcessOverride(stream, processingSourceProbePolicy, job, time.Now().Add(time.Hour), 700*memoryMiB); err != nil {
		t.Fatal(err)
	}
	client := &fakeProcessingRuntimeClient{}
	total := uint64(3072) * memoryMiB
	h := &ProcessingJobHandler{memorySnapshot: func() (uint64, uint64, error) {
		if !strings.Contains(strings.Join(client.calls, ","), "nuke_stream:") {
			return total, 700 * memoryMiB, nil
		}
		return total, total, nil
	}}
	_, err := h.admitLocalProcessingFallback(discardLog(), client, &ipcpb.ProcessingJobRequest{JobId: job}, stream, filepath.Join(t.TempDir(), "output.mkv"), 9, fourKLadder(t), mist.SourceMediaInfo{Width: 3840, Height: 2160}, nil, func(*ipcpb.ControlMessage) {})
	if err != nil {
		t.Fatalf("retiring generation counted twice: %v", err)
	}
	if !strings.Contains(strings.Join(client.calls, ","), "nuke_stream:") {
		t.Fatal("admitted over a running old generation")
	}
}
func TestAdmissionPreservesPermanentSourceFailure(t *testing.T) {
	err := errors.New("source unavailable: corrupt or unreadable source")
	if got := processingMemoryFailureStatus(err); got != "failed" {
		t.Fatalf("corrupt source burns retries: %s", got)
	}
}

func TestProcessingProbeResultBeforeAnyEncoderStarts(t *testing.T) {
	for _, kind := range []string{"corrupt"} {
		t.Run(kind, func(t *testing.T) {
			isolateProcessingOverrideState(t)
			hash := "roundsix" + kind
			stream := "processing+" + hash
			var booted atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/json_") {
					booted.Store(true)
					if kind == "corrupt" {
						signalProcessingSourceFailure(stream, "source unavailable: corrupt or unreadable source")
					}
					_, _ = w.Write([]byte("{}"))
					return
				}
				body, _ := io.ReadAll(r.Body)
				if strings.Contains(string(body)+r.URL.RawQuery, "nuke_stream") {
					booted.Store(false)
				}
				active := map[string]any{}
				if booted.Load() && kind != "corrupt" {
					codec := "H264"
					rotation := 0
					if kind == "unsupported" {
						codec = "VP9"
					}
					if kind == "rotated" {
						rotation = 90
					}
					active[stream] = map[string]any{"lastms": 60000.0, "health": map[string]any{"video_0": map[string]any{"codec": codec, "id": 0.0, "idx": 0.0, "width": 3840.0, "height": 2160.0, "rotation": float64(rotation)}}}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"authorize": map[string]any{"status": "OK"}, "active_streams": active})
			}))
			t.Cleanup(server.Close)
			h := NewProcessingJobHandler(logrus.New(), server.URL, t.TempDir(), t.TempDir())
			total := uint64(1536) * memoryMiB
			h.memorySnapshot = func() (uint64, uint64, error) { return total, total, nil }
			var profiles []map[string]any
			for _, height := range []int{2160, 1440, 1080, 720, 480, 360} {
				profiles = append(profiles, map[string]any{"height": height, "bitrate": 1000000})
			}
			encoded, err := json.Marshal([]map[string]any{{"process": "Livepeer", "target_profiles": profiles}})
			if err != nil {
				t.Fatal(err)
			}
			results := make(chan *ipcpb.ProcessingJobResult, 10)
			done := make(chan struct{})
			go func() {
				defer close(done)
				h.Handle(&ipcpb.ProcessingJobRequest{JobId: "job-" + hash, ArtifactHash: hash, SourceUrl: server.URL + "/source.mkv", JobType: "process", ProcessesJson: string(encoded)}, func(msg *ipcpb.ControlMessage) {
					if result := msg.GetProcessingJobResult(); result != nil {
						if result.GetStatus() == "cache_update" && result.GetOutputs()["processes_json"] == "" {
							return
						}
						results <- result
					}
				})
			}()
			t.Cleanup(func() {
				select {
				case <-done:
				case <-time.After(20 * time.Second):
					t.Error("handler did not finish")
				}
			})
			select {
			case result := <-results:
				if kind == "corrupt" {
					if result.GetStatus() != "failed" {
						t.Fatalf("corrupt source retried: %s %s", result.GetStatus(), result.GetError())
					}
				}
			case <-time.After(15 * time.Second):
				t.Fatal("probe did not report a verdict promptly")
			}
		})
	}
}

func TestProcessingResultEchoesDispatchedTenant(t *testing.T) {
	isolateProcessingOverrideState(t)
	const tenant = "7a1e0000-0000-4000-8000-000000000002"
	h := NewProcessingJobHandler(logrus.New(), "", t.TempDir(), t.TempDir())
	var got *ipcpb.ProcessingJobResult
	h.Handle(&ipcpb.ProcessingJobRequest{JobId: "tenant-result", TenantId: tenant, ArtifactHash: "tenantresult"}, func(msg *ipcpb.ControlMessage) {
		if result := msg.GetProcessingJobResult(); result != nil {
			got = result
		}
	})
	if got == nil || got.GetOutputs()["tenant_id"] != tenant {
		t.Fatalf("processing result lost the dispatched tenant: %+v", got)
	}
}
