package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
)

// newClusterLogsCmd creates the logs command
func newClusterLogsCmd() *cobra.Command {
	var follow bool
	var tail int

	cmd := &cobra.Command{
		Use:   "logs <service>",
		Short: "Show logs from a service",
		Long: `Show logs from a service running on the cluster.

For Docker mode:
  - Uses 'docker compose logs'

For native mode (systemd):
  - Uses 'journalctl -u frameworks-<service>'

The logs command automatically detects the service mode and uses
the appropriate log viewing method.`,
		Example: `  frameworks cluster logs quartermaster
  frameworks cluster logs foghorn-eu --tail 200
  frameworks cluster logs bridge --tail 100 --follow`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			return runLogs(cmd, rc.Manifest, args[0], follow, tail)
		},
	}

	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow log output")
	cmd.Flags().IntVarP(&tail, "tail", "n", 50, "Number of lines to show from the end")
	cmd.AddCommand(newClusterLogsSnapshotCmd())

	return cmd
}

type logsSnapshotOptions struct {
	Since        string
	Boot         bool
	Tail         int
	OutputDir    string
	Parallel     int
	EdgeManifest string
}

type logsSnapshotHost struct {
	Name string
	Host inventory.Host
}

func newClusterLogsSnapshotCmd() *cobra.Command {
	opts := logsSnapshotOptions{
		Since:    "4 hours ago",
		Tail:     500,
		Parallel: 6,
	}
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Collect a log snapshot from every cluster host",
		Long: `Collect a bounded debugging snapshot from every host in the cluster manifest.

Each host gets one local log file containing host metadata, failed units,
frameworks-* service status, and journal excerpts. Use --boot to scope
journalctl to the current boot, or --since for a relative/absolute time window.`,
		Example: `  frameworks cluster logs snapshot --since "2 hours ago"
  frameworks cluster logs snapshot --boot --tail 800 --edge-manifest ./clusters/production/edge.yaml
  frameworks cluster logs snapshot --output /tmp/fw-logs`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			snapshotOpts := opts
			if strings.TrimSpace(snapshotOpts.EdgeManifest) == "" && !cmd.Flags().Changed("edge-manifest") {
				snapshotOpts.EdgeManifest = defaultEdgeManifestPath(rc.ManifestPath)
			}
			return runLogsSnapshot(cmd, rc.Manifest, snapshotOpts)
		},
	}
	cmd.Flags().StringVar(&opts.Since, "since", opts.Since, "journalctl time window when --boot is not set")
	cmd.Flags().BoolVar(&opts.Boot, "boot", false, "collect logs from the current boot instead of --since")
	cmd.Flags().IntVar(&opts.Tail, "tail", opts.Tail, "maximum journal lines per unit; set 0 for all lines in scope")
	cmd.Flags().StringVarP(&opts.OutputDir, "output", "o", "", "local output directory; defaults to a temp directory")
	cmd.Flags().IntVar(&opts.Parallel, "parallel", opts.Parallel, "maximum hosts to collect in parallel")
	cmd.Flags().StringVar(&opts.EdgeManifest, "edge-manifest", "", "optional edge manifest to include edge nodes in the snapshot")
	return cmd
}

type logTarget struct {
	ServiceName string
	DeployName  string
	HostName    string
	Host        inventory.Host
}

