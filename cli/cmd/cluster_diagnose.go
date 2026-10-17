package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
	"frameworks/cli/pkg/system"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"

	"github.com/spf13/cobra"
)

// newClusterDiagnoseCmd creates the diagnose command
func newClusterDiagnoseCmd() *cobra.Command {
	opts := diagnoseOptions{Since: "4 hours ago", WindowHours: 24}
	cmd := &cobra.Command{
		Use:   "diagnose <component>",
		Short: "Run diagnostics on cluster components",
		Long: `Run diagnostic checks on cluster components.

Supported diagnostics:
  network    - Test network connectivity between hosts, edges included
  resources  - Check CPU, iowait, load, memory, and every data filesystem on all hosts and edges
  ports      - Check every port the manifest places on a host has a listener
  crashes    - Report unit failures, automatic restarts, core dumps, segfaults, and OOM kills since --since
  kafka      - Check Kafka cluster health, topic lag, broker status, KRaft quorum and leadership losses since --since
  dns        - Check Privateer serves only .internal (wg0 routing, forwarded public queries)
  media      - Capture media/DNS/federation service state on hosts and edges without provisioning
  media-authority - Trace media-authority refresh, versioning, delivery, and apply state
  dvr <dvr-hash>  - Show a DVR recording's segments, chapters, and finalize queue on its cell

Diagnostics help troubleshoot issues and identify problems before they
cause outages.`,
		Example: `  frameworks cluster diagnose network
  frameworks cluster diagnose resources
  frameworks cluster diagnose crashes --since "24 hours ago"
  frameworks cluster diagnose kafka
  frameworks cluster diagnose dns
  frameworks cluster diagnose media
  frameworks cluster diagnose media-authority --window-hours 6`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			return runDiagnose(cmd, rc, args[0], opts)
		},
	}
	cmd.Flags().StringVar(&opts.StreamID, "stream-id", "", "Stream ID to trace in media diagnostics")
	cmd.Flags().StringVar(&opts.TenantID, "tenant-id", "", "Tenant ID to include in stream database probes")
	cmd.Flags().StringVar(&opts.Since, "since", opts.Since, "Journal time window for media, kafka, and crashes diagnostics")
	cmd.Flags().IntVar(&opts.WindowHours, "window-hours", opts.WindowHours, "Database lookback in hours for media-authority diagnostics")
	cmd.Flags().StringVar(&opts.EdgeManifest, "edge-manifest", "", "Edge manifest whose nodes host-level diagnostics also probe (default: edge.yaml beside the cluster manifest)")
	cmd.AddCommand(newClusterDiagnoseDVRCmd())

	return cmd
}

type diagnoseOptions struct {
	StreamID     string
	TenantID     string
	Since        string
	WindowHours  int
	OutputJSON   bool
	EdgeManifest string
}

// runDiagnose executes diagnostic checks against an already-resolved cluster.
func runDiagnose(cmd *cobra.Command, rc *resolvedCluster, component string, opts diagnoseOptions) error {
	manifest := rc.Manifest
	jsonMode := output == "json"
	if jsonMode && component != "media" && component != "media-authority" {
		return fmt.Errorf("--output json is currently supported for media and media-authority diagnostics only")
	}
	if !jsonMode {
		ux.Heading(cmd.OutOrStdout(), fmt.Sprintf("Running %s diagnostics", component))
	}
	opts.OutputJSON = jsonMode
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Create SSH pool
	sshKey := stringFlag(cmd, "ssh-key").Value
	sshPool := ssh.NewPool(30*time.Second, sshKey)
	defer sshPool.Close()

	// Execute diagnostic based on component
	runnerFor := func(host inventory.Host) (ssh.Runner, error) { return getRunner(host, sshPool) }
	switch component {
	case "network", "resources", "ports", "crashes":
		targets := diagnoseHostTargets(cmd.ErrOrStderr(), rc, opts.EdgeManifest)
		var failures int
		switch component {
		case "network":
			failures = diagnoseNetwork(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr(), targets, runnerFor)
		case "resources":
			failures = diagnoseResources(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr(), targets, runnerFor)
		case "crashes":
			failures = diagnoseCrashes(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr(), targets, opts.Since, runnerFor)
		default:
			failures = diagnosePorts(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr(), manifest, targets, runnerFor)
		}
		if failures > 0 {
			return fmt.Errorf("%s diagnostics found %d problem(s)", component, failures)
		}
		return nil
	case "kafka":
		return diagnoseKafka(ctx, cmd, manifest, runnerFor, opts.Since)
	case "dns":
		failures := checkPrivateerDNSScope(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr(), manifest, func(host inventory.Host) (ssh.Runner, error) {
			return getRunner(host, sshPool)
		}, privateerDNSObservationWindow)
		if failures > 0 {
			return fmt.Errorf("privateer dns diagnostics found %d host(s) with a problem", failures)
		}
		return nil
	case "media":
		return diagnoseMedia(ctx, cmd, manifest, diagnoseHostTargets(cmd.ErrOrStderr(), rc, opts.EdgeManifest), runnerFor, opts)
	case "media-authority":
		return diagnoseMediaAuthority(ctx, cmd, rc, sshPool, opts)
	default:
		return fmt.Errorf("unknown component: %s (must be network, resources, ports, crashes, kafka, dns, media, or media-authority)", component)
	}
}

