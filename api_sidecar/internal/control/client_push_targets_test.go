package control

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	sidecarcfg "frameworks/api_sidecar/internal/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// resetActivePushes isolates the package-global multistream bookkeeping map.
func resetActivePushes(t *testing.T) {
	t.Helper()
	activePushesMu.Lock()
	prev := activePushes
	activePushes = map[string]map[string]int{}
	activePushesMu.Unlock()
	t.Cleanup(func() {
		activePushesMu.Lock()
		activePushes = prev
		activePushesMu.Unlock()
	})
}

func activePushesHas(stream string) bool {
	activePushesMu.Lock()
	defer activePushesMu.Unlock()
	_, ok := activePushes[stream]
	return ok
}

func TestHandleActivatePushTargets(t *testing.T) {
	t.Run("nil request is a no-op", func(t *testing.T) {
		resetActivePushes(t)
		handleActivatePushTargets(logging.NewLogger(), nil)
	})

	t.Run("no targets is a no-op", func(t *testing.T) {
		resetActivePushes(t)
		handleActivatePushTargets(logging.NewLogger(), &ipcpb.ActivatePushTargets{StreamName: "live+a"})
		if activePushesHas("live+a") {
			t.Fatal("empty target set must not register the stream")
		}
	})

	t.Run("missing config is a no-op", func(t *testing.T) {
		resetActivePushes(t)
		withConfig(t, nil)
		handleActivatePushTargets(logging.NewLogger(), &ipcpb.ActivatePushTargets{
			StreamName: "live+a",
			Targets:    []*ipcpb.PushTargetSpec{{TargetId: "t1", TargetUri: "rtmp://x/app"}},
		})
	})

	t.Run("starts a push per target and registers the stream", func(t *testing.T) {
		resetActivePushes(t)
		mock := newMockMistServer(t)
		withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: mock.srv.URL})

		handleActivatePushTargets(logging.NewLogger(), &ipcpb.ActivatePushTargets{
			StreamName: "live+a",
			Targets: []*ipcpb.PushTargetSpec{
				{TargetId: "t1", Name: "yt", TargetUri: "rtmp://yt/app"},
				{TargetId: "t2", Name: "tw", TargetUri: "rtmp://tw/app"},
			},
		})

		if n := len(mock.callsContainingKey("push_start")); n != 2 {
			t.Fatalf("expected 2 push_start calls, got %d", n)
		}
		if !activePushesHas("live+a") {
			t.Fatal("active stream must be registered for later deactivation")
		}
	})

	t.Run("a failing target is logged and does not abort the rest", func(t *testing.T) {
		resetActivePushes(t)
		withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: errMistServer(t)})
		// Both PushStarts fail (500); handler must not panic and still
		// register the stream (so a later deactivate can reconcile).
		handleActivatePushTargets(logging.NewLogger(), &ipcpb.ActivatePushTargets{
			StreamName: "live+a",
			Targets: []*ipcpb.PushTargetSpec{
				{TargetId: "t1", TargetUri: "rtmp://x/app"},
				{TargetId: "t2", TargetUri: "rtmp://y/app"},
			},
		})
		if !activePushesHas("live+a") {
			t.Fatal("stream should still be registered even if pushes fail to start")
		}
	})
}

// pushListMistServer answers auth + push_list (with the given entries) and
// records every command, so the deactivation path can be driven and asserted.
// Each push entry is [id, stream, target, actual].
func pushListMistServer(t *testing.T, entries [][]any) (url string, calls func(string) int) {
	t.Helper()
	var mu sync.Mutex
	var requests []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cmd := r.URL.Query().Get("command")
		var parsed map[string]any
		if cmd != "" {
			_ = json.Unmarshal([]byte(cmd), &parsed)
		} else {
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &parsed)
		}
		mu.Lock()
		requests = append(requests, parsed)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if _, ok := parsed["authorize"]; ok {
			_, _ = w.Write([]byte(`{"authorize":{"status":"OK"}}`))
			return
		}
		if _, ok := parsed["push_list"]; ok {
			resp, _ := json.Marshal(map[string]any{"push_list": entries})
			_, _ = w.Write(resp)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func(key string) int {
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for _, req := range requests {
			if _, ok := req[key]; ok {
				n++
			}
		}
		return n
	}
}

func TestHandleDeactivatePushTargets(t *testing.T) {
	t.Run("nil request is a no-op", func(t *testing.T) {
		handleDeactivatePushTargets(logging.NewLogger(), nil)
	})

	t.Run("empty stream name is a no-op", func(t *testing.T) {
		handleDeactivatePushTargets(logging.NewLogger(), &ipcpb.DeactivatePushTargets{})
	})

	t.Run("missing config is a no-op", func(t *testing.T) {
		withConfig(t, nil)
		handleDeactivatePushTargets(logging.NewLogger(), &ipcpb.DeactivatePushTargets{StreamName: "live+a"})
	})

	t.Run("push_list error is handled", func(t *testing.T) {
		withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: errMistServer(t)})
		handleDeactivatePushTargets(logging.NewLogger(), &ipcpb.DeactivatePushTargets{StreamName: "live+a"})
	})

	t.Run("no matching pushes still clears local bookkeeping", func(t *testing.T) {
		resetActivePushes(t)
		activePushesMu.Lock()
		activePushes["live+a"] = map[string]int{"t1": 1}
		activePushesMu.Unlock()

		mock := newMockMistServer(t) // returns no push_list
		withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: mock.srv.URL})

		handleDeactivatePushTargets(logging.NewLogger(), &ipcpb.DeactivatePushTargets{StreamName: "live+a"})
		if activePushesHas("live+a") {
			t.Fatal("local push bookkeeping must be cleared on deactivation")
		}
	})

	t.Run("stops only pushes matching the stream", func(t *testing.T) {
		resetActivePushes(t)
		url, calls := pushListMistServer(t, [][]any{
			{float64(1), "live+a", "rtmp://a/app", "rtmp://a/app"},
			{float64(2), "live+other", "rtmp://o/app", "rtmp://o/app"},
		})
		withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: url})

		handleDeactivatePushTargets(logging.NewLogger(), &ipcpb.DeactivatePushTargets{StreamName: "live+a"})

		if n := calls("push_stop"); n != 1 {
			t.Fatalf("expected exactly one push_stop (only the matching stream), got %d", n)
		}
	})
}
