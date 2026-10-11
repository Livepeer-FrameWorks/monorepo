package cmd

import (
	"regexp"
	"strings"
	"testing"
)

func TestMediaAuthorityDoctorJudgesConvergenceFromProbeOutput(t *testing.T) {
	healthy := `
######## database commodore
?column?
obligations_table=1
(1 row)
?column?
parked=0
(1 row)
refresh_oldest_due_seconds=4
bulk_oldest_due_seconds=900
expired_warm=0
renewal_missing=0
legacy_inbox_unfinished=0
delivery_stuck=0
delivery_rejected=0
authorities=58
versions_last_hour=7
invalid_indexes=0
legacy_schema_tenants_on_ready_cells=0

######## database foghorn_eu
collection_overdue=0
rejections_last_hour=0
`
	if problems, warnings := evaluateMediaAuthorityDoctor([]string{"commodore", "foghorn_eu"}, parseMediaAuthorityDoctorOutput(healthy)); len(problems) != 0 || len(warnings) != 0 {
		t.Fatalf("healthy state judged problems=%v warnings=%v", problems, warnings)
	}

	// A cell database on a host that could not be reached, and a probe whose
	// statement failed, must not read as healthy zeroes.
	partial := strings.Replace(healthy, "collection_overdue=0\n", "ERROR:  canceling statement due to statement timeout\n", 1)
	problems, warnings := evaluateMediaAuthorityDoctor([]string{"commodore", "foghorn_eu", "foghorn_us"}, parseMediaAuthorityDoctorOutput(partial))
	if len(problems) != 0 {
		t.Fatalf("unread state judged as problems: %v", problems)
	}
	for _, want := range []string{"foghorn_eu: could not be checked: collection_overdue", "foghorn_us: media authority state could not be read"} {
		if !strings.Contains(strings.Join(warnings, "\n"), want) {
			t.Fatalf("warnings %v missing %q", warnings, want)
		}
	}

	// The production incident shape: one target that can never compile, a
	// refresh lane minutes behind, and a cell rejecting obsolete deliveries. Two
	// authorities in use ran past their validity, which is a problem; a cell
	// still holding authority nobody uses is only slow housekeeping.
	incident := `
######## database commodore
obligations_table=1
parked=1
refresh_oldest_due_seconds=480
bulk_oldest_due_seconds=7200
expired_warm=2
renewal_missing=0
legacy_inbox_unfinished=2416
delivery_stuck=0
delivery_rejected=0
authorities=58
versions_last_hour=560
invalid_indexes=0
legacy_schema_tenants_on_ready_cells=0
######## database foghorn_eu
collection_overdue=0
rejections_last_hour=33
######## database foghorn_us
collection_overdue=2
rejections_last_hour=0
`
	problems, warnings = evaluateMediaAuthorityDoctor([]string{"commodore", "foghorn_eu", "foghorn_us"}, parseMediaAuthorityDoctorOutput(incident))
	for _, want := range []string{"1 refresh target(s) parked", "waited 480s", "2 authority(ies) in use are past their validity"} {
		if !strings.Contains(strings.Join(problems, "\n"), want) {
			t.Fatalf("problems %v missing %q", problems, want)
		}
	}
	if strings.Contains(strings.Join(problems, "\n"), "foghorn_us") {
		t.Fatalf("expired authority nobody uses judged a problem: %v", problems)
	}
	for _, want := range []string{
		"2416 unfinished legacy refresh inbox", "foghorn_eu: 33 signed authority apply rejection",
		"560 versions published in the last hour for 58 authorities", "oldest due bulk refresh has waited 7200s",
		"foghorn_us: 2 expired authority(ies) should have been forgotten",
	} {
		if !strings.Contains(strings.Join(warnings, "\n"), want) {
			t.Fatalf("warnings %v missing %q", warnings, want)
		}
	}

	// A Commodore schema without obligations: every obligation query errors, so
	// the check must say it cannot judge rather than report a healthy queue.
	preObligations := `
######## database commodore
obligations_table=0
ERROR:  relation "commodore.media_authority_refresh_obligations" does not exist
legacy_inbox_unfinished=2416
delivery_stuck=0
delivery_rejected=0
invalid_indexes=0
legacy_schema_tenants_on_ready_cells=0
`
	problems, warnings = evaluateMediaAuthorityDoctor([]string{"commodore"}, parseMediaAuthorityDoctorOutput(preObligations))
	if len(problems) != 0 || !strings.Contains(strings.Join(warnings, "\n"), "refresh obligations are not installed") {
		t.Fatalf("pre-obligation schema judged problems=%v warnings=%v", problems, warnings)
	}
}

