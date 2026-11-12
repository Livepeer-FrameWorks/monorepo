package worker

import (
	"frameworks/api_dns/internal/provider/bunny"
	"frameworks/api_dns/internal/store"
	"testing"
)

func TestTenantAliasRecordsUseRootGeographicRoutingPolicy(t *testing.T) {
	latEU, lonEU, latUS, lonUS := 52.0, 6.0, 39.0, -77.0
	st := newFakeTenantAliasStore()
	st.alias = &store.TenantAlias{TenantID: "tenant-1", Subdomain: "acme", Status: "cert_issued"}
	st.rows = []store.TenantEdgeApplyState{tenantEdge("tenant-1", "media-eu-1", "eu", "in_dns"), tenantEdge("tenant-1", "media-us-1", "us", "in_dns")}
	addrs := []ServiceAddress{{NodeID: "eu", IP: "192.0.2.10", Latitude: &latEU, Longitude: &lonEU}, {NodeID: "us", IP: "192.0.2.20", Latitude: &latUS, Longitude: &lonUS}}
	resolver := &fakeTenantEdgeResolver{serviceAddrs: map[string][]ServiceAddress{"edge": addrs, "edge-ingest": addrs, "edge-egress": addrs, "foghorn": addrs}}
	dns := &fakeTenantAliasDNS{zoneFound: true}
	w := newTestAliasWorker(st, dns, resolver)
	if callErr := w.PublishTenantAlias(t.Context(), "tenant-1"); callErr != nil {
		t.Fatal(callErr)
	}
	for _, name := range []string{"acme", "edge-ingest.acme", "edge-egress.acme", "foghorn.acme"} {
		records := dns.records[name]
		if len(records) != 2 {
			t.Fatalf("%s lost an authorized region: %+v", name, records)
		}
		for _, record := range records {
			if record.SmartRoutingType != bunny.SmartRoutingGeolocation || record.GeolocationLatitude == nil || record.GeolocationLongitude == nil {
				t.Errorf("%s lost GeoDNS metadata: %+v", name, record)
				continue
			}
			wantLat, wantLon := latEU, lonEU
			if record.Value == "192.0.2.20" {
				wantLat, wantLon = latUS, lonUS
			}
			if *record.GeolocationLatitude != wantLat || *record.GeolocationLongitude != wantLon {
				t.Errorf("%s changed location for %s", name, record.Value)
			}
		}
	}
}