// runLogs executes the logs command against an already-loaded manifest.
func runLogs(cmd *cobra.Command, manifest *inventory.Manifest, serviceName string, follow bool, tail int) error {
	targets, err := resolveLogTargets(manifest, serviceName)
	if err != nil {
		return err
	}
	if follow && len(targets) > 1 {
		return fmt.Errorf("%s has %d hosts; use non-follow logs or cluster logs snapshot for HA services", serviceName, len(targets))
	}

	ux.Heading(cmd.OutOrStdout(), fmt.Sprintf("Fetching logs for %s from %d host(s)", serviceName, len(targets)))
	fmt.Fprintln(cmd.OutOrStdout(), "")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle Ctrl+C gracefully
	if follow {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
		go func() {
			<-sigCh
			fmt.Fprintln(cmd.OutOrStderr(), "\nStopping log stream...")
			cancel()
		}()
	}

	// Create SSH pool
	sshKey := stringFlag(cmd, "ssh-key").Value
	sshPool := ssh.NewPool(30*time.Second, sshKey)
	defer sshPool.Close()

	for _, target := range targets {
		if len(targets) > 1 {
			fmt.Fprintf(cmd.OutOrStdout(), "== %s (%s) ==\n", target.HostName, firstNonEmpty(target.Host.ExternalIP, "local"))
		}

		detector := detect.NewDetector(sshPool, target.Host)
		state, err := detector.Detect(ctx, target.DeployName)
		if err != nil {
			return fmt.Errorf("%s: failed to detect service: %w", target.HostName, err)
		}

		if !state.Exists {
			return fmt.Errorf("service %s does not exist on %s", serviceName, target.Host.ExternalIP)
		}

		logCmd, err := buildLogCommand(target.DeployName, state.Mode, follow, tail)
		if err != nil {
			return err
		}

		if follow {
			return streamLogCommand(ctx, target.Host, stringFlag(cmd, "ssh-key").Value, logCmd)
		}

		var runner ssh.Runner
		if target.Host.ExternalIP == "" || target.Host.ExternalIP == "localhost" || target.Host.ExternalIP == "127.0.0.1" {
			runner = ssh.NewLocalRunner("")
		} else {
			sshConfig := &ssh.ConnectionConfig{
				Address:  target.Host.ExternalIP,
				Port:     22,
				User:     target.Host.User,
				KeyPath:  sshKey,
				HostName: target.Host.Name,
				Timeout:  30 * time.Second,
			}
			runner, err = sshPool.Get(sshConfig)
			if err != nil {
				return fmt.Errorf("%s: failed to connect to host: %w", target.HostName, err)
			}
		}

		result, err := runner.Run(ctx, logCmd)
		if err != nil {
			return fmt.Errorf("%s: failed to fetch logs: %w", target.HostName, err)
		}

		if result.ExitCode != 0 {
			fmt.Fprintf(cmd.OutOrStderr(), "Error fetching logs from %s: %s\n", target.HostName, result.Stderr)
			return fmt.Errorf("%s: log command exited with code %d", target.HostName, result.ExitCode)
		}

		fmt.Fprint(cmd.OutOrStdout(), result.Stdout)
		if len(targets) > 1 && !strings.HasSuffix(result.Stdout, "\n") {
			fmt.Fprintln(cmd.OutOrStdout())
		}
	}
	return nil
}

func buildLogCommand(deployName, mode string, follow bool, tail int) (string, error) {
	switch mode {
	case "docker":
		logCmd := fmt.Sprintf("cd /opt/frameworks/%s && docker compose logs", deployName)
		if tail > 0 {
			logCmd += fmt.Sprintf(" --tail=%d", tail)
		}
		if follow {
			logCmd += " --follow"
		}
		return logCmd, nil
	case "native":
		logCmd := fmt.Sprintf("journalctl -u frameworks-%s", deployName)
		if tail > 0 {
			logCmd += fmt.Sprintf(" -n %d", tail)
		}
		if follow {
			logCmd += " -f"
		}
		return logCmd, nil
	default:
		return "", fmt.Errorf("unknown service mode: %s (cannot determine log location)", mode)
	}
}

func resolveLogTargets(manifest *inventory.Manifest, serviceName string) ([]logTarget, error) {
	if manifest == nil {
		return nil, fmt.Errorf("manifest is required")
	}

	if serviceName == "postgres" && manifest.Infrastructure.Postgres != nil && manifest.Infrastructure.Postgres.Enabled {
		return infrastructureLogTargets(manifest, serviceName, "postgres", manifest.Infrastructure.Postgres.AllHosts())
	}
	if serviceName == "kafka" && manifest.Infrastructure.Kafka != nil && manifest.Infrastructure.Kafka.Enabled {
		return infrastructureLogTargets(manifest, serviceName, "kafka", kafkaLogHosts(manifest.Infrastructure.Kafka))
	}
	if serviceName == "clickhouse" && manifest.Infrastructure.ClickHouse != nil && manifest.Infrastructure.ClickHouse.Enabled {
		return infrastructureLogTargets(manifest, serviceName, "clickhouse", manifest.Infrastructure.ClickHouse.AllHosts())
	}

	if targets, ok, err := serviceLogTargets(manifest, serviceName, manifest.Services); ok || err != nil {
		return targets, err
	}
	if targets, ok, err := serviceLogTargets(manifest, serviceName, manifest.Interfaces); ok || err != nil {
		return targets, err
	}
	if targets, ok, err := serviceLogTargets(manifest, serviceName, manifest.Observability); ok || err != nil {
		return targets, err
	}

	return nil, fmt.Errorf("service %s not found or not enabled in manifest", serviceName)
}

