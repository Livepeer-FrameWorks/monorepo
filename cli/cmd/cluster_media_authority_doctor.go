package cmd

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"frameworks/cli/pkg/health"
	"frameworks/cli/pkg/ssh"
)

const (
	// A due refresh obligation or an unacknowledged current delivery older than
	// this means source changes, including revocations, are not reaching cells.
	mediaAuthorityDoctorStuckSeconds = 300
)

// Every statement yields "key=value" lines so one remote round trip answers the
// whole check. to_regclass keeps the probe meaningful on a cluster whose
// Commodore schema predates refresh obligations.
const commodoreMediaAuthorityDoctorSQL = `SET default_transaction_read_only = on;
SET statement_timeout = '20s';
SELECT 'obligations_table=' || (to_regclass('commodore.media_authority_refresh_obligations') IS NOT NULL)::int;
SELECT 'parked=' || count(*) FROM commodore.media_authority_refresh_obligations WHERE status = 'parked';
SELECT 'refresh_oldest_due_seconds=' || COALESCE(max(EXTRACT(EPOCH FROM (NOW() - GREATEST(pending_since, next_attempt_at))))::bigint, 0)
  FROM commodore.media_authority_refresh_obligations
  WHERE status IN ('pending', 'processing') AND next_attempt_at <= NOW() AND lane <> 'bulk';
SELECT 'bulk_oldest_due_seconds=' || COALESCE(max(EXTRACT(EPOCH FROM (NOW() - GREATEST(pending_since, next_attempt_at))))::bigint, 0)
  FROM commodore.media_authority_refresh_obligations
  WHERE status IN ('pending', 'processing') AND next_attempt_at <= NOW() AND lane = 'bulk';
SELECT 'expired_warm=' || (
  (SELECT count(*) FROM commodore.media_authority_refresh_obligations AS obligation
    WHERE obligation.lane IN ('object_deadline', 'tenant_deadline')
      AND obligation.status IN ('pending', 'processing', 'parked') AND obligation.expires_at < NOW())
  + (SELECT count(*) FROM commodore.media_authority_use AS used
    JOIN commodore.media_authority_refresh_obligations AS obligation
      ON obligation.target_key = used.authority_kind || ':' || used.authority_id
     AND obligation.lane IN ('object_deadline', 'tenant_deadline')
    WHERE used.last_used_at > NOW() - INTERVAL '30 days'
      AND obligation.status = 'dormant' AND obligation.expires_at < NOW()));
SELECT 'renewal_missing=' || count(*) FROM commodore.media_authority_current AS current
  JOIN commodore.media_authority_versions AS versions
    ON versions.authority_kind = current.authority_kind AND versions.authority_id = current.authority_id
   AND versions.authority_version = current.authority_version
  WHERE versions.valid_until > NOW()
    AND NOT versions.tombstone
    AND NOT EXISTS (
      SELECT 1 FROM commodore.media_authority_refresh_obligations AS obligation
      WHERE obligation.lane IN ('object_deadline', 'tenant_deadline')
        AND obligation.target_key = current.authority_kind || ':' || current.authority_id
        AND obligation.status IN ('pending', 'processing', 'parked', 'dormant'));
SELECT 'legacy_inbox_unfinished=' || count(*) FROM commodore.media_authority_refresh_inbox WHERE status <> 'completed';
SELECT 'delivery_stuck=' || count(*) FROM commodore.media_authority_deliveries AS delivery
  JOIN commodore.media_authority_current AS current
    ON current.authority_kind = delivery.authority_kind AND current.authority_id = delivery.authority_id
   AND current.authority_version = delivery.authority_version
  WHERE delivery.status IN ('pending', 'delivering')
    AND delivery.created_at < NOW() - INTERVAL '300 seconds';
SELECT 'delivery_rejected=' || count(*) FROM commodore.media_authority_actionable_rejections;
SELECT 'authorities=' || count(*) FROM commodore.media_authority_current;
SELECT 'versions_last_hour=' || count(*) FROM commodore.media_authority_versions WHERE issued_at > NOW() - INTERVAL '1 hour';
SELECT 'invalid_indexes=' || count(*) FROM pg_index AS i
  JOIN pg_class AS tbl ON tbl.oid = i.indrelid
  JOIN pg_namespace AS ns ON ns.oid = tbl.relnamespace
  WHERE ns.nspname = 'commodore' AND tbl.relname LIKE 'media_authority%' AND NOT (i.indisvalid AND i.indisready);
`

