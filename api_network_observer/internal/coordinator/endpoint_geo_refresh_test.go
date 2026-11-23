package coordinator

import (
	"testing"
	"time"

	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
)

func TestExistingEndpointRefreshesGeoIP(t *testing.T) {
	for _, name := range []string{"unknown", "stale"} {
		t.Run(name, func(t *testing.T) {
			s, engine, now, endpoint := fixture()
			if name == "stale" {
				endpoint.Location = obs.Location{Known: true, Latitude: 37.751, Longitude: -97.822, Country: "US", Source: "mmdb"}
			}
			endpoint.LastSuccess = now.Add(-time.Minute)
			endpoint.Status = "up"
			endpoint.Checks = []obs.Check{{ObserverID: "eu", ObservedAt: endpoint.LastSuccess, Success: true}}
			s.Endpoints[endpoint.ID] = endpoint
			location := obs.Location{Known: true, Latitude: 19.0748, Longitude: 72.8856, Country: "IN", Source: "mmdb"}
			engine.Locate = func(ip string) obs.Location {
				if ip != endpoint.IP {
					t.Fatalf("lookup used %q, want endpoint address %q", ip, endpoint.IP)
				}
				return location
			}
			engine.endpoint(s, endpoint.Resource, endpoint.IP, now.Add(time.Minute))
			got := s.Endpoints[endpoint.ID]
			if got.Location != location {
				t.Fatalf("existing endpoint kept cached geography: got=%+v want=%+v", got.Location, location)
			}
			if got.ID != endpoint.ID || got.FirstSeen != endpoint.FirstSeen || got.LastSuccess != endpoint.LastSuccess || got.Status != "up" || len(got.Checks) != 1 {
				t.Fatalf("geography refresh replaced endpoint history: %+v", got)
			}
			engine.Locate = func(string) obs.Location { return obs.Location{Source: "unknown"} }
			engine.endpoint(s, endpoint.Resource, endpoint.IP, now.Add(2*time.Minute))
			if got := s.Endpoints[endpoint.ID].Location; got != location {
				t.Fatalf("temporary lookup miss erased known geography: %+v", got)
			}
			engine.Locate = func(string) obs.Location {
				return obs.Location{Known: true, Latitude: 37.751, Longitude: -97.822, Source: "mmdb"}
			}
			engine.endpoint(s, endpoint.Resource, endpoint.IP, now)
			if got := s.Endpoints[endpoint.ID].Location; got != location {
				t.Fatalf("historical replay changed current geography: %+v", got)
			}
		})
	}
}
