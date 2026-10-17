package cmd

import (
	"context"
	"fmt"
	"io"
	"slices"
	"sort"
	"strconv"
	"strings"

	"frameworks/cli/internal/templates"
	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
)

// diagnoseHost is one machine a host-level diagnostic probes. Edge nodes are
// not in the cluster manifest's hosts map; they come from the edge manifest.
type diagnoseHost struct {
	Name string
	Host inventory.Host
	Edge bool
	// EdgeMode is the edge node's resolved install mode ("container" or
	// "native"); empty for cluster hosts.
	EdgeMode string
}

// diagnoseHostTargets lists the cluster manifest's hosts and the nodes of the
// edge manifest (--edge-manifest, else edge.yaml beside the cluster manifest),
// sorted by name. An edge manifest that cannot be loaded is reported on errOut
// and the cluster hosts are still returned.
func diagnoseHostTargets(errOut io.Writer, rc *resolvedCluster, edgeManifestFlag string) []diagnoseHost {
	edgePath := strings.TrimSpace(edgeManifestFlag)
	if edgePath == "" {
		edgePath = defaultEdgeManifestPath(rc.ManifestPath)
	}
	targets, err := logsSnapshotHosts(rc.Manifest, edgePath, rc.AgeKey)
	if err != nil {
		ux.Warn(errOut, fmt.Sprintf("Edge nodes not probed: %v", err))
		targets, _ = logsSnapshotHosts(rc.Manifest, "", "") //nolint:errcheck // without an edge manifest it cannot fail.
	}
	out := make([]diagnoseHost, 0, len(targets))
	for _, target := range targets {
		_, inCluster := rc.Manifest.Hosts[target.Name]
		out = append(out, diagnoseHost{Name: target.Name, Host: target.Host, Edge: !inCluster, EdgeMode: target.EdgeMode})
	}
	return out
}

func diagnoseHostLabel(target diagnoseHost) string {
	if target.Edge {
		return fmt.Sprintf("%s (%s, edge)", target.Name, target.Host.ExternalIP)
	}
	return fmt.Sprintf("%s (%s)", target.Name, target.Host.ExternalIP)
}

// diagnoseNetwork pings every target from every other target and returns the
// number of unreachable pairs and hosts that could not be reached over SSH.
func diagnoseNetwork(ctx context.Context, stdout, stderr io.Writer, targets []diagnoseHost, runnerFor func(inventory.Host) (ssh.Runner, error)) int {
	fmt.Fprintln(stdout, "Network Connectivity Diagnostics")
	failures := 0
	for i, source := range targets {
		runner, err := runnerFor(source.Host)
		if err != nil {
			failures++
			ux.Fail(stderr, fmt.Sprintf("Cannot connect to %s: %v", diagnoseHostLabel(source), err))
			continue
		}
		for j, target := range targets {
			if i == j {
				continue
			}
			result, err := runner.Run(ctx, fmt.Sprintf("ping -c 1 -W 2 %s", ssh.ShellQuote(target.Host.ExternalIP)))
			pair := fmt.Sprintf("%s → %s", source.Name, target.Name)
			if err != nil || result == nil || result.ExitCode != 0 {
				failures++
				ux.Fail(stderr, fmt.Sprintf("%s (%s): FAILED (no response)", pair, target.Host.ExternalIP))
				continue
			}
			ux.Success(stdout, fmt.Sprintf("%s (%s): OK", pair, target.Host.ExternalIP))
		}
	}
	return failures
}

// Resource thresholds: a FAIL counts toward the diagnostic's exit status, a
// WARN does not.
const (
	diskUsedFailPercent    = 90.0
	diskUsedWarnPercent    = 80.0
	memAvailWarnPercent    = 10.0
	memAvailFailPercent    = 5.0
	cpuIOWaitWarnPercent   = 20.0
	loadPerCoreWarnRatio   = 2.0
	resourceProbeCPUSample = "1"
)

// resourceProbeScript prints one tagged line per fact so the CLI formats and
// judges them; Run trims stdout, so nothing may depend on a trailing newline.
// CPU is two /proc/stat samples a second apart; disks are every real
// filesystem (pseudo, overlay and snap filesystems excluded), not only /.
const resourceProbeScript = `set +e
echo "ncpu $(nproc 2>/dev/null || getconf _NPROCESSORS_ONLN)"
echo "cpu1 $(grep '^cpu ' /proc/stat)"
sleep ` + resourceProbeCPUSample + `
echo "cpu2 $(grep '^cpu ' /proc/stat)"
awk '/^MemTotal:/{print "memtotal", $2*1024} /^MemAvailable:/{print "memavailable", $2*1024}' /proc/meminfo
awk '{print "load", $1, $2, $3}' /proc/loadavg
DF_EXCLUDE="-x tmpfs -x devtmpfs -x overlay -x squashfs -x efivarfs -x ramfs -x nsfs -x fuse.lxcfs"
df -P -B1 $DF_EXCLUDE 2>/dev/null | awk 'NR>1{print "df", $1, $2, $3, $4, $6}'
df -P -i $DF_EXCLUDE 2>/dev/null | awk 'NR>1{print "inode", $1, $2, $3, $4, $6}'
`

