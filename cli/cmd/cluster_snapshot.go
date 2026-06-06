package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
)

type clusterSnapshotOptions struct {
	logsSnapshotOptions
	SkipLogs bool
	SkipDB   bool
}

type postgresSnapshotTarget struct {
	Name        string
	HostName    string
	Host        inventory.Host
	Port        int
	User        string
	Password    string
	UsePeerAuth bool
	Binary      string
	Databases   []string
}

func newClusterSnapshotCmd() *cobra.Command {
	opts := clusterSnapshotOptions{
		logsSnapshotOptions: logsSnapshotOptions{
			Since:    "4 hours ago",
			Tail:     500,
			Parallel: 6,
		},
	}
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Collect logs and database state from a cluster",
		Long: `Collect a bounded debugging snapshot from the cluster manifest.

The snapshot includes HA-aware host logs plus read-only database metadata:
table sizes, row estimates, migration ledgers, ClickHouse parts, and pending
mutations. It does not dump tenant rows or secret values.`,
		Example: `  frameworks cluster snapshot --gitops-dir ../gitops --cluster production --since "2 hours ago"
  frameworks cluster snapshot --boot --tail 800 --output /tmp/fw-snapshot
  frameworks cluster snapshot --skip-logs --output /tmp/fw-db-state`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			if strings.TrimSpace(opts.EdgeManifest) == "" && !cmd.Flags().Changed("edge-manifest") {
				opts.EdgeManifest = defaultEdgeManifestPath(rc.ManifestPath)
			}
			return runClusterSnapshot(cmd, rc, opts)
		},
	}
	cmd.Flags().StringVar(&opts.Since, "since", opts.Since, "journalctl time window when --boot is not set")
	cmd.Flags().BoolVar(&opts.Boot, "boot", false, "collect logs from the current boot instead of --since")
	cmd.Flags().IntVar(&opts.Tail, "tail", opts.Tail, "maximum journal lines per unit; set 0 for all lines in scope")
	cmd.Flags().StringVarP(&opts.OutputDir, "output", "o", "", "local output directory; defaults to a temp directory")
	cmd.Flags().IntVar(&opts.Parallel, "parallel", opts.Parallel, "maximum hosts to collect in parallel")
	cmd.Flags().StringVar(&opts.EdgeManifest, "edge-manifest", "", "optional edge manifest to include edge nodes in the snapshot")
	cmd.Flags().BoolVar(&opts.SkipLogs, "skip-logs", false, "skip host and service logs")
	cmd.Flags().BoolVar(&opts.SkipDB, "skip-db", false, "skip database metadata")
	return cmd
}

func runClusterSnapshot(cmd *cobra.Command, rc *resolvedCluster, opts clusterSnapshotOptions) error {
	if rc == nil || rc.Manifest == nil {
		return fmt.Errorf("manifest is required")
	}
	if opts.SkipLogs && opts.SkipDB {
		return fmt.Errorf("at least one of logs or db snapshot must be enabled")
	}

	outDir, err := prepareSnapshotOutputDir(opts.OutputDir, "frameworks-cluster-snapshot-*")
	if err != nil {
		return err
	}
	opts.OutputDir = outDir

	ux.Heading(cmd.OutOrStdout(), "Collecting cluster snapshot")
	fmt.Fprintf(cmd.OutOrStdout(), "  Output: %s\n\n", outDir)

	var failures []string
	if !opts.SkipLogs {
		logOpts := opts.logsSnapshotOptions
		logOpts.OutputDir = outDir
		if err := runLogsSnapshot(cmd, rc.Manifest, logOpts); err != nil {
			failures = append(failures, fmt.Sprintf("logs: %v", err))
		}
	}

	if !opts.SkipDB {
		if err := runDBSnapshot(cmd, rc, outDir); err != nil {
			failures = append(failures, fmt.Sprintf("db: %v", err))
		}
	}

	if len(failures) > 0 {
		sort.Strings(failures)
		path := filepath.Join(outDir, "_snapshot_failures.txt")
		if writeErr := os.WriteFile(path, []byte(strings.Join(failures, "\n")+"\n"), 0o644); writeErr != nil {
			return fmt.Errorf("write failure summary: %w", writeErr)
		}
		return fmt.Errorf("snapshot completed with %d section failure(s); see %s", len(failures), path)
	}

	ux.Success(cmd.OutOrStdout(), "Cluster snapshot complete")
	fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", outDir)
	return nil
}

