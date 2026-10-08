package networkobservation

import (
	"testing"
	"time"
)

func TestMonitoringReferenceSurvivesHistoryExpiry(t *testing.T) {
	now := time.Now()
	e := Endpoint{Referenced: true, FirstSeen: now.Add(-90 * 24 * time.Hour)}
	if e.Retirable(now) {
		t.Fatal("active endpoint retired")
	}
	e.Referenced = false
	if !e.Retirable(now) {
		t.Fatal("abandoned endpoint not retired")
	}
	if !e.Hidden(now) {
		t.Fatal("old failed endpoint still default-visible")
	}
}
func TestAvailabilitySuccessIsRegionalAndMissingIsUnknown(t *testing.T) {
	now := time.Now()
	healthy := map[string]bool{"eu": true, "us": true}
	checks := []Check{{ObserverID: "eu", ObservedAt: now, Success: true}, {ObserverID: "us", ObservedAt: now, Qualifying: true}}
	if got := Availability(now, time.Minute, []string{"eu", "us"}, checks, healthy); got != "up" {
		t.Fatal(got)
	}
	checks = checks[1:]
	if got := Availability(now, time.Minute, []string{"eu", "us"}, checks, healthy); got != "unknown" {
		t.Fatal(got)
	}
	if got := Availability(now, time.Minute, []string{"us"}, checks, healthy); got != "unavailable" {
		t.Fatal(got)
	}
	healthy["us"] = false
	if got := Availability(now, time.Minute, []string{"us"}, checks, healthy); got != "unknown" {
		t.Fatal(got)
	}
}
func TestRelativeDistanceAndUnknownLocation(t *testing.T) {
	now := time.Now()
	origin := Location{Known: true}
	obs := []Observer{{ID: "near", LastSeen: now, Location: Location{Known: true, Longitude: 1}}, {ID: "far", LastSeen: now, Location: Location{Known: true, Longitude: 70}}}
	if got := Nearest(origin, obs, now, time.Minute, 2); len(got) != 1 {
		t.Fatal(got)
	}
	obs[1].Location.Longitude = 1.8
	if got := Nearest(origin, obs, now, time.Minute, 2); len(got) != 2 {
		t.Fatal(got)
	}
	obs[0].Location.Longitude = 18
	obs[1].Location.Longitude = 22
	if got := Nearest(origin, obs, now, time.Minute, 2); len(got) != 2 {
		t.Fatal("distant coverage lost", got)
	}
	if got := Nearest(Location{}, obs, now, time.Minute, 2); len(got) != 1 {
		t.Fatal(got)
	}
}
func TestNoFalseCarryForwardOrBackoffBeyondCap(t *testing.T) {
	now := time.Now()
	if got := Availability(now, time.Minute, []string{"a"}, []Check{{ObserverID: "a", ObservedAt: now.Add(-3 * time.Minute), Success: true}}, map[string]bool{"a": true}); got != "unknown" {
		t.Fatal(got)
	}
	for _, n := range []uint32{4, 5, 100, 4294967295} {
		if Backoff(n) != 15*time.Minute {
			t.Fatal(n)
		}
	}
}
