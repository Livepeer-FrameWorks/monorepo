package prober

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/telemetryspool"
)

func TestResponseTransitDelayIsNotClockSkew(t *testing.T) {
	q, err := telemetryspool.Open(telemetryspool.Config{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			stamp := time.Now().UTC()
			time.Sleep(1100 * time.Millisecond)
			_ = json.NewEncoder(w).Encode(map[string]any{"server_time": stamp})
			return
		}
		var batch obs.Batch
		if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
			t.Error(err)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"accepted": true, "event_id": batch.ID})
	}))
	defer server.Close()
	p := &Prober{URL: server.URL, ID: "site", Secret: "secret", Client: server.Client(), Queue: q, Concurrency: 1, Timeout: time.Second, Probe: resultProbe(func(context.Context, obs.Assignment) obs.ProbeResult { return obs.ProbeResult{} })}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx, nil) }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		last, _, _, readErr := q.DeliveryState()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !last.IsZero() {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	response := httptest.NewRecorder()
	p.Health().ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), "GET", "/ready", nil))
	cancel()
	<-done
	if response.Code != 200 {
		t.Fatalf("normal response latency falsely made the clock untrusted: ready=%d skew=%g", response.Code, p.deliveryHealth().ClockSkewSeconds)
	}
}

func TestMinimumClockOffset(t *testing.T) {
	base := time.Now()
	for _, test := range []struct {
		name         string
		server, want time.Duration
	}{
		{"within request", 100 * time.Millisecond, 0},
		{"definitely ahead", -10 * time.Second, 10 * time.Second},
		{"definitely behind", 10 * time.Second, -9 * time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := minimumClockOffset(base, base.Add(time.Second), base.Add(test.server)); got != test.want {
				t.Fatalf("offset=%v want=%v", got, test.want)
			}
		})
	}
}
