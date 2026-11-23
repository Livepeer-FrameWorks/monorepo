package prober

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/telemetryspool"
	"github.com/google/uuid"
)

func TestWorkDoesNotExecuteWithoutDurabilityOrTrustedClock(t *testing.T) {
	for _, mode := range []string{"full", "clock"} {
		t.Run(mode, func(t *testing.T) {
			queue, err := telemetryspool.Open(telemetryspool.Config{Directory: t.TempDir(), MaxRecords: 1})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "full" {
				raw, _ := json.Marshal(obs.Batch{ID: uuid.NewString(), ObserverID: "site"})
				if err := queue.Enqueue("existing", raw); err != nil {
					t.Fatal(err)
				}
			}
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					w.WriteHeader(503)
					return
				}
				serverTime := time.Now()
				if mode == "clock" {
					serverTime = serverTime.Add(2 * time.Hour)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"server_time": serverTime, "assignments": []map[string]any{{"id": "task", "deadline": time.Now().Add(time.Minute)}}})
			}))
			defer server.Close()
			p := &Prober{URL: server.URL, ID: "site", Secret: "secret", Client: server.Client(), Queue: queue, Concurrency: 1, Timeout: time.Second, Probe: resultProbe(func(_ context.Context, _ obs.Assignment) obs.ProbeResult {
				calls.Add(1)
				return obs.ProbeResult{ID: uuid.NewString()}
			})}
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() { done <- p.Run(ctx, nil) }()
			deadline := time.Now().Add(2 * time.Second)
			for p.LastWork.Load() == 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			cancel()
			<-done
			if p.LastWork.Load() == 0 {
				t.Fatal("test did not receive coordinator work")
			}
			if calls.Load() != 0 {
				t.Fatalf("%s: executed %d checks without safe admission", mode, calls.Load())
			}
			response := httptest.NewRecorder()
			p.Health().ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), "GET", "/ready", nil))
			if response.Code != 503 {
				t.Fatalf("%s: work polling masked degraded collection: %d", mode, response.Code)
			}
		})
	}
}