func buildStandardPorts() map[int]string {
	standardPorts := map[int]string{
		53:    "privateer-dns",
		18019: "foghorn-control",
		18029: "foghorn-external-grpc",
	}

	ids := make([]string, 0, len(servicedefs.Services))
	for id := range servicedefs.Services {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		svc := servicedefs.Services[id]
		if svc.DefaultPort != 0 {
			if _, exists := standardPorts[svc.DefaultPort]; !exists {
				standardPorts[svc.DefaultPort] = id
			}
		}
		if grpcPort, ok := servicedefs.DefaultGRPCPort(id); ok {
			if _, exists := standardPorts[grpcPort]; !exists {
				standardPorts[grpcPort] = fmt.Sprintf("%s-grpc", id)
			}
		}
	}

	return standardPorts
}

// diagnoseKafka checks Kafka cluster health
func diagnoseKafka(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, runnerFor func(inventory.Host) (ssh.Runner, error), since string) error {
	if manifest.Infrastructure.Kafka == nil || !manifest.Infrastructure.Kafka.Enabled {
		return fmt.Errorf("kafka not enabled in manifest")
	}

	fmt.Fprintln(cmd.OutOrStdout(), "Kafka Diagnostics")
	clusters := allKafkaClusters(manifest)
	if len(clusters) == 0 {
		return fmt.Errorf("no kafka brokers configured")
	}
	failures := 0
	for _, cluster := range clusters {
		region := strings.TrimSpace(cluster.RegionID)
		if region == "" {
			region = cluster.Role
		}
		if len(cluster.Brokers) == 0 {
			failures++
			ux.Fail(cmd.ErrOrStderr(), fmt.Sprintf("%s: no Kafka brokers configured", region))
			continue
		}
		broker := cluster.Brokers[0]
		host, found := manifest.GetHost(broker.Host)
		if !found {
			failures++
			ux.Fail(cmd.ErrOrStderr(), fmt.Sprintf("%s: broker host not found: %s", region, broker.Host))
			continue
		}
		runner, runnerErr := runnerFor(host)
		if runnerErr != nil {
			failures++
			ux.Fail(cmd.ErrOrStderr(), fmt.Sprintf("%s: connect to %s: %v", region, broker.Host, runnerErr))
			continue
		}
		port := broker.Port
		if port == 0 {
			port = 9092
		}
		fmt.Fprintf(cmd.OutOrStdout(), "\nCluster: %s (broker: %s)\n", region, broker.Host)
		for _, check := range kafkaDiagnosticCommands(manifest.Infrastructure.Kafka.Mode, kafkaLocalBootstrap(host, port)) {
			fmt.Fprintf(cmd.OutOrStdout(), "\n%s:\n", check.Heading)
			result, runErr := runner.Run(ctx, check.Command)
			if runErr != nil || result == nil || result.ExitCode != 0 {
				failures++
				ux.Fail(cmd.ErrOrStderr(), fmt.Sprintf("%s: %s: %s", region, check.Failure, diagnosticCommandError(result, runErr)))
				continue
			}
			if check.Success != "" {
				ux.Success(cmd.OutOrStdout(), check.Success)
			}
			if strings.TrimSpace(result.Stdout) != "" && (check.Success == "" || verbose) {
				fmt.Fprintln(cmd.OutOrStdout(), result.Stdout)
			}
		}
		fmt.Fprintf(cmd.OutOrStdout(), "\nKRaft Metadata Quorum (leadership losses since %s):\n", since)
		quorum := checkKafkaQuorum(ctx, manifest, cluster, manifest.Infrastructure.Kafka.Mode, since, runnerFor)
		printHealthResult(cmd, "quorum", quorum)
		if !quorum.OK {
			failures++
		}
	}
	failures += checkMirrorMakerTopicCoverage(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr(), manifest, runnerFor)
	if result := checkMirrorMakerWorkers(ctx, manifest, runnerFor); result != nil {
		fmt.Fprintln(cmd.OutOrStdout(), "\nMirrorMaker2 workers:")
		printHealthResult(cmd, "workers", result)
		if !result.OK {
			failures++
		}
	}
	if failures > 0 {
		return fmt.Errorf("kafka diagnostics detected %d failed check(s)", failures)
	}
	return nil
}

