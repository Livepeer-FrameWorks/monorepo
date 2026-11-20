package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_sidecar/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/models"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestFailedMistFetchDoesNotForwardCachedReport(t *testing.T) {
	pm := newStreamWSTestMonitor(t)
	runtime := newMistNodeRuntime(1, "edge", "http://mist.invalid", "", "")
	t.Cleanup(runtime.stop)
	pm.nodeRuntime = runtime
	pm.nodeID, pm.baseURL = runtime.nodeID, runtime.baseURL
	pm.stopChannel = make(chan bool)
	pm.updateChannel = make(chan models.NodeUpdate, 1)
	pm.nodeMetricsWake = make(chan struct{}, 1)
	pm.lastJSONData = map[string]any{"bw": []any{float64(2000), float64(1000)}}
	pm.updateChannel <- models.NodeUpdate{NodeID: runtime.nodeID, BaseURL: runtime.baseURL, Error: errors.New("Mist fetch failed")}
	close(pm.updateChannel)
	pm.processUpdates()
	var sends atomic.Int32
	pm.sendControlTriggerCtx = func(context.Context, *ipcpb.MistTrigger, logging.Logger) (*control.MistTriggerResult, error) {
		sends.Add(1)
		return &control.MistTriggerResult{}, nil
	}
	pm.forwardNodeMetricsContext(context.Background(), runtime)
	if len(pm.nodeMetricsWake) != 0 || sends.Load() != 0 {
		t.Fatalf("failed Mist read scheduled %d wakes and sent %d stale reports", len(pm.nodeMetricsWake), sends.Load())
	}
}

func TestConnectionReportDoesNotFallBackAfterMistFetchError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	pm := newStreamWSTestMonitor(t)
	runtime := newMistNodeRuntime(1, "edge", server.URL, "", "")
	t.Cleanup(runtime.stop)
	pm.nodeRuntime = runtime
	pm.nodeID, pm.baseURL = runtime.nodeID, runtime.baseURL
	pm.lastJSONData = map[string]any{"cpu": float64(10)}
	report, err := pm.connectionNodeLifecycleReport(context.Background())
	if err == nil || report != nil {
		t.Fatalf("failed reconnect fetch returned cached report=%v err=%v", report, err)
	}
	var sends atomic.Int32
	pm.sendControlTriggerCtx = func(context.Context, *ipcpb.MistTrigger, logging.Logger) (*control.MistTriggerResult, error) {
		sends.Add(1)
		return &control.MistTriggerResult{}, nil
	}
	pm.forwardNodeMetricsContext(context.Background(), runtime)
	if sends.Load() != 0 {
		t.Fatal("failed reconnect fetch left cached metrics eligible for the periodic forwarder")
	}
}

func TestNodeRateResetAndRapidConversion(t *testing.T) {
	for _, test := range []struct {
		name     string
		up, down float64
	}{
		{"receive counter reset", 2000, 1000},
		{"send counter reset", 500, 10000},
	} {
		t.Run(test.name, func(t *testing.T) {
			pm := &PrometheusMonitor{lastBwUp: 1000, lastBwDown: 5000, lastPollTime: time.Now().Add(-10 * time.Second)}
			report := pm.convertNodeAPIToMistTrigger("edge", map[string]any{"bw": []any{test.up, test.down}}, logging.NewLogger()).GetNodeLifecycleUpdate()
			if report.GetUpSpeed() != 0 || report.GetDownSpeed() != 0 || report.ObservedValid == nil || report.GetObservedValid() {
				t.Fatalf("counter reset must invalidate both rates: up=%d down=%d valid=%v", report.GetUpSpeed(), report.GetDownSpeed(), report.ObservedValid)
			}
		})
	}
	pm := &PrometheusMonitor{lastBwUp: 1000, lastBwDown: 5000, lastPollTime: time.Now().Add(-10 * time.Second)}
	data := map[string]any{"bw": []any{float64(2000), float64(10000)}}
	first := pm.convertNodeAPIToMistTrigger("edge", data, logging.NewLogger()).GetNodeLifecycleUpdate()
	second := pm.convertNodeAPIToMistTrigger("edge", data, logging.NewLogger()).GetNodeLifecycleUpdate()
	if first.GetUpSpeed() == 0 || second.GetUpSpeed() != first.GetUpSpeed() || second.GetDownSpeed() != first.GetDownSpeed() {
		t.Fatalf("rapid conversion erased rate: first=%d/%d second=%d/%d", first.GetUpSpeed(), first.GetDownSpeed(), second.GetUpSpeed(), second.GetDownSpeed())
	}
}

func TestNodeRateConversionsAreSerialized(t *testing.T) {
	pm := &PrometheusMonitor{lastBwUp: 1000, lastBwDown: 5000, lastPollTime: time.Now().Add(-10 * time.Second)}
	data := map[string]any{"bw": []any{float64(2000), float64(10000)}}
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 10 {
				pm.convertNodeAPIToMistTrigger("edge", data, logging.NewLogger())
			}
		}()
	}
	workers.Wait()
}
