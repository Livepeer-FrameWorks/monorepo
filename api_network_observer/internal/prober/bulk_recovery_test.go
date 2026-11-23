package prober

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/telemetryspool"
	"github.com/google/uuid"
)

func TestBacklogUploadAmortizesRegionalRoundTrips(t *testing.T) {
	q, err := telemetryspool.Open(telemetryspool.Config{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	wanted := map[string]bool{}
	for i := 0; i < 256; i++ {
		id := uuid.NewString()
		wanted[id] = true
		raw, _ := json.Marshal(obs.Batch{ID: id, ObserverID: "site", Report: &obs.ObserverReport{Kind: "heartbeat", ObservedAt: time.Now().UTC(), Healthy: true}})
		if err := q.Enqueue(id, raw); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	seen := map[string]bool{}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/work" {
			_ = json.NewEncoder(w).Encode(map[string]any{"control_version": 2, "ingest_batch_records": 64, "ingest_batch_bytes": 380 << 10, "server_time": time.Now().UTC()})
			return
		}
		mu.Lock()
		defer mu.Unlock()
		requests++
		var records [][]byte
		if r.URL.Path == "/v1/observation-batches" {
			var body struct {
				Records [][]byte `json:"records"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			records = body.Records
		} else {
			var b obs.Batch
			if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
				t.Error(err)
			}
			raw, _ := json.Marshal(b)
			records = [][]byte{raw}
		}
		results := []map[string]any{}
		for _, raw := range records {
			var b obs.Batch
			if err := json.Unmarshal(raw, &b); err != nil {
				t.Error(err)
			}
			if wanted[b.ID] {
				seen[b.ID] = true
			}
			results = append(results, map[string]any{"id": b.ID, "accepted": true})
		}
		if r.URL.Path == "/v1/observation-batches" {
			_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
		} else {
			_ = json.NewEncoder(w).Encode(map[string]any{"accepted": true, "event_id": results[0]["id"]})
		}
	}))
	defer server.Close()
	p := &Prober{URL: server.URL, ID: "site", Secret: "secret", Client: server.Client(), Queue: q, Concurrency: 1, Timeout: time.Second, Probe: resultProbe(func(context.Context, obs.Assignment) obs.ProbeResult { return obs.ProbeResult{} })}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx, nil) }()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		complete := len(seen) == 256
		mu.Unlock()
		n, _, _ := q.Stats()
		if complete && n == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 256 {
		t.Fatalf("backlog lost records: %d/256", len(seen))
	}
	if requests > 8 {
		t.Fatalf("backlog still needs one regional round trip per record: %d requests for 256 measurements", requests)
	}
}