type kafkaDiagnosticCheck struct {
	Heading, Command, Success, Failure string
}

// kafkaLocalBootstrap is the address of the broker on host as the host itself
// dials it: the WireGuard address its listener binds (kafkaRoleVars), or
// loopback on a single host without one.
func kafkaLocalBootstrap(host inventory.Host, port int) string {
	return net.JoinHostPort(kafkaListenAddress(host), strconv.Itoa(port))
}

// kafkaListenAddress is the address a broker on host binds: its WireGuard
// address, or loopback on a single host without one (kafkaRoleVars).
func kafkaListenAddress(host inventory.Host) string {
	if address := strings.TrimSpace(host.WireguardIP); address != "" {
		return address
	}
	return "127.0.0.1"
}

// kafkaToolCommand runs a Kafka CLI tool against the broker on this host at
// bootstrap (kafkaLocalBootstrap).
func kafkaToolCommand(mode, bootstrap, tool, args string) string {
	if strings.EqualFold(strings.TrimSpace(mode), "docker") {
		return system.DockerCommand(fmt.Sprintf("compose -f /opt/frameworks/kafka/docker-compose.yml exec -T kafka %s --bootstrap-server %s %s", tool, bootstrap, args))
	}
	return fmt.Sprintf("/opt/kafka/bin/%s.sh --bootstrap-server %s %s", tool, bootstrap, args)
}

func kafkaDiagnosticCommands(mode, bootstrap string) []kafkaDiagnosticCheck {
	command := func(tool, args string) string {
		return kafkaToolCommand(mode, bootstrap, tool, args)
	}
	return []kafkaDiagnosticCheck{
		{Heading: "Topics", Command: command("kafka-topics", "--list"), Failure: "Failed to list topics"},
		{Heading: "Consumer Groups", Command: command("kafka-consumer-groups", "--list"), Failure: "Failed to list consumer groups"},
		{Heading: "Consumer Lag", Command: command("kafka-consumer-groups", "--describe --all-groups"), Failure: "Failed to describe consumer lag"},
		{Heading: "Broker Status", Command: command("kafka-broker-api-versions", ""), Success: "Broker is responding", Failure: "Broker is not responding"},
	}
}

func diagnosticCommandError(result *ssh.CommandResult, err error) string {
	if result != nil {
		detail := strings.TrimSpace(result.Stderr)
		if detail != "" {
			return fmt.Sprintf("exit %d (%s)", result.ExitCode, detail)
		}
		if err != nil {
			return fmt.Sprintf("exit %d", result.ExitCode)
		}
		return fmt.Sprintf("exit %d (no stderr)", result.ExitCode)
	}
	if err != nil {
		return err.Error()
	}
	return "command returned no result"
}

type mediaDiagnosticHostReport struct {
	Host     string   `json:"host"`
	Address  string   `json:"address,omitempty"`
	Services []string `json:"services"`
	Stdout   string   `json:"stdout,omitempty"`
	Stderr   string   `json:"stderr,omitempty"`
	ExitCode int      `json:"exit_code"`
	Error    string   `json:"error,omitempty"`
}

