package cmd

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"

	"github.com/spf13/cobra"
)

// newClusterDiagnoseCmd creates the diagnose command
func newClusterDiagnoseCmd() *cobra.Command {
	opts := diagnoseOptions{Since: "4 hours ago"}
	cmd := &cobra.Command{
		Use:   "diagnose <component>",
		Short: "Run diagnostics on cluster components",
		Long: `Run diagnostic checks on cluster components.

Supported diagnostics:
  network    - Test network connectivity between hosts
  resources  - Check CPU, memory, disk usage on all hosts
  ports      - Check for port conflicts
  kafka      - Check Kafka cluster health, topic lag, broker status
  media      - Capture media/DNS/federation service state without provisioning

Diagnostics help troubleshoot issues and identify problems before they
cause outages.`,
		Example: `  frameworks cluster diagnose network
  frameworks cluster diagnose resources
  frameworks cluster diagnose kafka
  frameworks cluster diagnose media`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			return runDiagnose(cmd, rc.Manifest, args[0], opts)
		},
	}
	cmd.Flags().StringVar(&opts.StreamID, "stream-id", "", "Stream ID to trace in media diagnostics")
	cmd.Flags().StringVar(&opts.TenantID, "tenant-id", "", "Tenant ID to include in stream database probes")
	cmd.Flags().StringVar(&opts.Since, "since", opts.Since, "Journal time window for media diagnostics")

	return cmd
}

type diagnoseOptions struct {
	StreamID string
	TenantID string
	Since    string
}

// runDiagnose executes diagnostic checks against an already-loaded manifest.
func runDiagnose(cmd *cobra.Command, manifest *inventory.Manifest, component string, opts diagnoseOptions) error {
	ux.Heading(cmd.OutOrStdout(), fmt.Sprintf("Running %s diagnostics", component))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Create SSH pool
	sshKey := stringFlag(cmd, "ssh-key").Value
	sshPool := ssh.NewPool(30*time.Second, sshKey)
	defer sshPool.Close()

	// Execute diagnostic based on component
	switch component {
	case "network":
		return diagnoseNetwork(ctx, cmd, manifest, sshPool)
	case "resources":
		return diagnoseResources(ctx, cmd, manifest, sshPool)
	case "ports":
		return diagnosePorts(ctx, cmd, manifest, sshPool)
	case "kafka":
		return diagnoseKafka(ctx, cmd, manifest, sshPool)
	case "media":
		return diagnoseMedia(ctx, cmd, manifest, sshPool, opts)
	default:
		return fmt.Errorf("unknown component: %s (must be network, resources, ports, kafka, or media)", component)
	}
}

// diagnoseNetwork tests network connectivity
func diagnoseNetwork(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, pool *ssh.Pool) error {
	fmt.Fprintln(cmd.OutOrStdout(), "Network Connectivity Diagnostics")

	hosts := make([]inventory.Host, 0, len(manifest.Hosts))
	for _, h := range manifest.Hosts {
		hosts = append(hosts, h)
	}

	// Test connectivity from each host to every other host
	for i, sourceHost := range hosts {
		runner, err := getRunner(sourceHost, pool)
		if err != nil {
			ux.Fail(cmd.ErrOrStderr(), fmt.Sprintf("Cannot connect to %s: %v", sourceHost.ExternalIP, err))
			continue
		}

		for j, targetHost := range hosts {
			if i == j {
				continue // Skip self-ping
			}

			// Ping test
			pingCmd := fmt.Sprintf("ping -c 1 -W 2 %s", targetHost.ExternalIP)
			result, err := runner.Run(ctx, pingCmd)

			if err != nil || result.ExitCode != 0 {
				ux.Fail(cmd.ErrOrStderr(), fmt.Sprintf("%s → %s: FAILED (no response)", sourceHost.ExternalIP, targetHost.ExternalIP))
			} else {
				ux.Success(cmd.OutOrStdout(), fmt.Sprintf("%s → %s: OK", sourceHost.ExternalIP, targetHost.ExternalIP))
			}
		}
	}

	return nil
}

