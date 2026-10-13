package cmd

import (
	"bytes"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
)

// Both plans name the runtime-role session limits the service-database step
// converges on every SQL deployment and Postgres instance.
func TestReleasePlansNameRuntimeRoleSessionLimits(t *testing.T) {
	for _, step := range []string{releasePlanServiceDatabasesStep, releaseApplyServiceDatabasesStep} {
		if !strings.Contains(step, "runtime-role session limits") || !strings.Contains(step, "Postgres instance") {
			t.Errorf("step %q does not name the runtime-role session limits", step)
		}
	}
	if !strings.HasSuffix(releaseApplyServiceDatabasesStep, "then expand migrations") {
		t.Errorf("apply step %q lost its expand migrations", releaseApplyServiceDatabasesStep)
	}
}

// A release that upgrades Foghorn says it takes the cells one at a time behind
// the legacy-tenant gate; one that does not says nothing.
func TestReleasePlanDescribesTheFoghornCellGate(t *testing.T) {
	manifest := &inventory.Manifest{Services: map[string]inventory.ServiceConfig{
		"foghorn-eu": {Enabled: true, Deploy: "foghorn"},
		"commodore":  {Enabled: true},
	}}
	var out bytes.Buffer
	writeFoghornCellGatePlan(&out, manifest, []string{"commodore", "foghorn-eu"})
	for _, want := range []string{"foghorn-eu", "one cell at a time", "no tenant on the legacy (schema 1) authority", "15m"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("plan lacks %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	writeFoghornCellGatePlan(&out, manifest, []string{"commodore"})
	if out.Len() != 0 {
		t.Fatalf("plan without Foghorn describes the cell gate:\n%s", out.String())
	}
}
