package cmd

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

// privateerDNSProbe reads, without changing anything, how a host routes DNS to
// Privateer: whether wg0 carries the ~internal routing domain, whether wg0 is
// a default DNS route, whether Privateer has an upstream for other names, and
// how many non-.internal queries Privateer has answered.
const privateerDNSProbe = `st=$(resolvectl status wg0 2>/dev/null); ` +
	`if printf '%s\n' "$st" | grep -qE 'DNS Domain:.*~internal'; then echo wg0_internal=yes; else echo wg0_internal=no; fi; ` +
	`if printf '%s\n' "$st" | grep -qE '\+DefaultRoute|DefaultRoute setting: yes'; then echo wg0_default_route=yes; else echo wg0_default_route=no; fi; ` +
	`env=$(sudo -n cat /etc/privateer/privateer.env 2>/dev/null || cat /etc/privateer/privateer.env 2>/dev/null); ` +
	`if printf '%s\n' "$env" | grep -qE '^UPSTREAM_DNS=.+'; then echo upstream=yes; else echo upstream=no; fi; ` +
	`m=$(curl -fsS --max-time 3 http://127.0.0.1:18012/metrics 2>/dev/null) && ` +
	`printf '%s\n' "$m" | awk '/^privateer_dns_queries_total\{/ && /type="forward"/ {s += $NF} END {printf "forward_queries=%d\n", s}' || echo forward_queries=unknown`

// privateerDNSState is one host's parsed probe output.
type privateerDNSState struct {
	WG0Internal     bool
	WG0DefaultRoute bool
	Upstream        bool
	// ForwardQueries is -1 when Privateer's metrics could not be read.
	ForwardQueries int64
}

func parsePrivateerDNSProbe(stdout string) privateerDNSState {
	state := privateerDNSState{ForwardQueries: -1}
	for _, line := range strings.Split(stdout, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "wg0_internal":
			state.WG0Internal = value == "yes"
		case "wg0_default_route":
			state.WG0DefaultRoute = value == "yes"
		case "upstream":
			state.Upstream = value == "yes"
		case "forward_queries":
			if n, err := strconv.ParseInt(value, 10, 64); err == nil {
				state.ForwardQueries = n
			}
		}
	}
	return state
}

// problems lists why this host's resolver could send public names to
// Privateer, or stall them behind it.
func (s privateerDNSState) problems() []string {
	var out []string
	if !s.WG0Internal {
		out = append(out, "wg0 does not route ~internal to Privateer: .internal names do not resolve through the mesh")
	}
	if s.WG0DefaultRoute {
		out = append(out, "wg0 is a default DNS route: public lookups can be sent to Privateer")
	}
	if !s.Upstream && s.ForwardQueries > 0 {
		out = append(out, fmt.Sprintf("Privateer has no upstream but answered %d non-.internal quer(ies): public lookups are routed to it", s.ForwardQueries))
	}
	return out
}

// checkPrivateerDNSScope probes every Privateer host and reports hosts whose
// resolver routes public names to Privateer. It is read-only and returns the
// number of hosts with a problem or that could not be probed.
func checkPrivateerDNSScope(ctx context.Context, out, errOut io.Writer, manifest *inventory.Manifest, runnerFor func(inventory.Host) (ssh.Runner, error)) int {
	names := meshCheckHostNames(manifest)
	if len(names) == 0 {
		fmt.Fprintln(out, "No Privateer hosts in this manifest.")
		return 0
	}
	fmt.Fprintln(out, "\nPrivateer DNS scope:")
	failures := 0
	for _, name := range names {
		host, ok := manifest.GetHost(name)
		if !ok {
			failures++
			ux.Fail(errOut, fmt.Sprintf("%s: not found in the manifest", name))
			continue
		}
		runner, err := runnerFor(host)
		if err != nil {
			failures++
			ux.Fail(errOut, fmt.Sprintf("%s: connect: %v", name, err))
			continue
		}
		result, err := runner.Run(ctx, privateerDNSProbe)
		if err != nil || result == nil {
			failures++
			ux.Fail(errOut, fmt.Sprintf("%s: probe failed: %v", name, err))
			continue
		}
		state := parsePrivateerDNSProbe(result.Stdout)
		problems := state.problems()
		if len(problems) > 0 {
			failures++
			for _, problem := range problems {
				ux.Fail(errOut, fmt.Sprintf("%s: %s", name, problem))
			}
			continue
		}
		forwarded := "forward counter unreadable"
		if state.ForwardQueries >= 0 {
			forwarded = fmt.Sprintf("%d forwarded", state.ForwardQueries)
		}
		ux.Success(out, fmt.Sprintf("%s: ~internal on wg0 without default route (upstream=%t, %s)", name, state.Upstream, forwarded))
	}
	return failures
}
