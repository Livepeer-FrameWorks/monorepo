package triggers

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
)

func TestLivepeerGatewaySelectionKeepsJobInAuthoritativeCell(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cells     map[string]string
		local     []string
		wantHosts []string
	}{
		{
			name:  "private US source with EU official cluster",
			cells: map[string]string{"official.example.com": "media-eu", "us.example.com": "media-us"},
			local: []string{"us.example.com"}, wantHosts: []string{"https://us.example.com"},
		},
		{
			name:      "mixed assignments keep only gateway in job cell",
			cells:     map[string]string{"official.example.com": "media-eu", "us.example.com": "media-us"},
			local:     []string{"official.example.com", "us.example.com"},
			wantHosts: []string{"https://us.example.com"},
		},
		{
			name:  "foreign gateway is not a valid failover",
			cells: map[string]string{"official.example.com": "media-eu", "us.example.com": "media-eu"},
			local: []string{"official.example.com"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hosts := map[string][]string{
				"private-mac": nil, "official-eu": {"official.example.com"}, "media-us": tc.local,
			}
			disc := newFakeGatewayDiscoverer(hosts)
			disc.cells = tc.cells
			p := newGatewayProcessor(t, disc, "media-us")
			got := p.ApplyLivepeerBroadcasters(gatewayProfileTemplate)
			if disc.callCount("private-mac") != 0 || disc.callCount("official-eu") != 0 {
				t.Fatal("gateway discovery overrode the already admitted cell using tenant cluster metadata")
			}
			if len(tc.wantHosts) == 0 {
				if mist.HasLivepeerProcesses(got) || !strings.Contains(got, `"process":"AV"`) {
					t.Fatalf("foreign cell cannot authorize this job; want local AV fallback, got %s", got)
				}
				return
			}
			if hosts := broadcasterAddresses(t, got); !reflect.DeepEqual(hosts, tc.wantHosts) {
				t.Fatalf("job owned by media-us was assigned unauthorizable gateways: got %v want %v", hosts, tc.wantHosts)
			}
			if cells := mist.LivepeerGatewayClusters(got); !reflect.DeepEqual(cells, []string{"media-us"}) {
				t.Fatalf("job audience = %v, want only owning cell media-us", cells)
			}
			// Cached discovery must keep the same ownership boundary.
			again := p.ApplyLivepeerBroadcasters(gatewayProfileTemplate)
			if hosts := broadcasterAddresses(t, again); !reflect.DeepEqual(hosts, tc.wantHosts) {
				t.Fatalf("cached gateway assignment crossed job owner: %v", hosts)
			}
		})
	}
}
