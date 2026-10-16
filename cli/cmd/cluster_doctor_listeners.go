package cmd

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"frameworks/cli/pkg/health"
	"frameworks/cli/pkg/inventory"
	fwssh "frameworks/cli/pkg/ssh"
)

// doctorListenerCommand lists every TCP and UDP listener on a host, with the
// owning process when the doctor may run ss as root.
const doctorListenerCommand = "sudo -n ss -Hltunp 2>/dev/null || ss -Hltun"

// exposedListener is a socket bound to every interface of its host.
type exposedListener struct {
	Proto   string
	Address string
	Port    int
	Process string
}

func (l exposedListener) String() string {
	out := fmt.Sprintf("%s %s", l.Proto, net.JoinHostPort(strings.Trim(l.Address, "[]"), strconv.Itoa(l.Port)))
	if l.Process != "" {
		out += " (" + l.Process + ")"
	}
	return out
}

// publicListenerPorts are the ports a platform host may listen on with a
// wildcard address: SSH, the reverse proxy's 80/443 on a proxy host,
// Foghorn's external gRPC port on a Foghorn host (edges dial it from outside
// the mesh), and the WireGuard UDP port. Everything else binds loopback or the
// host's WireGuard address (docs/architecture/tls.md, Listener binds).
func publicListenerPorts(manifest *inventory.Manifest, hostName string) (tcp, udp map[int]bool) {
	tcp = map[int]bool{22: true}
	udp = map[int]bool{}
	if manifest == nil {
		return tcp, udp
	}
	host, _ := manifest.GetHost(hostName)
	wgPort := host.WireguardPort
	if wgPort == 0 && manifest.WireGuard != nil && manifest.WireGuard.ListenPort > 0 {
		wgPort = manifest.WireGuard.ListenPort
	}
	if wgPort == 0 {
		wgPort = 51820
	}
	udp[wgPort] = true
	runs := func(services map[string]inventory.ServiceConfig, deploy string) bool {
		for name, svc := range services {
			id := svc.Deploy
			if id == "" {
				id = name
			}
			if id != deploy || !svc.Enabled {
				continue
			}
			for _, h := range serviceHosts(svc) {
				if h == hostName {
					return true
				}
			}
		}
		return false
	}
	if runs(manifest.Interfaces, "nginx") || runs(manifest.Interfaces, "caddy") {
		tcp[80], tcp[443] = true, true
	}
	if runs(manifest.Services, "foghorn") {
		tcp[18029] = true
	}
	return tcp, udp
}

// parseWildcardListeners returns the listeners in `ss -Hltun[p]` output that
// bind every interface and are not in the allowed public ports.
func parseWildcardListeners(output string, tcpAllowed, udpAllowed map[int]bool) []exposedListener {
	var out []exposedListener
	for line := range strings.SplitSeq(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		proto := fields[0]
		local := fields[4]
		sep := strings.LastIndex(local, ":")
		if sep <= 0 {
			continue
		}
		address, portText := local[:sep], local[sep+1:]
		if iface := strings.Index(address, "%"); iface >= 0 {
			// A socket bound to one interface: loopback, or an interface
			// address such as a DHCP client, is not exposed on every one.
			continue
		}
		if !wildcardAddress(address) {
			continue
		}
		port, err := strconv.Atoi(portText)
		if err != nil {
			continue
		}
		if (proto == "tcp" && tcpAllowed[port]) || (proto == "udp" && udpAllowed[port]) {
			continue
		}
		process := ""
		if start := strings.Index(line, `(("`); start >= 0 {
			rest := line[start+3:]
			if end := strings.Index(rest, `"`); end >= 0 {
				process = rest[:end]
			}
		}
		out = append(out, exposedListener{Proto: proto, Address: address, Port: port, Process: process})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		return out[i].Proto+out[i].Address < out[j].Proto+out[j].Address
	})
	return out
}

func wildcardAddress(address string) bool {
	switch strings.Trim(address, "[]") {
	case "0.0.0.0", "*", "::", "::ffff:0.0.0.0":
		return true
	}
	return false
}

// checkListenerExposure reports every listener on host bound to every
// interface outside the public set, which a client on the LAN or the
// internet could reach past the reverse proxy and the mesh.
func checkListenerExposure(ctx context.Context, pool *fwssh.Pool, manifest *inventory.Manifest, host inventory.Host) *health.CheckResult {
	result := &health.CheckResult{Name: "listener_exposure", CheckedAt: time.Now(), Metadata: map[string]string{"access": "ssh-local-ss"}}
	runner, err := getRunner(host, pool)
	if err != nil {
		result.Status, result.Error = "unhealthy", fmt.Sprintf("ssh connect: %v", err)
		return result
	}
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	res, err := runner.Run(probeCtx, doctorListenerCommand)
	if err != nil || res == nil || res.ExitCode != 0 {
		result.Status, result.Error = "unhealthy", "list listeners: "+diagnosticCommandError(res, err)
		return result
	}
	tcp, udp := publicListenerPorts(manifest, host.Name)
	exposed := parseWildcardListeners(res.Stdout, tcp, udp)
	if len(exposed) == 0 {
		result.OK, result.Status = true, "healthy"
		result.Message = "no listener outside the public set binds every interface"
		return result
	}
	parts := make([]string, 0, len(exposed))
	for _, l := range exposed {
		parts = append(parts, l.String())
	}
	result.Status = "unhealthy"
	result.Error = fmt.Sprintf("%d listener(s) bind every interface outside the public set: %s", len(exposed), strings.Join(parts, ", "))
	return result
}

// doctorListenerChecks audits every platform host's listeners.
func doctorListenerChecks(pool *fwssh.Pool, manifest *inventory.Manifest) []doctorCheck {
	names := make([]string, 0, len(manifest.Hosts))
	for name := range manifest.Hosts {
		names = append(names, name)
	}
	sort.Strings(names)
	checks := make([]doctorCheck, 0, len(names))
	for _, name := range names {
		host, _ := manifest.GetHost(name)
		label := "Listeners on " + name
		checks = append(checks, doctorCheck{name: label, run: func(ctx context.Context) doctorOutcome {
			return doctorInfraOutcome(label, checkListenerExposure(ctx, pool, manifest, host))
		}})
	}
	return checks
}