type mediaDiagnosticReport struct {
	StreamID string                      `json:"stream_id,omitempty"`
	TenantID string                      `json:"tenant_id,omitempty"`
	Since    string                      `json:"since"`
	Hosts    []mediaDiagnosticHostReport `json:"hosts"`
}

func diagnoseMedia(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, targets []diagnoseHost, runnerFor func(inventory.Host) (ssh.Runner, error), opts diagnoseOptions) error {
	if !opts.OutputJSON {
		fmt.Fprintln(cmd.OutOrStdout(), "Media/DNS/Federation Diagnostics")
		fmt.Fprintln(cmd.OutOrStdout(), "Read-only host probes for service state, listeners, TLS SANs, and recent error logs.")
		if opts.StreamID != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "Tracing stream_id=%s\n", opts.StreamID)
		}
	}

	hostServices := mediaDiagnosticHostServices(manifest)
	edgeHosts := map[string]inventory.Host{}
	for _, target := range targets {
		if target.Edge {
			edgeHosts[target.Name] = target.Host
			hostServices[target.Name] = append([]string(nil), mediaDiagnosticEdgeServices...)
		}
	}
	if len(hostServices) == 0 {
		return fmt.Errorf("no media diagnostic service placements found in manifest")
	}

	hosts := make([]string, 0, len(hostServices))
	for hostName := range hostServices {
		hosts = append(hosts, hostName)
	}
	sort.Strings(hosts)

	report := mediaDiagnosticReport{StreamID: opts.StreamID, TenantID: opts.TenantID, Since: opts.Since}
	failures := 0
	for _, hostName := range hosts {
		hostReport := mediaDiagnosticHostReport{Host: hostName, Services: append([]string(nil), hostServices[hostName]...), ExitCode: -1}
		sort.Strings(hostReport.Services)
		host, ok := manifest.GetHost(hostName)
		_, edge := edgeHosts[hostName]
		if edge {
			host, ok = edgeHosts[hostName], true
		}
		ports := mediaDiagnosticPorts()
		if edge {
			ports = mediaDiagnosticEdgePorts()
		}
		if !ok {
			hostReport.Error = "host missing from manifest"
			report.Hosts = append(report.Hosts, hostReport)
			failures++
			if !opts.OutputJSON {
				ux.Fail(cmd.ErrOrStderr(), fmt.Sprintf("%s: %s", hostName, hostReport.Error))
			}
			continue
		}
		hostReport.Address = host.ExternalIP

		if !opts.OutputJSON {
			fmt.Fprintf(cmd.OutOrStdout(), "\nHost: %s (%s)\n", hostName, host.ExternalIP)
			fmt.Fprintf(cmd.OutOrStdout(), "Services: %s\n", strings.Join(hostReport.Services, ", "))
		}

		runner, err := runnerFor(host)
		if err != nil {
			hostReport.Error = err.Error()
			report.Hosts = append(report.Hosts, hostReport)
			failures++
			if !opts.OutputJSON {
				ux.Fail(cmd.ErrOrStderr(), fmt.Sprintf("Cannot connect to %s: %v", hostName, err))
			}
			continue
		}

		probe := mediaDiagnosticScript(hostReport.Services, ports, opts)
		if edge {
			probe += mediaDiagnosticEdgeContainerScript
		}
		result, err := runner.Run(ctx, "sh -lc "+ssh.ShellQuote(probe))
		if result != nil {
			hostReport.Stdout = strings.TrimSpace(result.Stdout)
			hostReport.Stderr = strings.TrimSpace(result.Stderr)
			hostReport.ExitCode = result.ExitCode
		}
		if err != nil {
			hostReport.Error = "diagnostic probe failed: " + diagnosticCommandError(result, err)
			failures++
		} else if result == nil {
			hostReport.Error = "diagnostic probe returned no result"
			failures++
		} else if result.ExitCode != 0 {
			hostReport.Error = fmt.Sprintf("diagnostic probe exited %d", result.ExitCode)
			failures++
		}
		report.Hosts = append(report.Hosts, hostReport)
		if !opts.OutputJSON {
			if hostReport.Stdout != "" {
				fmt.Fprintln(cmd.OutOrStdout(), hostReport.Stdout)
			}
			if hostReport.Stderr != "" {
				fmt.Fprintln(cmd.ErrOrStderr(), hostReport.Stderr)
			}
			if hostReport.Error != "" {
				ux.Fail(cmd.ErrOrStderr(), fmt.Sprintf("%s: %s", hostName, hostReport.Error))
			}
		}
	}

	if opts.OutputJSON {
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), "\nNext focused checks:")
		fmt.Fprintln(cmd.OutOrStdout(), "  - Compare service-cluster assignments with admin service-pool status for foghorn/chandler/livepeer-gateway.")
		fmt.Fprintln(cmd.OutOrStdout(), "  - If Chartroom stream reads still fail, inspect Commodore logs and DB row for the stream ID.")
		fmt.Fprintln(cmd.OutOrStdout(), "  - If Bunny zones remain empty, inspect Navigator logs after Quartermaster reports healthy media services.")
	}
	if failures > 0 {
		return fmt.Errorf("media diagnostics failed on %d host(s)", failures)
	}
	return nil
}

