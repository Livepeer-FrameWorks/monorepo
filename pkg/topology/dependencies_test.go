package topology

import "testing"

func TestInfraDependenciesOnlyIncludeDirectInfraClients(t *testing.T) {
	for _, serviceID := range []string{"bridge", "chandler", "deckhand"} {
		if deps := InfraDependencies(serviceID); len(deps) != 0 {
			t.Fatalf("%s infra deps = %#v, want none", serviceID, deps)
		}
	}
}
