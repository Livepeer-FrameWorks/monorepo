package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestAdmissionInfrastructureOutageDoesNotSpendTenantRetry(t *testing.T) {
	isolateProcessingOverrideState(t)
	const stream, job = "processing+admission-outage", "admission-outage"
	if err := setProcessingProcessOverride(stream, processingSourceProbePolicy, job, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	h := &ProcessingJobHandler{memorySnapshot: func() (uint64, uint64, error) { return 0, 0, errors.New("cgroup temporarily unreadable") }}
	_, err := h.admitProcessingPolicy(discardLog(), &ipcpb.ProcessingJobRequest{JobId: job}, stream, `[{"process":"AV","codec":"H264"}]`, mist.SourceMediaInfo{Width: 1920, Height: 1080}, nil, nil)
	outputs := processingMemoryFailureOutputs(err)
	if processingMemoryFailureStatus(err) != "retryable" || outputs[mist.ProcessingResultRetryCause] != "processing_node_unavailable" {
		t.Fatalf("infrastructure admission outage spends tenant retry: err=%v status=%s outputs=%v", err, processingMemoryFailureStatus(err), outputs)
	}
}

func TestReadinessStopsAtWholeUploadDeadline(t *testing.T) {
	const stream = "processing+upload-deadline"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"authorize": map[string]any{"status": "OK"}, "active_streams": map[string]any{stream: map[string]any{}}})
	}))
	defer srv.Close()
	h := &ProcessingJobHandler{mistServerURL: srv.URL}
	client := mist.NewClient(discardLog().Logger, mist.ClientConfig{BaseURL: srv.URL})
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	req := &ipcpb.ProcessingJobRequest{DeadlineUnixMs: time.Now().Add(60 * time.Millisecond).UnixMilli()}
	start := time.Now()
	_, _, err := h.waitForProcessingStreamReady(ctx, discardLog(), client, req, stream, "[]", nil, nil, nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 400*time.Millisecond {
		t.Fatalf("source readiness crossed whole-upload deadline: elapsed=%s error=%v", time.Since(start), err)
	}
}