// A cell holding an authority past its validity is not by itself a problem: an
// object nobody uses stops being renewed, its copy runs out, and the cell
// forgets it once no older signed version can still verify (thirty days after
// it was issued). Whether an authority IN USE expired is known to Commodore, and
// is checked there. What is checked here is that forgetting keeps up. A
// tombstone is terminal, is never forgotten, and is not counted.
const foghornMediaAuthorityDoctorSQL = `SET default_transaction_read_only = on;
SET statement_timeout = '20s';
SELECT 'collection_overdue=' || count(*) FROM foghorn.media_authorities AS held
  LEFT JOIN foghorn.tenant_authority_projection AS tenant
    ON held.authority_kind = 'tenant' AND tenant.tenant_id::text = held.authority_id
  LEFT JOIN foghorn.media_object_authority_projection AS object
    ON held.authority_kind = 'media_object' AND object.authority_id = held.authority_id
  WHERE held.valid_until < NOW() - INTERVAL '6 hours'
    AND held.issued_at < NOW() - INTERVAL '30 days 7 hours'
    AND COALESCE(tenant.lifecycle, object.lifecycle, '') <> 'tombstone';
SELECT 'rejections_last_hour=' || count(*) FROM foghorn.media_authority_apply_audit
  WHERE observed_at > NOW() - INTERVAL '1 hour' AND outcome NOT IN ('applied', 'duplicate');
`

// doctorMediaAuthorityConvergence reports whether signed media authority is
// converging: nothing parked, no refresh or delivery stuck, no cell refusing the
// current version, no authority in use past its validity.
func doctorMediaAuthorityConvergence(ctx context.Context, rc *resolvedCluster, pool *ssh.Pool, sshKey string) *health.CheckResult {
	result := &health.CheckResult{
		Name:      "media_authority_convergence",
		CheckedAt: time.Now(),
		Metadata:  map[string]string{"check_kind": "queue_state"},
	}
	targets, err := postgresSnapshotTargets(rc)
	if err != nil {
		result.Status, result.Error = "degraded", fmt.Sprintf("resolve database targets: %v", err)
		return result
	}
	// Cells keep their Foghorn database on their own cluster, so every target
	// that holds an authority database is probed, not only the first.
	values := map[string]map[string]int64{}
	var expected []string
	var unread []string
	for i := range targets {
		target := targets[i]
		var probes []mediaAuthorityDatabaseProbe
		for _, database := range target.Databases {
			switch {
			case database == "commodore":
				probes = append(probes, mediaAuthorityDatabaseProbe{Database: database, SQL: commodoreMediaAuthorityDoctorSQL})
			case database == "foghorn" || strings.HasPrefix(database, "foghorn_"):
				probes = append(probes, mediaAuthorityDatabaseProbe{Database: database, SQL: foghornMediaAuthorityDoctorSQL})
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
		if output != nil {
			for database, state := range parseMediaAuthorityDoctorOutput(output.Stdout) {
				values[database] = state
			}
		}
		if runErr != nil || output == nil || output.ExitCode != 0 {
			unread = append(unread, fmt.Sprintf("read media authority state on %s: %s", target.HostName, diagnosticCommandError(output, runErr)))
			continue
		}
	}
	if len(expected) == 0 {
		result.OK, result.Status, result.Message = true, "healthy", "no Commodore or Foghorn database in manifest"
		return result
	}
	problems, warnings := evaluateMediaAuthorityDoctor(expected, values)
	warnings = append(warnings, unread...)
	switch {
	case len(problems) > 0:
		result.Status, result.Error = "unhealthy", strings.Join(append(problems, warnings...), "; ")
	case len(warnings) > 0:
		result.Status, result.Error = "degraded", strings.Join(warnings, "; ")
	default:
		result.OK, result.Status, result.Message = true, "healthy", "refresh, renewal, and delivery are converging"
	}
	return result
}

// parseMediaAuthorityDoctorOutput returns database -> key -> value from the
// "######## database <name>" sections of the probe output.
func parseMediaAuthorityDoctorOutput(stdout string) map[string]map[string]int64 {
	values := map[string]map[string]int64{}
	database := ""
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if name, ok := strings.CutPrefix(line, "######## database "); ok {
			database = strings.TrimSpace(name)
			values[database] = map[string]int64{}
			continue
		}
		key, raw, ok := strings.Cut(line, "=")
		if !ok || database == "" {
			continue
		}
		if value, parseErr := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); parseErr == nil {
			values[database][strings.TrimSpace(key)] = value
		}
	}
	return values
}

