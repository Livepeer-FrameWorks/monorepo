package coordinator

import (
	"frameworks/api_network_observer/internal/store"
	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
	"testing"
	"time"
)

func TestObserverDefinitionsPreserveHealthAndRemoveOverride(t *testing.T) {
	definitions, err := ObserverDefinitions(`[{"id":"mumbai","region":"ap-south","location":{"known":true,"latitude":19.076,"longitude":72.878}}]`)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	health := &obs.DeliveryHealth{Sequence: 9, PendingRecords: 111, DroppedRecords: 42}
	s := store.NewState()
	s.Observers["mumbai"] = obs.Observer{ID: "mumbai", Kind: "prober", LastSeen: now, Delivery: health, SourceIP: "192.0.2.1", Location: obs.Location{Known: true, Latitude: 37.751, Longitude: -97.822, Source: "mmdb"}}
	ReconcileObserverDefinitions(s, definitions, now)
	got := s.Observers["mumbai"]
	if got.Region != "ap-south" || got.Location != definitions["mumbai"].Location || got.Delivery != health || got.LastSeen != now || got.SourceIP != "192.0.2.1" {
		t.Fatalf("configuration reset evidence or retained wrong location: %+v", got)
	}
	ReconcileObserverDefinitions(s, nil, now.Add(time.Second))
	got = s.Observers["mumbai"]
	if got.Location.Valid() || got.Location.Source != "unknown" || got.Delivery != health {
		t.Fatalf("removed override stayed authoritative: %+v", got)
	}
}
func TestObserverDefinitionsRejectInvalidConfiguration(t *testing.T) {
	for _, raw := range []string{`[{"id":"duplicate"},{"id":"duplicate"}]`, `[{"id":"gateway:owned"}]`, `[{"id":"bad","location":{"known":true,"latitude":91}}]`, `[{"id":"bad","kind":"gateway"}]`} {
		if _, err := ObserverDefinitions(raw); err == nil {
			t.Fatalf("accepted invalid configuration %s", raw)
		}
	}
}
