package services

import (
	"testing"

	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/servicedefs"
)

func TestCatalogServicesHaveRegistryEntries(t *testing.T) {
	catalog, err := LoadCatalog()
	if err != nil {
		t.Fatalf("failed to load catalog: %v", err)
	}

	for name := range catalog.Services {
		if _, ok := servicedefs.Lookup(name); !ok {
			t.Errorf("catalog service %q missing from servicedefs registry", name)
		}
		if _, ok := provisioner.ServicePorts[name]; !ok {
			t.Errorf("catalog service %q missing from provisioner ServicePorts", name)
		}
	}
}
