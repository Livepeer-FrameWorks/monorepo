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

func TestHandleActivatePushTargets(t *testing.T) {
	t.Run("nil request is a no-op", func(t *testing.T) {
		handleActivatePushTargets(logging.NewLogger(), nil, nil)
	})

	t.Run("no targets is a no-op", func(t *testing.T) {
		handleActivatePushTargets(logging.NewLogger(), &ipcpb.ActivatePushTargets{StreamName: "live+a"}, nil)
	})

	t.Run("missing config is a no-op", func(t *testing.T) {
		withConfig(t, nil)
		RecordAdmittedIngestGeneration("live+a", "gen-a", 201)
		handleActivatePushTargets(logging.NewLogger(), &ipcpb.ActivatePushTargets{
			StreamName:       "live+a",
			SourceGeneration: "gen-a",
			Targets:          []*ipcpb.PushTargetSpec{{TargetId: "t1", TargetUri: "rtmp://x/app"}},
		}, nil)
	})

	t.Run("starts a push per target and registers the stream", func(t *testing.T) {
		// Stateful mock: push_list reflects pushes started so far, so the handler's post-start
		// confirmation (process creation, not just command parsing) can pass.
		var pmu sync.Mutex
		var startedPushes [][]any
		var startCalls int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cmd := r.URL.Query().Get("command")
			var parsed map[string]any
			if cmd != "" {
				_ = json.Unmarshal([]byte(cmd), &parsed)
			} else {
				body, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(body, &parsed)
			}
			w.Header().Set("Content-Type", "application/json")
			if _, ok := parsed["authorize"]; ok {
				_, _ = w.Write([]byte(`{"authorize":{"status":"OK"}}`))
				return
			}
			if ps, ok := parsed["push_start"].(map[string]any); ok {
				pmu.Lock()
				startCalls++
				startedPushes = append(startedPushes, []any{float64(startCalls), ps["stream"], ps["target"], ps["target"]})
				pmu.Unlock()
				_, _ = w.Write([]byte(`{}`))
				return
			}
			if _, ok := parsed["push_list"]; ok {
				pmu.Lock()
				resp, _ := json.Marshal(map[string]any{"push_list": startedPushes})
				pmu.Unlock()
				_, _ = w.Write(resp)
				return
			}
			_, _ = w.Write([]byte(`{}`))
		}))
		t.Cleanup(srv.Close)
		withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: srv.URL})
		RecordAdmittedIngestGeneration("live+a", "gen-a", 201)

		var acks []*ipcpb.ActivatePushTargetsResult
		handleActivatePushTargets(logging.NewLogger(), &ipcpb.ActivatePushTargets{
			StreamName:       "live+a",
			SourceGeneration: "gen-a",
			Targets: []*ipcpb.PushTargetSpec{
				{TargetId: "t1", Name: "yt", TargetUri: "rtmp://yt/app"},
				{TargetId: "t2", Name: "tw", TargetUri: "rtmp://tw/app"},
			},
		}, func(m *ipcpb.ControlMessage) { acks = append(acks, m.GetActivatePushTargetsResult()) })

		pmu.Lock()
		gotStarts := startCalls
		pmu.Unlock()
		if gotStarts != 2 {
			t.Fatalf("expected 2 push_start calls, got %d", gotStarts)
		}
		if len(acks) != 1 || !acks[0].GetConverged() {
			t.Fatalf("a fully started activation must acknowledge converged=true, got %+v", acks)
		}
	})

	t.Run("a failing target is logged and does not abort the rest", func(t *testing.T) {
		withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: errMistServer(t)})
		RecordAdmittedIngestGeneration("live+a", "gen-a", 201)
		// The Mist API is down (500s): the push list cannot be read, so the
		// handler fails closed — no blind PushStart — and acknowledges
		// converged=false so the durable obligation retries. The stream is
		// still registered so a later deactivate can reconcile.
		var acks []*ipcpb.ActivatePushTargetsResult
		handleActivatePushTargets(logging.NewLogger(), &ipcpb.ActivatePushTargets{
			StreamName:       "live+a",
			SourceGeneration: "gen-a",
			Targets: []*ipcpb.PushTargetSpec{
				{TargetId: "t1", TargetUri: "rtmp://x/app"},
				{TargetId: "t2", TargetUri: "rtmp://y/app"},
			},
		}, func(m *ipcpb.ControlMessage) { acks = append(acks, m.GetActivatePushTargetsResult()) })
		if len(acks) != 1 || acks[0].GetConverged() {
			t.Fatalf("a failed activation must acknowledge converged=false for the obligation retry, got %+v", acks)
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
		RecordAdmittedIngestGeneration("live+a", "gen-a", 201)
		handleDeactivatePushTargets(logging.NewLogger(), &ipcpb.DeactivatePushTargets{StreamName: "live+a", SourceGeneration: "gen-a"})
	})

	t.Run("push_list error is handled", func(t *testing.T) {
		withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: errMistServer(t)})
		RecordAdmittedIngestGeneration("live+a", "gen-a", 201)
		handleDeactivatePushTargets(logging.NewLogger(), &ipcpb.DeactivatePushTargets{StreamName: "live+a", SourceGeneration: "gen-a"})
	})

	t.Run("no matching pushes is a no-op", func(t *testing.T) {
		mock := newMockMistServer(t) // returns no push_list
		withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: mock.srv.URL})
		RecordAdmittedIngestGeneration("live+a", "gen-a", 201)
		handleDeactivatePushTargets(logging.NewLogger(), &ipcpb.DeactivatePushTargets{StreamName: "live+a", SourceGeneration: "gen-a"})
	})

	t.Run("stops only pushes matching the stream", func(t *testing.T) {
		url, calls := pushListMistServer(t, [][]any{
			{float64(1), "live+a", "rtmp://a/app", "rtmp://a/app"},
			{float64(2), "live+other", "rtmp://o/app", "rtmp://o/app"},
		})
		withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: url})
		RecordAdmittedIngestGeneration("live+a", "gen-a", 201)

		handleDeactivatePushTargets(logging.NewLogger(), &ipcpb.DeactivatePushTargets{StreamName: "live+a", SourceGeneration: "gen-a"})

		if n := calls("push_stop"); n != 1 {
			t.Fatalf("expected exactly one push_stop (only the matching stream), got %d", n)
		}
	})
}