// diagnoseResources checks resource usage on all hosts
func diagnoseResources(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, pool *ssh.Pool) error {
	fmt.Fprintln(cmd.OutOrStdout(), "Resource Usage Diagnostics")

	for hostname, host := range manifest.Hosts {
		fmt.Fprintf(cmd.OutOrStdout(), "Host: %s (%s)\n", hostname, host.ExternalIP)

		runner, err := getRunner(host, pool)
		if err != nil {
			ux.Fail(cmd.ErrOrStderr(), fmt.Sprintf("Cannot connect: %v", err))
			continue
		}

		// CPU usage
		cpuCmd := "top -bn1 | grep 'Cpu(s)' | awk '{print $2}'"
		if result, err := runner.Run(ctx, cpuCmd); err == nil && result.ExitCode == 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "  CPU: %s%% used\n", result.Stdout)
		}

		// Memory usage
		memCmd := "free -h | awk 'NR==2{printf \"  Memory: %s / %s (%.2f%%)\\n\", $3, $2, $3*100/$2}'"
		if result, err := runner.Run(ctx, memCmd); err == nil && result.ExitCode == 0 {
			fmt.Fprint(cmd.OutOrStdout(), result.Stdout)
		}

		// Disk usage
		diskCmd := "df -h / | awk 'NR==2{printf \"  Disk: %s / %s (%s used)\\n\", $3, $2, $5}'"
		if result, err := runner.Run(ctx, diskCmd); err == nil && result.ExitCode == 0 {
			fmt.Fprint(cmd.OutOrStdout(), result.Stdout)
		}

		// Load average
		loadCmd := "uptime | awk -F'load average:' '{print $2}'"
		if result, err := runner.Run(ctx, loadCmd); err == nil && result.ExitCode == 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "  Load:%s\n", result.Stdout)
		}

		fmt.Fprintln(cmd.OutOrStdout(), "")
	}

	return nil
}

// diagnosePorts checks for port conflicts
func diagnosePorts(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, pool *ssh.Pool) error {
	fmt.Fprintln(cmd.OutOrStdout(), "Port Conflict Diagnostics")

	// Check standard ports on each host
	standardPorts := buildStandardPorts()

	for hostname, host := range manifest.Hosts {
		fmt.Fprintf(cmd.OutOrStdout(), "Host: %s (%s)\n", hostname, host.ExternalIP)

		runner, err := getRunner(host, pool)
		if err != nil {
			ux.Fail(cmd.ErrOrStderr(), fmt.Sprintf("Cannot connect: %v", err))
			continue
		}

		for port, service := range standardPorts {
			checkCmd := fmt.Sprintf("netstat -tuln | grep ':%d ' || echo 'free'", port)
			result, err := runner.Run(ctx, checkCmd)
			if err == nil && result.ExitCode == 0 {
				if result.Stdout == "free\n" {
					fmt.Fprintf(cmd.OutOrStdout(), "  Port %d (%s): FREE\n", port, service)
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "  Port %d (%s): IN USE\n", port, service)
				}
			}
		}

		fmt.Fprintln(cmd.OutOrStdout(), "")
	}

	return nil
}

