package cmd

import (
	"fmt"
	"strconv"
	"strings"

	"frameworks/cli/internal/ux"
)

// purserFreeFallbackWaitDays is the provider settlement wait in Purser's
// CountRepeatedFreeFallbackFailures after which a move to Free counts as stuck.
const purserFreeFallbackWaitDays = 14

// purserBillingLifecycleDoctorSQL reads the inputs of PurserFreeFallbackStuck
// (Purser's CountRepeatedFreeFallbackFailures: closed tenants and granted
// subscriptions excluded), PurserOperatorGrantExpiryStuck
// (CountRepeatedOperatorGrantExpiryFailures), the operator grant notices
// that keep failing (CountRepeatedOperatorGrantNoticeFailures) and those
// abandoned after five failures, which wait for an operator requeue. It runs after
// the other Purser probes: on a schema without these tables and columns its
// statements fail and only its own keys are missing, which
// billing_lifecycle_installed=0 explains.
var purserBillingLifecycleDoctorSQL = fmt.Sprintf(`SELECT 'billing_lifecycle_installed=' || (to_regclass('purser.operator_grant_notices') IS NOT NULL)::int;
WITH excluded AS (
  SELECT subscription.id FROM purser.tenant_subscriptions subscription
  WHERE EXISTS (SELECT 1 FROM purser.tenant_billing_closures closure WHERE closure.tenant_id = subscription.tenant_id)
     OR EXISTS (SELECT 1 FROM purser.subscription_operator_grants grant_row WHERE grant_row.subscription_id = subscription.id)
), moves AS (
  SELECT subscription.tenant_id, subscription.free_fallback_failures AS failures FROM purser.tenant_subscriptions subscription
  WHERE subscription.free_fallback_at IS NOT NULL AND subscription.free_fallback_moved_at IS NULL
    AND ((subscription.free_fallback_error IS NOT NULL AND subscription.free_fallback_failures > 1)
      OR subscription.free_fallback_wait_since <= NOW() - INTERVAL '%[1]d days')
    AND subscription.id NOT IN (SELECT id FROM excluded)
), notices AS (
  SELECT subscription.tenant_id, subscription.free_fallback_notice_failures AS failures,
         subscription.free_fallback_notice_abandoned_at IS NOT NULL AS abandoned
  FROM purser.tenant_subscriptions subscription
  WHERE subscription.free_fallback_at IS NOT NULL AND subscription.free_fallback_notice_error IS NOT NULL
    AND subscription.free_fallback_notice_failures > 1
    AND subscription.id NOT IN (SELECT id FROM excluded)
), listed AS (
  (SELECT 'free_fallback_move_tenant:' AS prefix, tenant_id, failures FROM moves ORDER BY failures DESC, tenant_id LIMIT 10)
  UNION ALL
  (SELECT 'free_fallback_notice_tenant:', tenant_id, failures FROM notices WHERE NOT abandoned ORDER BY failures DESC, tenant_id LIMIT 10)
  UNION ALL
  (SELECT 'free_fallback_notice_abandoned_tenant:', tenant_id, failures FROM notices WHERE abandoned ORDER BY failures DESC, tenant_id LIMIT 10)
)
SELECT 'free_fallback_moves_failing=' || (SELECT count(*) FROM moves)
UNION ALL SELECT 'free_fallback_notices_failing=' || (SELECT count(*) FROM notices WHERE NOT abandoned)
UNION ALL SELECT 'free_fallback_notices_abandoned=' || (SELECT count(*) FROM notices WHERE abandoned)
UNION ALL SELECT prefix || tenant_id::text || '=' || failures FROM listed;
SELECT 'grant_expiries_failing=' || count(*) FROM purser.subscription_operator_grants
  WHERE expiry_error IS NOT NULL AND expiry_failures > 1;
SELECT 'grant_expiry_tenant:' || subscription.tenant_id::text || '=' || grant_row.expiry_failures
  FROM purser.subscription_operator_grants grant_row
  JOIN purser.tenant_subscriptions subscription ON subscription.id = grant_row.subscription_id
  WHERE grant_row.expiry_error IS NOT NULL AND grant_row.expiry_failures > 1
  ORDER BY grant_row.expiry_failures DESC, subscription.tenant_id LIMIT 10;
SELECT 'grants_ending_within_7d=' || count(*) FROM purser.subscription_operator_grants
  WHERE expires_at > NOW() AND expires_at <= NOW() + INTERVAL '7 days';
SELECT 'grant_notices_failing=' || count(*) FROM purser.operator_grant_notices
  WHERE notified_at IS NULL AND error IS NOT NULL AND failures > 1 AND abandoned_at IS NULL;
SELECT 'grant_notice_tenant:' || tenant_id::text || '=' || max(failures) FROM purser.operator_grant_notices
  WHERE notified_at IS NULL AND error IS NOT NULL AND failures > 1 AND abandoned_at IS NULL
  GROUP BY tenant_id ORDER BY max(failures) DESC, tenant_id LIMIT 10;
SELECT 'grant_notices_abandoned=' || count(*) FROM purser.operator_grant_notices
  WHERE notified_at IS NULL AND abandoned_at IS NOT NULL;
SELECT 'grant_notice_abandoned_tenant:' || tenant_id::text || '=' || max(failures) FROM purser.operator_grant_notices
  WHERE notified_at IS NULL AND abandoned_at IS NOT NULL
  GROUP BY tenant_id ORDER BY max(failures) DESC, tenant_id LIMIT 10;
`, purserFreeFallbackWaitDays)