func serviceLogTargets(manifest *inventory.Manifest, serviceName string, configs map[string]inventory.ServiceConfig) ([]logTarget, bool, error) {
	cfg, ok := configs[serviceName]
	if !ok {
		return nil, false, nil
	}
	if !cfg.Enabled {
		return nil, true, fmt.Errorf("service %s not found or not enabled in manifest", serviceName)
	}
	deployName, err := resolveDeployName(serviceName, cfg)
	if err != nil {
		return nil, true, err
	}
	hostNames := serviceHostNames(cfg)
	if len(hostNames) == 0 {
		return nil, true, fmt.Errorf("service %s has no host(s) in manifest", serviceName)
	}
	targets, err := hostLogTargets(manifest, serviceName, deployName, hostNames)
	return targets, true, err
}

func serviceHostNames(cfg inventory.ServiceConfig) []string {
	if len(cfg.Hosts) > 0 {
		return dedupeStrings(cfg.Hosts)
	}
	if cfg.Host != "" {
		return []string{cfg.Host}
	}
	return nil
}

func infrastructureLogTargets(manifest *inventory.Manifest, serviceName, deployName string, hosts []string) ([]logTarget, error) {
	hosts = dedupeStrings(hosts)
	if len(hosts) == 0 {
		return nil, fmt.Errorf("service %s has no host(s) in manifest", serviceName)
	}
	return hostLogTargets(manifest, serviceName, deployName, hosts)
}

func hostLogTargets(manifest *inventory.Manifest, serviceName, deployName string, hostNames []string) ([]logTarget, error) {
	targets := make([]logTarget, 0, len(hostNames))
	for _, hostName := range hostNames {
		host, ok := manifest.GetHost(hostName)
		if !ok {
			return nil, fmt.Errorf("service %s references unknown host %s", serviceName, hostName)
		}
		host.Name = firstNonEmpty(host.Name, hostName)
		targets = append(targets, logTarget{
			ServiceName: serviceName,
			DeployName:  deployName,
			HostName:    hostName,
			Host:        host,
		})
	}
	return targets, nil
}

func kafkaLogHosts(cfg *inventory.KafkaConfig) []string {
	hosts := make([]string, 0, len(cfg.Brokers)+len(cfg.Controllers))
	for _, controller := range cfg.Controllers {
		hosts = append(hosts, controller.Host)
	}
	for _, broker := range cfg.Brokers {
		hosts = append(hosts, broker.Host)
	}
	for _, regional := range cfg.Regional {
		for _, controller := range regional.Controllers {
			hosts = append(hosts, controller.Host)
		}
		for _, broker := range regional.Brokers {
			hosts = append(hosts, broker.Host)
		}
	}
	if len(hosts) == 0 {
		for _, broker := range cfg.Brokers {
			hosts = append(hosts, broker.Host)
		}
	}
	return hosts
}

func dedupeStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func defaultEdgeManifestPath(clusterManifestPath string) string {
	clusterManifestPath = strings.TrimSpace(clusterManifestPath)
	if clusterManifestPath == "" {
		return ""
	}
	candidate := filepath.Join(filepath.Dir(clusterManifestPath), "edge.yaml")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return ""
}

