package cmd

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"frameworks/cli/internal/templates"
	"frameworks/cli/pkg/health"
	"frameworks/cli/pkg/inventory"
	fwssh "frameworks/cli/pkg/ssh"
)

// ephemeralPortsCommand prints the host's automatic source-port range and the
// ports reserved from it.
const ephemeralPortsCommand = `cat /proc/sys/net/ipv4/ip_local_port_range; echo; cat /proc/sys/net/ipv4/ip_local_reserved_ports`

// portSet is a list of single ports and inclusive ranges, as
// ip_local_reserved_ports prints it ("51820,60000-60010").
type portSet [][2]int

func parsePortSet(text string) (portSet, error) {
	var set portSet
	for part := range strings.SplitSeq(strings.TrimSpace(text), ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		lowText, highText, isRange := strings.Cut(part, "-")
		low, err := strconv.Atoi(lowText)
		if err != nil {
			return nil, fmt.Errorf("reserved port %q: %w", part, err)
		}
		high := low
		if isRange {
			if high, err = strconv.Atoi(highText); err != nil {
				return nil, fmt.Errorf("reserved port %q: %w", part, err)
			}
		}
		set = append(set, [2]int{low, high})
	}
	return set, nil
}

func (s portSet) contains(port int) bool {
	for _, r := range s {
		if port >= r[0] && port <= r[1] {
			return true
		}
	}
	return false
}

// parseEphemeralPorts reads ephemeralPortsCommand output: the range line,
// then the reserved list (empty when none is set).
func parseEphemeralPorts(output string) (low, high int, reserved portSet, err error) {
	lines := strings.SplitN(output, "\n", 2)
	fields := strings.Fields(lines[0])
	if len(fields) != 2 {
		return 0, 0, nil, fmt.Errorf("ip_local_port_range %q", strings.TrimSpace(lines[0]))
	}
	if low, err = strconv.Atoi(fields[0]); err != nil {
		return 0, 0, nil, fmt.Errorf("ip_local_port_range %q: %w", lines[0], err)
	}
	if high, err = strconv.Atoi(fields[1]); err != nil {
		return 0, 0, nil, fmt.Errorf("ip_local_port_range %q: %w", lines[0], err)
	}
	if len(lines) == 2 {
		reserved, err = parsePortSet(lines[1])
	}
	return low, high, reserved, err
}

// listenersInEphemeralRange returns the planned listeners whose port the
// kernel may hand out as a source port while their owner is down: inside
// [low, high] and not reserved.
func listenersInEphemeralRange(low, high int, reserved portSet, planned []plannedListener) []plannedListener {
	var out []plannedListener
	for _, l := range planned {
		if l.Port >= low && l.Port <= high && !reserved.contains(l.Port) {
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out
}

// hostFixedListeners returns every fixed listener the manifest plans on a
// platform host, its WireGuard port included.
func hostFixedListeners(manifest *inventory.Manifest, planned map[string]map[int]string, hostName string) []plannedListener {
	var out []plannedListener
	for port, owner := range planned[hostName] {
		out = append(out, plannedListener{Port: port, Owner: owner})
	}
	if port, ok := manifest.WireguardListenPort(hostName); ok {
		out = append(out, plannedListener{Port: port, Proto: "udp", Owner: "wireguard"})
	}
	return out
}

// checkEphemeralRange fails when a fixed listener planned on the host sits
// inside its automatic source-port range unreserved: an outbound connection
// can then take the port while the listener restarts and block its bind.
func checkEphemeralRange(ctx context.Context, runner func() (fwssh.Runner, error), planned []plannedListener) *health.CheckResult {
	result := &health.CheckResult{Name: "ephemeral_port_range", CheckedAt: time.Now(), Metadata: map[string]string{"access": "ssh-proc-sys"}}
	r, err := runner()
	if err != nil {
		result.Status, result.Error = "unhealthy", fmt.Sprintf("ssh connect: %v", err)
		return result
	}
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	res, err := r.Run(probeCtx, ephemeralPortsCommand)
	if err != nil || res == nil || res.ExitCode != 0 {
		result.Status, result.Error = "unhealthy", "read ephemeral port range: "+diagnosticCommandError(res, err)
		return result
	}
	low, high, reserved, err := parseEphemeralPorts(res.Stdout)
	if err != nil {
		result.Status, result.Error = "unhealthy", err.Error()
		return result
	}
	result.Metadata["range"] = fmt.Sprintf("%d-%d", low, high)
	exposed := listenersInEphemeralRange(low, high, reserved, planned)
	if len(exposed) == 0 {
		result.OK, result.Status = true, "healthy"
		result.Message = fmt.Sprintf("no fixed listener inside the source-port range %d-%d unreserved", low, high)
		return result
	}
	parts := make([]string, 0, len(exposed))
	for _, l := range exposed {
		parts = append(parts, fmt.Sprintf("%d (%s)", l.Port, l.Owner))
	}
	result.Status = "unhealthy"
	result.Error = fmt.Sprintf("%d fixed listener(s) inside the source-port range %d-%d and not reserved: %s", len(exposed), low, high, strings.Join(parts, ", "))
	return result
}

// doctorEphemeralRangeChecks audits each platform host and native edge.
// A container edge's listeners live in the container's network namespace,
// whose range the edge compose file sets.
func doctorEphemeralRangeChecks(pool *fwssh.Pool, manifest *inventory.Manifest, targets []diagnoseHost) []doctorCheck {
	planned := diagnosePlannedPorts(manifest)
	checks := make([]doctorCheck, 0, len(targets))
	for _, target := range targets {
		host := target.Host
		label := "Source-port range on " + target.Name
		var fixed []plannedListener
		if target.Edge {
			if templates.NormalizeEdgeMode(target.EdgeMode) != "native" {
				continue
			}
			label += " (edge)"
			fixed = edgeBundleListeners(target.EdgeMode, true)
		} else {
			fixed = hostFixedListeners(manifest, planned, target.Name)
		}
		checks = append(checks, doctorCheck{name: label, run: func(ctx context.Context) doctorOutcome {
			return doctorInfraOutcome(label, checkEphemeralRange(ctx, func() (fwssh.Runner, error) { return getRunner(host, pool) }, fixed))
		}})
	}
	return checks
}