type resourceMount struct {
	Source, Mount           string
	Total, Used, Available  uint64
	InodeTotal, InodesUsed  uint64
	hasInodes, hasBlockSize bool
}

type resourceSnapshot struct {
	CPUs                  int
	CPUBusyPct, IOWaitPct float64
	CPUSampled            bool
	MemTotal, MemAvail    uint64
	Load                  [3]float64
	HasLoad               bool
	Mounts                []resourceMount
}

func safeField(fields []string, i int) string {
	if i < len(fields) {
		return fields[i]
	}
	return ""
}

func parseResourceProbe(stdout string) resourceSnapshot {
	var snap resourceSnapshot
	var cpu1, cpu2 []uint64
	mounts := map[string]*resourceMount{}
	var order []string
	mount := func(source, path string) *resourceMount {
		// One filesystem mounted at several paths is reported once, at its first path.
		if m, ok := mounts[source]; ok {
			return m
		}
		m := &resourceMount{Source: source, Mount: path}
		mounts[source] = m
		order = append(order, source)
		return m
	}
	for _, line := range strings.Split(stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "ncpu":
			if len(fields) == 2 {
				if n, err := strconv.Atoi(fields[1]); err == nil {
					snap.CPUs = n
				}
			}
		case "cpu1", "cpu2":
			// "cpuN cpu user nice system idle iowait irq softirq steal ..."
			var values []uint64
			for _, f := range fields[2:] {
				v, err := strconv.ParseUint(f, 10, 64)
				if err != nil {
					break
				}
				values = append(values, v)
			}
			if fields[0] == "cpu1" {
				cpu1 = values
			} else {
				cpu2 = values
			}
		case "memtotal", "memavailable":
			if v, err := strconv.ParseFloat(safeField(fields, 1), 64); err == nil && len(fields) == 2 {
				if fields[0] == "memtotal" {
					snap.MemTotal = uint64(v)
				} else {
					snap.MemAvail = uint64(v)
				}
			}
		case "load":
			if len(fields) == 4 {
				snap.HasLoad = true
				for i := range 3 {
					v, err := strconv.ParseFloat(fields[i+1], 64)
					if err != nil {
						snap.HasLoad = false
						break
					}
					snap.Load[i] = v
				}
			}
		case "df", "inode":
			if len(fields) != 6 {
				continue
			}
			total, err1 := strconv.ParseUint(fields[2], 10, 64)
			used, err2 := strconv.ParseUint(fields[3], 10, 64)
			avail, err3 := strconv.ParseUint(fields[4], 10, 64)
			if err1 != nil || err2 != nil || err3 != nil {
				continue
			}
			m := mount(fields[1], fields[5])
			if fields[0] == "df" {
				m.Total, m.Used, m.Available, m.hasBlockSize = total, used, avail, true
			} else if total > 0 {
				m.InodeTotal, m.InodesUsed, m.hasInodes = total, used, true
			}
		}
	}
	if len(cpu1) >= 8 && len(cpu2) >= 8 {
		sum := func(v []uint64) (total, idle, iowait uint64) {
			for _, x := range v[:8] {
				total += x
			}
			return total, v[3], v[4]
		}
		t1, i1, w1 := sum(cpu1)
		t2, i2, w2 := sum(cpu2)
		if t2 > t1 {
			dt := float64(t2 - t1)
			snap.CPUBusyPct = 100 * (dt - float64((i2-i1)+(w2-w1))) / dt
			snap.IOWaitPct = 100 * float64(w2-w1) / dt
			snap.CPUSampled = true
		}
	}
	for _, source := range order {
		if m := mounts[source]; m.hasBlockSize {
			snap.Mounts = append(snap.Mounts, *m)
		}
	}
	return snap
}

func humanUint(v uint64) string { return humanBytes(int64(v)) } //nolint:gosec // byte counts of one host fit in int64.

