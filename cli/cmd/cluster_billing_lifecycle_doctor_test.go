package cmd

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// healthyBillingLifecycleOutput is the Purser billing lifecycle probe of a
// platform with nothing stuck.
const healthyBillingLifecycleOutput = `billing_lifecycle_installed=1
free_fallback_moves_failing=0
free_fallback_notices_failing=0
free_fallback_notices_abandoned=0
grant_expiries_failing=0
grants_ending_within_7d=0
grant_notices_failing=0
`

func evaluatePurserLifecycle(t *testing.T, purser string) accountRecoveryDoctorReport {
	t.Helper()
	output := "\n######## database purser\nheld_downgrades=0\nrecovery_open=0\nrecovery_stuck=0\nspend_held=0\nspend_held_below_limit=0\n" + purser
	return evaluateAccountRecoveryDoctor([]string{"purser"}, parseAccountRecoveryDoctorOutput(output))
}

func TestBillingLifecycleDoctorPassesAQuietPlatform(t *testing.T) {
	report := evaluatePurserLifecycle(t, strings.Replace(healthyBillingLifecycleOutput, "grants_ending_within_7d=0", "grants_ending_within_7d=2", 1))
	if len(report.problems) != 0 || len(report.warnings) != 0 || len(report.steps) != 0 {
		t.Fatalf("quiet platform judged %+v", report)
	}
	if notes := strings.Join(report.notes, "; "); !strings.Contains(notes, "2 operator grant(s) end within 7 days") {
		t.Fatalf("notes=%q", notes)
	}
}

// Every alert condition behind PurserFreeFallbackStuck and
// PurserOperatorGrantExpiryStuck, and failing grant notices, fail the check
// naming the tenants and the command that resolves them.
func TestBillingLifecycleDoctorNamesTenantsBehindEveryCondition(t *testing.T) {
	report := evaluatePurserLifecycle(t, `billing_lifecycle_installed=1
free_fallback_moves_failing=1
free_fallback_notices_failing=1
free_fallback_notices_abandoned=1
free_fallback_move_tenant:tenant-a=4
free_fallback_notice_tenant:tenant-b=3
free_fallback_notice_abandoned_tenant:tenant-c=6
grant_expiries_failing=1
grant_expiry_tenant:tenant-d=2
grants_ending_within_7d=0
grant_notices_failing=1
grant_notice_tenant:tenant-e=3
`)
	problems := strings.Join(report.problems, "\n")
	for _, want := range []string{
		"1 move(s) to Free failing or waiting on settlement over 14 days (tenant-a: 4 failures)",
		"1 grace or move-to-Free email(s) failing (tenant-b: 3 failures)",
		"1 grace or move-to-Free email(s) abandoned after five failures (tenant-c: 6 failures)",
		"1 operator grant expiry(ies) failing repeatedly (tenant-d: 2 failures)",
		"1 operator grant ending or ended email(s) failing (tenant-e: 3 failures)",
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
	for _, want := range []string{
		"frameworks admin billing recovery list",
		"frameworks admin billing recovery notice --tenant-id <tenant-id> --reason <reason>",
		"frameworks admin billing grant list --include-expired",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("next steps %q missing %q", joined, want)
		}
	}
}

// A Purser schema from before these tables reports them as not installed;
// a probe that stopped part way reports what it could not check.
func TestBillingLifecycleDoctorHandlesOlderAndPartialState(t *testing.T) {
	report := evaluatePurserLifecycle(t, "billing_lifecycle_installed=0\nERROR:  column \"free_fallback_at\" does not exist\n")
	if len(report.problems) != 0 || len(report.warnings) != 0 || !strings.Contains(strings.Join(report.notes, ";"), "not installed") {
		t.Fatalf("a Purser schema without the billing lifecycle judged %+v", report)
	}
	report = evaluatePurserLifecycle(t, "billing_lifecycle_installed=1\nfree_fallback_moves_failing=0\nERROR:  canceling statement due to statement timeout\n")
	if warnings := strings.Join(report.warnings, "\n"); !strings.Contains(warnings, "purser: could not be checked: free_fallback_notices_failing") {
		t.Fatalf("warnings=%q", warnings)
	}
}

// The doctor copies Purser's predicates; a change there fails here until the
// doctor follows.
func TestBillingLifecycleDoctorMatchesServiceRules(t *testing.T) {
	read := func(path string) string {
		t.Helper()
		body, err := os.ReadFile(filepath.Join("..", "..", path))
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(strings.Fields(string(body)), " ")
	}
	doctor := strings.Join(strings.Fields(strings.ReplaceAll(purserBillingLifecycleDoctorSQL, "subscription.", "")), " ")
	fallback := read("api_billing/internal/database/queries/free_fallback.sql")
	for _, predicate := range []string{
		"free_fallback_at IS NOT NULL AND free_fallback_moved_at IS NULL AND free_fallback_error IS NOT NULL AND free_fallback_failures > 1",
		"free_fallback_wait_since <= NOW() - INTERVAL '" + strconv.Itoa(purserFreeFallbackWaitDays) + " days'",
		"free_fallback_notice_error IS NOT NULL AND free_fallback_notice_failures > 1 AND free_fallback_notice_abandoned_at IS NULL",
	} {
		if !strings.Contains(fallback, predicate) {
			t.Fatalf("CountRepeatedFreeFallbackFailures no longer matches %q", predicate)
		}
	}
	for _, predicate := range []string{
		"free_fallback_at IS NOT NULL AND free_fallback_moved_at IS NULL AND ((free_fallback_error IS NOT NULL AND free_fallback_failures > 1) OR free_fallback_wait_since <= NOW() - INTERVAL '" + strconv.Itoa(purserFreeFallbackWaitDays) + " days')",
		"free_fallback_notice_error IS NOT NULL AND free_fallback_notice_failures > 1",
	} {
		if !strings.Contains(doctor, predicate) {
			t.Fatalf("doctor Free fallback predicate drifted: want %q in\n%s", predicate, doctor)
		}
	}
	grants := read("api_billing/internal/database/queries/operator_grants.sql")
	if !strings.Contains(grants, "SELECT COUNT(*) FROM purser.subscription_operator_grants WHERE expiry_error IS NOT NULL AND expiry_failures>1;") ||
		!strings.Contains(doctor, "WHERE expiry_error IS NOT NULL AND expiry_failures > 1") {
		t.Fatal("doctor grant expiry predicate drifted from CountRepeatedOperatorGrantExpiryFailures")
	}
	notices := read("api_billing/internal/database/queries/operator_grant_notices.sql")
	if !strings.Contains(notices, "WHERE notified_at IS NULL AND error IS NOT NULL AND failures > 1;") ||
		!strings.Contains(doctor, "WHERE notified_at IS NULL AND error IS NOT NULL AND failures > 1") {
		t.Fatal("doctor grant notice predicate drifted from CountRepeatedOperatorGrantNoticeFailures")
	}
}
