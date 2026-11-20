package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"frameworks/api_sidecar/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/models"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestNodeAudienceExcludesProcessingAndSurvivesRestart(t *testing.T) {
	setupTriggerTest(t, "tenant")
	var mu sync.Mutex
	tagged := false
	taggedDone := make(chan struct{}, 1)
	clientsCalls := 0
	tagsRequested := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var command map[string]json.RawMessage
		_ = json.Unmarshal([]byte(r.URL.Query().Get("command")), &command)
		if raw, ok := command["tag_sessid"]; ok {
			var tags map[string]string
			_ = json.Unmarshal(raw, &tags)
			if tags["b2"] == "fwproc" {
				tagged = true
				select {
				case taggedDone <- struct{}{}:
				default:
				}
			}
		}
		if raw, ok := command["clients"]; ok {
			clientsCalls++
			var query struct {
				Fields []string `json:"fields"`
			}
			_ = json.Unmarshal(raw, &query)
			tagsRequested = slices.Contains(query.Fields, "tags")
		}
		tags := []string{}
		if tagged {
			tags = append(tags, "fwproc")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"authorize": map[string]any{"status": "OK"},
			"streams":   map[string]any{"live+stream": map[string]any{"curr": []int{2, 1, 1, 1, 0}}},
			"clients": map[string]any{"fields": []string{"stream", "sessid", "tags"}, "data": [][]any{
				{"live+stream", "a1", []string{}}, {"live+stream", "b2", tags}, {"live+stream", "Iinput", []string{}}, {"live+stream", "Ooutput", []string{}}, {"live+stream", "Uunknown", []string{}},
			}},
		})
	}))
	defer server.Close()
	pm := newStreamWSTestMonitor(t)
	pm.nodeID, pm.baseURL = triggerTestNodeID, server.URL
	pm.updateChannel = make(chan models.NodeUpdate, 1)
	original := prometheusMonitor
	prometheusMonitor = pm
	t.Cleanup(func() { prometheusMonitor = original })
	runtime := newMistNodeRuntime(1, pm.nodeID, server.URL, "", "")
	pm.nodeRuntime = runtime
	t.Cleanup(runtime.stop)
	stubSendMistTrigger(t, func(*ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
		return &control.MistTriggerResult{Response: "true"}, nil
	})
	ctx, _ := newWebhookContext("live+stream\n127.0.0.1\nfwproc-job\nHLS\nhttp://localhost/live/index.m3u8?tkn=fwproc-job\nb2")
	HandleUserNew(ctx)
	select {
	case <-taggedDone:
	case <-time.After(2 * time.Second):
		t.Fatal("processing tag was not persisted before simulated restart")
	}
	checkAudience := func() {
		pm.emitNodeLifecycleRuntime(pm.nodeRuntime)
		update := <-pm.updateChannel
		if update.Error != nil {
			t.Fatalf("node poll failed: %v", update.Error)
		}
		stream := pm.nodeLifecycleTrigger(pm.nodeID, update.JSONData).GetNodeLifecycleUpdate().GetStreams()["stream"]
		if stream == nil || stream.AudienceViewers == nil || stream.GetAudienceViewers() != 1 || stream.GetTotal() != 2 {
			t.Errorf("node census = %v, want audience 1 and raw total 2", stream)
		}
	}
	checkAudience()
	runtime2 := newMistNodeRuntime(2, pm.nodeID, server.URL, "", "")
	pm.nodeRuntime = runtime2
	t.Cleanup(runtime2.stop)
	checkAudience()
	mu.Lock()
	defer mu.Unlock()
	if !tagged {
		t.Error("processing USER_NEW never tagged its Mist session")
	}
	if clientsCalls != 2 || !tagsRequested {
		t.Errorf("client queries=%d tags requested=%v, want one query per poll with tags", clientsCalls, tagsRequested)
	}
}

func TestReviewRound2EmptyClientSessionDoesNotDiscardAudience(t *testing.T) {
	runtime := newMistNodeRuntime(1, "node", "http://localhost", "", "")
	t.Cleanup(runtime.stop)
	counts, _, err := runtime.audienceViewers(map[string]any{"clients": map[string]any{"fields": []any{"stream", "sessid", "tags"}, "data": []any{[]any{"live+stream", "valid", []any{}}, []any{"live+stream", "", []any{}}}}})
	if err != nil || counts["live+stream"] != 1 {
		t.Fatalf("empty session discarded valid audience: %v %v", counts, err)
	}
}

func TestProcessingUserNewDoesNotWaitForMistTagging(t *testing.T) {
	setupTriggerTest(t, "tenant")
	release := make(chan struct{})
	tagging := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case tagging <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"authorize": map[string]any{"status": "OK"}})
	}))
	defer server.Close()
	pm := newStreamWSTestMonitor(t)
	pm.nodeID, pm.baseURL = triggerTestNodeID, server.URL
	original := prometheusMonitor
	prometheusMonitor = pm
	t.Cleanup(func() { prometheusMonitor = original })
	runtime := newMistNodeRuntime(1, pm.nodeID, server.URL, "", "")
	pm.nodeRuntime = runtime
	t.Cleanup(runtime.stop)
	stubSendMistTrigger(t, func(*ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
		return &control.MistTriggerResult{Response: "true"}, nil
	})
	ctx, recorder := newWebhookContext("live+stream\n127.0.0.1\nfwproc-job\nHLS\nhttp://localhost/live/index.m3u8?tkn=fwproc-job\nb2")
	done := make(chan struct{})
	go func() { HandleUserNew(ctx); close(done) }()
	t.Cleanup(func() { close(release); <-done })
	select {
	case <-done:
		if recorder.Code != http.StatusOK || recorder.Body.String() != "true" {
			t.Fatalf("webhook response=%d %q", recorder.Code, recorder.Body.String())
		}
	case <-time.After(500 * time.Millisecond):
		t.Error("USER_NEW waited for Mist tag_sessid before responding")
	}
	select {
	case <-tagging:
	case <-time.After(time.Second):
		t.Error("processing session was not queued for tagging")
	}
}
