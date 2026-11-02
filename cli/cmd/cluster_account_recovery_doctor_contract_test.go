package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestAccountRecoveryDoctorMatchesServiceRules ties the doctor's copied
// predicates and thresholds to the Purser and Commodore sources whose alerts
// and decisions it mirrors, so a change there fails here until the doctor
// follows.
func TestAccountRecoveryDoctorMatchesServiceRules(t *testing.T) {
	read := func(path string) string {
		t.Helper()
		body, err := os.ReadFile(filepath.Join("..", "..", path))
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	constant := func(source, pattern string) int64 {
		t.Helper()
		match := regexp.MustCompile(pattern).FindStringSubmatch(source)
		if match == nil {
			t.Fatalf("source no longer declares %s", pattern)
		}
		n, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if grace := constant(read("api_billing/internal/handlers/tier_downgrade.go"), `const HeldDowngradeGrace = (\d+) \* time\.Hour`); grace != purserHeldDowngradeGraceHours {
		t.Fatalf("Purser HeldDowngradeGrace is %dh, the doctor uses %dh", grace, purserHeldDowngradeGraceHours)
	}
	if threshold := constant(read("api_billing/internal/handlers/threshold.go"), `const suspensionThresholdCents int64 = (-?\d+)`); threshold != purserSuspensionThresholdCents {
		t.Fatalf("Purser suspensionThresholdCents is %d, the doctor uses %d", threshold, purserSuspensionThresholdCents)
	}

	normalize := func(sql string) string {
		sql = strings.ReplaceAll(sql, "subscription.", "")
		return strings.Join(strings.Fields(sql), " ")
	}
	doctor := normalize(purserAccountRecoveryDoctorSQL)
	query := func(source, name string) string {
		t.Helper()
		_, rest, ok := strings.Cut(source, "-- name: "+name+" ")
		if !ok {
			t.Fatalf("query %s not found", name)
		}
		var statement []string
		for _, line := range strings.Split(rest, "\n")[1:] {
			if !strings.HasPrefix(strings.TrimSpace(line), "--") {
				statement = append(statement, line)
			}
		}
		body, _, _ := strings.Cut(strings.Join(statement, "\n"), ";")
		return normalize(body)
	}
	held := query(read("api_billing/internal/database/queries/tier_downgrades.sql"), "CountHeldConfirmedDowngrades")
	for _, predicate := range []string{
		"pending_tier_id IS NOT NULL",
		"pending_reason = 'downgrade'",
		"pending_provider_tier_id = pending_tier_id",
		"pending_effective_at < NOW() - (sqlc.arg(grace_seconds)::bigint * INTERVAL '1 second')",
	} {
		if !strings.Contains(held, predicate) {
			t.Fatalf("CountHeldConfirmedDowngrades no longer filters on %q:\n%s", predicate, held)
		}
	}
	if !strings.Contains(doctor, "WHERE pending_tier_id IS NOT NULL AND pending_reason = 'downgrade' AND pending_provider_tier_id = pending_tier_id AND pending_effective_at < NOW() - INTERVAL '"+strconv.Itoa(purserHeldDowngradeGraceHours)+" hours'") {
		t.Fatalf("doctor held-downgrade predicate drifted from CountHeldConfirmedDowngrades:\n%s", doctor)
	}

	stuck := query(read("api_billing/internal/database/queries/provider_webhook_inbox.sql"), "ProviderWebhookInboxRepeatedFailures")
	for _, predicate := range []string{
		"WHERE status IN ('pending', 'failed', 'processing') AND attempts > 1",
		"WHERE status IN ('pending', 'failed', 'processing') AND unresolved_since < NOW() - INTERVAL '" + strconv.Itoa(purserRecoveryUnresolvedDays) + " days'",
	} {
		if !strings.Contains(stuck, predicate) {
			t.Fatalf("ProviderWebhookInboxRepeatedFailures no longer matches %q:\n%s", predicate, stuck)
		}
		if strings.Count(doctor, predicate) != 2 {
			t.Fatalf("doctor recovery predicates drifted from ProviderWebhookInboxRepeatedFailures: want %q in both stuck queries", predicate)
		}
	}

	// Commodore refuses new sessions for a prepaid balance at or below zero;
	// the doctor's held set is the same rule.
	authority := read("api_control/internal/grpc/media_authority.go")
	if !strings.Contains(authority, "model == mediaauthoritypb.TenantBillingModel_TENANT_BILLING_MODEL_PREPAID && billing.GetBalanceCents() <= 0:") {
		t.Fatal("Commodore's prepaid_balance_unavailable rule changed; update purserSpendHoldDoctorSQL")
	}
	if !strings.Contains(doctor, "billing_model = 'prepaid'") || !strings.Contains(doctor, "AND COALESCE(balance.balance_cents, 0) <= 0") {
		t.Fatalf("doctor spend-hold rule drifted:\n%s", doctor)
	}
}
