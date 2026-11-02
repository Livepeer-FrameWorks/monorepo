package cmd

import (
	"strings"
	"testing"
)

const healthyAccountRecoveryOutput = `
######## database commodore
account_states_table=1
closures_unsynced=1
closures_failing=0
ingest_stops_pending=1
ingest_stops_oldest_seconds=12
######## database purser
held_downgrades=0
recovery_open=2
recovery_stuck=0
`

func TestAccountRecoveryDoctorPassesNormalWaits(t *testing.T) {
	report := evaluateAccountRecoveryDoctor([]string{"commodore", "purser"}, parseAccountRecoveryDoctorOutput(healthyAccountRecoveryOutput))
	if len(report.problems) != 0 || len(report.warnings) != 0 || len(report.steps) != 0 {
		t.Fatalf("normal waits judged a problem: %+v", report)
	}
	notes := strings.Join(report.notes, "; ")
	if !strings.Contains(notes, "1 closure(s) synchronizing") || !strings.Contains(notes, "2 billing recovery item(s) waiting normally") {
		t.Fatalf("notes=%q", notes)
	}
}

func TestAccountRecoveryDoctorNamesTenantsBehindEveryAlertCondition(t *testing.T) {
	stuck := `
######## database commodore
account_states_table=1
closures_unsynced=2
closures_failing=2
?column?
closure_failing_tenant:tenant-b=3
closure_failing_tenant:tenant-a=7
(2 rows)
ingest_stops_pending=4
ingest_stops_oldest_seconds=7200
ingest_stop_tenant:tenant-c=3
######## database purser
held_downgrades=1
held_downgrade_tenant:tenant-d=93600
recovery_open=5
recovery_stuck=2
recovery_stuck_tenant:unbound=1
recovery_stuck_tenant:tenant-e=1
`
	report := evaluateAccountRecoveryDoctor([]string{"purser", "commodore"}, parseAccountRecoveryDoctorOutput(stuck))
	problems := strings.Join(report.problems, "\n")
	for _, want := range []string{
		"2 tenant closure(s) failing Quartermaster synchronization (tenant-a: 7 failures, tenant-b: 3 failures)",
		"4 session stop(s) pending, oldest 7200s (tenant-c: 3 stops)",
		"1 confirmed downgrade(s) held past their effective time (tenant-d: 93600 s past effective)",
		"2 billing recovery item(s) failing repeatedly or unresolved over 14 days (tenant-e: 1 items, unbound: 1 items)",
	} {
		if !strings.Contains(problems, want) {
			t.Fatalf("problems %q missing %q", problems, want)
		}
	}
	var commands []string
	for _, step := range report.steps {
		commands = append(commands, step.Cmd)
	}
	joined := strings.Join(commands, "\n")
	for _, want := range []string{"frameworks admin tenants get <tenant-id>", "frameworks admin clusters retire <cluster-id> --reason <reason>", "frameworks admin billing recovery list"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("next steps %q missing %q", joined, want)
		}
	}
}

// A probe that stopped part way prints some keys and not others; an absent
// count must read as "could not be checked", never as zero.
func TestAccountRecoveryDoctorWarnsOnUnreadCounts(t *testing.T) {
	partial := `
######## database commodore
account_states_table=1
closures_unsynced=0
ERROR:  canceling statement due to statement timeout
`
	report := evaluateAccountRecoveryDoctor([]string{"commodore", "purser"}, parseAccountRecoveryDoctorOutput(partial))
	warnings := strings.Join(report.warnings, "\n")
	if !strings.Contains(warnings, "commodore: could not be checked: closures_failing, ingest_stops_pending, ingest_stops_oldest_seconds") || !strings.Contains(warnings, "purser: recovery state could not be read") {
		t.Fatalf("warnings=%q", warnings)
	}

	preLifecycle := "\n######## database commodore\naccount_states_table=0\nERROR:  relation \"commodore.tenant_account_states\" does not exist\n"
	report = evaluateAccountRecoveryDoctor([]string{"commodore"}, parseAccountRecoveryDoctorOutput(preLifecycle))
	if len(report.problems) != 0 || len(report.warnings) != 0 || !strings.Contains(strings.Join(report.notes, ";"), "not installed") {
		t.Fatalf("a Commodore schema without account lifecycle judged %+v", report)
	}
}