func mediaDiagnosticHostServices(manifest *inventory.Manifest) map[string][]string {
	out := map[string][]string{}
	if manifest == nil {
		return out
	}
	serviceTypes := map[string]struct{}{
		"bridge":             {},
		"chandler":           {},
		"commodore":          {},
		"decklog":            {},
		"foghorn":            {},
		"livepeer-gateway":   {},
		"navigator":          {},
		"periscope-metering": {},
		"periscope-query":    {},
		"quartermaster":      {},
		"signalman":          {},
	}

	for name, svc := range manifest.Services {
		if !svc.Enabled {
			continue
		}
		serviceType := strings.TrimSpace(svc.Deploy)
		if serviceType == "" {
			serviceType = name
		}
		if _, ok := serviceTypes[serviceType]; !ok {
			continue
		}
		for _, host := range serviceHosts(svc) {
			host = strings.TrimSpace(host)
			if host == "" {
				continue
			}
			out[host] = appendUniqueString(out[host], serviceType)
		}
	}
	return out
}

func mediaDiagnosticPorts() []int {
	serviceTypes := []string{
		"bridge",
		"chandler",
		"commodore",
		"decklog",
		"foghorn",
		"livepeer-gateway",
		"navigator",
		"periscope-metering",
		"periscope-query",
		"quartermaster",
		"signalman",
	}
	seen := map[int]struct{}{}
	var ports []int
	for _, serviceType := range serviceTypes {
		if svc, ok := servicedefs.Services[serviceType]; ok && svc.DefaultPort != 0 {
			if _, exists := seen[svc.DefaultPort]; !exists {
				seen[svc.DefaultPort] = struct{}{}
				ports = append(ports, svc.DefaultPort)
			}
		}
		if port, ok := servicedefs.DefaultGRPCPort(serviceType); ok && port != 0 {
			if _, exists := seen[port]; !exists {
				seen[port] = struct{}{}
				ports = append(ports, port)
			}
		}
	}
	sort.Ints(ports)
	return ports
}

// mediaDiagnosticEdgeServices are the units of a native edge; the probe runs
// frameworks-<name>.service for each.
var mediaDiagnosticEdgeServices = []string{"caddy", "helmsman", "mistserver"}

// mediaDiagnosticEdgePorts are the TCP listeners of the edge bundle, which the
// media probe lists with `ss -ltnp`.
func mediaDiagnosticEdgePorts() []int {
	var ports []int
	for _, l := range edgeBundleListeners("container", false) {
		if l.Proto == "tcp" {
			ports = append(ports, l.Port)
		}
	}
	sort.Ints(ports)
	return slices.Compact(ports)
}

// mediaDiagnosticEdgeContainerScript reports a container-mode edge, whose
// services run inside the single frameworks-edge container rather than as
// units. It reuses SINCE from mediaDiagnosticScript.
const mediaDiagnosticEdgeContainerScript = `
fw_docker() { if docker version >/dev/null 2>&1; then docker "$@"; else sudo -n docker "$@"; fi; }
if command -v docker >/dev/null 2>&1 && fw_docker inspect frameworks-edge >/dev/null 2>&1; then
  echo "== frameworks-edge container =="
  fw_docker inspect -f 'status={{.State.Status}} restarts={{.RestartCount}} started={{.State.StartedAt}} oom_killed={{.State.OOMKilled}}' frameworks-edge 2>/dev/null || true
  echo "-- suspicious container log lines since ${SINCE} (count, most frequent first) --"
  since_epoch=$(date -d "${SINCE}" +%s 2>/dev/null || echo 0)
  fw_docker logs --since "${since_epoch}" frameworks-edge 2>&1 \
    | grep -Ei 'error|warn|failed|x509|deadline|panic|segfault' \
    | sed -E 's/"time":"[^"]+",?//g; s/,"time":"[^"]+"//g' \
    | sort | uniq -c | sort -rn \
    | head -n 20 || true
fi
`

