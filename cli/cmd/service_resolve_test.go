package cmd

import (
	"testing"

	"frameworks/cli/internal/releases"
	"frameworks/cli/pkg/inventory"
)

// An aliased Foghorn (e.g. manifest key "foghorn-us", deploy: "foghorn") must resolve to the "foghorn" DEPLOY name,
// and database ownership must be looked up by that deploy name — otherwise the migration gate finds no owner for the
// alias and skips, letting an aliased Foghorn install ahead of its own migrations. Regression for the upgrade-gate
// ownership bypass.
func TestAliasedFoghornResolvesDatabaseOwnership(t *testing.T) {
	t.Parallel()

	deployName, err := resolveDeployName("foghorn-us", inventory.ServiceConfig{Deploy: "foghorn"})
	if err != nil {
		t.Fatalf("resolveDeployName(foghorn-us) error: %v", err)
	}
	if deployName != "foghorn" {
		t.Fatalf("deployName = %q, want foghorn", deployName)
	}
	// Ownership looked up by the LOGICAL alias finds nothing (the pre-fix bypass); by the DEPLOY name it finds foghorn.
	if db, ok := releases.ServiceDatabaseLookup("foghorn-us"); ok || db != "" {
		t.Fatalf("ServiceDatabaseLookup(foghorn-us) = (%q, %v), want empty/false", db, ok)
	}
	if db := releases.ServiceDatabase(deployName); db != "foghorn" {
		t.Fatalf("ServiceDatabase(%q) = %q, want foghorn", deployName, db)
	}
}

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
