package services

import (
	"slices"
	"strconv"
	"strings"
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

// notCentralCatalog are the servicedefs services `services plan` never
// deploys as a central-tier container, each with the path that deploys it.
var notCentralCatalog = map[string]string{
	"helmsman":       "edge bundle, deployed by edge provision",
	"mistserver":     "edge bundle, deployed by edge provision",
	"privateer":      "mesh agent on every host, deployed by cluster provision",
	"network-prober": "runs on external probe hosts through its own installer",
	"postgres":       "data role of cluster provision",
	"kafka":          "data role of cluster provision",
	"clickhouse":     "data role of cluster provision",
	"nginx":          "reverse-proxy role of cluster provision",
	"caddy":          "reverse-proxy role of cluster provision",
}

// TestCatalogCoversEveryCentralService fails when servicedefs gains a service
// that neither the central-tier catalog nor notCentralCatalog accounts for.
func TestCatalogCoversEveryCentralService(t *testing.T) {
	catalog, err := LoadCatalog()
	if err != nil {
		t.Fatalf("failed to load catalog: %v", err)
	}
	listed := map[string]bool{}
	for name, spec := range catalog.Services {
		listed[name] = true
		if spec.Deploy != "" {
			listed[spec.Deploy] = true
		}
	}
	for id := range servicedefs.Services {
		if listed[id] {
			if reason, ok := notCentralCatalog[id]; ok {
				t.Errorf("%s is in the catalog and also excluded (%s)", id, reason)
			}
			continue
		}
		if _, ok := notCentralCatalog[id]; !ok {
			t.Errorf("servicedefs service %q is missing from catalog.yaml", id)
		}
	}
}

// Catalog ports repeat servicedefs: every published or health-checked port of
// a service is one servicedefs registers for it.
func TestCatalogPortsAreRegisteredListeners(t *testing.T) {
	catalog, err := LoadCatalog()
	if err != nil {
		t.Fatalf("failed to load catalog: %v", err)
	}
	for name, spec := range catalog.Services {
		id := name
		if spec.Deploy != "" {
			id = spec.Deploy
		}
		registered := map[int]bool{}
		if def, ok := servicedefs.Lookup(id); ok {
			registered[def.DefaultPort] = true
		}
		if port, ok := servicedefs.DefaultGRPCPort(id); ok {
			registered[port] = true
		}
		for _, aux := range servicedefs.AuxiliaryPorts(id) {
			registered[aux.Port] = true
		}
		if port, ok := servicedefs.ContainerPorts[id]; ok {
			registered[port] = true
		}
		if spec.Health.Port != 0 && !registered[spec.Health.Port] {
			t.Errorf("%s: health port %d is not a listener servicedefs registers for %s", name, spec.Health.Port, id)
		}
		for _, mapping := range spec.Ports {
			for _, side := range strings.Split(mapping, ":") {
				port, err := strconv.Atoi(side)
				if err != nil {
					t.Errorf("%s: port mapping %q", name, mapping)
					continue
				}
				if !registered[port] {
					t.Errorf("%s: published port %d is not a listener servicedefs registers for %s", name, port, id)
				}
			}
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

func TestLogbookHealthMatchesRuntimeRegistry(t *testing.T) {
	catalog, err := LoadCatalog()
	if err != nil {
		t.Fatalf("failed to load catalog: %v", err)
	}

	spec, ok := catalog.Services["logbook"]
	if !ok {
		t.Fatal("logbook missing from service catalog")
	}
	definition, ok := servicedefs.Lookup("logbook")
	if !ok {
		t.Fatal("logbook missing from service registry")
	}
	if spec.Health.Protocol != definition.HealthProtocol || spec.Health.Path != definition.HealthPath || spec.Health.Port != definition.DefaultPort {
		t.Fatalf("catalog health = %+v, registry = %s %s on %d", spec.Health, definition.HealthProtocol, definition.HealthPath, definition.DefaultPort)
	}
}
