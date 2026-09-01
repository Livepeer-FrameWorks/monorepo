package services

import (
	"slices"
	"testing"

	"frameworks/cli/pkg/provisioner"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
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

func TestPeriscopeMeteringIsFirstClassService(t *testing.T) {
	catalog, err := LoadCatalog()
	if err != nil {
		t.Fatalf("failed to load catalog: %v", err)
	}

	spec, ok := catalog.Services["periscope-metering"]
	if !ok {
		t.Fatal("periscope-metering missing from service catalog")
	}
	if spec.Health.Port != 18021 || spec.Health.Path != "/health" {
		t.Fatalf("periscope-metering health = %+v, want HTTP /health on 18021", spec.Health)
	}
	for _, profile := range []string{"central-all", "platform", "data"} {
		if !slices.Contains(catalog.Profiles[profile], "periscope-metering") {
			t.Errorf("profile %q does not include periscope-metering", profile)
		}
	}
	if got := provisioner.ServicePorts["periscope-metering"]; got != 18021 {
		t.Fatalf("provisioner port = %d, want 18021", got)
	}
	if _, err := provisioner.GetProvisioner("periscope-metering", nil); err != nil {
		t.Fatalf("GetProvisioner(periscope-metering): %v", err)
	}
}
