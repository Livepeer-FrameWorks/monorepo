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
		sql = strings.ReplaceAll(sql, "current_subscription.", "")
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
	if !strings.Contains(stuck, "FROM purser.billing_recovery_failures") || strings.Count(doctor, "FROM purser.billing_recovery_failures") != 2 {
		t.Fatal("service and deep doctor must read the same Purser-owned failure inventory")
	}

	// Commodore permits bounded grace at a spent balance; the doctor excludes
	// the same live episode using Purser's reserved-balance calculation.
	authority := read("api_control/internal/grpc/media_authority.go")
	if !strings.Contains(authority, "model == mediaauthoritypb.TenantBillingModel_TENANT_BILLING_MODEL_PREPAID && billing.GetBalanceCents() <= 0 && !billing.GetPaymentGrace().GetActive():") {
		t.Fatal("Commodore's prepaid_balance_unavailable rule changed; update purserSpendHoldDoctorSQL")
	}
	if !strings.Contains(doctor, "billing_model = 'prepaid'") || !strings.Contains(doctor, "AND COALESCE(balance.balance_cents, 0) <= 0") {
		t.Fatalf("doctor spend-hold rule drifted:\n%s", doctor)
	}
	grace := read("api_billing/internal/grpc/payment_grace.go")
	for _, predicate := range []string{"status != \"active\"", "!ends.Time.After(time.Now())", "credit <= 0", "available <= -credit"} {
		if !strings.Contains(grace, predicate) {
			t.Fatalf("Purser payment grace changed at %q; update the doctor", predicate)
		}
	}
	for _, predicate := range []string{"AND NOT COALESCE((status = 'active'", "balance.grace_ends_at > NOW()", "balance.grace_credit_cents > 0", "balance.balance_cents - reservations.reserved_balance_cents > -balance.grace_credit_cents", "updated_at >= NOW() - INTERVAL '3 minutes'"} {
		if !strings.Contains(doctor, predicate) {
			t.Fatalf("doctor grace predicate missing %q", predicate)
		}
	}
}