// reportResourceSnapshot prints the snapshot, one metric per line, and returns
// the number of FAIL findings.
func reportResourceSnapshot(out io.Writer, snap resourceSnapshot) int {
	failures := 0
	emit := func(level int, text string) {
		switch level {
		case 2:
			failures++
			ux.Fail(out, "  "+text)
		case 1:
			ux.Warn(out, "  "+text)
		default:
			fmt.Fprintln(out, "  "+text)
		}
	}
	if snap.CPUSampled {
		level := 0
		if snap.IOWaitPct >= cpuIOWaitWarnPercent {
			level = 1
		}
		emit(level, fmt.Sprintf("CPU: %.1f%% busy, %.1f%% iowait (%d cores)", snap.CPUBusyPct, snap.IOWaitPct, snap.CPUs))
	} else {
		emit(1, "CPU: not sampled (/proc/stat unreadable)")
	}
	if snap.HasLoad {
		level, perCore := 0, ""
		if snap.CPUs > 0 {
			ratio := snap.Load[0] / float64(snap.CPUs)
			perCore = fmt.Sprintf(" (%.2f per core)", ratio)
			if ratio >= loadPerCoreWarnRatio {
				level = 1
			}
		}
		emit(level, fmt.Sprintf("Load: %.2f %.2f %.2f%s", snap.Load[0], snap.Load[1], snap.Load[2], perCore))
	}
	if snap.MemTotal > 0 {
		availPct := 100 * float64(snap.MemAvail) / float64(snap.MemTotal)
		level := 0
		switch {
		case availPct < memAvailFailPercent:
			level = 2
		case availPct < memAvailWarnPercent:
			level = 1
		}
		emit(level, fmt.Sprintf("Memory: %s available of %s (%.1f%% used)", humanUint(snap.MemAvail), humanUint(snap.MemTotal), 100-availPct))
	} else {
		emit(1, "Memory: not read (/proc/meminfo unreadable)")
	}
	if len(snap.Mounts) == 0 {
		emit(2, "Disk: no filesystems reported by df")
	}
	for _, m := range snap.Mounts {
		// df's Use% is used/(used+available): root-reserved blocks count as full.
		usedPct := 0.0
		if m.Used+m.Available > 0 {
			usedPct = 100 * float64(m.Used) / float64(m.Used+m.Available)
		}
		inodePct := 0.0
		if m.hasInodes {
			inodePct = 100 * float64(m.InodesUsed) / float64(m.InodeTotal)
		}
		worst := max(usedPct, inodePct)
		level := 0
		switch {
		case worst >= diskUsedFailPercent:
			level = 2
		case worst >= diskUsedWarnPercent:
			level = 1
		}
		text := fmt.Sprintf("Disk %s: %s used of %s (%.0f%% used", m.Mount, humanUint(m.Used), humanUint(m.Total), usedPct)
		if m.hasInodes {
			text += fmt.Sprintf(", inodes %.0f%%", inodePct)
		}
		emit(level, text+")")
	}
	return failures
}

// diagnoseResources reports CPU, load, memory and every real filesystem per
// target and returns the number of FAIL findings and unreachable hosts.
func diagnoseResources(ctx context.Context, stdout, stderr io.Writer, targets []diagnoseHost, runnerFor func(inventory.Host) (ssh.Runner, error)) int {
	fmt.Fprintln(stdout, "Resource Usage Diagnostics")
	failures := 0
	for _, target := range targets {
		fmt.Fprintf(stdout, "\nHost: %s\n", diagnoseHostLabel(target))
		runner, err := runnerFor(target.Host)
		if err != nil {
			failures++
			ux.Fail(stderr, fmt.Sprintf("%s: cannot connect: %v", target.Name, err))
			continue
		}
		result, err := runner.Run(ctx, "sh -c "+ssh.ShellQuote(resourceProbeScript))
		if err != nil || result == nil {
			failures++
			ux.Fail(stderr, fmt.Sprintf("%s: resource probe failed: %s", target.Name, diagnosticCommandError(result, err)))
			continue
		}
		failures += reportResourceSnapshot(stdout, parseResourceProbe(result.Stdout))
	}
	return failures
}

// plannedListener is one socket a host should hold. An empty Proto accepts a
// TCP listener or a bound UDP socket on the port.
type plannedListener struct {
	Port  int
	Proto string
	Owner string
}

