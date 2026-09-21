package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
)

const (
	mediaAuthorityDiagnosticMaxWindowHours = 30 * 24
	mediaAuthorityDiagnosticWindowToken    = "{{WINDOW_HOURS}}"
)

// mediaAuthorityDiagnosticOrder follows the authority data flow: source
// outboxes, then the Commodore compiler and delivery queue, then the cells that
// apply envelopes.
var mediaAuthorityDiagnosticOrder = map[string]int{
	"quartermaster": 0,
	"purser":        1,
	"commodore":     2,
	"foghorn":       3,
}

type mediaAuthorityDiagnosticTarget struct {
	Service string
	Deploy  string
	Host    string
}

// mediaAuthorityDiagnosticTargets picks one host per manifest service entry.
// Replicas of one entry share a database, so probing each of them would only
// repeat the same rows; separate entries (foghorn-eu, foghorn-us) are separate
// cell databases and are each probed.
func mediaAuthorityDiagnosticTargets(manifest *inventory.Manifest) []mediaAuthorityDiagnosticTarget {
	if manifest == nil {
		return nil
	}
	var targets []mediaAuthorityDiagnosticTarget
	for name, svc := range manifest.Services {
		if !svc.Enabled {
			continue
		}
		deploy := strings.TrimSpace(svc.Deploy)
		if deploy == "" {
			deploy = name
		}
		if _, ok := mediaAuthorityDiagnosticOrder[deploy]; !ok {
			continue
		}
		hosts := append([]string(nil), serviceHosts(svc)...)
		sort.Strings(hosts)
		for _, host := range hosts {
			if host = strings.TrimSpace(host); host != "" {
				targets = append(targets, mediaAuthorityDiagnosticTarget{Service: name, Deploy: deploy, Host: host})
				break
			}
		}
	}
	sort.Slice(targets, func(i, j int) bool {
		if oi, oj := mediaAuthorityDiagnosticOrder[targets[i].Deploy], mediaAuthorityDiagnosticOrder[targets[j].Deploy]; oi != oj {
			return oi < oj
		}
		return targets[i].Service < targets[j].Service
	})
	return targets
}

