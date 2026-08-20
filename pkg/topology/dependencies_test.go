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
			{Kind: InfraClickHouse, Provider: InfraProviderPrimary, Purpose: "analytics writes"},
			{Kind: InfraKafka, Provider: InfraProviderAggregator, Purpose: "analytics and service event ingestion"},
		},
		"periscope-query": {
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
	cases := map[string][]string{
		"commodore":     {"decklog"},
		"quartermaster": {"decklog"},
		"skipper":       {"bridge", "decklog"},
	}

	for serviceID, want := range cases {
		if got := GlobalDNSServiceDependencies(serviceID); !equalStrings(got, want) {
			t.Fatalf("GlobalDNSServiceDependencies(%s) = %v, want %v", serviceID, got, want)
		}
	}

	if got, want := GlobalDNSServiceDependents([]string{"bridge"}), []string{"skipper"}; !equalStrings(got, want) {
		t.Fatalf("GlobalDNSServiceDependents(bridge) = %v, want %v", got, want)
	}
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