func runLogsSnapshot(cmd *cobra.Command, manifest *inventory.Manifest, opts logsSnapshotOptions) error {
	if manifest == nil {
		return fmt.Errorf("manifest is required")
	}
	if opts.Parallel <= 0 {
		opts.Parallel = 1
	}
	if opts.Tail < 0 {
		return fmt.Errorf("--tail must be >= 0")
	}
	if strings.TrimSpace(opts.Since) == "" && !opts.Boot {
		return fmt.Errorf("--since is required unless --boot is set")
	}

	hosts, err := logsSnapshotHosts(manifest, opts.EdgeManifest, stringFlag(cmd, "age-key").Value)
	if err != nil {
		return err
	}
	if len(hosts) == 0 {
		return fmt.Errorf("no hosts found in manifest")
	}

	outDir := strings.TrimSpace(opts.OutputDir)
	if outDir == "" {
		outDir, err = os.MkdirTemp("", "frameworks-cluster-logs-*")
		if err != nil {
			return fmt.Errorf("create snapshot directory: %w", err)
		}
	} else {
		if mkErr := os.MkdirAll(outDir, 0o755); mkErr != nil {
			return fmt.Errorf("create snapshot directory: %w", mkErr)
		}
	}
	absOutDir, err := filepath.Abs(outDir)
	if err != nil {
		return fmt.Errorf("resolve snapshot directory: %w", err)
	}

	ux.Heading(cmd.OutOrStdout(), fmt.Sprintf("Collecting log snapshot from %d host(s)", len(hosts)))
	fmt.Fprintf(cmd.OutOrStdout(), "  Output: %s\n", absOutDir)
	if opts.Boot {
		fmt.Fprintln(cmd.OutOrStdout(), "  Window: current boot")
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "  Window: since %s\n", opts.Since)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "  Tail: %d lines per unit\n\n", opts.Tail)

	sshKey := stringFlag(cmd, "ssh-key").Value
	sshPool := ssh.NewPool(45*time.Second, sshKey)
	defer sshPool.Close()

	ctx := cmd.Context()
	g, gCtx := errgroup.WithContext(ctx)
	g.SetLimit(opts.Parallel)
	var (
		mu       sync.Mutex
		failures []string
	)
	for _, target := range hosts {
		target := target
		g.Go(func() error {
			if err := collectLogsSnapshotHost(gCtx, sshPool, sshKey, target, absOutDir, opts); err != nil {
				mu.Lock()
				failures = append(failures, fmt.Sprintf("%s: %v", target.Name, err))
				mu.Unlock()
				fmt.Fprintf(cmd.OutOrStderr(), "  ✗ %s: %v\n", target.Name, err)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "  ✓ %s\n", target.Name)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}
	if len(failures) > 0 {
		sort.Strings(failures)
		summary := strings.Join(failures, "\n")
		if writeErr := os.WriteFile(filepath.Join(absOutDir, "_failures.txt"), []byte(summary+"\n"), 0o644); writeErr != nil {
			return fmt.Errorf("write failure summary: %w", writeErr)
		}
		return fmt.Errorf("snapshot completed with %d host failure(s); see %s", len(failures), filepath.Join(absOutDir, "_failures.txt"))
	}
	ux.Success(cmd.OutOrStdout(), "Log snapshot complete")
	fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", absOutDir)
	return nil
}

func logsSnapshotHosts(manifest *inventory.Manifest, edgeManifestPath, ageKeyFile string) ([]logsSnapshotHost, error) {
	seen := make(map[string]struct{}, len(manifest.Hosts))
	names := make([]string, 0, len(manifest.Hosts))
	for name := range manifest.Hosts {
		names = append(names, name)
	}
	sort.Strings(names)

	hosts := make([]logsSnapshotHost, 0, len(names))
	for _, name := range names {
		host := manifest.Hosts[name]
		host.Name = firstNonEmpty(host.Name, name)
		hosts = append(hosts, logsSnapshotHost{Name: name, Host: host})
		seen[name] = struct{}{}
	}

	edgeManifestPath = strings.TrimSpace(edgeManifestPath)
	if edgeManifestPath == "" {
		return hosts, nil
	}
	edgeManifest, err := inventory.LoadEdgeWithHosts(edgeManifestPath, ageKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load edge manifest: %w", err)
	}
	for _, node := range edgeManifest.Nodes {
		if _, ok := seen[node.Name]; ok {
			continue
		}
		host, err := edgeNodeSnapshotHost(node)
		if err != nil {
			return nil, err
		}
		hosts = append(hosts, logsSnapshotHost{Name: node.Name, Host: host})
		seen[node.Name] = struct{}{}
	}
	sort.SliceStable(hosts, func(i, j int) bool {
		return hosts[i].Name < hosts[j].Name
	})
	return hosts, nil
}

func edgeNodeSnapshotHost(node inventory.EdgeNode) (inventory.Host, error) {
	user, addr, err := splitSSHTarget(node.SSH)
	if err != nil {
		return inventory.Host{}, fmt.Errorf("edge node %s: %w", node.Name, err)
	}
	return inventory.Host{
		Name:       node.Name,
		User:       user,
		ExternalIP: firstNonEmpty(node.ExternalIP, addr),
	}, nil
}

func splitSSHTarget(target string) (string, string, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", "", fmt.Errorf("ssh target is required")
	}
	user := "root"
	host := target
	if at := strings.LastIndex(target, "@"); at >= 0 {
		user = strings.TrimSpace(target[:at])
		host = strings.TrimSpace(target[at+1:])
	}
	if user == "" || host == "" {
		return "", "", fmt.Errorf("invalid ssh target %q", target)
	}
	return user, host, nil
}