func buildStandardPorts() map[int]string {
	standardPorts := map[int]string{
		53:    "privateer-dns",
		18019: "foghorn-control",
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
func diagnoseKafka(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, pool *ssh.Pool) error {
	if !manifest.Infrastructure.Kafka.Enabled {
		return fmt.Errorf("kafka not enabled in manifest")
	}

	fmt.Fprintln(cmd.OutOrStdout(), "Kafka Diagnostics")

	// Check first broker
	if len(manifest.Infrastructure.Kafka.Brokers) == 0 {
		return fmt.Errorf("no kafka brokers configured")
	}

	broker := manifest.Infrastructure.Kafka.Brokers[0]
	host, found := manifest.GetHost(broker.Host)
	if !found {
		return fmt.Errorf("broker host not found: %s", broker.Host)
	}

	runner, err := getRunner(host, pool)
	if err != nil {
		return err
	}

	// List topics
	fmt.Fprintln(cmd.OutOrStdout(), "Topics:")
	topicsCmd := "docker compose -f /opt/frameworks/kafka/docker-compose.yml exec -T kafka kafka-topics --bootstrap-server localhost:9092 --list"
	if result, err := runner.Run(ctx, topicsCmd); err == nil && result.ExitCode == 0 {
		fmt.Fprint(cmd.OutOrStdout(), result.Stdout)
	} else {
		ux.Fail(cmd.ErrOrStderr(), fmt.Sprintf("Failed to list topics: %v", err))
	}

	// Check consumer groups
	fmt.Fprintln(cmd.OutOrStdout(), "\nConsumer Groups:")
	groupsCmd := "docker compose -f /opt/frameworks/kafka/docker-compose.yml exec -T kafka kafka-consumer-groups --bootstrap-server localhost:9092 --list"
	if result, err := runner.Run(ctx, groupsCmd); err == nil && result.ExitCode == 0 {
		fmt.Fprint(cmd.OutOrStdout(), result.Stdout)
	} else {
		ux.Fail(cmd.ErrOrStderr(), fmt.Sprintf("Failed to list consumer groups: %v", err))
	}

	// Check broker config
	fmt.Fprintln(cmd.OutOrStdout(), "\nBroker Status:")
	brokerCmd := "docker compose -f /opt/frameworks/kafka/docker-compose.yml exec -T kafka kafka-broker-api-versions --bootstrap-server localhost:9092"
	if result, err := runner.Run(ctx, brokerCmd); err == nil && result.ExitCode == 0 {
		ux.Success(cmd.OutOrStdout(), "Broker is responding")
	} else {
		ux.Fail(cmd.ErrOrStderr(), fmt.Sprintf("Broker is not responding: %v", err))
	}

	return nil
}

func diagnoseMedia(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, pool *ssh.Pool, opts diagnoseOptions) error {
	fmt.Fprintln(cmd.OutOrStdout(), "Media/DNS/Federation Diagnostics")
	fmt.Fprintln(cmd.OutOrStdout(), "Read-only host probes for service state, listeners, TLS SANs, and recent error logs.")
	if opts.StreamID != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Tracing stream_id=%s\n", opts.StreamID)
	}

	hostServices := mediaDiagnosticHostServices(manifest)
	if len(hostServices) == 0 {
		return fmt.Errorf("no media diagnostic service placements found in manifest")
	}

	hosts := make([]string, 0, len(hostServices))
	for hostName := range hostServices {
		hosts = append(hosts, hostName)
	}
	sort.Strings(hosts)

	ports := mediaDiagnosticPorts()
	for _, hostName := range hosts {
		host, ok := manifest.GetHost(hostName)
		if !ok {
			ux.Fail(cmd.ErrOrStderr(), fmt.Sprintf("%s: host missing from manifest", hostName))
			continue
		}

		services := hostServices[hostName]
		sort.Strings(services)
		fmt.Fprintf(cmd.OutOrStdout(), "\nHost: %s (%s)\n", hostName, host.ExternalIP)
		fmt.Fprintf(cmd.OutOrStdout(), "Services: %s\n", strings.Join(services, ", "))

		runner, err := getRunner(host, pool)
		if err != nil {
			ux.Fail(cmd.ErrOrStderr(), fmt.Sprintf("Cannot connect to %s: %v", hostName, err))
			continue
		}

		probe := mediaDiagnosticScript(services, ports, opts)
		result, err := runner.Run(ctx, "sh -lc "+ssh.ShellQuote(probe))
		if err != nil {
			ux.Fail(cmd.ErrOrStderr(), fmt.Sprintf("%s: diagnostic probe failed: %v", hostName, err))
			continue
		}
		if strings.TrimSpace(result.Stdout) != "" {
			fmt.Fprint(cmd.OutOrStdout(), result.Stdout)
		}
		if strings.TrimSpace(result.Stderr) != "" {
			fmt.Fprint(cmd.ErrOrStderr(), result.Stderr)
		}
		if result.ExitCode != 0 {
			ux.Fail(cmd.ErrOrStderr(), fmt.Sprintf("%s: diagnostic probe exited %d", hostName, result.ExitCode))
		}
	}

	fmt.Fprintln(cmd.OutOrStdout(), "\nNext focused checks:")
	fmt.Fprintln(cmd.OutOrStdout(), "  - Compare service-cluster assignments with admin service-pool status for foghorn/chandler/livepeer-gateway.")
	fmt.Fprintln(cmd.OutOrStdout(), "  - If Chartroom stream reads still fail, inspect Commodore logs and DB row for the stream ID.")
	fmt.Fprintln(cmd.OutOrStdout(), "  - If Bunny zones remain empty, inspect Navigator logs after Quartermaster reports healthy media services.")
	return nil
}

func mediaDiagnosticHostServices(manifest *inventory.Manifest) map[string][]string {
	out := map[string][]string{}
	if manifest == nil {
		return out
	}
	serviceTypes := map[string]struct{}{
		"bridge":           {},
		"chandler":         {},
		"commodore":        {},
		"decklog":          {},
		"foghorn":          {},
		"livepeer-gateway": {},
		"navigator":        {},
		"periscope-query":  {},
		"quartermaster":    {},
		"signalman":        {},
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
echo "== host =="
hostname -f 2>/dev/null || hostname
echo "== resource snapshot =="
uptime 2>/dev/null || true
free -h 2>/dev/null | sed -n '1,2p' || true
df -h / 2>/dev/null | sed -n '1,2p' || true
echo "== listeners =="
ss -ltnp 2>/dev/null | grep -E %s || true
for svc in %s; do
  unit="frameworks-${svc}.service"
  echo "== ${unit} =="
  systemctl is-active "${unit}" 2>/dev/null || true
  systemctl show "${unit}" -p ActiveState -p SubState -p ExecMainStatus -p MainPID --no-page 2>/dev/null || true
  echo "-- recent suspicious logs --"
  journalctl -u "${unit}" --since "${SINCE}" -n 200 --no-pager 2>/dev/null \
    | grep -Ei 'error|warn|failed|x509|deadline|No healthy|unsupported protocol|nan|inf|signalman|decklog|storage_usage|bunny|cloudflare' \
    | sed -E 's/^[A-Z][a-z]{2} [ 0-9][0-9] [0-9:]+ [^ ]+ [^:]+: //' \
    | sed -E 's/"time":"[^"]+",?//g; s/,"time":"[^"]+"//g' \
    | awk '!seen[$0]++' \
    | tail -n 20 || true
  if [ -n "${STREAM_ID}" ]; then
    echo "-- stream-specific logs --"
    journalctl -u "${unit}" --since "${SINCE}" -n 300 --no-pager 2>/dev/null \
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
      psql "${DATABASE_URL}" -X -P pager=off -A -F ' | ' -c "${QMASTER_SQL}" 2>&1 || true
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
      psql "${DATABASE_URL}" -X -P pager=off -A -F ' | ' -c "${STREAM_SQL}" 2>&1 || true
    else
      echo "psql or DATABASE_URL unavailable"
    fi
  else
    echo "/etc/frameworks/commodore.env unavailable"
  fi
fi
`, ssh.ShellQuote(since), ssh.ShellQuote(streamID), ssh.ShellQuote(tenantID), ssh.ShellQuote(streamSQL), ssh.ShellQuote(quartermasterSQL), ssh.ShellQuote(portRegex), strings.Join(quotedServices, " "), strings.Join(quotedServices, " "), strings.Join(quotedServices, " "))
}

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
