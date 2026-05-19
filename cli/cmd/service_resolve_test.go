package cmd

import (
	"testing"

	"frameworks/cli/pkg/inventory"
)

func TestResolvePortUsesDeployDefaultForAliasedService(t *testing.T) {
	t.Parallel()

	port, err := resolvePort("livepeer-gateway-eu", inventory.ServiceConfig{Deploy: "livepeer-gateway"})
	if err != nil {
		t.Fatalf("resolvePort returned error: %v", err)
	}
	if port != 8935 {
		t.Fatalf("resolvePort = %d, want 8935", port)
	}
}

func TestResolveServiceDefinitionUsesDeployHealthPathForAliasedService(t *testing.T) {
	t.Parallel()

	def, ok := resolveServiceDefinition("livepeer-gateway-eu", inventory.ServiceConfig{Deploy: "livepeer-gateway"})
	if !ok {
		t.Fatal("resolveServiceDefinition returned !ok")
	}
	if def.HealthPath != "/healthz" {
		t.Fatalf("HealthPath = %q, want /healthz", def.HealthPath)
	}
}