func collectLogsSnapshotHost(ctx context.Context, pool *ssh.Pool, sshKey string, target logsSnapshotHost, outputDir string, opts logsSnapshotOptions) error {
	host := target.Host
	var runner ssh.Runner
	var err error
	if host.ExternalIP == "" || host.ExternalIP == "localhost" || host.ExternalIP == "127.0.0.1" {
		runner = ssh.NewLocalRunner("")
	} else {
		runner, err = pool.Get(&ssh.ConnectionConfig{
			Address:  host.ExternalIP,
			Port:     22,
			User:     host.User,
			KeyPath:  sshKey,
			HostName: host.Name,
			Timeout:  45 * time.Second,
		})
		if err != nil {
			return fmt.Errorf("connect: %w", err)
		}
	}

	result, err := runner.Run(ctx, logsSnapshotScript(opts))
	if err != nil {
		return fmt.Errorf("collect logs: %w", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# host: %s\n", target.Name)
	fmt.Fprintf(&b, "# address: %s\n", firstNonEmpty(host.ExternalIP, "local"))
	fmt.Fprintf(&b, "# exit_code: %d\n", result.ExitCode)
	fmt.Fprintf(&b, "# collected_at: %s\n\n", time.Now().UTC().Format(time.RFC3339))
	b.WriteString(result.Stdout)
	if strings.TrimSpace(result.Stderr) != "" {
		b.WriteString("\n== stderr ==\n")
		b.WriteString(result.Stderr)
		if !strings.HasSuffix(result.Stderr, "\n") {
			b.WriteString("\n")
		}
	}

	path := filepath.Join(outputDir, safeSnapshotFilename(target.Name)+".log")
	if writeErr := os.WriteFile(path, []byte(b.String()), 0o644); writeErr != nil {
		return fmt.Errorf("write %s: %w", path, writeErr)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("remote snapshot exited with code %d", result.ExitCode)
	}
	return nil
}

func safeSnapshotFilename(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "host"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

func logsSnapshotScript(opts logsSnapshotOptions) string {
	boot := "0"
	if opts.Boot {
		boot = "1"
	}
	return fmt.Sprintf(`set +e
SINCE=%s
BOOT=%s
TAIL=%d
run_journal() {
  if command -v sudo >/dev/null 2>&1 && sudo -n true >/dev/null 2>&1; then
    sudo journalctl "$@"
  else
    journalctl "$@"
  fi
}
journal_unit() {
  unit="$1"
  if [ "$TAIL" -gt 0 ]; then
    if [ "$BOOT" = "1" ]; then
      run_journal -u "$unit" -b -n "$TAIL" --no-pager -o short-iso 2>&1
    else
      run_journal -u "$unit" "--since=${SINCE}" -n "$TAIL" --no-pager -o short-iso 2>&1
    fi
  else
    if [ "$BOOT" = "1" ]; then
      run_journal -u "$unit" -b --no-pager -o short-iso 2>&1
    else
      run_journal -u "$unit" "--since=${SINCE}" --no-pager -o short-iso 2>&1
    fi
  fi
}
redact_snapshot_secrets() {
  sed -E \
    -e 's/(password:[[:space:]]*).*/\1[redacted]/I' \
    -e 's/(password=).*/\1[redacted]/I' \
    -e 's/(VMAGENT_REMOTE_WRITE_BASIC_AUTH_PASSWORD=).*/\1[redacted]/' \
    -e 's/(-remoteWrite\.basicAuth\.password=)[^[:space:]]+/\1[redacted]/g' \
    -e 's/(bearerToken=)[^[:space:]]+/\1[redacted]/Ig' \
    -e 's/(Authorization:[[:space:]]*Bearer[[:space:]]+)[A-Za-z0-9._=+-]+/\1[redacted]/Ig'
}
echo "== host =="
hostname -f 2>/dev/null || hostname
date -u +"%%Y-%%m-%%dT%%H:%%M:%%SZ"
echo "== boot =="
uptime 2>/dev/null || true
who -b 2>/dev/null || true
echo "== resources =="
free -h 2>/dev/null || true
df -h / /var /var/lib 2>/dev/null || true
echo "== failed units =="
systemctl --failed --no-pager 2>/dev/null || true
echo "== listeners =="
ss -ltnp 2>/dev/null | sed -n '1,120p' || true
echo "== frameworks units =="
units="$(
  {
    systemctl list-units --all --type=service 'frameworks-*' --no-legend --no-pager 2>/dev/null | awk '{print $1}'
    systemctl list-unit-files 'frameworks-*' --type=service --no-legend --no-pager 2>/dev/null | awk '{print $1}'
  } | sort -u
)"
if [ -z "$units" ]; then
  echo "(none)"
fi
printf '%%s\n' "$units"
for unit in $units; do
  echo
  echo "== ${unit} status =="
  systemctl show "$unit" -p Id -p LoadState -p ActiveState -p SubState -p ExecMainStatus -p MainPID -p NRestarts --no-page 2>/dev/null || true
  echo "-- recent suspicious lines --"
  journal_unit "$unit" | grep -Ei 'error|warn|failed|fatal|panic|x509|deadline|denied|unavailable|mismatch|resolver|no healthy|timeout|bootstrap|decklog|foghorn|navigator|privateer|quartermaster|skipper|embedding' | tail -n 80 || true
  echo "-- journal --"
  journal_unit "$unit" || true
done
echo
echo "== durable trigger WAL diagnostics =="
if systemctl list-unit-files frameworks-helmsman.service --no-legend --no-pager >/dev/null 2>&1 || systemctl list-units frameworks-helmsman.service --all --no-legend --no-pager >/dev/null 2>&1; then
  systemctl show frameworks-helmsman.service -p ActiveState -p SubState -p MainPID -p ExecMainStatus -p NRestarts --no-page 2>/dev/null || true
  echo "-- trigger WAL admin snapshot --"
  if command -v curl >/dev/null 2>&1; then
    curl -fsS --max-time 2 http://127.0.0.1:18007/triggers/wal 2>&1 || true
    echo
  fi
  echo "-- trigger ack failure context --"
  journal_unit frameworks-helmsman.service | grep -Ei 'trigger ack|MistTriggerAck|source_event_id|retryable|dead-letter|WAL|timeout|foghorn|unavailable|failed' | tail -n 160 || true
else
  echo "(frameworks-helmsman.service not installed)"
fi
echo
echo "== privateer sync diagnostics =="
if systemctl list-unit-files frameworks-privateer.service --no-legend --no-pager >/dev/null 2>&1 || systemctl list-units frameworks-privateer.service --all --no-legend --no-pager >/dev/null 2>&1; then
  systemctl show frameworks-privateer.service -p ActiveState -p SubState -p MainPID -p ExecMainStatus -p NRestarts --no-page 2>/dev/null || true
  echo "-- privateer safe env --"
  env_files="$(
    systemctl show frameworks-privateer.service -p EnvironmentFiles --value --no-page 2>/dev/null |
      tr ' ' '\n' |
      sed -E 's/^-//; s/;.*$//' |
      awk 'NF'
  )"
  if [ -z "$env_files" ]; then
    env_files="$(systemctl cat frameworks-privateer.service 2>/dev/null | sed -nE 's/^EnvironmentFile=-?([^ ;]+).*/\1/p')"
  fi
  if [ -z "$env_files" ]; then
    env_files="$(find /etc/frameworks -maxdepth 3 -type f -name '*privateer*.env' 2>/dev/null | sort)"
  fi
  if [ -z "$env_files" ]; then
    echo "(no privateer environment files found)"
  else
    for env_file in $env_files; do
      [ -f "$env_file" ] || continue
      echo "### $env_file"
      grep -E '^(QUARTERMASTER_GRPC_ADDR|NAVIGATOR_GRPC_ADDR|PRIVATEER_(PORT|SYNC_INTERVAL|SYNC_TIMEOUT|CERT_SYNC_INTERVAL|DATA_DIR|STATIC_PEERS_FILE)|NODE_|CLUSTER_ID)=' "$env_file" 2>/dev/null || true
    done
  fi
  echo "-- privateer sync errors --"
  journal_unit frameworks-privateer.service | grep -Ei 'SyncMesh|sync infrastructure|sync after node registration|node registration|mesh revision|last-known|deadline|timeout|unavailable|failed' | tail -n 120 || true
  echo "-- privateer health --"
  if command -v curl >/dev/null 2>&1; then
    curl -fsS --max-time 2 http://127.0.0.1:18012/health 2>&1 || true
    echo
    curl -fsS --max-time 2 http://127.0.0.1:18012/metrics 2>/dev/null | grep -Ei 'privateer|mesh|sync|wireguard' | tail -n 80 || true
  fi
  echo "-- wireguard --"
  if command -v wg >/dev/null 2>&1; then
    wg show 2>&1 || true
  fi
else
  echo "(frameworks-privateer.service not installed)"
fi
echo
echo "== telemetry remote write diagnostics =="
for unit in vmauth.service vmagent.service frameworks-vmagent-edge.service; do
  if systemctl list-unit-files "$unit" --no-legend --no-pager >/dev/null 2>&1 || systemctl list-units "$unit" --all --no-legend --no-pager >/dev/null 2>&1; then
    echo "-- ${unit} status --"
    systemctl show "$unit" -p Id -p LoadState -p ActiveState -p SubState -p MainPID -p ExecMainStatus -p NRestarts --no-page 2>/dev/null || true
    echo "-- ${unit} unit --"
    systemctl cat "$unit" 2>/dev/null | redact_snapshot_secrets || true
    echo "-- ${unit} remote write/backend lines --"
    journal_unit "$unit" | grep -Ei 'remote.?write|backend|vmauth|vmagent|victoria|unavailable|502|401|403|5[0-9][0-9]|error|warn|failed|timeout' | tail -n 120 || true
    echo "-- ${unit} backend failure context --"
    journal_unit "$unit" | grep -Ei 'all the [0-9]+ backends|backend.*(unavailable|failed|error)|dial|connect|upstream|proxying|api/v1/write|connection refused|no route|timeout|deadline|502|503' | tail -n 160 || true
  fi
done
echo "-- telemetry configs --"
for conf in /etc/vmauth/config.yml /etc/vmagent/scrape.yml /etc/vmagent/vmagent.env /etc/frameworks/vmagent-edge.yml; do
  [ -f "$conf" ] || continue
  echo "### $conf"
  sed -n '1,180p' "$conf" 2>/dev/null | redact_snapshot_secrets || true
done
if command -v curl >/dev/null 2>&1; then
  echo "-- local vmagent metrics --"
  curl -fsS --max-time 2 http://127.0.0.1:8429/metrics 2>/dev/null | grep -Ei 'vmagent_remotewrite|remote_write|vmauth|queue|dropped|retries|errors' | tail -n 100 || true
  echo "-- local vmauth metrics --"
  curl -fsS --max-time 2 http://127.0.0.1:8427/metrics 2>/dev/null | grep -Ei 'vmauth|backend|requests|errors|upstream' | tail -n 100 || true
fi
echo
echo "== redis sentinel diagnostics =="
sentinel_units="$(
  {
    systemctl list-units --all --type=service 'frameworks-redis-*sentinel*.service' --no-legend --no-pager 2>/dev/null | awk '{print $1}'
    systemctl list-unit-files 'frameworks-redis-*sentinel*.service' --type=service --no-legend --no-pager 2>/dev/null | awk '{print $1}'
  } | sort -u
)"
if [ -z "$sentinel_units" ]; then
  echo "(no named Redis Sentinel units)"
else
  printf '%%s\n' "$sentinel_units"
  for unit in $sentinel_units; do
    echo
    echo "-- ${unit} status --"
    systemctl show "$unit" -p Id -p LoadState -p ActiveState -p SubState -p ExecMainStatus -p MainPID -p NRestarts --no-page 2>/dev/null || true
    echo "-- ${unit} recent sentinel lines --"
    journal_unit "$unit" | grep -Ei 'sentinel|master|sdown|odown|failover|tilt|error|warn|denied|auth|timeout|connection|failed' | tail -n 80 || true
  done
fi
echo "-- sentinel listeners --"
ss -ltnp 2>/dev/null | grep ':26379' || true
echo "-- sentinel configs --"
for conf in /var/lib/frameworks/redis/*-sentinel/sentinel.conf; do
  [ -f "$conf" ] || continue
  echo "### $conf"
  grep -E '^(port|bind|protected-mode|sentinel monitor|sentinel down-after-milliseconds|sentinel failover-timeout)' "$conf" 2>/dev/null || true
done
if command -v redis-cli >/dev/null 2>&1; then
  for port in $(ss -ltn 2>/dev/null | awk '/:26379 / {print $4}' | sed -E 's/.*:([0-9]+)$/\1/' | sort -u); do
    echo "-- redis-cli sentinel probe port ${port} --"
    redis-cli -p "$port" INFO sentinel 2>&1 | sed -n '1,80p' || true
    redis-cli -p "$port" SENTINEL masters 2>&1 | sed -n '1,120p' || true
  done
fi
echo
echo "== pki service certificate pairs =="
if [ -d /etc/frameworks/pki/services ]; then
  for dir in /etc/frameworks/pki/services/*; do
    [ -d "$dir" ] || continue
    svc="$(basename "$dir")"
    cert="$dir/tls.crt"
    key="$dir/tls.key"
    if [ -f "$cert" ] && [ -f "$key" ]; then
      cert_hash="$(openssl x509 -in "$cert" -pubkey -noout 2>/dev/null | openssl pkey -pubin -outform der 2>/dev/null | sha256sum 2>/dev/null | awk '{print $1}')"
      key_hash="$(openssl pkey -in "$key" -pubout -outform der 2>/dev/null | sha256sum 2>/dev/null | awk '{print $1}')"
      status="match"
      [ -n "$cert_hash" ] && [ "$cert_hash" = "$key_hash" ] || status="mismatch"
      echo "$svc $status cert=$cert_hash key=$key_hash"
    fi
  done
fi
exit 0
`, ssh.ShellQuote(opts.Since), boot, opts.Tail)
}

func streamLogCommand(ctx context.Context, host inventory.Host, sshKey, command string) error {
	if host.ExternalIP == "" || host.ExternalIP == "localhost" || host.ExternalIP == "127.0.0.1" {
		c := exec.CommandContext(ctx, "sh", "-c", command)
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		return c.Run()
	}

	cfg := &ssh.ConnectionConfig{
		Address:  host.ExternalIP,
		Port:     22,
		User:     host.User,
		KeyPath:  sshKey,
		HostName: host.Name,
		Timeout:  30 * time.Second,
	}
	resolver := &ssh.DefaultResolver{}
	resolution, err := resolver.Resolve(ctx, cfg)
	if err != nil {
		return fmt.Errorf("resolve ssh target: %w", err)
	}
	args := ssh.BuildSSHArgs(cfg, resolution)
	args = append(args, resolution.Target, "sh", "-c", ssh.ShellQuote(command))
	c := exec.CommandContext(ctx, "ssh", args...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}