// edgeBundleListeners returns the sockets an edge holds on its host. Both
// modes run Caddy (HTTP, HTTPS and HTTP/3), Helmsman (webhook and management
// listeners) and MistServer (controller API, HTTP output and the RTMP, DTSC,
// RTSP, SRT and WebRTC listeners of Helmsman's managed protocol set). Native
// Caddy serves its admin API on loopback TCP; the edge image serves it on a
// unix socket. vmagent is installed only with telemetry and node_exporter is
// installed beside it, so a listening vmagent plans node_exporter.
func edgeBundleListeners(mode string, vmagentRunning bool) []plannedListener {
	listeners := []plannedListener{
		{80, "tcp", "caddy-http"},
		{443, "tcp", "caddy-https"},
		{443, "udp", "caddy-http3"},
		{servicedefs.Services["helmsman"].DefaultPort, "tcp", "helmsman"},
		{servicedefs.HelmsmanManagementPort, "tcp", "helmsman-management"},
		{servicedefs.Services["mistserver"].DefaultPort, "tcp", "mist-http"},
		{servicedefs.MistAPIPort, "tcp", "mist-api"},
		{servicedefs.MistRTMPPort, "tcp", "mist-rtmp"},
		{servicedefs.MistDTSCPort, "tcp", "mist-dtsc"},
		{servicedefs.MistRTSPPort, "tcp", "mist-rtsp"},
		{servicedefs.MistSRTPort, "udp", "mist-srt"},
		{servicedefs.MistWebRTCPort, "udp", "mist-webrtc"},
	}
	if templates.NormalizeEdgeMode(mode) == "native" {
		listeners = append(listeners, plannedListener{edgeNativeCaddyAdminPort, "tcp", "caddy-admin"})
	}
	if vmagentRunning {
		listeners = append(listeners,
			plannedListener{servicedefs.Services["vmagent"].DefaultPort, "tcp", "vmagent"},
			plannedListener{provisioner.NodeExporterPort, "tcp", "node-exporter"})
	}
	sort.SliceStable(listeners, func(i, j int) bool {
		if listeners[i].Port != listeners[j].Port {
			return listeners[i].Port < listeners[j].Port
		}
		return listeners[i].Proto < listeners[j].Proto
	})
	return listeners
}

// edgeNativeCaddyAdminPort is the admin listener of Caddyfile.native.j2
// (admin localhost:2019).
const edgeNativeCaddyAdminPort = 2019

// diagnosePlannedPorts returns, per host, every port the manifest places
// there and the owner that binds it: SQL, ClickHouse and Kafka (including
// regional Kafka clusters), Redis instances and Sentinels, and every enabled
// service, interface and observability component with its gRPC and auxiliary
// listeners.
func diagnosePlannedPorts(manifest *inventory.Manifest) map[string]map[int]string {
	planned := manifest.InfrastructurePortOwners()
	add := func(host string, port int, owner string) {
		host = strings.TrimSpace(host)
		if host == "" || port <= 0 {
			return
		}
		if planned[host] == nil {
			planned[host] = map[int]string{}
		}
		if _, ok := planned[host][port]; !ok {
			planned[host][port] = owner
		}
	}
	for _, cluster := range allKafkaClusters(manifest) {
		for _, broker := range cluster.Brokers {
			port := broker.Port
			if port == 0 {
				port = 9092
			}
			add(broker.Host, port, fmt.Sprintf("kafka-broker-%d", broker.ID))
		}
		for _, ctrl := range cluster.Controllers {
			port := ctrl.Port
			if port == 0 {
				port = 9093
			}
			add(ctrl.Host, port, fmt.Sprintf("kafka-controller-%d", ctrl.ID))
		}
		if len(cluster.Controllers) == 0 {
			port := cluster.ControllerPort
			if port == 0 {
				port = 9093
			}
			for _, broker := range cluster.Brokers {
				add(broker.Host, port, fmt.Sprintf("kafka-broker-%d-controller", broker.ID))
			}
		}
	}
	if redis := manifest.Infrastructure.Redis; redis != nil && redis.Enabled {
		for _, inst := range redis.Instances {
			port := inst.Port
			if port == 0 {
				port = 6379
			}
			owner := "redis-" + inst.Name
			add(inst.Host, port, owner)
			for _, replica := range inst.ReplicaHosts {
				add(replica, port, owner)
			}
			for _, sentinel := range inst.Sentinels {
				sentinelPort := sentinel.Port
				if sentinelPort == 0 {
					sentinelPort = 26379
				}
				add(sentinel.Host, sentinelPort, owner+"-sentinel")
			}
		}
	}
	for _, group := range []map[string]inventory.ServiceConfig{manifest.Services, manifest.Interfaces, manifest.Observability} {
		for name, svc := range group {
			if !svc.Enabled {
				continue
			}
			port, err := resolvePort(name, svc)
			if err != nil {
				continue
			}
			defID := name
			if def, ok := resolveServiceDefinition(name, svc); ok {
				defID = def.ID
			}
			grpcPort := svc.GRPCPort
			if grpcPort == 0 {
				grpcPort, _ = servicedefs.DefaultGRPCPort(defID)
			}
			for _, host := range doctorServiceHostNames(name, svc, manifest) {
				add(host, port, name)
				if grpcPort != 0 && grpcPort != port {
					add(host, grpcPort, name+"-grpc")
				}
				for _, aux := range servicedefs.AuxiliaryPorts(defID) {
					add(host, aux.Port, aux.Name)
				}
			}
		}
	}
	return planned
}

