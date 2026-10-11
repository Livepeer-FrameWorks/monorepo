package topology

import (
	"slices"
	"testing"
)

func TestNetworkObserverRequiresPrimaryDatabaseAndAggregatorDiscovery(t *testing.T) {
	deps := InfraDependencies("network-observer")
	for _, want := range []struct{ kind, provider string }{
		{InfraDatabase, InfraProviderPrimary}, {InfraKafka, InfraProviderAggregator},
	} {
		if !slices.ContainsFunc(deps, func(dep InfraDependency) bool {
			return dep.Kind == want.kind && dep.Provider == want.provider && !dep.Optional
		}) {
			t.Errorf("observer infra dependencies %v missing %s/%s", deps, want.kind, want.provider)
		}
	}
	if got := AggregatorRegionDNSServiceDependencies("network-observer"); !slices.Equal(got, []string{"decklog"}) {
		t.Errorf("observer aggregator DNS = %v, want decklog", got)
	}
	if deps := InfraDependencies("network-prober"); len(deps) != 0 {
		t.Errorf("external prober requires platform infrastructure: %v", deps)
	}
}
