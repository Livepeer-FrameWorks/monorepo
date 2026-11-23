//go:build schema_verify

package coordinator

import (
	"frameworks/api_network_observer/internal/store"
	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
	"github.com/google/uuid"
	"net/http/httptest"
	"testing"
)

func TestRegisteredProberRefreshesGeoIP_RealPG(t *testing.T) {
	db := startObserverPostgres(t)
	location := obs.Location{Known: true, Latitude: 37.751, Longitude: -97.822, Country: "US", Source: "mmdb"}
	lookups := 0
	a := &API{Store: &store.Store{DB: db, TenantID: uuid.NewString()}, Secret: "geo-test", MaxObservers: 8, Engine: Engine{Locate: func(ip string) obs.Location {
		if ip != "80.65.208.83" {
			t.Fatalf("lookup used proxy address instead of prober: %q", ip)
		}
		lookups++
		return location
	}}}
	poll := func() {
		t.Helper()
		r := httptest.NewRequestWithContext(t.Context(), "GET", "/v1/work?observer_id=stronk-mumbai&region=ap-south", nil)
		r.RemoteAddr = "80.65.208.83:40000"
		r.Header.Set("Authorization", "Bearer "+a.Secret)
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("work poll failed: %d %s", w.Code, w.Body)
		}
	}
	poll()
	// These are the current production MMDB lookup facts for this actual IP.
	location = obs.Location{Known: true, Latitude: 19.0748, Longitude: 72.8856, Country: "IN", City: "Mumbai", Source: "mmdb"}
	poll()
	state, err := a.Store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	got := state.Observers["stronk-mumbai"]
	if lookups < 2 || got.Location != location {
		t.Fatalf("valid stale coordinates prevented new GeoIP lookup: lookups=%d location=%+v", lookups, got.Location)
	}
	if got.Region != "ap-south" {
		t.Fatalf("GeoIP changed independently configured region: %s", got.Region)
	}
}
