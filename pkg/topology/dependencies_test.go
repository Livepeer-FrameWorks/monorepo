package topology

import (
	"reflect"
	"testing"
)

func TestInfraDependenciesOnlyIncludeDirectInfraClients(t *testing.T) {
	for _, serviceID := range []string{"bridge", "chandler", "deckhand"} {
		if deps := InfraDependencies(serviceID); len(deps) != 0 {
			t.Fatalf("%s infra deps = %#v, want none", serviceID, deps)
		}
	}
}

func TestPeriscopeInfraDependenciesMatchEntrypoints(t *testing.T) {
	tests := map[string][]InfraDependency{
		"periscope-ingest": {
			{Kind: InfraDatabase, Provider: InfraProviderPrimary, Purpose: "distributed ledger-worker leases"},
			{Kind: InfraClickHouse, Provider: InfraProviderPrimary, Purpose: "analytics writes"},
			{Kind: InfraKafka, Provider: InfraProviderAggregator, Purpose: "analytics and service event ingestion"},
		},
		"periscope-query": {
			{Kind: InfraDatabase, Provider: InfraProviderPrimary, Purpose: "delegated-token replay fencing"},
			{Kind: InfraClickHouse, Provider: InfraProviderPrimary, Purpose: "analytics reads"},
		},
		"periscope-metering": {
			{Kind: InfraDatabase, Provider: InfraProviderPrimary, Purpose: "metering leases and billing cursors"},
			{Kind: InfraClickHouse, Provider: InfraProviderPrimary, Purpose: "analytics metering reads"},
			{Kind: InfraKafka, Provider: InfraProviderAggregator, Purpose: "billing usage report publication"},
		},
	}

	for serviceID, want := range tests {
		if got := InfraDependencies(serviceID); !reflect.DeepEqual(got, want) {
			t.Errorf("InfraDependencies(%q) = %#v, want %#v", serviceID, got, want)
		}
	}
}

func TestServiceDependentsFindDirectCallers(t *testing.T) {
	dependents := ServiceDependents([]string{"quartermaster"})
	for _, want := range []string{"chandler", "privateer"} {
		found := false
		for _, got := range dependents {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("ServiceDependents(quartermaster) missing %q in %v", want, dependents)
		}
	}
}

func TestGlobalDNSDependencies(t *testing.T) {
	if got, want := GlobalDNSServiceDependencies("skipper"), []string{"bridge"}; !equalStrings(got, want) {
		t.Fatalf("GlobalDNSServiceDependencies(skipper) = %v, want %v", got, want)
	}
	for _, serviceID := range []string{"commodore", "quartermaster"} {
		if got := GlobalDNSServiceDependencies(serviceID); len(got) != 0 {
			t.Fatalf("GlobalDNSServiceDependencies(%s) = %v, want none", serviceID, got)
		}
	}

	if got, want := GlobalDNSServiceDependents([]string{"bridge"}), []string{"skipper"}; !equalStrings(got, want) {
		t.Fatalf("GlobalDNSServiceDependents(bridge) = %v, want %v", got, want)
	}
}

func TestCentralWritersReachDecklogInAggregatorRegion(t *testing.T) {
	for _, serviceID := range []string{"commodore", "deckhand", "purser", "quartermaster", "lookout", "steward"} {
		if got := AggregatorRegionDNSServiceDependencies(serviceID); !equalStrings(got, []string{"decklog"}) {
			t.Fatalf("AggregatorRegionDNSServiceDependencies(%s) = %v, want [decklog]", serviceID, got)
		}
		if scope := ServiceDependencyScope(serviceID, "decklog", "DECKLOG_GRPC_ADDR"); scope != DNSScopeAggregatorRegion {
			t.Fatalf("%s decklog scope = %q, want %q", serviceID, scope, DNSScopeAggregatorRegion)
		}
	}
	if got, want := AggregatorRegionDNSServiceDependencies("skipper"), []string{"decklog", "lookout"}; !equalStrings(got, want) {
		t.Fatalf("AggregatorRegionDNSServiceDependencies(skipper) = %v, want %v", got, want)
	}
	for _, serviceID := range []string{"skipper"} {
		if scope := ServiceDependencyScope(serviceID, "decklog", "DECKLOG_GRPC_ADDR"); scope != DNSScopeAggregatorRegion {
			t.Fatalf("%s decklog scope = %q, want %q", serviceID, scope, DNSScopeAggregatorRegion)
		}
	}
	// Regional Gateways reach the single Lookout and Bosun in the aggregator
	// region; their Decklog writes stay cluster-local.
	if got, want := AggregatorRegionDNSServiceDependencies("bridge"), []string{"bosun", "lookout"}; !equalStrings(got, want) {
		t.Fatalf("AggregatorRegionDNSServiceDependencies(bridge) = %v, want %v", got, want)
	}
	if got := AggregatorRegionDNSServiceDependencies("foghorn"); len(got) != 0 {
		t.Fatalf("AggregatorRegionDNSServiceDependencies(foghorn) = %v, want none; media producers stay cluster-local", got)
	}
	for _, serviceID := range []string{"bridge", "foghorn"} {
		if scope := ServiceDependencyScope(serviceID, "decklog", "DECKLOG_GRPC_ADDR"); scope != "" {
			t.Fatalf("%s decklog scope = %q, want cluster-local", serviceID, scope)
		}
	}
}

