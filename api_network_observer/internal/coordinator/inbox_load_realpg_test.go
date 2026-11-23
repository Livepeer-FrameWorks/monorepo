//go:build schema_verify

package coordinator

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_network_observer/internal/store"
	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
	"github.com/google/uuid"
)

func TestObserverInboxBacklogLoad_RealPG(t *testing.T) {
	requested := os.Getenv("FRAMEWORKS_NETWORK_LOAD_RECORDS")
	if requested == "" {
		t.Skip("run make verify-network-observer-load")
	}
	count, err := strconv.Atoi(requested)
	if err != nil || count < 700 {
		t.Fatal("load fixture requires at least 700 records")
	}
	db := startObserverPostgres(t)
	a := &API{Store: &store.Store{DB: db, TenantID: uuid.NewString()}, Secret: "load-secret", ClusterID: "test", Engine: Engine{Network: "42161", Environment: "load"}}
	now := time.Now().UTC()
	resources := make([]obs.Resource, 135)
	if err := a.Store.Update(t.Context(), func(s *store.State, _ *sql.Tx) error {
		for i := range resources {
			r := obs.Resource{Provider: "livepeer", Network: "42161", Environment: "load", ID: fmt.Sprintf("orch-%d", i), URI: fmt.Sprintf("https://orch-%d.invalid:8935", i), Active: true, CatalogSeen: now}
			resources[i] = r
			s.Resources[r.Key()] = r
			p, _ := obs.NewEndpoint(r, fmt.Sprintf("192.0.2.%d", i+1), now)
			s.Endpoints[p.ID] = p
		}
		for i := 0; i < 7; i++ {
			id := fmt.Sprintf("site-%d", i)
			s.Observers[id] = obs.Observer{ID: id, Kind: "prober", LastSeen: now}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Minute)
	defer cancel()
	var wg sync.WaitGroup
	var mu sync.Mutex
	var latencies, workLatencies []time.Duration
	failures := make(chan error, 7)
	started := time.Now()
	for site := 0; site < 7; site++ {
		wg.Add(1)
		go func(site int) {
			defer wg.Done()
			var records [][]byte
			flush := func() error {
				if len(records) == 0 {
					return nil
				}
				// Model different regional RTTs without changing backend timeouts.
				rtt := []time.Duration{20, 180, 100, 160, 200, 220, 260}[site] * time.Millisecond
				timer := time.NewTimer(rtt)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
				envelope, _ := json.Marshal(map[string]any{"records": records})
				r := httptest.NewRequestWithContext(ctx, "POST", "/v1/observation-batches", bytes.NewReader(envelope))
				r.Header.Set("Authorization", "Bearer "+a.Secret)
				w := httptest.NewRecorder()
				before := time.Now()
				a.Handler().ServeHTTP(w, r)
				if w.Code != 200 {
					return fmt.Errorf("bulk receipt failed: %d %s", w.Code, w.Body)
				}
				mu.Lock()
				latencies = append(latencies, time.Since(before))
				mu.Unlock()
				records = nil
				return nil
			}
			for i := site; i < count; i += 7 {
				resource := resources[i%len(resources)]
				at := now.Add(-12*time.Hour + time.Duration(i)*time.Millisecond)
				batch := obs.Batch{ID: uuid.NewString(), ObserverID: fmt.Sprintf("site-%d", site), DNS: []obs.DNSResult{{Resource: resource, ObservedAt: at, IPs: []string{fmt.Sprintf("192.0.2.%d", i%135+1)}}}}
				if i%701 == 0 {
					batch.DNS = nil
					batch.Report = &obs.ObserverReport{Kind: "heartbeat", ObservedAt: now.Add(time.Hour)}
				}
				raw, _ := json.Marshal(batch)
				records = append(records, raw)
				if len(records) == 64 {
					if err := flush(); err != nil {
						failures <- err
						return
					}
				}
			}
			if err := flush(); err != nil {
				failures <- err
			}

		}(site)
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p95 := latencies[len(latencies)*95/100]
	if p95 >= time.Second {
		t.Fatalf("receipt p95=%v while collecting the backlog", p95)
	}
	acceptedAt := time.Now()
	var liveCount atomic.Int64
	liveStop, liveDone := make(chan struct{}), make(chan struct{})
	liveErrors := make(chan error, 1)
	go func() {
		defer close(liveDone)
		ticker := time.NewTicker(125 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-liveStop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				sequence := liveCount.Load()
				before := time.Now()
				if sequence%2 == 0 {
					raw, _ := json.Marshal(obs.Batch{ID: uuid.NewString(), ObserverID: "site-0", Report: &obs.ObserverReport{Kind: "heartbeat", ObservedAt: time.Now().UTC(), Healthy: true}})
					r := httptest.NewRequestWithContext(ctx, "POST", "/v1/observations", bytes.NewReader(raw))
					r.Header.Set("Authorization", "Bearer "+a.Secret)
					w := httptest.NewRecorder()
					a.Handler().ServeHTTP(w, r)
					if w.Code != 200 {
						liveErrors <- fmt.Errorf("live receipt failed: %d", w.Code)
						return
					}
				} else {
					event := kafka.AnalyticsEvent{EventID: uuid.NewString(), EventType: "orchestrator_observer_report", Timestamp: time.Now().UTC(), Data: map[string]any{"producer_kind": "gateway", "cluster_owner_tenant_id": a.Store.TenantID, "network_id": "42161", "environment": "load", "gateway_id": "gateway", "gateway_region": "eu", "healthy": true, "report_kind": "heartbeat"}}
					raw, _ := json.Marshal(event)
					if err := a.Gateway(ctx, kafka.Message{Value: raw}); err != nil {
						liveErrors <- err
						return
					}
				}
				mu.Lock()
				latencies = append(latencies, time.Since(before))
				mu.Unlock()
				liveCount.Add(1)
				poll, _ := json.Marshal(obs.WorkRequest{ObserverID: "site-0", Region: "load-region"})
				r := httptest.NewRequestWithContext(ctx, "POST", "/v1/work", bytes.NewReader(poll))
				r.Header.Set("Authorization", "Bearer "+a.Secret)
				w := httptest.NewRecorder()
				before = time.Now()
				a.Handler().ServeHTTP(w, r)
				if w.Code != 200 {
					liveErrors <- fmt.Errorf("work polling failed during backlog: %d %s", w.Code, w.Body)
					return
				}
				mu.Lock()
				workLatencies = append(workLatencies, time.Since(before))
				mu.Unlock()
			}
		}
	}()
	for ctx.Err() == nil {
		n, err := a.ProcessBatch(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			if time.Since(acceptedAt) >= 2*time.Second {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	close(liveStop)
	<-liveDone
	select {
	case err := <-liveErrors:
		t.Fatal(err)
	default:
	}
	for ctx.Err() == nil {
		n, err := a.ProcessBatch(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
	if err := ctx.Err(); err != nil {
		t.Fatal(err)
	}
	var total, pending, terminal, rejected int
	if err := db.QueryRowContext(ctx, `SELECT count(*),count(*) FILTER(WHERE status='pending'),count(*) FILTER(WHERE status IN ('applied','rejected')),count(*) FILTER(WHERE status='rejected') FROM network_observer.observation_inbox WHERE tenant_id=$1`, a.Store.TenantID).Scan(&total, &pending, &terminal, &rejected); err != nil {
		t.Fatal(err)
	}
	expected := count + int(liveCount.Load())
	if total != expected || pending != 0 || terminal != expected {
		t.Fatalf("unexplained receipt loss: total=%d pending=%d terminal=%d want=%d", total, pending, terminal, expected)
	}
	if rejected != (count+700)/701 {
		t.Fatalf("valid data rejected during backlog: got=%d want_poison=%d", rejected, (count+700)/701)
	}
	var quota int64
	if err := db.QueryRowContext(ctx, "SELECT sum(pending_records+pending_bytes) FROM network_observer.inbox_limits WHERE tenant_id=$1", a.Store.TenantID).Scan(&quota); err != nil || quota != 0 {
		t.Fatalf("terminal backlog retained quota: %d (%v)", quota, err)
	}
	if len(workLatencies) == 0 {
		t.Fatal("no control polling happened during backlog")
	}
	sort.Slice(workLatencies, func(i, j int) bool { return workLatencies[i] < workLatencies[j] })
	workP95 := workLatencies[len(workLatencies)*95/100]
	if workP95 >= time.Second {
		t.Fatalf("work p95=%v during backlog", workP95)
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p95 = latencies[len(latencies)*95/100]
	if p95 >= time.Second {
		t.Fatalf("live receipt p95=%v while applying backlog", p95)
	}
	rate := float64(expected) / time.Since(acceptedAt).Seconds()
	if rate < 16 {
		t.Fatalf("application throughput %.2f records/s below twice the 8/s fixture input", rate)
	}
	t.Logf("records=%d receipt_p95=%s work_p95=%s rejected=%d receive_elapsed=%s apply_rate=%.2f/s total_elapsed=%s", count, p95, workP95, rejected, acceptedAt.Sub(started), rate, time.Since(started))
}
