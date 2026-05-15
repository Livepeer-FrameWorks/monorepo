package topology

import "testing"

func TestInfraDependenciesOnlyIncludeDirectInfraClients(t *testing.T) {
	for _, serviceID := range []string{"bridge", "chandler", "deckhand"} {
		if deps := InfraDependencies(serviceID); len(deps) != 0 {
			t.Fatalf("%s infra deps = %#v, want none", serviceID, deps)
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