func TestLookoutDependencies(t *testing.T) {
	required := map[string]string{}
	for _, dep := range RequiredServiceEnv("lookout") {
		required[dep.TargetServiceID] = dep.EnvKey
	}
	if required["quartermaster"] != "QUARTERMASTER_GRPC_ADDR" || required["decklog"] != "DECKLOG_GRPC_ADDR" {
		t.Fatalf("RequiredServiceEnv(lookout) = %v, want quartermaster and decklog", required)
	}
	var hasDatabase, hasAggregatorKafka bool
	for _, dep := range InfraDependencies("lookout") {
		switch {
		case dep.Kind == InfraDatabase && dep.Provider == InfraProviderPrimary && !dep.Optional:
			hasDatabase = true
		case dep.Kind == InfraKafka && dep.Provider == InfraProviderAggregator && !dep.Optional:
			hasAggregatorKafka = true
		}
	}
	if !hasDatabase || !hasAggregatorKafka {
		t.Fatalf("InfraDependencies(lookout) = %v, want required primary database and aggregator Kafka", InfraDependencies("lookout"))
	}
	if scope := ServiceDependencyScope("alertmanager", "lookout", "ALERTMANAGER_LOOKOUT_URL"); scope != DNSScopeAggregatorRegion {
		t.Fatalf("alertmanager lookout scope = %q, want %q", scope, DNSScopeAggregatorRegion)
	}
	for _, dep := range ServiceDependencies("skipper") {
		if dep.TargetServiceID == "lookout" {
			if dep.EnvKey != "LOOKOUT_GRPC_ADDR" || !dep.Optional || dep.DNSScope != DNSScopeAggregatorRegion {
				t.Fatalf("skipper lookout dependency = %+v, want optional aggregator-region LOOKOUT_GRPC_ADDR", dep)
			}
			return
		}
	}
	t.Fatal("skipper has no lookout dependency")
}

func TestMetricsDNSDependencies(t *testing.T) {
	if got, want := DNSServiceDependencies("vmagent"), []string{"victoriametrics", "vmauth"}; !equalStrings(got, want) {
		t.Fatalf("DNSServiceDependencies(vmagent) = %v, want %v", got, want)
	}
	if got, want := DNSServiceDependencies("vmauth"), []string{"victoriametrics"}; !equalStrings(got, want) {
		t.Fatalf("DNSServiceDependencies(vmauth) = %v, want %v", got, want)
	}
	if got, want := GlobalDNSServiceDependencies("vmagent"), []string{"victoriametrics", "vmauth"}; !equalStrings(got, want) {
		t.Fatalf("GlobalDNSServiceDependencies(vmagent) = %v, want %v", got, want)
	}
	if got, want := GlobalDNSServiceDependencies("vmalert"), []string{"alertmanager", "victoriametrics"}; !equalStrings(got, want) {
		t.Fatalf("GlobalDNSServiceDependencies(vmalert) = %v, want %v", got, want)
	}
	required := map[string]string{}
	for _, dep := range RequiredServiceEnv("vmalert") {
		required[dep.TargetServiceID] = dep.EnvKey
	}
	if required["victoriametrics"] != "VMALERT_DATASOURCE_URL" || required["alertmanager"] != "VMALERT_NOTIFIER_URL" {
		t.Fatalf("RequiredServiceEnv(vmalert) = %v, want datasource and notifier env", required)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
