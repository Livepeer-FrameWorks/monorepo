package cmd

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/health"
	"frameworks/cli/pkg/ssh"
)

// accountRecoveryDoctorListed bounds the tenants named per finding.
const accountRecoveryDoctorListed = 10

// The Commodore conditions are the inputs of CommodoreTenantAccountClosureStuck
// (a closure whose Quartermaster synchronization failed more than once) and
// CommodoreAccountIngestRevocationStuck (a session stop pending over 300s).
const commodoreAccountRecoveryDoctorSQL = `SET default_transaction_read_only = on;
SET statement_timeout = '20s';
SELECT 'account_states_table=' || (to_regclass('commodore.tenant_account_states') IS NOT NULL)::int;
SELECT 'closures_unsynced=' || count(*) FROM commodore.tenant_account_states
  WHERE closed_at IS NOT NULL AND quartermaster_closed_at IS NULL;
SELECT 'closures_failing=' || count(*) FROM commodore.tenant_account_states
  WHERE quartermaster_closed_at IS NULL AND closure_error IS NOT NULL AND closure_failures > 1;
SELECT 'closure_failing_tenant:' || tenant_id::text || '=' || closure_failures FROM commodore.tenant_account_states
  WHERE quartermaster_closed_at IS NULL AND closure_error IS NOT NULL AND closure_failures > 1
  ORDER BY closure_failures DESC, tenant_id LIMIT 10;
SELECT 'ingest_stops_pending=' || count(*) FROM commodore.media_authority_refresh_obligations
  WHERE lane = 'ingest_revocation' AND status IN ('pending', 'processing');
SELECT 'ingest_stops_oldest_seconds=' || COALESCE(max(EXTRACT(EPOCH FROM (NOW() - pending_since)))::bigint, 0)
  FROM commodore.media_authority_refresh_obligations
  WHERE lane = 'ingest_revocation' AND status IN ('pending', 'processing');
SELECT 'ingest_stop_tenant:' || tenant_id::text || '=' || count(*) FROM commodore.media_authority_refresh_obligations
  WHERE lane = 'ingest_revocation' AND status IN ('pending', 'processing') AND pending_since < NOW() - INTERVAL '300 seconds'
  GROUP BY tenant_id ORDER BY count(*) DESC, tenant_id LIMIT 10;
`

// The Purser rules the doctor mirrors. TestAccountRecoveryDoctorMatchesServiceRules
// ties each value to the service source it copies.
const (
	// Purser's HeldDowngradeGrace.
	purserHeldDowngradeGraceHours = 26
	// The unresolved age in Purser's ProviderWebhookInboxRepeatedFailures.
	purserRecoveryUnresolvedDays = 14
	// Purser's suspensionThresholdCents: a prepaid balance below it suspends.
	purserSuspensionThresholdCents = -1000
)

// The Purser conditions are the inputs of PurserTierDowngradeHeld (Purser's
// CountHeldConfirmedDowngrades with its HeldDowngradeGrace) and
// PurserProviderWebhookInboxStuck (ProviderWebhookInboxRepeatedFailures).
var purserAccountRecoveryDoctorSQL = fmt.Sprintf(`SET default_transaction_read_only = on;
SET statement_timeout = '20s';
SELECT 'held_downgrades=' || count(*) FROM purser.tenant_subscriptions
  WHERE pending_tier_id IS NOT NULL AND pending_reason = 'downgrade'
    AND pending_provider_tier_id = pending_tier_id AND pending_effective_at < NOW() - INTERVAL '%[1]d hours';
SELECT 'held_downgrade_tenant:' || tenant_id::text || '=' || EXTRACT(EPOCH FROM (NOW() - pending_effective_at))::bigint
  FROM purser.tenant_subscriptions
  WHERE pending_tier_id IS NOT NULL AND pending_reason = 'downgrade'
    AND pending_provider_tier_id = pending_tier_id AND pending_effective_at < NOW() - INTERVAL '%[1]d hours'
  ORDER BY pending_effective_at, tenant_id LIMIT 10;
SELECT 'recovery_open=' || count(*) FROM purser.provider_webhook_inbox WHERE status <> 'processed';
WITH stuck AS (
  SELECT id, headers FROM purser.provider_webhook_inbox
  WHERE status IN ('pending', 'failed', 'processing') AND attempts > 1
  UNION
  SELECT id, headers FROM purser.provider_webhook_inbox
  WHERE status IN ('pending', 'failed', 'processing') AND unresolved_since < NOW() - INTERVAL '%[2]d days'
)
SELECT 'recovery_stuck=' || count(*) FROM stuck;
WITH stuck AS (
  SELECT id, headers FROM purser.provider_webhook_inbox
  WHERE status IN ('pending', 'failed', 'processing') AND attempts > 1
  UNION
  SELECT id, headers FROM purser.provider_webhook_inbox
  WHERE status IN ('pending', 'failed', 'processing') AND unresolved_since < NOW() - INTERVAL '%[2]d days'
)
SELECT 'recovery_stuck_tenant:' || tenant || '=' || count(*)
  FROM (SELECT COALESCE(NULLIF(headers->>'tenant_id', ''), 'unbound') AS tenant FROM stuck) stuck_tenant
  GROUP BY tenant ORDER BY count(*) DESC, tenant LIMIT 10;
`, purserHeldDowngradeGraceHours, purserRecoveryUnresolvedDays) + purserSpendHoldDoctorSQL + purserBillingLifecycleDoctorSQL

