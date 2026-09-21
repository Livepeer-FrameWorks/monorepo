package cmd

import (
	"strings"
	"testing"

	"frameworks/cli/pkg/health"
	"frameworks/cli/pkg/inventory"
)

func TestPurserLedgerCurrencyResult(t *testing.T) {
	healthy := purserLedgerCurrencyResult(&health.CheckResult{Metadata: map[string]string{}}, 0, 0)
	if !healthy.OK || healthy.Status != "healthy" {
		t.Fatalf("zero non-EUR rows = %+v, want healthy", healthy)
	}
	degraded := purserLedgerCurrencyResult(&health.CheckResult{Metadata: map[string]string{}}, 3, 2)
	if degraded.OK || degraded.Status != "degraded" || !strings.Contains(degraded.Error, "purser_eur_ledger_conversion_v0_3_11") {
		t.Fatalf("stranded rows = %+v, want degraded naming the data migration", degraded)
	}
	if degraded.Metadata["non_eur_balance_rows"] != "3" || degraded.Metadata["tenants"] != "2" {
		t.Fatalf("metadata = %v", degraded.Metadata)
	}
}

func TestPurserLedgerCurrencyDoctorIsReadOnlyCount(t *testing.T) {
	query := strings.ToUpper(purserLedgerCurrencyDoctorSQL)
	for _, verb := range []string{"INSERT", "UPDATE", "DELETE", "ALTER", "DROP", "CREATE"} {
		if strings.Contains(query, verb) {
			t.Fatalf("doctor query contains %s: %s", verb, purserLedgerCurrencyDoctorSQL)
		}
	}
}

func TestPurserServiceForResolvesDeployName(t *testing.T) {
	manifest := &inventory.Manifest{Services: map[string]inventory.ServiceConfig{
		"billing": {Enabled: true, Deploy: "purser"},
		"bridge":  {Enabled: true},
	}}
	name, _, ok := purserServiceFor(manifest)
	if !ok || name != "billing" {
		t.Fatalf("purserServiceFor = %q, %v", name, ok)
	}
	if _, _, ok := purserServiceFor(&inventory.Manifest{Services: map[string]inventory.ServiceConfig{"purser": {Enabled: false}}}); ok {
		t.Fatal("disabled purser must not resolve")
	}
}