func prepareSnapshotOutputDir(requested, pattern string) (string, error) {
	requested = strings.TrimSpace(requested)
	var (
		outDir string
		err    error
	)
	if requested == "" {
		outDir, err = os.MkdirTemp("", pattern)
		if err != nil {
			return "", fmt.Errorf("create snapshot directory: %w", err)
		}
	} else {
		outDir = requested
		err = os.MkdirAll(outDir, 0o755)
		if err != nil {
			return "", fmt.Errorf("create snapshot directory: %w", err)
		}
	}
	abs, err := filepath.Abs(outDir)
	if err != nil {
		return "", fmt.Errorf("resolve snapshot directory: %w", err)
	}
	return abs, nil
}

func runDBSnapshot(cmd *cobra.Command, rc *resolvedCluster, outputDir string) error {
	dbDir := filepath.Join(outputDir, "_db")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		return fmt.Errorf("create db snapshot directory: %w", err)
	}

	sshKey := stringFlag(cmd, "ssh-key").Value
	pool := ssh.NewPool(45*time.Second, sshKey)
	defer pool.Close()

	var failures []string
	if err := collectPostgresSnapshots(cmd.Context(), rc, pool, sshKey, dbDir); err != nil {
		failures = append(failures, err.Error())
	}
	if err := collectClickHouseSnapshot(cmd.Context(), rc, pool, sshKey, dbDir); err != nil {
		failures = append(failures, err.Error())
	}
	if len(failures) > 0 {
		sort.Strings(failures)
		return errors.New(strings.Join(failures, "; "))
	}
	ux.Success(cmd.OutOrStdout(), "Database state snapshot complete")
	fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", dbDir)
	return nil
}

