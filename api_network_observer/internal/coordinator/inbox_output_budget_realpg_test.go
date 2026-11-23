//go:build schema_verify

package coordinator

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"frameworks/api_network_observer/internal/store"
	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
	"github.com/google/uuid"
)

func TestObserverInboxOutputExpansionMakesProgress_RealPG(t *testing.T) {
	db := startObserverPostgres(t)
	now := time.Now().UTC()
	a := &API{Store: &store.Store{DB: db, TenantID: uuid.NewString()}, Engine: Engine{Network: "42161", Environment: "test"}}
	r := obs.Resource{Provider: "livepeer", Network: "42161", Environment: "test", ID: "0xabc", URI: "https://192.0.2.1:8935", Active: true, CatalogSeen: now}
	endpoint, err := obs.NewEndpoint(r, "192.0.2.1", now)
	if err != nil {
		t.Fatal(err)
	}
	endpoint.NextCheck = now.Add(time.Minute)
	assignments := map[string]obs.Assignment{}
	for _, site := range []string{"site-a", "site-b", "site-c"} {
		assignments[site] = obs.Assignment{ID: uuid.NewString(), ObserverID: site, Endpoint: endpoint, DueAt: now.Add(-time.Second), Deadline: now.Add(45 * time.Second)}
	}
	if err := a.Store.Update(t.Context(), func(s *store.State, _ *sql.Tx) error {
		s.Resources[r.Key()] = r
		s.Endpoints[endpoint.ID] = endpoint
		for site, assignment := range assignments {
			s.Assignments[assignment.ID] = assignment
			s.Observers[site] = obs.Observer{ID: site, Kind: "prober", LastSeen: now}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Each admissible batch expands into 2,000 outbox events. A group must be
	// paged before application rather than exceed even an empty outbox forever.
	for i := range 51 {
		site := []string{"site-a", "site-b", "site-c"}[i/17]
		assignment := assignments[site]
		b := obs.Batch{ID: uuid.NewString(), ObserverID: site}
		for range 1000 {
			b.Results = append(b.Results, obs.ProbeResult{ID: uuid.NewString(), AssignmentID: assignment.ID, EndpointID: endpoint.ID, Check: obs.Check{ObserverID: site, ObservedAt: now, Success: true, Qualifying: true, ActualIP: endpoint.IP}})
		}
		raw, _ := json.Marshal(b)
		digest := sha256.Sum256(raw)
		if err := a.Store.Receive(t.Context(), b.ID, hex.EncodeToString(digest[:]), b.ObserverID, "prober", raw); err != nil {
			t.Fatal(err)
		}
	}
	n, err := a.ProcessBatch(t.Context())
	if err != nil {
		t.Fatalf("valid output expansion permanently blocked an empty outbox: %v", err)
	}
	if n < 1 || n >= 51 {
		t.Fatalf("worker did not page expanded output: processed=%d", n)
	}
	var applied, events int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM network_observer.observation_inbox WHERE tenant_id=$1 AND status='applied'", a.Store.TenantID).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM network_observer.telemetry_outbox WHERE tenant_id=$1", a.Store.TenantID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if applied != n || events != n*2000 {
		t.Fatalf("expanded batch was partly acknowledged: applied=%d processed=%d events=%d", applied, n, events)
	}
	if _, err := a.ProcessBatch(t.Context()); err != nil {
		t.Fatal(err)
	}
	var thirdSite int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM network_observer.observation_inbox WHERE tenant_id=$1 AND observer_id='site-c' AND status='applied'", a.Store.TenantID).Scan(&thirdSite); err != nil {
		t.Fatal(err)
	}
	if thirdSite == 0 {
		t.Fatal("paging expanded output starved the third observer behind older backlogs")
	}
}
