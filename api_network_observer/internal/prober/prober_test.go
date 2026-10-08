package prober

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/telemetryspool"
)

type blockingProbe struct{ entered chan struct{} }

func (p blockingProbe) Check(ctx context.Context, a obs.Assignment) obs.ProbeResult {
	close(p.entered)
	<-ctx.Done()
	return obs.ProbeResult{AssignmentID: a.ID, EndpointID: a.Endpoint.ID, ID: "check"}
}

func TestDNSReportingContinuesWhileRPCIsBlocked(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dns := make(chan obs.DNSResult, 4)
	probe := blockingProbe{entered: make(chan struct{})}
	resources := []obs.Resource{{ID: "first", URI: "https://192.0.2.1"}, {ID: "second", URI: "https://192.0.2.2"}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("missing ingest authentication")
		}
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(obs.Work{Catalog: resources, Assignments: []obs.Assignment{{ID: "assignment", Deadline: time.Now().Add(time.Minute)}}, DNSIntervalSeconds: 300})
			return
		}
		var batch obs.Batch
		if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
			t.Error(err)
			return
		}
		if batch.ID == "" || batch.ObserverID != "prober" {
			t.Error("batch lost stable identity")
		}
		for _, result := range batch.DNS {
			dns <- result
		}
	}))
	defer server.Close()
	queue, err := telemetryspool.Open(telemetryspool.Config{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	p := Prober{URL: server.URL, Secret: "secret", ID: "prober", Region: "eu", Client: server.Client(), Queue: queue, Probe: probe, Concurrency: 1, Timeout: time.Second}
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx, func(error) {}) }()
	select {
	case <-probe.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("RPC did not start")
	}
	seen := map[string]bool{}
	for len(seen) < len(resources) {
		select {
		case result := <-dns:
			seen[result.Resource.ID] = true
		case <-time.After(2 * time.Second):
			t.Fatal("DNS scan/reporting waited for the blocked RPC")
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("prober did not shut down")
	}
}

func TestReadinessRequiresRecentCoordinatorWork(t *testing.T) {
	queue, err := telemetryspool.Open(telemetryspool.Config{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	p := Prober{Queue: queue}
	for _, tc := range []struct {
		seen   int64
		status int
	}{{0, 503}, {time.Now().Add(-3 * time.Minute).Unix(), 503}, {time.Now().Unix(), 200}} {
		p.LastWork.Store(tc.seen)
		response := httptest.NewRecorder()
		p.Health().ServeHTTP(response, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/ready", nil))
		if response.Code != tc.status {
			t.Fatalf("readiness=%d want %d", response.Code, tc.status)
		}
	}
}