var purserBillingLifecycleDoctorKeys = []string{
	"free_fallback_moves_failing", "free_fallback_notices_failing", "free_fallback_notices_abandoned",
	"grant_expiries_failing", "grants_ending_within_7d", "grant_notices_failing", "grant_notices_abandoned",
}

// evaluateBillingLifecycleDoctor judges moves to Free, grant expiry and their
// tenant emails in the Purser state, adding to report.
func evaluateBillingLifecycleDoctor(state map[string]string, report *accountRecoveryDoctorReport) {
	if state["billing_lifecycle_installed"] == "0" {
		report.notes = append(report.notes, "purser: Free fallback and grant notice state is not installed")
		return
	}
	count := func(key string) int64 {
		n, err := strconv.ParseInt(state[key], 10, 64)
		if err != nil {
			return 0
		}
		return n
	}
	var absent []string
	for _, key := range purserBillingLifecycleDoctorKeys {
		if _, err := strconv.ParseInt(state[key], 10, 64); err != nil {
			absent = append(absent, key)
		}
	}
	if len(absent) > 0 {
		report.warnings = append(report.warnings, fmt.Sprintf("purser: could not be checked: %s", strings.Join(absent, ", ")))
	}

	recoveryList := false
	if n := count("free_fallback_moves_failing"); n > 0 {
		report.problems = append(report.problems, fmt.Sprintf("purser: %d move(s) to Free failing or waiting on settlement over %d days (%s)", n, purserFreeFallbackWaitDays, listedTenants(state, "free_fallback_move_tenant:", countUnit("failure"))))
		recoveryList = true
	}
	if n := count("free_fallback_notices_failing"); n > 0 {
		report.problems = append(report.problems, fmt.Sprintf("purser: %d grace or move-to-Free email(s) failing (%s)", n, listedTenants(state, "free_fallback_notice_tenant:", countUnit("failure"))))
		recoveryList = true
	}
	if n := count("free_fallback_notices_abandoned"); n > 0 {
		report.problems = append(report.problems, fmt.Sprintf("purser: %d grace or move-to-Free email(s) abandoned after five failures (%s)", n, listedTenants(state, "free_fallback_notice_abandoned_tenant:", countUnit("failure"))))
		report.steps = append(report.steps, ux.NextStep{Cmd: "frameworks admin billing recovery notice --tenant-id <tenant-id> --reason <reason>", Why: "Send an abandoned grace or move-to-Free email again once the tenant's billing email is fixed."})
	}
	if recoveryList {
		report.steps = append(report.steps, ux.NextStep{Cmd: "frameworks admin billing recovery list", Why: "Show each move to Free and its email with the last error."})
	}
	if n := count("grant_expiries_failing"); n > 0 {
		report.problems = append(report.problems, fmt.Sprintf("purser: %d operator grant expiry(ies) failing repeatedly (%s)", n, listedTenants(state, "grant_expiry_tenant:", countUnit("failure"))))
		report.steps = append(report.steps, ux.NextStep{Cmd: "frameworks admin billing grant list --include-expired", Why: "Show each grant whose expiry keeps failing with its error; Purser retries every five minutes."})
	}
	if n := count("grant_notices_failing"); n > 0 {
		report.problems = append(report.problems, fmt.Sprintf("purser: %d operator grant ending or ended email(s) failing (%s)", n, listedTenants(state, "grant_notice_tenant:", countUnit("failure"))))
		report.steps = append(report.steps, ux.NextStep{Cmd: "frameworks cluster logs purser", Why: "Find the mail error of the operator grant notice; fix the tenant's billing email or the mail service."})
	}
	if n := count("grant_notices_abandoned"); n > 0 {
		report.problems = append(report.problems, fmt.Sprintf("purser: %d operator grant ending or ended email(s) abandoned after five failures (%s)", n, listedTenants(state, "grant_notice_abandoned_tenant:", countUnit("failure"))))
		report.steps = append(report.steps, ux.NextStep{Cmd: "frameworks admin billing grant notice --tenant-id <tenant-id> --reason <reason>", Why: "Send an abandoned grant ending or ended email again once the tenant's billing email is fixed."})
	}
	if n := count("grants_ending_within_7d"); n > 0 {
		report.notes = append(report.notes, fmt.Sprintf("purser: %d operator grant(s) end within 7 days", n))
	}
}