// A cell that attests placement enforcement refuses a tenant still on schema 1,
// so any such tenant is an outage in that cell and makes the check unhealthy,
// named per cell. A probe that could not count them is not read as zero.
func TestMediaAuthorityDoctorFlagsLegacySchemaTenantsOnEnforcingCells(t *testing.T) {
	healthyCommodore := `
######## database commodore
obligations_table=1
parked=0
refresh_oldest_due_seconds=0
bulk_oldest_due_seconds=0
expired_warm=0
renewal_missing=0
delivery_stuck=0
delivery_rejected=0
authorities=12
versions_last_hour=1
invalid_indexes=0
`
	stranded := healthyCommodore + `?column?
legacy_schema_tenants_on_ready_cells=3
legacy_schema_tenants_cell:cell-us=1
legacy_schema_tenants_cell:cell-eu=2
(3 rows)
`
	problems, _ := evaluateMediaAuthorityDoctor([]string{"commodore"}, parseMediaAuthorityDoctorOutput(stranded))
	want := "3 tenant authority(ies) still on schema 1 in cells that enforce placement (cell-eu: 2, cell-us: 1)"
	if !strings.Contains(strings.Join(problems, "\n"), want) {
		t.Fatalf("problems %v missing %q", problems, want)
	}

	clean := healthyCommodore + "legacy_schema_tenants_on_ready_cells=0\n"
	problems, warnings := evaluateMediaAuthorityDoctor([]string{"commodore"}, parseMediaAuthorityDoctorOutput(clean))
	if len(problems) != 0 || len(warnings) != 0 {
		t.Fatalf("no stranded tenants judged problems=%v warnings=%v", problems, warnings)
	}

	problems, warnings = evaluateMediaAuthorityDoctor([]string{"commodore"}, parseMediaAuthorityDoctorOutput(healthyCommodore))
	if len(problems) != 0 || !strings.Contains(strings.Join(warnings, "\n"), "could not be checked: legacy_schema_tenants_on_ready_cells") {
		t.Fatalf("uncounted stranded tenants judged problems=%v warnings=%v", problems, warnings)
	}

	for _, sql := range []string{commodoreMediaAuthorityDoctorSQL, commodoreMediaAuthorityDiagnosticSQL} {
		if !strings.Contains(sql, "version.payload_schema_version < 2") || !strings.Contains(sql, "capability.enforcement_ready") {
			t.Fatal("legacy-schema count does not compare tenant schema with the cell's attested enforcement")
		}
	}
}

func TestMediaAuthorityDoctorSQLIsReadOnly(t *testing.T) {
	write := regexp.MustCompile(`(?i)\b(insert|update|delete|truncate|alter|drop|create|grant)\b`)
	for name, sql := range map[string]string{"commodore": commodoreMediaAuthorityDoctorSQL, "foghorn": foghornMediaAuthorityDoctorSQL} {
		if !strings.HasPrefix(sql, "SET default_transaction_read_only = on;") {
			t.Fatalf("%s doctor SQL must open by forcing a read-only session", name)
		}
		if match := write.FindString(sql); match != "" {
			t.Fatalf("%s doctor SQL must stay read-only, found %q", name, match)
		}
	}
}
