package coordinator

import (
	"frameworks/api_network_observer/internal/store"
	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
	"github.com/google/uuid"
	"testing"
	"time"
)

func fixture() (*store.State, Engine, time.Time, obs.Endpoint) {
	now := time.Now().UTC()
	s := store.NewState()
	r := obs.Resource{Provider: "livepeer", Network: "42161", Environment: "production", ID: "0xabc", URI: "https://orch.example:8935", Active: true}
	s.Resources[r.Key()] = r
	p, _ := obs.NewEndpoint(r, "192.0.2.1", now)
	s.Endpoints[p.ID] = p
	s.Observers["eu"] = obs.Observer{ID: "eu", LastSeen: now}
	return s, Engine{}, now, p
}
func TestFullDNSUnionAndBackoffDoesNotRetireActive(t *testing.T) {
	s, e, now, p := fixture()
	b := obs.Batch{ID: uuid.NewString(), ObserverID: "eu", DNS: []obs.DNSResult{{Resource: p.Resource, IPs: []string{"192.0.2.2", "192.0.2.3"}, ObservedAt: now}}}
	if err := e.Apply(s, b, now); err != nil {
		t.Fatal(err)
	}
	if len(s.Endpoints) != 3 {
		t.Fatalf("DNS union lost instances: %d", len(s.Endpoints))
	}
	for id, p := range s.Endpoints {
		p.FirstSeen = now.Add(-40 * 24 * time.Hour)
		p.DNSSeen = now
		s.Endpoints[id] = p
	}
	e.Schedule(s, now)
	if len(s.Endpoints) != 3 {
		t.Fatal("referenced endpoint retired")
	}
	if len(s.Assignments) != 3 {
		t.Fatalf("expected coverage for all endpoints, got %d", len(s.Assignments))
	}
}
func TestMissingObserverIsUnknownAndDoesNotIncreaseFailureBackoff(t *testing.T) {
	s, e, now, p := fixture()
	e.Schedule(s, now)
	e.Schedule(s, now.Add(time.Minute))
	if got := s.Endpoints[p.ID]; got.Status != "unknown" || got.FailureStreak != 0 {
		t.Fatalf("missing report became failure: %+v", got)
	}
}
func TestPreviousCycleFailureCannotBackOffAMissingReport(t *testing.T) {
	s, e, now, p := fixture()
	p.Checks = []obs.Check{{ObserverID: "eu", ObservedAt: now.Add(-time.Second), Qualifying: true}}
	p.AssignedObservers = []string{"eu"}
	s.Endpoints[p.ID] = p
	e.Schedule(s, now)
	e.Schedule(s, now.Add(46*time.Second))
	if got := s.Endpoints[p.ID]; got.FailureStreak != 0 {
		t.Fatalf("previous failure counted as this cycle's report: streak=%d", got.FailureStreak)
	}
}
func TestAttributableSuccessAndCompleteFailure(t *testing.T) {
	s, e, now, p := fixture()
	e.Schedule(s, now)
	var a obs.Assignment
	for _, v := range s.Assignments {
		a = v
	}
	b := obs.Batch{ID: uuid.NewString(), ObserverID: "eu", Results: []obs.ProbeResult{{ID: uuid.NewString(), AssignmentID: a.ID, EndpointID: p.ID, Check: obs.Check{ObserverID: "eu", ObservedAt: now.Add(time.Second), Success: true, Qualifying: true, ActualIP: p.IP}}}}
	if err := e.Apply(s, b, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	e.Schedule(s, now.Add(time.Second))
	if s.Endpoints[p.ID].Status != "up" {
		t.Fatal("successful discovery missing")
	}
	b.Results[0].Check.ActualIP = "192.0.2.99"
	if e.Apply(s, b, now) == nil {
		t.Fatal("accepted unrelated socket success")
	}
}
func TestChecksFromDifferentRegionsSurviveReverseDelivery(t *testing.T) {
	s, e, now, p := fixture()
	s.Observers["us"] = obs.Observer{ID: "us", LastSeen: now}
	for i, observer := range []string{"eu", "us"} {
		a := obs.Assignment{ID: uuid.NewString(), Endpoint: p, ObserverID: observer, DueAt: now, Deadline: now.Add(time.Minute)}
		s.Assignments[a.ID] = a
		b := obs.Batch{ID: uuid.NewString(), ObserverID: observer, Results: []obs.ProbeResult{{ID: uuid.NewString(), AssignmentID: a.ID, EndpointID: p.ID, Check: obs.Check{ObserverID: observer, ObservedAt: now.Add(time.Duration(2-i) * time.Second), Qualifying: true, Success: true, ActualIP: p.IP}}}}
		if err := e.Apply(s, b, now.Add(3*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.Endpoints[p.ID].Checks) != 2 {
		t.Fatal("older observation from a different region was discarded")
	}
}

func TestAbandonedCatalogRecordExpiresWithEndpoint(t *testing.T) {
	s, e, now, p := fixture()
	r := p.Resource
	r.Active = false
	r.CatalogSeen = now.Add(-40 * 24 * time.Hour)
	s.Resources[r.Key()] = r
	p.Resource = r
	p.FirstSeen = r.CatalogSeen
	p.DNSSeen = r.CatalogSeen
	s.Endpoints[p.ID] = p
	e.Schedule(s, now)
	if len(s.Endpoints) != 0 || len(s.Resources) != 0 {
		t.Fatal("abandoned catalog metadata must not be retained indefinitely")
	}
}
