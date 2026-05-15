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

func TestSkipperBridgeDependencyIsGlobalDNS(t *testing.T) {
	deps := GlobalDNSServiceDependencies("skipper")
	if len(deps) != 1 || deps[0] != "bridge" {
		t.Fatalf("GlobalDNSServiceDependencies(skipper) = %v, want [bridge]", deps)
	}

	dependents := GlobalDNSServiceDependents([]string{"bridge"})
	if len(dependents) != 1 || dependents[0] != "skipper" {
		t.Fatalf("GlobalDNSServiceDependents(bridge) = %v, want [skipper]", dependents)
	}
}
