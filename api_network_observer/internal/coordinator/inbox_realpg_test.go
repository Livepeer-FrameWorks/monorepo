//go:build schema_verify

package coordinator

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"frameworks/api_network_observer/internal/store"
	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/google/uuid"
)

func TestObserverReceiptDoesNotWaitForScheduler_RealPG(t *testing.T) {
	db := startObserverPostgres(t)
	tenant := uuid.NewString()
	a := &API{Store: &store.Store{DB: db, TenantID: tenant}, Secret: "receipt-secret"}
	if err := a.Store.Update(t.Context(), func(s *store.State, _ *sql.Tx) error {
		s.Observers["site"] = obs.Observer{ID: "site", Kind: "prober"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	lock, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback() //nolint:errcheck // The transaction deliberately holds the scheduling lock.
	var id string
	if err := lock.QueryRowContext(t.Context(), "SELECT tenant_id FROM network_observer.scheduler_lock WHERE tenant_id=$1 FOR UPDATE", tenant).Scan(&id); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(obs.Batch{ID: uuid.NewString(), ObserverID: "site", Report: &obs.ObserverReport{Kind: "heartbeat", ObservedAt: time.Now().UTC()}})
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	r := httptest.NewRequestWithContext(ctx, "POST", "/v1/observations", bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+a.Secret)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("durable receipt waited for unrelated scheduling: status=%d body=%s", w.Code, w.Body)
	}
}

func TestObserverInboxPoisonIsolation_RealPG(t *testing.T) {
	verifyInboxPoisonIsolation(t, startObserverPostgres(t))
}

func TestObserverInboxBulkPreservesBytesAndPartialConflicts_RealPG(t *testing.T) {
	verifyInboxBulk(t, startObserverPostgres(t))
}
func TestObserverInboxBulkPreservesBytesAndPartialConflicts_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteBaseline(t, "network_observer", "network_observer")
	if !ok {
		t.Fatal("run through the shared Yugabyte fixture")
	}
	verifyInboxBulk(t, db)
}
func verifyInboxBulk(t *testing.T, db *sql.DB) {
	t.Helper()
	a := &API{Store: &store.Store{DB: db, TenantID: uuid.NewString()}, Secret: "bulk-secret"}
	id, other := uuid.NewString(), uuid.NewString()
	original := []byte(" {\"id\":\"" + id + "\", \"observer_id\":\"site\"} \n")
	second, _ := json.Marshal(obs.Batch{ID: other, ObserverID: "site"})
	conflict, _ := json.Marshal(obs.Batch{ID: id, ObserverID: "site", Report: &obs.ObserverReport{Kind: "heartbeat", ObservedAt: time.Now()}})
	post := func(path string, raw []byte) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequestWithContext(t.Context(), "POST", path, bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+a.Secret)
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("receipt failed: %d %s", w.Code, w.Body)
		}
		return w
	}
	post("/v1/observations", original)
	envelope, _ := json.Marshal(map[string]any{"records": [][]byte{original, second, conflict}})
	w := post("/v1/observation-batches", envelope)
	var response struct {
		Results []struct {
			ID       string `json:"id"`
			Accepted bool   `json:"accepted"`
			Code     string `json:"code"`
		} `json:"results"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 3 || !response.Results[0].Accepted || !response.Results[1].Accepted || response.Results[2].Accepted || response.Results[2].Code != "EVENT_ID_CONFLICT" {
		t.Fatalf("bulk lost partial dispositions: %s", w.Body)
	}
	var stored []byte
	if err := db.QueryRowContext(t.Context(), "SELECT payload FROM network_observer.observation_inbox WHERE tenant_id=$1 AND id=$2", a.Store.TenantID, id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, original) {
		t.Fatal("bulk replay rewrote original bytes or replaced their receipt")
	}
	var records, quota int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM network_observer.ingest_receipts WHERE tenant_id=$1", a.Store.TenantID).Scan(&records); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(t.Context(), "SELECT pending_records FROM network_observer.inbox_limits WHERE tenant_id=$1 AND observer_id=''", a.Store.TenantID).Scan(&quota); err != nil {
		t.Fatal(err)
	}
	if records != 2 || quota != 2 {
		t.Fatalf("replays/conflicts consumed duplicate quota: receipts=%d quota=%d", records, quota)
	}
}

func TestObserverWorkRefreshesRegionAndGeography_RealPG(t *testing.T) {
	db := startObserverPostgres(t)
	location := obs.Location{Known: true, Latitude: 37.751, Longitude: -97.822, Source: "mmdb"}
	a := &API{Store: &store.Store{DB: db, TenantID: uuid.NewString()}, Secret: "work-secret", MaxObservers: 8, Engine: Engine{Locate: func(string) obs.Location { return location }}}
	poll := func(region string) {
		t.Helper()
		r := httptest.NewRequestWithContext(t.Context(), "GET", "/v1/work?observer_id=site&region="+region, nil)
		r.Header.Set("Authorization", "Bearer "+a.Secret)
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Body)
		}
	}
	poll("wrong")
	location = obs.Location{Known: true, Latitude: 19.076, Longitude: 72.878, Source: "mmdb"}
	poll("ap-south")
	s, err := a.Store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	got := s.Observers["site"]
	if got.Region != "ap-south" || got.Location != location {
		t.Fatalf("existing site retained wrong identity/location: %+v", got)
	}
	configured := obs.Observer{ID: "site", Location: obs.Location{Known: true, Latitude: 19.08, Longitude: 72.88, Source: "configured"}}
	a.Definitions = map[string]obs.Observer{"site": configured}
	poll("ap-south")
	s, err = a.Store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if s.Observers["site"].Location != configured.Location {
		t.Fatal("configured location did not override GeoIP")
	}
	for _, health := range []*obs.DeliveryHealth{{Sequence: 2, CapacityAvailable: true}, {Sequence: 1, CapacityAvailable: false}} {
		raw, _ := json.Marshal(obs.WorkRequest{ObserverID: "site", Region: "ap-south", Delivery: health})
		r := httptest.NewRequestWithContext(t.Context(), "POST", "/v1/work", bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+a.Secret)
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("control health unavailable: %d %s", w.Code, w.Body)
		}
	}
	s, err = a.Store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Observers["site"].Delivery; got == nil || got.Sequence != 2 || !got.CapacityAvailable {
		t.Fatalf("delayed control reset current health: %+v", got)
	}
	a.Definitions = nil
	poll("ap-south")
	s, err = a.Store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if s.Observers["site"].Location != location {
		t.Fatal("removed override prevented the next GeoIP refresh")
	}
}

func TestObserverInboxPoisonIsolation_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteBaseline(t, "network_observer", "network_observer")
	if !ok {
		t.Fatal("run through the shared Yugabyte fixture")
	}
	verifyInboxPoisonIsolation(t, db)
}

func verifyInboxPoisonIsolation(t *testing.T, db *sql.DB) {
	t.Helper()
	api := &API{Store: &store.Store{DB: db, TenantID: uuid.NewString()}, Secret: "test-secret", ClusterID: "test", Engine: Engine{Network: "42161", Environment: "test"}}
	if err := api.Store.Update(t.Context(), func(s *store.State, _ *sql.Tx) error {
		for _, id := range []string{"bad", "healthy"} {
			s.Observers[id] = obs.Observer{ID: id, Kind: "prober", LastSeen: time.Now().UTC()}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	receive := func(batch obs.Batch) []byte {
		t.Helper()
		raw, err := json.Marshal(batch)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequestWithContext(t.Context(), "POST", "/v1/observations", bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+api.Secret)
		w := httptest.NewRecorder()
		api.Handler().ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("receipt failed: %d %s", w.Code, w.Body)
		}
		return raw
	}
	now := time.Now().UTC()
	for i := 0; i < 101; i++ {
		receive(obs.Batch{ID: uuid.NewString(), ObserverID: "bad", Report: &obs.ObserverReport{Kind: "heartbeat", ObservedAt: now.Add(time.Hour)}})
	}
	healthy := uuid.NewString()
	receive(obs.Batch{ID: healthy, ObserverID: "healthy", Report: &obs.ObserverReport{Kind: "heartbeat", ObservedAt: now, Healthy: true}})
	if err := api.Process(t.Context()); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := db.QueryRowContext(t.Context(), "SELECT status FROM network_observer.observation_inbox WHERE tenant_id=$1 AND id=$2", api.Store.TenantID, healthy).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "applied" {
		t.Fatalf("poison observer starved healthy receipt: %s", status)
	}
	api = &API{Store: api.Store, Secret: api.Secret, ClusterID: api.ClusterID, Engine: api.Engine}
	if err := api.Process(t.Context()); err != nil {
		t.Fatal(err)
	}
	var rejected, retained, pending int
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FILTER(WHERE status='rejected'),count(*) FILTER(WHERE status='rejected' AND payload IS NOT NULL),count(*) FILTER(WHERE status='pending') FROM network_observer.observation_inbox WHERE tenant_id=$1`, api.Store.TenantID).Scan(&rejected, &retained, &pending); err != nil {
		t.Fatal(err)
	}
	if rejected != 101 || retained != 0 || pending != 0 {
		t.Fatalf("terminal dispositions did not release payloads: rejected=%d retained=%d pending=%d", rejected, retained, pending)
	}
	var outbox int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM network_observer.telemetry_outbox WHERE tenant_id=$1", api.Store.TenantID).Scan(&outbox); err != nil {
		t.Fatal(err)
	}
	if outbox != 1 {
		t.Fatalf("invalid clocks became public telemetry or replay duplicated valid event: %d", outbox)
	}
}
