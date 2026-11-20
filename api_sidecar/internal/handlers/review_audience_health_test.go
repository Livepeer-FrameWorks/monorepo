package handlers

import (
	"encoding/json"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/models"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestReviewCensusFailureDoesNotDiscardHealthyNodeMetrics(t *testing.T) {
	for _, failure := range []string{"invalid-session-type", "unavailable", "slow"} {
		t.Run(failure, func(t *testing.T) {
			setupTriggerTest(t, "tenant")
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var command map[string]json.RawMessage
				_ = json.Unmarshal([]byte(r.URL.Query().Get("command")), &command)
				if _, ok := command["clients"]; ok {
					if failure == "slow" {
						select {
						case <-release:
						case <-r.Context().Done():
							return
						}
					}
					if failure != "invalid-session-type" {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"authorize": map[string]any{"status": "OK"}, "cpu": 10, "bw": []int{123, 456}, "streams": map[string]any{"live+stream": map[string]any{"curr": []int{2, 1, 0, 0, 0}}}, "clients": map[string]any{"fields": []string{"stream", "sessid", "tags"}, "data": [][]any{{"live+stream", nil, []string{}}}}})
			}))
			defer server.Close()
			pm := newStreamWSTestMonitor(t)
			pm.nodeID, pm.baseURL = triggerTestNodeID, server.URL
			pm.updateChannel = make(chan models.NodeUpdate, 2)
			runtime := newMistNodeRuntime(1, pm.nodeID, server.URL, "", "")
			pm.nodeRuntime = runtime
			defer runtime.stop()
			done := make(chan struct{})
			go func() { pm.emitNodeLifecycleRuntime(runtime); close(done) }()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Error("slow census blocked node metrics beyond a lifecycle-safe deadline")
			}
			close(release)
			<-done
			update := <-pm.updateChannel
			if update.Error != nil || update.JSONData == nil {
				t.Fatalf("census failure discarded healthy node telemetry: %v", update.Error)
			}
			if _, ok := update.JSONData[audienceSnapshotKey]; ok {
				t.Fatal("failed census fabricated authoritative audience")
			}
			if failure != "slow" {
				report, err := pm.connectionNodeLifecycleReport(t.Context())
				if err != nil || report == nil {
					t.Fatalf("census failure refused reconnect telemetry: %v", err)
				}
			}
		})
	}
}