func collectPostgresSnapshots(ctx context.Context, rc *resolvedCluster, pool *ssh.Pool, sshKey, dbDir string) error {
	targets, err := postgresSnapshotTargets(rc)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return nil
	}

	var failures []string
	for _, target := range targets {
		runner, err := snapshotRunner(pool, sshKey, target.Host, 45*time.Second)
		if err != nil {
			failures = append(failures, fmt.Sprintf("postgres %s: connect: %v", target.Name, err))
			continue
		}
		result, err := runner.RunScript(ctx, postgresSnapshotScript(target))
		if err != nil {
			failures = append(failures, fmt.Sprintf("postgres %s: %v", target.Name, err))
			continue
		}
		content := formatSnapshotCommandResult("postgres/"+target.Name, target.Host, result)
		path := filepath.Join(dbDir, safeSnapshotFilename("postgres-"+target.Name)+".txt")
		if writeErr := os.WriteFile(path, []byte(content), 0o644); writeErr != nil {
			failures = append(failures, fmt.Sprintf("postgres %s: write: %v", target.Name, writeErr))
			continue
		}
		if result.ExitCode != 0 {
			failures = append(failures, fmt.Sprintf("postgres %s: remote snapshot exited %d", target.Name, result.ExitCode))
		}
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

func collectClickHouseSnapshot(ctx context.Context, rc *resolvedCluster, pool *ssh.Pool, sshKey, dbDir string) error {
	manifest := rc.Manifest
	ch := manifest.Infrastructure.ClickHouse
	if ch == nil || !ch.Enabled {
		return nil
	}
	host, ok := manifest.GetHost(ch.Host)
	if !ok {
		return fmt.Errorf("clickhouse: host %s not found", ch.Host)
	}
	host.Name = firstNonEmpty(host.Name, ch.Host)

	sharedEnv, err := rc.SharedEnv()
	if err != nil {
		return fmt.Errorf("clickhouse: load manifest env_files: %w", err)
	}
	user := firstNonEmpty(sharedEnv["CLICKHOUSE_USER"], "frameworks")
	password := sharedEnv["CLICKHOUSE_PASSWORD"]
	if password == "" {
		user = "default"
	}
	port := ch.Port
	if port == 0 {
		port = 9000
	}

	runner, err := snapshotRunner(pool, sshKey, host, 45*time.Second)
	if err != nil {
		return fmt.Errorf("clickhouse: connect: %w", err)
	}
	result, err := runner.RunScript(ctx, clickHouseSnapshotScript(ch.Databases, port, user, password))
	if err != nil {
		return fmt.Errorf("clickhouse: %w", err)
	}
	content := formatSnapshotCommandResult("clickhouse", host, result)
	path := filepath.Join(dbDir, "clickhouse.txt")
	if writeErr := os.WriteFile(path, []byte(content), 0o644); writeErr != nil {
		return fmt.Errorf("clickhouse: write: %w", writeErr)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("clickhouse: remote snapshot exited %d", result.ExitCode)
	}
	return nil
}

func postgresSnapshotTargets(rc *resolvedCluster) ([]postgresSnapshotTarget, error) {
	manifest := rc.Manifest
	pg := manifest.Infrastructure.Postgres
	if pg == nil || !pg.Enabled {
		return nil, nil
	}

	var sharedEnv map[string]string
	if pg.IsYugabyte() || pg.Password != "" || len(pg.Instances) > 0 {
		env, err := rc.SharedEnv()
		if err != nil {
			return nil, fmt.Errorf("postgres: load manifest env_files: %w", err)
		}
		sharedEnv = env
	}

	dbConfigs := pg.Databases
	if pg.IsYugabyte() {
		dbConfigs = expandedYugabyteDatabaseConfigs(pg.Databases, manifest)
	}
	dbNames := databaseNamesFromConfigs(dbConfigs)

	targets := make([]postgresSnapshotTarget, 0, 1+len(pg.Instances))
	if len(dbNames) > 0 {
		hostName := pg.Host
		if pg.IsYugabyte() && len(pg.Nodes) > 0 {
			hostName = pg.Nodes[0].Host
		}
		host, ok := manifest.GetHost(hostName)
		if !ok {
			return nil, fmt.Errorf("postgres: host %s not found", hostName)
		}
		host.Name = firstNonEmpty(host.Name, hostName)
		password, err := resolveYugabytePassword(pg, sharedEnv)
		if err != nil {
			return nil, err
		}
		if !pg.IsYugabyte() && pg.Password != "" {
			password, err = inventory.ResolveSharedEnvPlaceholder(pg.Password, sharedEnv)
			if err != nil {
				return nil, fmt.Errorf("postgres: resolve password: %w", err)
			}
		}
		targets = append(targets, postgresSnapshotTarget{
			Name:        "primary",
			HostName:    hostName,
			Host:        host,
			Port:        pg.EffectivePort(),
			User:        postgresDoctorUser(pg),
			Password:    password,
			UsePeerAuth: !pg.IsYugabyte() && password == "",
			Binary:      postgresSnapshotBinary(pg.IsYugabyte()),
			Databases:   dbNames,
		})
	}

	for _, inst := range pg.Instances {
		dbNames := databaseNamesFromConfigs(inst.Databases)
		if len(dbNames) == 0 {
			continue
		}
		host, ok := manifest.GetHost(inst.Host)
		if !ok {
			return nil, fmt.Errorf("postgres instance %s: host %s not found", inst.Name, inst.Host)
		}
		host.Name = firstNonEmpty(host.Name, inst.Host)
		port := inst.Port
		if port == 0 {
			port = 5432
		}
		password := inst.Password
		if password != "" {
			resolved, err := inventory.ResolveSharedEnvPlaceholder(password, sharedEnv)
			if err != nil {
				return nil, fmt.Errorf("postgres instance %s: resolve password: %w", inst.Name, err)
			}
			password = resolved
		}
		name := strings.TrimSpace(inst.Name)
		if name == "" {
			name = inst.Host
		}
		targets = append(targets, postgresSnapshotTarget{
			Name:        name,
			HostName:    inst.Host,
			Host:        host,
			Port:        port,
			User:        "postgres",
			Password:    password,
			UsePeerAuth: password == "",
			Binary:      "psql",
			Databases:   dbNames,
		})
	}
	return targets, nil
}

func databaseNamesFromConfigs(configs []inventory.DatabaseConfig) []string {
	names := make([]string, 0, len(configs))
	for _, cfg := range configs {
		if name := strings.TrimSpace(cfg.Name); name != "" {
			names = append(names, name)
		}
	}
	return dedupeStrings(names)
}

func postgresSnapshotBinary(yugabyte bool) string {
	if yugabyte {
		return "ysqlsh"
	}
	return "psql"
}

func snapshotRunner(pool *ssh.Pool, sshKey string, host inventory.Host, timeout time.Duration) (ssh.Runner, error) {
	if host.ExternalIP == "" || host.ExternalIP == "localhost" || host.ExternalIP == "127.0.0.1" {
		return ssh.NewLocalRunner(""), nil
	}
	return pool.Get(&ssh.ConnectionConfig{
		Address:  host.ExternalIP,
		Port:     22,
		User:     host.User,
		KeyPath:  sshKey,
		HostName: host.Name,
		Timeout:  timeout,
	})
}

func postgresSnapshotScript(target postgresSnapshotTarget) string {
	peer := "0"
	if target.UsePeerAuth {
		peer = "1"
	}
	dbs := strings.Join(safeDBNames(target.Databases), " ")
	return fmt.Sprintf(`set +e
PORT=%d
USER_NAME=%s
PASSWORD=%s
BINARY=%s
PEER=%s
DATABASES=%s
resolve_sql_binary() {
  if command -v "$BINARY" >/dev/null 2>&1; then
    command -v "$BINARY"
    return 0
  fi
  if [ "$BINARY" = "ysqlsh" ]; then
    for path in /home/yugabyte/tserver/bin/ysqlsh /opt/yugabyte/bin/ysqlsh /usr/local/bin/ysqlsh; do
      if [ -x "$path" ]; then
        echo "$path"
        return 0
      fi
    done
  fi
  return 1
}
SQL_BIN="$(resolve_sql_binary)"
REQUIRED_FAILED=0
REQUIRED_OK=0
run_sql() {
  db="$1"
  sql="$2"
  if [ -z "$SQL_BIN" ]; then
    echo "$BINARY not found"
    return 127
  fi
  if [ "$PEER" = "1" ]; then
    if command -v sudo >/dev/null 2>&1 && sudo -n true >/dev/null 2>&1; then
      sudo -u "$USER_NAME" "$SQL_BIN" -X -p "$PORT" -d "$db" -A -F '	' -P pager=off -c "$sql" 2>&1
    else
      "$SQL_BIN" -X -p "$PORT" -d "$db" -A -F '	' -P pager=off -c "$sql" 2>&1
    fi
  else
    PGPASSWORD="$PASSWORD" "$SQL_BIN" -X -h 127.0.0.1 -p "$PORT" -U "$USER_NAME" -d "$db" -A -F '	' -P pager=off -c "$sql" 2>&1
  fi
}
run_section() {
  title="$1"
  db="$2"
  sql="$3"
  required="$4"
  echo
  echo "== ${db}: ${title} =="
  if run_sql "$db" "$sql"; then
    if [ "$required" = "1" ]; then
      REQUIRED_OK=1
    fi
  else
    echo "(query failed)"
    if [ "$required" = "1" ]; then
      REQUIRED_FAILED=1
    fi
  fi
}
echo "== postgres target =="
echo "host=%s"
echo "port=$PORT"
echo "user=$USER_NAME"
echo "binary=${SQL_BIN:-$BINARY}"
for db in $DATABASES; do
  run_section "version" "$db" "SELECT current_database() AS database, version() AS version;" 1
  run_section "largest user tables" "$db" "SELECT schemaname, relname, n_live_tup, pg_size_pretty(pg_total_relation_size(relid)) AS total_size FROM pg_stat_user_tables ORDER BY pg_total_relation_size(relid) DESC LIMIT 80;" 1
  run_section "migration ledger" "$db" "SELECT version, phase, seq, checksum FROM _migrations ORDER BY version, phase, seq LIMIT 200;" 0
  run_section "recent data migrations" "$db" "SELECT id, status, updated_at FROM data_migrations ORDER BY updated_at DESC LIMIT 80;" 0
done
if [ "$REQUIRED_OK" != "1" ] || [ "$REQUIRED_FAILED" = "1" ]; then
  exit 1
fi
exit 0
`, target.Port, ssh.ShellQuote(target.User), ssh.ShellQuote(target.Password), ssh.ShellQuote(target.Binary), peer, ssh.ShellQuote(dbs), ssh.ShellQuote(target.HostName))
}

func clickHouseSnapshotScript(databases []string, port int, user, password string) string {
	dbs := strings.Join(safeDBNames(databases), " ")
	return fmt.Sprintf(`set +e
PORT=%d
USER_NAME=%s
PASSWORD=%s
DATABASES=%s
run_ch() {
  sql="$1"
  if [ -n "$PASSWORD" ]; then
    CLICKHOUSE_PASSWORD="$PASSWORD" clickhouse-client --host localhost --port "$PORT" --user "$USER_NAME" --multiquery --query "$sql" 2>&1
  else
    clickhouse-client --host localhost --port "$PORT" --user "$USER_NAME" --multiquery --query "$sql" 2>&1
  fi
}
run_section() {
  title="$1"
  sql="$2"
  echo
  echo "== ${title} =="
  run_ch "$sql" || echo "(query failed)"
}
echo "== clickhouse target =="
echo "port=$PORT"
echo "user=$USER_NAME"
echo "databases=$DATABASES"
for db in $DATABASES; do
  run_section "${db}: tables" "SELECT database, name AS table, total_rows, formatReadableSize(total_bytes) AS total_size FROM system.tables WHERE database = '${db}' ORDER BY total_bytes DESC, name FORMAT PrettyCompact"
  run_section "${db}: active parts" "SELECT database, table, sum(rows) AS rows, formatReadableSize(sum(bytes_on_disk)) AS bytes FROM system.parts WHERE active AND database = '${db}' GROUP BY database, table ORDER BY sum(bytes_on_disk) DESC, table FORMAT PrettyCompact"
  run_section "${db}: pending mutations" "SELECT database, table, mutation_id, command, is_done, latest_fail_reason FROM system.mutations WHERE database = '${db}' AND is_done = 0 ORDER BY create_time DESC LIMIT 100 FORMAT PrettyCompact"
  run_section "${db}: migration ledger" "SELECT version, phase, seq, checksum FROM ${db}._migrations ORDER BY version, phase, seq LIMIT 200 FORMAT PrettyCompact"
  if [ "$db" = "periscope" ]; then
    run_section "${db}: ledger rebuild cursors" "SELECT ledger_name, last_processed_projection_ms, toDateTime(last_processed_projection_ms / 1000) AS last_processed_at, updated_at_ms, toDateTime(updated_at_ms / 1000) AS updated_at FROM ${db}.ledger_rebuild_cursors ORDER BY ledger_name FORMAT PrettyCompact"
    run_section "${db}: stream runtime finalized sessions" "SELECT count() AS sessions, countIf(duration_seconds > 0) AS positive_duration_sessions, countIf(source_started_at_ms <= 0) AS missing_start_sessions, min(duration_seconds) AS min_duration_seconds, max(duration_seconds) AS max_duration_seconds, quantiles(0.5, 0.95)(duration_seconds) AS duration_quantiles FROM (SELECT source_started_at_ms, greatest(0, intDiv(source_ended_at_ms - source_started_at_ms, 1000)) AS duration_seconds FROM ${db}.stream_sessions_final_v) FORMAT PrettyCompact"
    run_section "${db}: stream runtime ledger rows" "SELECT count() AS rows, countIf(active_seconds > 0) AS positive_rows, sum(active_seconds) AS total_active_seconds, min(window_start) AS first_window, max(window_start) AS last_window FROM ${db}.stream_runtime_5m_v FORMAT PrettyCompact"
    run_section "${db}: live stream current state" "SELECT status, count() AS rows, countIf(isNull(started_at)) AS missing_started_at, min(started_at) AS min_started_at, max(started_at) AS max_started_at, max(updated_at) AS max_updated_at FROM ${db}.stream_state_current FINAL GROUP BY status ORDER BY status FORMAT PrettyCompact"
    run_section "${db}: stream event log lifecycle coverage" "SELECT event_type, count() AS rows, min(timestamp) AS first_seen, max(timestamp) AS last_seen FROM ${db}.stream_event_log WHERE event_type IN ('stream_start', 'stream_lifecycle', 'stream_buffer', 'track_list_update', 'stream_end') GROUP BY event_type ORDER BY event_type FORMAT PrettyCompact"
    run_section "${db}: player boot telemetry recency" "SELECT outcome, count() AS rows, countIf(cluster_attributed = 1) AS attributed_rows, uniqExact(session_id) AS sessions, min(timestamp) AS first_seen, max(timestamp) AS last_seen, quantiles(0.5, 0.95)(total_ttf_ms) AS ttf_ms_quantiles FROM ${db}.player_boot_samples WHERE timestamp >= now() - INTERVAL 24 HOUR GROUP BY outcome ORDER BY rows DESC FORMAT PrettyCompact"
    run_section "${db}: player session telemetry recency" "SELECT content_type, is_live, count() AS rows, countIf(cluster_attributed = 1) AS attributed_rows, uniqExact(session_id) AS sessions, sum(played_ms) AS played_ms, sum(rebuffer_ms) AS rebuffer_ms, sum(rebuffer_count) AS rebuffer_count, sum(fatal_error) AS fatal_errors, min(timestamp) AS first_seen, max(timestamp) AS last_seen FROM ${db}.client_qoe_session_deltas FINAL WHERE timestamp >= now() - INTERVAL 24 HOUR GROUP BY content_type, is_live ORDER BY rows DESC FORMAT PrettyCompact"
    run_section "${db}: vod retention telemetry recency" "SELECT count() AS rows, uniqExact(session_id) AS sessions, uniqExact(content_id) AS content_items, sum(seconds_watched) AS seconds_watched, min(timestamp) AS first_seen, max(timestamp) AS last_seen FROM ${db}.vod_retention_buckets FINAL WHERE timestamp >= now() - INTERVAL 24 HOUR FORMAT PrettyCompact"
  fi
done
exit 0
`, port, ssh.ShellQuote(user), ssh.ShellQuote(password), ssh.ShellQuote(dbs))
}

func safeDBNames(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func formatSnapshotCommandResult(name string, host inventory.Host, result *ssh.CommandResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# target: %s\n", name)
	fmt.Fprintf(&b, "# address: %s\n", firstNonEmpty(host.ExternalIP, "local"))
	if result != nil {
		fmt.Fprintf(&b, "# exit_code: %d\n", result.ExitCode)
	}
	fmt.Fprintf(&b, "# collected_at: %s\n\n", time.Now().UTC().Format(time.RFC3339))
	if result == nil {
		b.WriteString("(no command result)\n")
		return b.String()
	}
	b.WriteString(result.Stdout)
	if !strings.HasSuffix(result.Stdout, "\n") {
		b.WriteString("\n")
	}
	if strings.TrimSpace(result.Stderr) != "" {
		b.WriteString("\n== stderr ==\n")
		b.WriteString(result.Stderr)
		if !strings.HasSuffix(result.Stderr, "\n") {
			b.WriteString("\n")
		}
	}
	return b.String()
}
