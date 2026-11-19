package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/models"
)

func TestNodePollPublishesConfiguredMP4Methods(t *testing.T) {
	data, err := os.ReadFile("../../../pkg/mist/testdata/output-catalog-production.json")
	if err != nil {
		t.Fatal(err)
	}
	var inventory map[string]any
	if err := json.Unmarshal(data, &inventory); err != nil {
		t.Fatal(err)
	}
	inventory["authorize"] = map[string]any{"status": "OK"}
	inventory["clients"] = map[string]any{
		"fields": []string{"stream", "sessid", "tags"}, "data": []any{},
	}
	var unavailable atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metrics.json" {
			_ = json.NewEncoder(w).Encode(map[string]any{"outputs": inventory["outputs"], "streams": map[string]any{}})
			return
		}
		var command map[string]any
		_ = json.Unmarshal([]byte(r.URL.Query().Get("command")), &command)
		if command["capabilities"] == true && unavailable.Load() {
			http.Error(w, "inventory unavailable", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(inventory)
	}))
	defer server.Close()
	pm := newStreamWSTestMonitor(t)
	pm.nodeID, pm.baseURL = "catalog-node", server.URL
	pm.updateChannel = make(chan models.NodeUpdate, 1)
	runtime := newMistNodeRuntime(1, pm.nodeID, server.URL, "", "")
	pm.nodeRuntime = runtime
	t.Cleanup(runtime.stop)
	pm.emitNodeLifecycleRuntime(runtime)
	update := <-pm.updateChannel
	if update.Error != nil {
		t.Fatal(update.Error)
	}
	outputs, _ := update.JSONData["outputs"].(map[string]any)
	if outputs["MP4 progressive"] != "https://edge.example/view/$.mp4" {
		t.Fatalf("node poll lost enabled progressive MP4: %v", outputs["MP4 progressive"])
	}
	if outputs["MP4 WebSocket"] != "wss://edge.example/view/$.mp4" {
		t.Fatalf("node poll lost enabled WebSocket MP4: %v", outputs["MP4 WebSocket"])
	}
	unavailable.Store(true)
	pm.emitNodeLifecycleRuntime(runtime)
	update = <-pm.updateChannel
	if update.Error != nil {
		t.Fatal("method-inventory failure discarded healthy metrics:", update.Error)
	}
	outputs, _ = update.JSONData["outputs"].(map[string]any)
	if outputs["MP4 progressive"] != nil || outputs["MP4 WebSocket"] != nil {
		t.Fatal("failed inventory retained stale method capabilities")
	}
	if outputs["HLS"] == nil {
		t.Fatal("failed inventory discarded the observed HLS listener")
	}
}