// mediaAuthorityDiagnosticProbe is one remote probe: a service journal summary
// or the SQL run against the databases of one Postgres target.
type mediaAuthorityDiagnosticProbe struct {
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Host     string `json:"host"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
	ExitCode int    `json:"exit_code"`
	Error    string `json:"error,omitempty"`
}

type mediaAuthorityDiagnosticReport struct {
	Since       string                          `json:"since"`
	WindowHours int                             `json:"window_hours"`
	Probes      []mediaAuthorityDiagnosticProbe `json:"probes"`
}

func diagnoseMediaAuthority(ctx context.Context, cmd *cobra.Command, rc *resolvedCluster, pool *ssh.Pool, opts diagnoseOptions) error {
	if opts.WindowHours < 1 || opts.WindowHours > mediaAuthorityDiagnosticMaxWindowHours {
		return fmt.Errorf("--window-hours must be between 1 and %d", mediaAuthorityDiagnosticMaxWindowHours)
	}
	if rc == nil || rc.Manifest == nil {
		return fmt.Errorf("manifest is required")
	}
	manifest := rc.Manifest
	journalTargets := mediaAuthorityDiagnosticTargets(manifest)
	if len(journalTargets) == 0 {
		return fmt.Errorf("no media-authority service placements found in manifest")
	}

	out := cmd.OutOrStdout()
	if !opts.OutputJSON {
		fmt.Fprintln(out, "Media Authority Diagnostics")
		fmt.Fprintf(out, "Read-only probes of refresh outboxes, Commodore obligations/versions/deliveries, and cell apply audits (last %dh).\n", opts.WindowHours)
	}
	report := mediaAuthorityDiagnosticReport{Since: opts.Since, WindowHours: opts.WindowHours}
	record := func(probe mediaAuthorityDiagnosticProbe, result *ssh.CommandResult, err error) {
		probe.ExitCode = -1
		if result != nil {
			probe.Stdout, probe.Stderr, probe.ExitCode = strings.TrimSpace(result.Stdout), strings.TrimSpace(result.Stderr), result.ExitCode
		}
		switch {
		case err != nil:
			probe.Error = "diagnostic probe failed: " + diagnosticCommandError(result, err)
		case result == nil:
			probe.Error = "diagnostic probe returned no result"
		case result.ExitCode != 0:
			probe.Error = fmt.Sprintf("diagnostic probe exited %d", result.ExitCode)
		}
		report.Probes = append(report.Probes, probe)
		if opts.OutputJSON {
			return
		}
		fmt.Fprintf(out, "\n#### %s %s on %s\n", probe.Kind, probe.Name, probe.Host)
		if probe.Stdout != "" {
			fmt.Fprintln(out, probe.Stdout)
		}
		if probe.Stderr != "" {
			fmt.Fprintln(cmd.ErrOrStderr(), probe.Stderr)
		}
		if probe.Error != "" {
			ux.Fail(cmd.ErrOrStderr(), fmt.Sprintf("%s: %s", probe.Host, probe.Error))
		}
	}

	for _, target := range journalTargets {
		probe := mediaAuthorityDiagnosticProbe{Kind: "journal", Name: target.Service, Host: target.Host}
		host, ok := manifest.GetHost(target.Host)
		if !ok {
			record(probe, nil, fmt.Errorf("host missing from manifest"))
			continue
		}
		runner, err := getRunner(host, pool)
		if err != nil {
			record(probe, nil, fmt.Errorf("connect: %w", err))
			continue
		}
		result, err := runner.Run(ctx, "sh -lc "+ssh.ShellQuote(mediaAuthorityJournalScript(target.Deploy, opts)))
		record(probe, result, err)
	}

	// SQL runs on the database host with the cluster's database credential, the
	// same path `cluster snapshot` uses. Service hosts carry no SQL client, and
	// each Foghorn cell has its own database that only this path enumerates.
	sqlTargets, err := postgresSnapshotTargets(rc)
	if err != nil {
		return fmt.Errorf("resolve database targets: %w", err)
	}
	sshKey := stringFlag(cmd, "ssh-key").Value
	for _, target := range sqlTargets {
		probes := mediaAuthorityDatabaseProbes(target.Databases, opts.WindowHours)
		if len(probes) == 0 {
			continue
		}
		probe := mediaAuthorityDiagnosticProbe{Kind: "database", Name: target.Name, Host: target.HostName}
		runner, runnerErr := snapshotRunner(pool, sshKey, target.Host, 90*time.Second)
		if runnerErr != nil {
			record(probe, nil, fmt.Errorf("connect: %w", runnerErr))
			continue
		}
		result, runErr := runner.RunScript(ctx, mediaAuthorityDatabaseScript(target, probes))
		record(probe, result, runErr)
	}

	if opts.OutputJSON {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			return err
		}
	}
	failures := 0
	for _, probe := range report.Probes {
		if probe.Error != "" {
			failures++
		}
	}
	if failures > 0 {
		return fmt.Errorf("media-authority diagnostics failed on %d probe(s)", failures)
	}
	return nil
}

type mediaAuthorityDatabaseProbe struct {
	Database string
	SQL      string
}

// mediaAuthorityDatabaseProbes maps each database to the probe SQL of the
// service that owns it. Foghorn databases are per cell (foghorn_eu, foghorn_us).
func mediaAuthorityDatabaseProbes(databases []string, windowHours int) []mediaAuthorityDatabaseProbe {
	var probes []mediaAuthorityDatabaseProbe
	for _, database := range databases {
		owner := database
		if database == "foghorn" || strings.HasPrefix(database, "foghorn_") {
			owner = "foghorn"
		}
		sql := mediaAuthorityDiagnosticSQL(owner)
		if sql == "" {
			continue
		}
		probes = append(probes, mediaAuthorityDatabaseProbe{
			Database: database,
			SQL: "SET default_transaction_read_only = on;\nSET statement_timeout = '20s';\n" +
				strings.ReplaceAll(sql, mediaAuthorityDiagnosticWindowToken, strconv.Itoa(windowHours)),
		})
	}
	sort.SliceStable(probes, func(i, j int) bool {
		return mediaAuthorityDatabaseOrder(probes[i].Database) < mediaAuthorityDatabaseOrder(probes[j].Database)
	})
	return probes
}

func mediaAuthorityDatabaseOrder(database string) int {
	if strings.HasPrefix(database, "foghorn") {
		return mediaAuthorityDiagnosticOrder["foghorn"]
	}
	return mediaAuthorityDiagnosticOrder[database]
}

// mediaAuthorityJournalScript summarizes a service's authority log lines with
// timestamps, durations, and IDs normalized, so a line repeated a thousand
// times reads as one line with a count.
func mediaAuthorityJournalScript(deploy string, opts diagnoseOptions) string {
	since := strings.TrimSpace(opts.Since)
	if since == "" {
		since = "4 hours ago"
	}
	var b strings.Builder
	b.WriteString("set +e\n")
	b.WriteString("SINCE=" + ssh.ShellQuote(since) + "\n")
	b.WriteString("SVC=" + ssh.ShellQuote(deploy) + "\n")
	// A journal that cannot be read fails the probe; one with no authority lines
	// is an answer.
	b.WriteString(`echo "== frameworks-${SVC}: authority log lines since ${SINCE} (deduplicated) =="
if ! JOURNAL="$(journalctl -u "frameworks-${SVC}.service" --since "${SINCE}" --no-pager -o cat 2>&1)"; then
  echo "!! journal of frameworks-${SVC} could not be read: ${JOURNAL}"
  exit 2
fi
printf '%s\n' "$JOURNAL" \
  | grep -i 'authority' \
  | sed -E 's/"time":"[^"]+",?//g; s/,"time":"[^"]+"//g; s/"duration":[0-9]+,?//g' \
  | sed -E 's/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/<uuid>/g' \
  | sort | uniq -c | sort -rn | head -n 15 || true
`)
	return b.String()
}

// mediaAuthorityDatabaseScript runs every probe against its database on the
// Postgres target's own host. Each probe's SQL opens by forcing the session
// read-only, and is fed on stdin so its quoting never meets the shell.
func mediaAuthorityDatabaseScript(target postgresSnapshotTarget, probes []mediaAuthorityDatabaseProbe) string {
	peer := "0"
	if target.UsePeerAuth {
		peer = "1"
	}
	var b strings.Builder
	b.WriteString("set +e\n")
	fmt.Fprintf(&b, "PORT=%d\n", target.Port)
	b.WriteString("USER_NAME=" + ssh.ShellQuote(target.User) + "\n")
	b.WriteString("PASSWORD=" + ssh.ShellQuote(target.Password) + "\n")
	b.WriteString("BINARY=" + ssh.ShellQuote(target.Binary) + "\n")
	b.WriteString("PEER=" + peer + "\n")
	b.WriteString(`SQL_BIN=""
for candidate in "$BINARY" /home/yugabyte/tserver/bin/ysqlsh /opt/yugabyte/bin/ysqlsh /usr/local/bin/ysqlsh; do
  if command -v "$candidate" >/dev/null 2>&1; then
    SQL_BIN="$(command -v "$candidate")"
    break
  fi
done
if [ -z "$SQL_BIN" ]; then
  echo "$BINARY not found on the database host"
  exit 127
fi
run_probe() {
  db="$1"
  echo
  echo "######## database ${db}"
  if [ "$PEER" = "1" ] && command -v sudo >/dev/null 2>&1 && sudo -n true >/dev/null 2>&1; then
    sudo -u "$USER_NAME" "$SQL_BIN" -X -q -v ON_ERROR_STOP=1 -p "$PORT" -d "$db" -P pager=off -A -F ' | ' -f - 2>&1
  elif [ "$PEER" = "1" ]; then
    "$SQL_BIN" -X -q -v ON_ERROR_STOP=1 -p "$PORT" -d "$db" -P pager=off -A -F ' | ' -f - 2>&1
  else
    PGPASSWORD="$PASSWORD" "$SQL_BIN" -X -q -v ON_ERROR_STOP=1 -h 127.0.0.1 -p "$PORT" -U "$USER_NAME" -d "$db" -P pager=off -A -F ' | ' -f - 2>&1
  fi
}
FAILED=0
`)
	// Every database is probed even when one fails, and each failure is named in
	// the output; the script exits non-zero when any did, so the probe is
	// reported as failed. A statement error stops only its own probe.
	for _, probe := range probes {
		fmt.Fprintf(&b, "probe_db=%s\nprintf '%%s\\n' %s | run_probe \"$probe_db\"\nstatus=$?\nif [ \"$status\" -ne 0 ]; then echo \"!! probe of database ${probe_db} failed (exit $status)\"; FAILED=1; fi\n",
			ssh.ShellQuote(probe.Database), ssh.ShellQuote(probe.SQL))
	}
	b.WriteString("exit $FAILED\n")
	return b.String()
}

func mediaAuthorityDiagnosticSQL(deploy string) string {
	switch deploy {
	case "commodore":
		return commodoreMediaAuthorityDiagnosticSQL
	case "foghorn":
		return foghornMediaAuthorityDiagnosticSQL
	case "purser":
		return purserMediaAuthorityDiagnosticSQL
	case "quartermaster":
		return quartermasterMediaAuthorityDiagnosticSQL
	default:
		return ""
	}
}

const commodoreMediaAuthorityDiagnosticSQL = `\echo == authorities and retained versions by kind ==
SELECT authority_kind, count(DISTINCT authority_id) AS authorities, count(*) AS versions,
       min(issued_at) AS oldest_issued, max(issued_at) AS newest_issued
FROM commodore.media_authority_versions
GROUP BY 1 ORDER BY 1;

\echo == version issuance in window: identical re-signs and near-simultaneous successors ==
WITH chain AS (
    SELECT authority_kind, authority_id, issued_at, payload_sha256,
           lag(payload_sha256) OVER w AS previous_sha,
           lag(issued_at) OVER w AS previous_issued
    FROM commodore.media_authority_versions
    WHERE issued_at > NOW() - INTERVAL '{{WINDOW_HOURS}} hours'
    WINDOW w AS (PARTITION BY authority_kind, authority_id ORDER BY authority_version)
)
SELECT authority_kind, count(*) AS versions, count(DISTINCT authority_id) AS authorities,
       count(*) FILTER (WHERE previous_sha = payload_sha256) AS identical_payload_resigns,
       count(*) FILTER (WHERE issued_at - previous_issued < INTERVAL '60 seconds') AS within_60s_of_predecessor,
       round(count(*)::numeric / GREATEST(count(DISTINCT authority_id), 1) / {{WINDOW_HOURS}}, 2) AS versions_per_authority_hour
FROM chain
GROUP BY 1 ORDER BY 1;

\echo == versions issued per hour ==
SELECT date_trunc('hour', issued_at) AS hour, authority_kind, count(*) AS versions,
       count(DISTINCT authority_id) AS authorities
FROM commodore.media_authority_versions
WHERE issued_at > NOW() - INTERVAL '{{WINDOW_HOURS}} hours'
GROUP BY 1, 2 ORDER BY 1 DESC, 2 LIMIT 48;

\echo == busiest authorities in window ==
SELECT authority_kind, authority_id, count(*) AS versions_in_window,
       max(authority_version) AS latest_version, max(issued_at) AS latest_issued
FROM commodore.media_authority_versions
WHERE issued_at > NOW() - INTERVAL '{{WINDOW_HOURS}} hours'
GROUP BY 1, 2 ORDER BY 3 DESC LIMIT 15;

\echo == publish decisions in window: versions carrying a content digest ==
SELECT authority_kind, count(*) AS versions, count(content_digest) AS with_content_digest,
       count(DISTINCT content_digest) AS distinct_contents
FROM commodore.media_authority_versions
WHERE issued_at > NOW() - INTERVAL '{{WINDOW_HOURS}} hours'
GROUP BY 1 ORDER BY 1;

\echo == refresh obligations by lane and status (one row per target and lane) ==
SELECT lane, target_kind, status, count(*) AS targets, max(attempts) AS max_attempts,
       sum(revision) AS folded_events, min(next_attempt_at) AS earliest_due,
       count(*) FILTER (WHERE lease_expires_at > NOW()) AS leased
FROM commodore.media_authority_refresh_obligations
GROUP BY 1, 2, 3 ORDER BY 1, 2, 3;

\echo == due refresh obligations: how far behind each lane is ==
SELECT lane, count(*) AS due_targets,
       max(NOW() - GREATEST(pending_since, next_attempt_at)) AS oldest_waiting
FROM commodore.media_authority_refresh_obligations
WHERE status IN ('pending', 'processing') AND next_attempt_at <= NOW()
GROUP BY 1 ORDER BY 1;

\echo == parked refresh targets: cannot compile until their source state changes ==
SELECT lane, target_key, tenant_id, park_reason, revision, updated_at AS parked_at, left(last_error, 160) AS last_error
FROM commodore.media_authority_refresh_obligations
WHERE status = 'parked'
ORDER BY updated_at DESC LIMIT 25;

\echo == retrying refresh targets ==
SELECT lane, target_key, tenant_id, attempts, next_attempt_at, last_reason, left(last_error, 160) AS last_error
FROM commodore.media_authority_refresh_obligations
WHERE status IN ('pending', 'processing') AND attempts > 1
ORDER BY attempts DESC LIMIT 25;

\echo == authorities by whether they are in use (renewal state; dormant = nobody uses it, not renewed, copies run out) ==
SELECT obligation.lane, obligation.status, count(*) AS authorities,
       count(*) FILTER (WHERE obligation.expires_at < NOW()) AS bound_version_expired,
       min(obligation.expires_at) AS earliest_expiry, min(obligation.next_attempt_at) AS next_renewal
FROM commodore.media_authority_refresh_obligations AS obligation
WHERE obligation.lane IN ('object_deadline', 'tenant_deadline')
GROUP BY 1, 2 ORDER BY 1, 2;

\echo == authorities in use past their validity (renewal is not reaching them) ==
SELECT obligation.target_key, obligation.tenant_id, obligation.status, obligation.attempts,
       obligation.expires_at, obligation.next_attempt_at, obligation.park_reason, left(obligation.last_error, 160) AS last_error
FROM commodore.media_authority_refresh_obligations AS obligation
WHERE obligation.lane IN ('object_deadline', 'tenant_deadline')
  AND obligation.status IN ('pending', 'processing', 'parked') AND obligation.expires_at < NOW()
ORDER BY obligation.expires_at LIMIT 25;

\echo == recorded use (an authority with no row counts as used when recording started) ==
SELECT (SELECT started_at FROM commodore.media_authority_use_epoch) AS recording_started,
       authority_kind, count(*) AS recorded,
       count(*) FILTER (WHERE last_used_at > NOW() - INTERVAL '30 days') AS used_last_30_days,
       max(last_used_at) AS latest_use
FROM commodore.media_authority_use GROUP BY authority_kind ORDER BY authority_kind;

\echo == what each cell attested (long validity and use reports decide 30-day objects and cooling) ==
SELECT cell_id, max_schema_version, enforcement_ready, live_replicas, long_validity_ready, use_reports_ready, attested_at
FROM commodore.media_cell_placement_capabilities ORDER BY cell_id;

\echo == deliveries waiting, per cell (replay = the cell asked to be sent everything again) ==
SELECT cell_id, replay, status, count(*) AS deliveries, min(next_attempt_at) AS oldest_due, max(attempts) AS max_attempts
FROM commodore.media_authority_deliveries
WHERE status IN ('pending', 'delivering')
GROUP BY 1, 2, 3 ORDER BY 1, 2, 3;

\echo == live current authorities without a live renewal obligation (tombstones are never renewed) ==
SELECT current.authority_kind, count(*) AS authorities,
       count(*) FILTER (WHERE versions.valid_until <= NOW()) AS already_expired,
       min(versions.valid_until) AS earliest_valid_until
FROM commodore.media_authority_current AS current
JOIN commodore.media_authority_versions AS versions
  ON versions.authority_kind = current.authority_kind AND versions.authority_id = current.authority_id
 AND versions.authority_version = current.authority_version
WHERE NOT versions.tombstone
  AND NOT EXISTS (
    SELECT 1 FROM commodore.media_authority_refresh_obligations AS obligation
    WHERE obligation.lane IN ('object_deadline', 'tenant_deadline')
      AND obligation.target_key = current.authority_kind || ':' || current.authority_id
      AND obligation.status IN ('pending', 'processing', 'parked', 'dormant')
)
GROUP BY 1 ORDER BY 1;

\echo == legacy refresh inbox in window by producer class ==
SELECT source_service,
       CASE WHEN source_event_id LIKE 'authority-deadline:%' THEN 'authority-deadline'
            WHEN source_event_id LIKE 'tenant-deadline-fanout:%' THEN 'tenant-deadline-fanout'
            WHEN source_event_id LIKE 'tenant-fanout:%' THEN 'tenant-fanout'
            WHEN source_event_id LIKE 'reconcile:%' THEN 'reconcile'
            ELSE split_part(source_event_id, ':', 1) END AS event_class,
       regexp_replace(reason, '^(media_object:[a-z_]+):[^:]+:', '\1:*:') AS reason_class,
       status, count(*) AS events, max(attempts) AS max_attempts,
       min(created_at) AS first_seen, max(created_at) AS last_seen
FROM commodore.media_authority_refresh_inbox
WHERE created_at > NOW() - INTERVAL '{{WINDOW_HOURS}} hours'
GROUP BY 1, 2, 3, 4 ORDER BY events DESC LIMIT 40;

\echo == refresh inbox backlog (all time) ==
SELECT status, count(*) AS events, min(next_attempt_at) AS oldest_due, max(attempts) AS max_attempts,
       count(*) FILTER (WHERE lease_expires_at > NOW()) AS leased
FROM commodore.media_authority_refresh_inbox
WHERE status <> 'completed'
GROUP BY 1 ORDER BY 1;

\echo == refresh inbox failure causes (unfinished events) ==
SELECT left(last_error, 160) AS last_error, count(*) AS events, max(attempts) AS max_attempts
FROM commodore.media_authority_refresh_inbox
WHERE status <> 'completed' AND last_error IS NOT NULL
GROUP BY 1 ORDER BY 2 DESC LIMIT 10;

\echo == most-retried unfinished refresh targets ==
SELECT regexp_replace(reason, ':[a-z_]+$', '') AS refresh_target, tenant_id, count(*) AS unfinished_events,
       max(attempts) AS max_attempts, sum(attempts) AS total_attempts,
       count(*) FILTER (WHERE updated_at > NOW() - INTERVAL '10 minutes') AS retried_last_10m,
       min(created_at) AS oldest_event, left(max(last_error), 120) AS last_error
FROM commodore.media_authority_refresh_inbox
WHERE status <> 'completed'
GROUP BY 1, 2 ORDER BY 4 DESC LIMIT 15;

\echo == live streams the compiler refuses: active without an ingest owner cluster ==
SELECT id AS stream_id, tenant_id, ingest_mode, always_on, active_ingest_cluster_id,
       active_ingest_cluster_updated_at, updated_at
FROM commodore.streams
WHERE deleted_at IS NULL
  AND id::text IN (
      SELECT split_part(reason, ':', 3)
      FROM commodore.media_authority_refresh_inbox
      WHERE status <> 'completed' AND reason LIKE 'media_object:live_stream:%' AND last_error IS NOT NULL
  )
ORDER BY updated_at DESC LIMIT 15;

\echo == live streams: ingest owner, current authority, unfinished refreshes ==
SELECT stream.id AS stream_id, stream.tenant_id, stream.ingest_mode, stream.always_on,
       stream.active_ingest_cluster_id AS ingest_owner, stream.active_ingest_cluster_updated_at AS owner_updated,
       head.authority_version AS authority_version, version.issued_at AS authority_issued,
       (SELECT count(*) FROM commodore.media_authority_refresh_inbox AS inbox
        WHERE inbox.status <> 'completed'
          AND inbox.reason LIKE 'media_object:live_stream:' || stream.id::text || ':%') AS unfinished_refreshes
FROM commodore.streams AS stream
LEFT JOIN commodore.media_authority_current AS head
       ON head.authority_kind = 'media_object' AND head.authority_id = 'live_stream:' || stream.id::text
LEFT JOIN commodore.media_authority_versions AS version
       ON version.authority_kind = head.authority_kind AND version.authority_id = head.authority_id
      AND version.authority_version = head.authority_version
WHERE stream.deleted_at IS NULL
ORDER BY unfinished_refreshes DESC, owner_updated DESC NULLS LAST LIMIT 40;

\echo == database sessions by state and wait ==
SELECT usename, state, wait_event_type, wait_event, count(*) AS sessions,
       max(NOW() - query_start) AS longest_running
FROM pg_stat_activity
WHERE datname = current_database()
GROUP BY 1, 2, 3, 4 ORDER BY sessions DESC LIMIT 20;

\echo == deliveries by cell and status ==
SELECT cell_id, status, count(*) AS deliveries, min(created_at) AS oldest, max(attempts) AS max_attempts,
       count(*) FILTER (WHERE lease_expires_at > NOW()) AS leased
FROM commodore.media_authority_deliveries
GROUP BY 1, 2 ORDER BY 1, 2;

\echo == unacknowledged deliveries versus current version ==
SELECT delivery.authority_kind, delivery.authority_id, delivery.cell_id,
       delivery.authority_version AS queued_version, head.authority_version AS current_version,
       LEAST(version.valid_until, delivery.correction_until) AS recipient_valid_until,
       delivery.correction_until,
       delivery.status, delivery.attempts, delivery.next_attempt_at, left(delivery.last_error, 120) AS last_error
FROM commodore.media_authority_deliveries AS delivery
JOIN commodore.media_authority_versions AS version
       ON version.authority_kind = delivery.authority_kind AND version.authority_id = delivery.authority_id
      AND version.authority_version = delivery.authority_version
LEFT JOIN commodore.media_authority_current AS head
       ON head.authority_kind = delivery.authority_kind AND head.authority_id = delivery.authority_id
WHERE delivery.status IN ('pending', 'delivering')
ORDER BY delivery.attempts DESC, delivery.created_at LIMIT 25;

\echo == compile fences ==
SELECT count(*) AS scopes, max(generation) AS max_generation,
       count(*) FILTER (WHERE updated_at > NOW() - INTERVAL '{{WINDOW_HOURS}} hours') AS touched_in_window
FROM commodore.media_authority_compile_fences;

\echo == index validity ==
SELECT tbl.relname AS table_name, idx.relname AS index_name, i.indisvalid, i.indisready
FROM pg_index AS i
JOIN pg_class AS idx ON idx.oid = i.indexrelid
JOIN pg_class AS tbl ON tbl.oid = i.indrelid
JOIN pg_namespace AS ns ON ns.oid = tbl.relnamespace
WHERE ns.nspname = 'commodore' AND tbl.relname LIKE 'media_authority%'
ORDER BY i.indisvalid, tbl.relname, idx.relname;

\echo == short-lease claim predicate plan ==
EXPLAIN (ANALYZE, COSTS OFF)
SELECT queued.cell_id, count(*)
FROM commodore.media_authority_deliveries AS queued
JOIN commodore.media_authority_current AS current
  ON current.authority_kind = queued.authority_kind
 AND current.authority_id = queued.authority_id
 AND current.authority_version = queued.authority_version
WHERE queued.status IN ('pending', 'delivering')
  AND queued.next_attempt_at <= NOW()
  AND (queued.lease_expires_at IS NULL OR queued.lease_expires_at <= NOW())
  AND queued.short_lease
  AND (queued.correction_until IS NULL OR queued.correction_until > NOW())
GROUP BY 1;
`

const foghornMediaAuthorityDiagnosticSQL = `\echo == database ==
SELECT current_database() AS database;

\echo == apply audit outcomes in window ==
SELECT outcome, authority_kind, count(*) AS applies, min(observed_at) AS first_seen, max(observed_at) AS last_seen
FROM foghorn.media_authority_apply_audit
WHERE observed_at > NOW() - INTERVAL '{{WINDOW_HOURS}} hours'
GROUP BY 1, 2 ORDER BY applies DESC;

\echo == rejections per hour ==
SELECT date_trunc('hour', observed_at) AS hour, outcome, count(*) AS rejections
FROM foghorn.media_authority_apply_audit
WHERE observed_at > NOW() - INTERVAL '{{WINDOW_HOURS}} hours'
  AND outcome NOT IN ('applied', 'duplicate')
GROUP BY 1, 2 ORDER BY 1 DESC, 2 LIMIT 48;

\echo == recent rejections versus held version ==
SELECT audit.observed_at, audit.outcome, audit.authority_kind, audit.authority_id,
       audit.authority_version AS rejected_version, held.authority_version AS held_version,
       held.issued_at AS held_issued_at, left(audit.reason, 160) AS reason
FROM foghorn.media_authority_apply_audit AS audit
LEFT JOIN foghorn.media_authorities AS held
       ON held.authority_kind = audit.authority_kind AND held.authority_id = audit.authority_id
WHERE audit.observed_at > NOW() - INTERVAL '{{WINDOW_HOURS}} hours'
  AND audit.outcome NOT IN ('applied', 'duplicate')
ORDER BY audit.observed_at DESC LIMIT 25;

\echo == held authority freshness ==
SELECT held.authority_kind,
       COALESCE(tenant.lifecycle, object.lifecycle, 'unprojected') AS lifecycle,
       count(*) AS authorities,
       count(*) FILTER (WHERE held.valid_until <= NOW()) AS hard_expired,
       count(*) FILTER (WHERE held.refresh_after <= NOW() AND held.valid_until > NOW()) AS soft_expired,
       min(held.issued_at) AS oldest_issued, max(held.issued_at) AS newest_issued
FROM foghorn.media_authorities AS held
LEFT JOIN foghorn.tenant_authority_projection AS tenant
       ON held.authority_kind = 'tenant' AND tenant.tenant_id::text = held.authority_id
LEFT JOIN foghorn.media_object_authority_projection AS object
       ON held.authority_kind = 'media_object' AND object.authority_id = held.authority_id
GROUP BY 1, 2 ORDER BY 1, 2;
`

const purserMediaAuthorityDiagnosticSQL = `\echo == refresh outbox by reason (rows touched in window) ==
SELECT reason, status, count(*) AS obligations, max(revision) AS max_revision,
       sum(revision) AS coalesced_enqueues, max(attempts) AS max_attempts,
       min(pending_since) FILTER (WHERE status <> 'completed') AS oldest_pending, max(updated_at) AS last_touched
FROM purser.media_authority_refresh_outbox
WHERE updated_at > NOW() - INTERVAL '{{WINDOW_HOURS}} hours'
GROUP BY 1, 2 ORDER BY coalesced_enqueues DESC;

\echo == delivered_minutes usage writes per hour (each one enqueues allowance_usage_changed) ==
SELECT date_trunc('hour', updated_at) AS hour, count(*) AS usage_rows, count(DISTINCT tenant_id) AS tenants
FROM purser.usage_records
WHERE usage_type = 'delivered_minutes'
  AND updated_at > (NOW() - INTERVAL '{{WINDOW_HOURS}} hours')::timestamp
GROUP BY 1 ORDER BY 1 DESC LIMIT 48;
`

const quartermasterMediaAuthorityDiagnosticSQL = `\echo == refresh outbox by reason (rows touched in window) ==
SELECT reason, status, count(*) AS obligations, max(attempts) AS max_attempts,
       min(created_at) FILTER (WHERE status <> 'completed') AS oldest_pending, max(updated_at) AS last_touched
FROM quartermaster.media_authority_refresh_outbox
WHERE updated_at > NOW() - INTERVAL '{{WINDOW_HOURS}} hours'
GROUP BY 1, 2 ORDER BY obligations DESC;
`