// purserSpendHoldDoctorSQL reports the tenants whose signed authority refuses
// new sessions for a spent balance: Commodore's prepaid_balance_unavailable
// decision is a prepaid current subscription, not suspended or closed, whose
// EUR balance (absent counts as zero) is at or below zero. Below the
// suspension threshold Purser suspends the tenant and running sessions stop,
// so a held tenant past it is a failed enforcement. Both listings read the
// same held set as the counts.
var purserSpendHoldDoctorSQL = fmt.Sprintf(`WITH current_subscription AS (
  SELECT DISTINCT ON (tenant_id) tenant_id, billing_model, status
  FROM purser.tenant_subscriptions WHERE status <> 'cancelled'
  ORDER BY tenant_id, created_at DESC
), held AS (
  SELECT current_subscription.tenant_id, COALESCE(balance.balance_cents, 0) AS balance_cents
  FROM current_subscription
  LEFT JOIN purser.prepaid_balances balance
    ON balance.tenant_id = current_subscription.tenant_id AND balance.currency = 'EUR'
  WHERE current_subscription.billing_model = 'prepaid' AND current_subscription.status <> 'suspended'
    AND NOT EXISTS (SELECT 1 FROM purser.tenant_billing_closures closure WHERE closure.tenant_id = current_subscription.tenant_id)
    AND COALESCE(balance.balance_cents, 0) <= 0
), listed AS (
  (SELECT 'spend_held_tenant:' AS prefix, tenant_id, balance_cents FROM held
    ORDER BY balance_cents, tenant_id LIMIT 10)
  UNION ALL
  (SELECT 'spend_below_limit_tenant:', tenant_id, balance_cents FROM held WHERE balance_cents < %[1]d
    ORDER BY balance_cents, tenant_id LIMIT 10)
)
SELECT 'spend_held=' || (SELECT count(*) FROM held)
UNION ALL SELECT 'spend_held_below_limit=' || (SELECT count(*) FROM held WHERE balance_cents < %[1]d)
UNION ALL SELECT prefix || tenant_id::text || '=' || -balance_cents FROM listed;
`, purserSuspensionThresholdCents)

var (
	commodoreAccountRecoveryDoctorKeys = []string{"closures_unsynced", "closures_failing", "ingest_stops_pending", "ingest_stops_oldest_seconds"}
	purserAccountRecoveryDoctorKeys    = []string{"held_downgrades", "recovery_open", "recovery_stuck", "spend_held", "spend_held_below_limit"}
)

