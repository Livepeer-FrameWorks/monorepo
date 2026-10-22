package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"frameworks/cli/pkg/health"
	fwssh "frameworks/cli/pkg/ssh"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
)

// edgeListenerCommand prints an edge's listeners, then the host's ephemeral
// port range, then the MistServer runtime config (the native unit's
// /etc/mistserver.conf, or the edge container's /etc/frameworks/mistserver.conf).
const edgeListenerCommand = `(` + hostListenerShell + `)
echo "` + edgePortRangeMarker + ` $(cat /proc/sys/net/ipv4/ip_local_port_range 2>/dev/null)"
echo "` + edgeMistConfigMarker + `"
fw_docker() { if docker version >/dev/null 2>&1; then docker "$@"; else sudo -n docker "$@"; fi; }
if command -v docker >/dev/null 2>&1 && fw_docker inspect frameworks-edge >/dev/null 2>&1; then
  fw_docker exec frameworks-edge cat /etc/frameworks/mistserver.conf
else
  sudo -n cat /etc/mistserver.conf 2>/dev/null || cat /etc/mistserver.conf
fi`

const (
	edgePortRangeMarker  = "@@ip_local_port_range"
	edgeMistConfigMarker = "@@mistserver_config"
	// mistWebRTCProcess binds WebRTC's configured UDP port and, for an
	// outgoing session (a WHIP push out), a socket on an ephemeral port.
	mistWebRTCProcess = "MistOutWebRTC"
)

// mistPublicConnectors are the MistServer connectors an edge serves on every
// interface (docs/architecture/tls.md, Edge listeners), with the protocol and
// Mist's compiled-in port used when the config sets none.
var mistPublicConnectors = map[string]struct {
	proto       string
	defaultPort int
}{
	"RTMP":   {"tcp", servicedefs.MistRTMPPort},
	"RTSP":   {"tcp", servicedefs.MistRTSPPort},
	"DTSC":   {"tcp", servicedefs.MistDTSCPort},
	"TSSRT":  {"udp", servicedefs.MistSRTPort},
	"WebRTC": {"udp", servicedefs.MistWebRTCPort},
}

// edgeListenerPolicy is what an edge may listen on with a wildcard address.
type edgeListenerPolicy struct {
	tcp, udp map[int]bool
	// ephemeralLow/High is the host's ip_local_port_range: MistOutWebRTC's
	// outgoing sockets bind there. Zero when the range could not be read.
	ephemeralLow, ephemeralHigh int
}

func (p edgeListenerPolicy) allows(l exposedListener) bool {
	switch l.Proto {
	case "tcp":
		return p.tcp[l.Port]
	case "udp":
		if p.udp[l.Port] {
			return true
		}
		return l.Process == mistWebRTCProcess && p.ephemeralLow > 0 && l.Port >= p.ephemeralLow && l.Port <= p.ephemeralHigh
	}
	return false
}

// parseEdgeListenerProbe splits edgeListenerCommand's output and derives the
// edge's public listener policy: SSH, Caddy's 80/443 (TCP and QUIC), and the
// public Mist connectors on the ports the Mist config sets.
func parseEdgeListenerProbe(output string) (string, edgeListenerPolicy, error) {
	policy := edgeListenerPolicy{
		tcp: map[int]bool{22: true, 80: true, 443: true},
		udp: map[int]bool{443: true},
	}
	rangeAt := strings.Index(output, edgePortRangeMarker)
	configAt := strings.Index(output, edgeMistConfigMarker)
	if rangeAt < 0 || configAt < rangeAt {
		return "", policy, fmt.Errorf("edge listener probe output is incomplete")
	}
	listeners := normalizeHostListeners(output[:rangeAt])
	if fields := strings.Fields(output[rangeAt+len(edgePortRangeMarker) : configAt]); len(fields) == 2 {
		low, lowErr := strconv.Atoi(fields[0])
		high, highErr := strconv.Atoi(fields[1])
		if lowErr == nil && highErr == nil && low > 0 && low <= high {
			policy.ephemeralLow, policy.ephemeralHigh = low, high
		}
	}

	var config struct {
		Config struct {
			Protocols []map[string]any `json:"protocols"`
		} `json:"config"`
	}
	raw := strings.TrimSpace(output[configAt+len(edgeMistConfigMarker):])
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		return listeners, policy, fmt.Errorf("read MistServer config: %w", err)
	}
	for _, protocol := range config.Config.Protocols {
		name, named := protocol["connector"].(string)
		connector, public := mistPublicConnectors[name]
		if !named || !public {
			continue
		}
		port := connector.defaultPort
		if configured, ok := protocol["port"].(float64); ok && configured > 0 {
			port = int(configured)
		}
		if connector.proto == "tcp" {
			policy.tcp[port] = true
		} else {
			policy.udp[port] = true
		}
	}
	return listeners, policy, nil
}

// checkEdgeListenerExposure reports every listener on an edge bound to every
// interface outside the edge's public set.
func checkEdgeListenerExposure(ctx context.Context, runnerFor func() (fwssh.Runner, error)) *health.CheckResult {
	result := &health.CheckResult{Name: "edge_listener_exposure", CheckedAt: time.Now(), Metadata: map[string]string{"access": "ssh-local-ss"}}
	runner, err := runnerFor()
	if err != nil {
		result.Status, result.Error = "unhealthy", fmt.Sprintf("ssh connect: %v", err)
		return result
	}
	probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	res, err := runner.Run(probeCtx, edgeListenerCommand)
	if err != nil || res == nil || res.ExitCode != 0 {
		result.Status, result.Error = "unhealthy", "list listeners: "+diagnosticCommandError(res, err)
		return result
	}
	return edgeListenerExposureResult(result, res.Stdout)
}

func edgeListenerExposureResult(result *health.CheckResult, output string) *health.CheckResult {
	listeners, policy, err := parseEdgeListenerProbe(output)
	if err != nil {
		result.Status, result.Error = "unhealthy", err.Error()
		return result
	}
	exposed := parseWildcardListenersAllowing(listeners, policy.allows)
	if len(exposed) == 0 {
		result.OK, result.Status = true, "healthy"
		result.Message = "no listener outside the edge's public set binds every interface"
		return result
	}
	parts := make([]string, 0, len(exposed))
	for _, l := range exposed {
		parts = append(parts, l.String())
	}
	result.Status = "unhealthy"
	result.Error = fmt.Sprintf("%d listener(s) bind every interface outside the edge's public set: %s", len(exposed), strings.Join(parts, ", "))
	return result
}