func appendUniqueString(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func mediaDiagnosticScript(services []string, ports []int, opts diagnoseOptions) string {
	quotedServices := make([]string, 0, len(services))
	for _, service := range services {
		quotedServices = append(quotedServices, ssh.ShellQuote(service))
	}

	portPatterns := make([]string, 0, len(ports))
	for _, port := range ports {
		portPatterns = append(portPatterns, fmt.Sprintf("%d", port))
	}
	portRegex := ":(" + strings.Join(portPatterns, "|") + ")([[:space:]]|$)"
	since := strings.TrimSpace(opts.Since)
	if since == "" {
		since = "4 hours ago"
	}
	streamID := safeDiagnosticValue(opts.StreamID)
	tenantID := safeDiagnosticValue(opts.TenantID)
	streamSQL := commodoreStreamDiagnosticSQL(streamID, tenantID)
	quartermasterSQL := quartermasterMediaDiagnosticSQL()

	return fmt.Sprintf(`set +e
SINCE=%s
STREAM_ID=%s
TENANT_ID=%s
STREAM_SQL=%s
QMASTER_SQL=%s
%s
echo "== host =="
hostname -f 2>/dev/null || hostname
echo "== resource snapshot =="
uptime 2>/dev/null || true
free -h 2>/dev/null | sed -n '1,2p' || true
df -h -x tmpfs -x devtmpfs -x overlay -x squashfs -x efivarfs 2>/dev/null || true
echo "== listeners =="
ss -ltnp 2>/dev/null | grep -E %s || true
for svc in %s; do
  unit="frameworks-${svc}.service"
  echo "== ${unit} =="
  systemctl is-active "${unit}" 2>/dev/null || true
  systemctl show "${unit}" -p ActiveState -p SubState -p ExecMainStatus -p MainPID --no-page 2>/dev/null || true
  # The whole --since window is scanned; repeats collapse into a count so the
  # 20 most frequent distinct lines are shown regardless of when they occurred.
  echo "-- suspicious log lines since ${SINCE} (count, most frequent first) --"
  journalctl -u "${unit}" --since "${SINCE}" --no-pager 2>/dev/null \
    | grep -Ei 'error|warn|failed|x509|deadline|No healthy|unsupported protocol|(^|[^[:alnum:]_])(nan|inf)([^[:alnum:]_]|$)|storage_usage|bunny|cloudflare' \
    | sed -E 's/^[A-Z][a-z]{2} [ 0-9][0-9] [0-9:]+ [^ ]+ [^:]+: //' \
    | sed -E 's/"time":"[^"]+",?//g; s/,"time":"[^"]+"//g' \
    | sort | uniq -c | sort -rn \
    | head -n 20 || true
  if [ -n "${STREAM_ID}" ]; then
    echo "-- stream-specific logs since ${SINCE} (latest 20) --"
    journalctl -u "${unit}" --since "${SINCE}" --no-pager 2>/dev/null \
      | grep -F "${STREAM_ID}" \
      | tail -n 20 || true
  fi
done
echo "== decklog certificate SANs =="
if [ -f /etc/frameworks/pki/services/decklog/tls.crt ]; then
  openssl x509 -in /etc/frameworks/pki/services/decklog/tls.crt -noout -subject -issuer -dates -ext subjectAltName 2>/dev/null || true
else
  echo "decklog cert not found"
fi
if printf '%%s\n' %s | grep -qx quartermaster && [ -n "${QMASTER_SQL}" ]; then
  echo "== quartermaster media placement snapshot =="
  if [ -r /etc/frameworks/quartermaster.env ]; then
    set -a
    . /etc/frameworks/quartermaster.env
    set +a
    if [ -n "${DATABASE_URL}" ] && command -v psql >/dev/null 2>&1; then
      psql "$(fw_libpq_url "${DATABASE_URL}")" -X -P pager=off -A -F ' | ' -c "${QMASTER_SQL}" 2>&1 || true
    else
      echo "psql or DATABASE_URL unavailable"
    fi
  else
    echo "/etc/frameworks/quartermaster.env unavailable"
  fi
fi
if printf '%%s\n' %s | grep -qx commodore && [ -n "${STREAM_SQL}" ]; then
  echo "== commodore stream row =="
  if [ -r /etc/frameworks/commodore.env ]; then
    set -a
    . /etc/frameworks/commodore.env
    set +a
    if [ -n "${DATABASE_URL}" ] && command -v psql >/dev/null 2>&1; then
      psql "$(fw_libpq_url "${DATABASE_URL}")" -X -P pager=off -A -F ' | ' -c "${STREAM_SQL}" 2>&1 || true
    else
      echo "psql or DATABASE_URL unavailable"
    fi
  else
    echo "/etc/frameworks/commodore.env unavailable"
  fi
fi
`, ssh.ShellQuote(since), ssh.ShellQuote(streamID), ssh.ShellQuote(tenantID), ssh.ShellQuote(streamSQL), ssh.ShellQuote(quartermasterSQL), diagnosticLibpqURLShellFunc, ssh.ShellQuote(portRegex), strings.Join(quotedServices, " "), strings.Join(quotedServices, " "), strings.Join(quotedServices, " "))
}

// diagnosticLibpqURLShellFunc defines fw_libpq_url for host-side probes that
// hand a service's DATABASE_URL to psql.
const diagnosticLibpqURLShellFunc = `fw_libpq_url() {
  # psql/libpq rejects pgx-only connection params (load_balance,
  # default_query_exec_mode); strip them, then normalize separators (handles
  # adjacent params). Multi-host URIs and connect_timeout ARE libpq-safe, kept.
  printf '%s' "$1" | sed -E 's/(load_balance|default_query_exec_mode)=[^&]*//g; s/&+/\&/g; s/\?&/?/g; s/[?&]+$//'
}`

func safeDiagnosticValue(value string) string {
	value = strings.TrimSpace(value)
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == ':' {
			continue
		}
		return ""
	}
	return value
}

