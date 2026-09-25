package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/sirupsen/logrus"
)

// A processing input that builds a missing header stays out of active_streams
// for longer than the boot request's timeout. Readiness must keep waiting on
// that one boot instead of sending another /json_, which starts a second
// input for the same stream.
func TestWaitForProcessingStreamReadyBootsOnceWhileInputBoots(t *testing.T) {
	prevTimeout := processingBootRequestTimeout
	processingBootRequestTimeout = 50 * time.Millisecond
	t.Cleanup(func() { processingBootRequestTimeout = prevTimeout })

	const stream = "processing+bootonce"
	var boots, apiCalls atomic.Int32
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/json_") {
			boots.Add(1)
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return
		}
		body := map[string]any{"authorize": map[string]any{"status": "OK"}}
		active := map[string]any{}
		// The stream appears only after several polls, as it does while
		// Mist is still generating the header.
		if apiCalls.Add(1) > 4 {
			active[stream] = map[string]any{
				"health": map[string]any{
					"video_H264_1280x720_30fps_0": map[string]any{"codec": "H264", "width": 1280.0, "height": 720.0},
				},
			}
		}
		body["active_streams"] = active
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)

	h := &ProcessingJobHandler{mistServerURL: srv.URL}
	client := mist.NewClient(logging.NewLogger(), mist.ClientConfig{BaseURL: srv.URL})
	req := &ipcpb.ProcessingJobRequest{ProcessesJson: `[]`}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, _, err := h.waitForProcessingStreamReady(ctx, logrus.NewEntry(logrus.New()), client, req, stream, req.GetProcessesJson(), nil, nil, nil, map[string]int{})
	if err != nil {
		t.Fatalf("readiness: %v", err)
	}
	if got := boots.Load(); got != 1 {
		t.Fatalf("boot requests = %d, want 1 while the input was still booting", got)
	}
}

// Foghorn persists a fallback ladder only on the exact dispatched job, so the
// cache update must carry that job's id rather than a synthetic one.
func TestUpdateProcessConfigCacheCarriesDispatchedJobID(t *testing.T) {
	var sent []*ipcpb.ControlMessage
	h := &ProcessingJobHandler{}
	h.updateProcessConfigCache(func(m *ipcpb.ControlMessage) { sent = append(sent, m) },
		"4a8b0f0e-1c2d-4e5f-8a9b-0c1d2e3f4a5b", "artifacthash", `[{"process":"AV"}]`)
	if len(sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(sent))
	}
	result := sent[0].GetProcessingJobResult()
	if result.GetJobId() != "4a8b0f0e-1c2d-4e5f-8a9b-0c1d2e3f4a5b" || result.GetStatus() != "cache_update" {
		t.Fatalf("cache update = job %q status %q, want the dispatched job id", result.GetJobId(), result.GetStatus())
	}
	if result.GetOutputs()["artifact_hash"] != "artifacthash" {
		t.Fatalf("artifact_hash output = %q", result.GetOutputs()["artifact_hash"])
	}
}

// A new Helmsman process owns no processing jobs, so every processing+ stream
// still running in Mist belongs to a job the previous process lost. Those are
// stopped; streams claimed by a job of this process and other streams stay.
func TestNukeOrphanProcessingStreamsStopsOnlyUnclaimedProcessingStreams(t *testing.T) {
	const orphan, claimed = "processing+orphanjob", "processing+claimedjob"
	if _, _, ok := claimPendingJob(claimed, "job-claimed"); !ok {
		t.Fatal("could not claim test stream")
	}
	t.Cleanup(func() { releasePendingJob(claimed, "job-claimed") })

	var mu sync.Mutex
	var nuked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var command map[string]any
		_ = json.Unmarshal([]byte(r.URL.Query().Get("command")), &command)
		if name, ok := command["nuke_stream"].(string); ok {
			mu.Lock()
			nuked = append(nuked, name)
			mu.Unlock()
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"authorize": map[string]any{"status": "OK"},
			"active_streams": map[string]any{
				orphan: map[string]any{}, claimed: map[string]any{}, "live+somestream": map[string]any{},
			},
		})
	}))
	t.Cleanup(srv.Close)

	client := mist.NewClient(logging.NewLogger(), mist.ClientConfig{BaseURL: srv.URL})
	stopped := nukeOrphanProcessingStreams(client, logging.NewLogger())
	mu.Lock()
	defer mu.Unlock()
	if len(nuked) != 1 || nuked[0] != orphan || len(stopped) != 1 || stopped[0] != orphan {
		t.Fatalf("nuked %v (reported %v), want only %s", nuked, stopped, orphan)
	}
}

// Register reports the jobs this process is running, so Foghorn can tell a
// restarted sidecar from a reconnecting one.
func TestActiveProcessingJobIDsListsClaimedJobs(t *testing.T) {
	if _, _, ok := claimPendingJob("processing+inventoryjob", "4a8b0f0e-0000-4000-8000-00000000abcd"); !ok {
		t.Fatal("could not claim test stream")
	}
	t.Cleanup(func() { releasePendingJob("processing+inventoryjob", "4a8b0f0e-0000-4000-8000-00000000abcd") })
	found := false
	for _, id := range ActiveProcessingJobIDs() {
		if id == "4a8b0f0e-0000-4000-8000-00000000abcd" {
			found = true
		}
	}
	if !found {
		t.Fatalf("active jobs %v do not include the claimed job", ActiveProcessingJobIDs())
	}
}