// doctorAccountRecovery reports the account and billing recovery work that
// needs an operator: failed tenant closures, session stops a cell has not
// confirmed, held downgrades, stuck billing recovery items and spent prepaid
// tenants that crossed the suspension threshold without being suspended.
func doctorAccountRecovery(ctx context.Context, rc *resolvedCluster, pool *ssh.Pool, sshKey string) doctorOutcome {
	result := &health.CheckResult{Name: "account_recovery", CheckedAt: time.Now(), Metadata: map[string]string{"check_kind": "queue_state"}}
	targets, err := postgresSnapshotTargets(rc)
	if err != nil {
		result.Status, result.Error = "degraded", fmt.Sprintf("resolve database targets: %v", err)
		return doctorOutcome{result: result}
	}
	values := map[string]map[string]string{}
	var expected, unread []string
	for i := range targets {
		target := targets[i]
		var probes []mediaAuthorityDatabaseProbe
		for _, database := range target.Databases {
			if _, seen := values[database]; seen {
				continue
			}
			switch database {
			case "commodore":
				probes = append(probes, mediaAuthorityDatabaseProbe{Database: database, SQL: commodoreAccountRecoveryDoctorSQL})
			case "purser":
				probes = append(probes, mediaAuthorityDatabaseProbe{Database: database, SQL: purserAccountRecoveryDoctorSQL})
			}
		}
		if len(probes) == 0 {
			continue
		}
		for _, probe := range probes {
			expected = append(expected, probe.Database)
		}
		runner, runnerErr := snapshotRunner(pool, sshKey, target.Host, 60*time.Second)
		if runnerErr != nil {
			unread = append(unread, fmt.Sprintf("connect to %s: %v", target.HostName, runnerErr))
			continue
		}
		output, runErr := runner.RunScript(ctx, mediaAuthorityDatabaseScript(target, probes))
		sections := 0
		if output != nil {
			for database, state := range parseAccountRecoveryDoctorOutput(output.Stdout) {
				values[database] = state
				sections++
			}
		}
		// A probe that stopped part way still prints its section; its absent
		// counts are judged per key, so a failed script is reported here only
		// when some database printed no section.
		if (runErr != nil || output == nil || output.ExitCode != 0) && sections < len(probes) {
			unread = append(unread, fmt.Sprintf("read recovery state on %s: %s", target.HostName, diagnosticCommandError(output, runErr)))
		}
	}
	if len(expected) == 0 {
		result.OK, result.Status, result.Message = true, "healthy", "no Commodore or Purser database in manifest"
		return doctorOutcome{result: result, passed: true}
	}
	report := evaluateAccountRecoveryDoctor(expected, values)
	report.warnings = append(report.warnings, unread...)
	switch {
	case len(report.problems) > 0:
		result.Status, result.Error = "unhealthy", strings.Join(append(report.problems, report.warnings...), "; ")
	case len(report.warnings) > 0:
		result.Status, result.Error = "degraded", strings.Join(report.warnings, "; ")
	default:
		result.OK, result.Status, result.Message = true, "healthy", strings.Join(report.notes, "; ")
	}
	return doctorOutcome{result: result, passed: result.OK, steps: report.steps}
}

// parseAccountRecoveryDoctorOutput returns database -> key -> raw value from
// the "######## database <name>" sections of the probe output.
func parseAccountRecoveryDoctorOutput(stdout string) map[string]map[string]string {
	values := map[string]map[string]string{}
	database := ""
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if name, ok := strings.CutPrefix(line, "######## database "); ok {
			database = strings.TrimSpace(name)
			values[database] = map[string]string{}
			continue
		}
		key, raw, ok := strings.Cut(line, "=")
		if !ok || database == "" {
			continue
		}
		values[database][strings.TrimSpace(key)] = strings.TrimSpace(raw)
	}
	return values
}

type accountRecoveryDoctorReport struct {
	problems, warnings, notes []string
	steps                     []ux.NextStep
}