func commodoreStreamDiagnosticSQL(streamID, tenantID string) string {
	if streamID == "" {
		return ""
	}
	where := fmt.Sprintf("s.id = '%s'", streamID)
	if tenantID != "" {
		where += fmt.Sprintf(" AND s.tenant_id = '%s'", tenantID)
	}
	return fmt.Sprintf(`SELECT s.id, s.tenant_id, s.user_id, s.internal_name, s.playback_id, s.ingest_mode, s.active_ingest_cluster_id, s.active_ingest_cluster_updated_at, s.created_at, s.updated_at
FROM commodore.streams s
WHERE %s;`, where)
}

func quartermasterMediaDiagnosticSQL() string {
	return `SELECT cluster_id, cluster_name, cluster_type, region_id, health_status, is_active
FROM quartermaster.infrastructure_clusters
WHERE cluster_id IN ('media-central-primary', 'media-eu-1', 'media-us-1')
   OR cluster_id LIKE 'media-%'
ORDER BY cluster_id;

SELECT COALESCE(svc.type, si.service_id) AS service_type,
       si.instance_id,
       si.cluster_id AS physical_cluster,
       sca.cluster_id AS assigned_cluster,
       si.node_id,
       si.advertise_host,
       si.port,
       si.status,
       si.health_status,
       sca.is_active AS assignment_active,
       si.last_health_check
FROM quartermaster.service_instances si
LEFT JOIN quartermaster.services svc ON svc.service_id = si.service_id
LEFT JOIN quartermaster.service_cluster_assignments sca
       ON sca.service_instance_id = si.id
      AND sca.is_active = true
WHERE COALESCE(svc.type, si.service_id) IN (
    'edge-egress', 'edge-ingest', 'edge-storage', 'edge-processing',
    'foghorn', 'chandler', 'livepeer-gateway', 'signalman', 'decklog', 'navigator'
)
ORDER BY service_type, si.instance_id, assigned_cluster NULLS LAST;`
}
