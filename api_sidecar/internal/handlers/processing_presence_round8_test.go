package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
)

func TestDrainRetriesUnknownControllerSnapshot(t *testing.T) {
	calls := 0
	err := drainProcessingGenerationFromActiveStreams(discardLog(), "processing+presence", func() (map[string]interface{}, error) {
		calls++
		if calls == 1 {
			return map[string]interface{}{}, nil
		}
		if calls == 2 {
			return map[string]interface{}{"active_streams": map[string]interface{}{"processing+presence": map[string]interface{}{}}}, nil
		}
		return map[string]interface{}{"active_streams": nil}, nil
	}, time.Second, time.Millisecond)
	if err != nil || calls != 3 {
		t.Fatalf("unknown controller snapshot was not retried until stopped: calls=%d err=%v", calls, err)
	}
}

func TestWaitStoppedRejectsUnknownControllerSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"authorize":{"status":"OK"}}`)) }))
	t.Cleanup(server.Close)
	client := mist.NewClient(discardLog().Logger, mist.ClientConfig{})
	client.BaseURL = server.URL
	if waitProcessingStreamStopped(client, "processing+presence", time.Millisecond) {
		t.Fatal("unknown controller response released a running generation")
	}
}

func TestUnknownControllerSnapshotKeepsMemoryReservation(t *testing.T) {
	isolateProcessingOverrideState(t)
	const stream, job = "processing+unknown-reservation", "unknown-reservation"
	if err := setProcessingProcessOverride(stream, processingSourceProbePolicy, job, time.Now().Add(time.Hour), 256*memoryMiB); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"authorize":{"status":"OK"}}`)) }))
	t.Cleanup(server.Close)
	client := mist.NewClient(discardLog().Logger, mist.ClientConfig{})
	client.BaseURL = server.URL
	(&ProcessingJobHandler{}).finishProcessingMemory(discardLog(), client, stream, job)
	processingProcessOverridesMu.Lock()
	defer processingProcessOverridesMu.Unlock()
	if processingOverrideRecords[stream].MemoryBytes != 256*memoryMiB {
		t.Fatal("unknown controller state released the running generation's reservation")
	}
}

func TestIdleControllerReleasesExpiredProcessingMemory(t *testing.T) {
	isolateProcessingOverrideState(t)
	const stream = "processing+idle-reservation"
	if err := setProcessingProcessOverride(stream, processingSourceProbePolicy, "idle-reservation", time.Now().Add(-time.Minute), 256*memoryMiB); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"authorize":{"status":"OK"},"active_streams":null}`))
	}))
	t.Cleanup(server.Close)
	reconcileProcessingOverridePersistence(server.URL, discardLog().Logger)
	processingProcessOverridesMu.Lock()
	defer processingProcessOverridesMu.Unlock()
	if record, ok := processingOverrideRecords[stream]; ok {
		t.Fatalf("idle controller retained expired processing memory: %d bytes", record.MemoryBytes)
	}
}