// evaluateAccountRecoveryDoctor judges the probed state. A database or count
// that could not be read is a warning, never a zero.
func evaluateAccountRecoveryDoctor(expected []string, values map[string]map[string]string) accountRecoveryDoctorReport {
	var report accountRecoveryDoctorReport
	databases := append([]string(nil), expected...)
	sort.Strings(databases)
	for _, database := range databases {
		state, read := values[database]
		if !read {
			report.warnings = append(report.warnings, fmt.Sprintf("%s: recovery state could not be read", database))
			continue
		}
		count := func(key string) int64 {
			n, err := strconv.ParseInt(state[key], 10, 64)
			if err != nil {
				return 0
			}
			return n
		}
		missing := func(keys []string) {
			var absent []string
			for _, key := range keys {
				if _, err := strconv.ParseInt(state[key], 10, 64); err != nil {
					absent = append(absent, key)
				}
			}
			if len(absent) > 0 {
				report.warnings = append(report.warnings, fmt.Sprintf("%s: could not be checked: %s", database, strings.Join(absent, ", ")))
			}
		}
		switch database {
		case "commodore":
			if state["account_states_table"] == "0" {
				report.notes = append(report.notes, "commodore: account lifecycle state is not installed")
				continue
			}
			missing(commodoreAccountRecoveryDoctorKeys)
			if n := count("closures_failing"); n > 0 {
				report.problems = append(report.problems, fmt.Sprintf("commodore: %d tenant closure(s) failing Quartermaster synchronization (%s)", n, listedTenants(state, "closure_failing_tenant:", countUnit("failure"))))
				report.steps = append(report.steps, ux.NextStep{Cmd: "frameworks admin tenants get <tenant-id>", Why: "Show the closure's last synchronization error and next attempt."})
			} else if n := count("closures_unsynced"); n > 0 {
				report.notes = append(report.notes, fmt.Sprintf("commodore: %d closure(s) synchronizing", n))
			}
			if age := count("ingest_stops_oldest_seconds"); age > mediaAuthorityDoctorStuckSeconds {
				report.problems = append(report.problems, fmt.Sprintf("commodore: %d session stop(s) pending, oldest %s (%s)", count("ingest_stops_pending"), doctorAge(age), listedTenants(state, "ingest_stop_tenant:", countUnit("stop"))))
				report.steps = append(report.steps,
					ux.NextStep{Cmd: "frameworks admin tenants get <tenant-id>", Why: "List each pending stop with the cells that have not confirmed it."},
					ux.NextStep{Cmd: "frameworks admin clusters retire <cluster-id> --reason <reason>", Why: "Settle stops owed to a cell that was removed for good; restore any other cell."})
			}
		case "purser":
			missing(purserAccountRecoveryDoctorKeys)
			if n := count("held_downgrades"); n > 0 {
				report.problems = append(report.problems, fmt.Sprintf("purser: %d confirmed downgrade(s) held past their effective time (%s)", n, listedTenants(state, "held_downgrade_tenant:", func(seconds int64) string { return doctorAge(seconds) + " past effective" })))
			}
			if n := count("recovery_stuck"); n > 0 {
				report.problems = append(report.problems, fmt.Sprintf("purser: %d billing recovery item(s) failing repeatedly or unresolved over %d days (%s)", n, purserRecoveryUnresolvedDays, listedTenants(state, "recovery_stuck_tenant:", countUnit("item"))))
			}
			if n := count("spend_held_below_limit"); n > 0 {
				report.problems = append(report.problems, fmt.Sprintf("purser: %d prepaid tenant(s) below the -%s suspension threshold but not suspended; their running sessions continue (%s)", n, wholeEuros(-purserSuspensionThresholdCents), listedTenants(state, "spend_below_limit_tenant:", owed)))
				report.steps = append(report.steps, ux.NextStep{Cmd: "frameworks cluster logs purser", Why: "Find why threshold enforcement did not suspend the tenant."})
			}
			if n := count("spend_held"); n > 0 {
				report.notes = append(report.notes, fmt.Sprintf("purser: %d prepaid tenant(s) at or below €0; running sessions continue, new ones are refused (%s)", n, listedTenants(state, "spend_held_tenant:", owed)))
			}
			if count("held_downgrades") > 0 || count("recovery_stuck") > 0 {
				report.steps = append(report.steps, ux.NextStep{Cmd: "frameworks admin billing recovery list", Why: "Inspect held downgrades and recovery items with their errors and resolve them."})
			} else if n := count("recovery_open"); n > 0 {
				report.notes = append(report.notes, fmt.Sprintf("purser: %d billing recovery item(s) waiting normally", n))
			}
			evaluateBillingLifecycleDoctor(state, &report)
		}
	}
	if len(report.notes) == 0 {
		report.notes = append(report.notes, "no account or billing recovery work needs an operator")
	}
	return report
}

// listedTenants renders "<id>: <value>" for up to accountRecoveryDoctorListed
// keys with prefix, largest first.
func listedTenants(state map[string]string, prefix string, render func(int64) string) string {
	type entry struct {
		id string
		n  int64
	}
	var entries []entry
	for key, raw := range state {
		id, ok := strings.CutPrefix(key, prefix)
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			continue
		}
		entries = append(entries, entry{id: id, n: n})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].n != entries[j].n {
			return entries[i].n > entries[j].n
		}
		return entries[i].id < entries[j].id
	})
	if len(entries) > accountRecoveryDoctorListed {
		entries = entries[:accountRecoveryDoctorListed]
	}
	parts := make([]string, 0, len(entries))
	for _, e := range entries {
		parts = append(parts, e.id+": "+render(e.n))
	}
	if len(parts) == 0 {
		return "tenants not listed"
	}
	return strings.Join(parts, ", ")
}

// countUnit renders "1 <noun>" or "<n> <noun>s".
func countUnit(noun string) func(int64) string {
	return func(n int64) string {
		if n == 1 {
			return "1 " + noun
		}
		return fmt.Sprintf("%d %ss", n, noun)
	}
}

// doctorAge renders whole seconds as a duration, in hours and minutes from
// one hour up (26h0m).
func doctorAge(seconds int64) string {
	age := time.Duration(seconds) * time.Second
	if age < time.Hour {
		return age.String()
	}
	return fmt.Sprintf("%dh%dm", int64(age/time.Hour), int64(age%time.Hour/time.Minute))
}

// owed renders a non-negative amount owed in cents as euros.
func owed(cents int64) string {
	return fmt.Sprintf("€%d.%02d owed", cents/100, cents%100)
}

// wholeEuros renders a non-negative whole-euro amount given in cents.
func wholeEuros(cents int64) string {
	return fmt.Sprintf("€%d", cents/100)
}
