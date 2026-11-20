package graph

import (
	"frameworks/api_gateway/graph/generated"
	"testing"
)

func TestNodeCapacityPublicContract(t *testing.T) {
	schema := generated.NewExecutableSchema(generated.Config{Resolvers: &Resolver{}}).Schema()
	for typename, fields := range map[string][]string{
		"InfrastructureNode":   {"capacity"},
		"NodeCapacity":         {"egressBudgetBytesPerSec", "ingressBudgetBytesPerSec", "egressSource", "ingressSource", "revision", "updatedAt", "appliedRevision"},
		"Cluster":              {"capacity"},
		"ClusterCapacity":      {"egressBudgetBytesPerSec", "ingressBudgetBytesPerSec", "egressUsableBytesPerSec", "ingressUsableBytesPerSec", "defaultBudgetNodes"},
		"Mutation":             {"setNodeCapacity"},
		"NetworkClusterStatus": {"egressUsableMbps", "defaultBudgetNodes"},
	} {
		definition := schema.Types[typename]
		if definition == nil {
			t.Errorf("missing type %s", typename)
			continue
		}
		for _, field := range fields {
			if definition.Fields.ForName(field) == nil {
				t.Errorf("missing %s.%s", typename, field)
			}
		}
	}
	if field := schema.Types["Cluster"].Fields.ForName("maxBandwidthMbps"); field == nil || field.Directives.ForName("deprecated") == nil {
		t.Error("legacy cluster budget must remain deprecated for live SDK lines")
	}
}