// listenerSet holds the bound sockets of a host, keyed by protocol then port.
type listenerSet map[string]map[int]bool

// has reports a socket on port for proto; an empty proto accepts either.
func (s listenerSet) has(proto string, port int) bool {
	if proto == "" {
		return s["tcp"][port] || s["udp"][port]
	}
	return s[proto][port]
}

// listeningPorts parses `ss -Hltnu` output into the TCP listeners and bound
// UDP sockets of a host.
func listeningPorts(ssOutput string) listenerSet {
	set := listenerSet{"tcp": {}, "udp": {}}
	for _, line := range strings.Split(ssOutput, "\n") {
		fields := strings.Fields(line)
		// Netid State Recv-Q Send-Q Local:Port Peer:Port [Process]
		if len(fields) < 5 {
			continue
		}
		proto := strings.ToLower(fields[0])
		if _, ok := set[proto]; !ok {
			continue
		}
		local := fields[4]
		idx := strings.LastIndex(local, ":")
		if idx < 0 {
			continue
		}
		if port, err := strconv.Atoi(local[idx+1:]); err == nil {
			set[proto][port] = true
		}
	}
	return set
}

// diagnosePorts compares each target's listeners with the ports the manifest
// places on it. A planned port with no listener is a failure; catalog ports
// held by something the manifest does not place there are listed so a
// conflicting process is visible.
func diagnosePorts(ctx context.Context, stdout, stderr io.Writer, manifest *inventory.Manifest, targets []diagnoseHost, runnerFor func(inventory.Host) (ssh.Runner, error)) int {
	fmt.Fprintln(stdout, "Port Diagnostics (manifest placement versus listeners)")
	planned := diagnosePlannedPorts(manifest)
	catalog := buildStandardPorts()
	failures := 0
	for _, target := range targets {
		fmt.Fprintf(stdout, "\nHost: %s\n", diagnoseHostLabel(target))
		runner, err := runnerFor(target.Host)
		if err != nil {
			failures++
			ux.Fail(stderr, fmt.Sprintf("%s: cannot connect: %v", target.Name, err))
			continue
		}
		result, err := runner.Run(ctx, "ss -Hltnu")
		if err != nil || result == nil {
			failures++
			ux.Fail(stderr, fmt.Sprintf("%s: listing listeners failed: %s", target.Name, diagnosticCommandError(result, err)))
			continue
		}
		listening := listeningPorts(result.Stdout)
		var hostPlan []plannedListener
		if target.Edge {
			hostPlan = edgeBundleListeners(target.EdgeMode, listening.has("tcp", servicedefs.Services["vmagent"].DefaultPort))
		} else {
			for port, owner := range planned[target.Name] {
				hostPlan = append(hostPlan, plannedListener{Port: port, Owner: owner})
			}
			sort.Slice(hostPlan, func(i, j int) bool { return hostPlan[i].Port < hostPlan[j].Port })
		}
		if len(hostPlan) == 0 {
			fmt.Fprintln(stdout, "  No ports placed on this host by the manifest.")
		}
		mine := map[int]bool{}
		for _, want := range hostPlan {
			mine[want.Port] = true
			label := fmt.Sprintf("%d %s", want.Port, want.Owner)
			if want.Proto != "" {
				label += " (" + want.Proto + ")"
			}
			if listening.has(want.Proto, want.Port) {
				ux.Success(stdout, fmt.Sprintf("  %s: listening", label))
				continue
			}
			failures++
			ux.Fail(stdout, fmt.Sprintf("  %s: NOT LISTENING", label))
		}
		var others []string
		for _, proto := range []string{"tcp", "udp"} {
			for port := range listening[proto] {
				if mine[port] {
					continue
				}
				if owner, ok := catalog[port]; ok {
					others = append(others, fmt.Sprintf("%d (%s)", port, owner))
				}
			}
		}
		slices.Sort(others)
		others = slices.Compact(others)
		if len(others) > 0 {
			fmt.Fprintf(stdout, "  Catalog ports held by something this host is not planned to run: %s\n", strings.Join(others, ", "))
		}
	}
	return failures
}