// Keys every probe must report. A statement that fails prints nothing, and an
// absent count must read as "could not be checked", never as zero.
var (
	commodoreMediaAuthorityDoctorKeys = []string{
		"parked", "refresh_oldest_due_seconds", "bulk_oldest_due_seconds", "expired_warm", "renewal_missing",
		"delivery_stuck", "delivery_rejected", "authorities", "versions_last_hour", "invalid_indexes",
	}
	foghornMediaAuthorityDoctorKeys = []string{"collection_overdue", "rejections_last_hour"}
)

func missingMediaAuthorityDoctorKeys(state map[string]int64, keys []string) []string {
	var missing []string
	for _, key := range keys {
		if _, ok := state[key]; !ok {
			missing = append(missing, key)
		}
	}
	return missing
}

// evaluateMediaAuthorityDoctor judges the probed state of every expected
// database. A database or a count that could not be read is a warning.
func evaluateMediaAuthorityDoctor(expected []string, values map[string]map[string]int64) (problems, warnings []string) {
	databases := append([]string(nil), expected...)
	sort.Strings(databases)
	for _, database := range databases {
		state, read := values[database]
		if !read {
			warnings = append(warnings, fmt.Sprintf("%s: media authority state could not be read", database))
			continue
		}
		if database == "commodore" {
			if state["obligations_table"] == 0 {
				warnings = append(warnings, "commodore: refresh obligations are not installed; refresh state cannot be judged")
			} else if missing := missingMediaAuthorityDoctorKeys(state, commodoreMediaAuthorityDoctorKeys); len(missing) > 0 {
				warnings = append(warnings, fmt.Sprintf("commodore: could not be checked: %s", strings.Join(missing, ", ")))
			}
			if n := state["parked"]; n > 0 {
				problems = append(problems, fmt.Sprintf("commodore: %d refresh target(s) parked (cannot compile)", n))
			}
			if age := state["refresh_oldest_due_seconds"]; age > mediaAuthorityDoctorStuckSeconds {
				problems = append(problems, fmt.Sprintf("commodore: oldest due refresh has waited %ds", age))
			}
			if n := state["expired_warm"]; n > 0 {
				problems = append(problems, fmt.Sprintf("commodore: %d authority(ies) in use are past their validity (cells fetch them on every decision and refuse them if Commodore is unreachable)", n))
			}
			// Fan-out and reconciliation never stand in front of a change, so a
			// backlog there is slow, not unsafe.
			if age := state["bulk_oldest_due_seconds"]; age > 3600 {
				warnings = append(warnings, fmt.Sprintf("commodore: oldest due bulk refresh has waited %ds (a tenant change is not reaching its objects)", age))
			}
			if n := state["delivery_stuck"]; n > 0 {
				problems = append(problems, fmt.Sprintf("commodore: %d current delivery(ies) unacknowledged for over %ds", n, mediaAuthorityDoctorStuckSeconds))
			}
			if n := state["delivery_rejected"]; n > 0 {
				problems = append(problems, fmt.Sprintf("commodore: %d current delivery(ies) refused by their cell", n))
			}
			if n := state["invalid_indexes"]; n > 0 {
				problems = append(problems, fmt.Sprintf("commodore: %d invalid media-authority index(es)", n))
			}
			if n := state["renewal_missing"]; n > 0 && state["obligations_table"] == 1 {
				warnings = append(warnings, fmt.Sprintf("commodore: %d current authority(ies) have no renewal obligation yet", n))
			}
			// Renewal publishes about one version per authority every eight hours.
			// More than two per authority in one hour means versions are being
			// published without anything having changed.
			if versions, authorities := state["versions_last_hour"], state["authorities"]; authorities > 0 && versions > 2*authorities {
				warnings = append(warnings, fmt.Sprintf("commodore: %d versions published in the last hour for %d authorities (expected well under one each)", versions, authorities))
			}
			if n := state["legacy_inbox_unfinished"]; n > 0 {
				warnings = append(warnings, fmt.Sprintf("commodore: %d unfinished legacy refresh inbox row(s) awaiting adoption", n))
			}
			continue
		}
		if missing := missingMediaAuthorityDoctorKeys(state, foghornMediaAuthorityDoctorKeys); len(missing) > 0 {
			warnings = append(warnings, fmt.Sprintf("%s: could not be checked: %s", database, strings.Join(missing, ", ")))
		}
		if n := state["collection_overdue"]; n > 0 {
			warnings = append(warnings, fmt.Sprintf("%s: %d expired authority(ies) should have been forgotten by now (the cell's collection is not keeping up)", database, n))
		}
		if n := state["rejections_last_hour"]; n > 0 {
			warnings = append(warnings, fmt.Sprintf("%s: %d signed authority apply rejection(s) in the last hour", database, n))
		}
	}
	return problems, warnings
}