func TestPushTargetCommands_UnknownGenerationFailsClosed(t *testing.T) {
	const runtimeName = "live+push-unknown-generation"
	url, calls := pushListMistServer(t, [][]any{{float64(1), runtimeName, "rtmp://a/app", "rtmp://a/app"}})
	withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: url})

	var activation *ipcpb.ActivatePushTargetsResult
	handleActivatePushTargets(logging.NewLogger(), &ipcpb.ActivatePushTargets{
		StreamName:       runtimeName,
		SourceGeneration: "unproven-generation",
		Targets:          []*ipcpb.PushTargetSpec{{TargetId: "t1", TargetUri: "rtmp://new/app"}},
	}, func(m *ipcpb.ControlMessage) {
		activation = m.GetActivatePushTargetsResult()
	})
	if activation == nil || activation.GetConverged() || activation.GetError() == "" {
		t.Fatalf("activation without a known local generation was not rejected: %+v", activation)
	}
	handleDeactivatePushTargets(logging.NewLogger(), &ipcpb.DeactivatePushTargets{
		StreamName:       runtimeName,
		SourceGeneration: "unproven-generation",
	})
	if calls("push_start") != 0 || calls("push_stop") != 0 {
		t.Fatal("unknown-generation push command reached Mist")
	}
}

func TestActivatePushTargets_RejectsEndedGeneration(t *testing.T) {
	const runtimeName = "live+ended-generation"
	if err := RecordAdmittedIngestGeneration(runtimeName, "generation-ended", 211); err != nil {
		t.Fatalf("RecordAdmittedIngestGeneration: %v", err)
	}
	if err := MarkAdmittedIngestGenerationEnded(runtimeName, 211); err != nil {
		t.Fatalf("MarkAdmittedIngestGenerationEnded: %v", err)
	}
	url, calls := pushListMistServer(t, nil)
	withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: url})
	var result *ipcpb.ActivatePushTargetsResult
	handleActivatePushTargets(logging.NewLogger(), &ipcpb.ActivatePushTargets{
		StreamName:       runtimeName,
		SourceGeneration: "generation-ended",
		Targets:          []*ipcpb.PushTargetSpec{{TargetId: "target", TargetUri: "rtmp://new/app"}},
	}, func(message *ipcpb.ControlMessage) {
		result = message.GetActivatePushTargetsResult()
	})
	if result == nil || result.GetConverged() || result.GetError() == "" {
		t.Fatalf("activation for ended generation was not rejected: %+v", result)
	}
	if calls("push_start") != 0 {
		t.Fatal("activation for ended generation reached Mist")
	}
}

func TestPushTargetCommands_RejectSupersededGeneration(t *testing.T) {
	const runtimeName = "live+push-generation-fence"
	RecordAdmittedIngestGeneration(runtimeName, "current-generation", 202)
	url, calls := pushListMistServer(t, [][]any{{float64(1), runtimeName, "rtmp://a/app", "rtmp://a/app"}})
	withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: url})

	var activation *ipcpb.ActivatePushTargetsResult
	handleActivatePushTargets(logging.NewLogger(), &ipcpb.ActivatePushTargets{
		StreamName:       runtimeName,
		SourceGeneration: "retired-generation",
		Targets:          []*ipcpb.PushTargetSpec{{TargetId: "t1", TargetUri: "rtmp://new/app"}},
	}, func(m *ipcpb.ControlMessage) {
		activation = m.GetActivatePushTargetsResult()
	})
	if activation == nil || activation.GetConverged() || activation.GetError() == "" {
		t.Fatalf("superseded activation was not rejected: %+v", activation)
	}
	if calls("push_start") != 0 {
		t.Fatal("superseded activation reached Mist")
	}

	handleDeactivatePushTargets(logging.NewLogger(), &ipcpb.DeactivatePushTargets{
		StreamName:       runtimeName,
		SourceGeneration: "retired-generation",
	})
	if calls("push_stop") != 0 {
		t.Fatal("superseded deactivation reached Mist")
	}
}
