package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

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
	`echo "resolved_dns_ex=$(busctl get-property org.freedesktop.resolve1 /org/freedesktop/resolve1 org.freedesktop.resolve1.Manager DNSEx 2>/dev/null)"; ` +
	privateerDNSForwardCounterProbe

// privateerDNSForwardCounterProbe prints Privateer's cumulative count of
// non-.internal queries since it started.
const privateerDNSForwardCounterProbe = `m=$(curl -fsS --max-time 3 http://127.0.0.1:18012/metrics 2>/dev/null) && ` +
	`printf '%s\n' "$m" | awk '/^privateer_dns_queries_total\{/ && /type="forward"/ {s += $NF} END {printf "forward_queries=%d\n", s}' || echo forward_queries=unknown`

// privateerDNSObservationWindow is how long the check watches the forward
// counter. The counter is cumulative since Privateer started, so one refused
// query days ago says nothing about how the resolver routes names now; only
// growth during the window does.
const privateerDNSObservationWindow = 15 * time.Second

// privateerDNSState is one host's parsed probe output.
type privateerDNSState struct {
	WG0Internal     bool
	WG0DefaultRoute bool
	Upstream        bool
	// ForwardQueries is Privateer's cumulative forward counter, -1 when its
	// metrics could not be read.
	ForwardQueries int64
	// GlobalLoopbackDNS lists the loopback resolvers in resolved's global
	// scope, which resolved asks for every name no link routing domain claims.
	GlobalLoopbackDNS []string
}

// resolvedGlobalDNSServers returns the global-scope servers (ifindex 0) from
// busctl's rendering of resolve1.Manager.DNSEx, a(iiayqs): ifindex, family,
// address bytes, port, server name. resolved reports a loopback address set on
// a link under the loopback ifindex, and resolvectl prints those as "Global";
// they are still link-scoped and are not returned.
func resolvedGlobalDNSServers(dnsEx string) ([]string, error) {
	fields := strings.Fields(strings.TrimSpace(dnsEx))
	if len(fields) < 2 || fields[0] != "a(iiayqs)" {
		return nil, fmt.Errorf("unexpected DNSEx %q", dnsEx)
	}
	count, err := strconv.Atoi(fields[1])
	if err != nil {
		return nil, fmt.Errorf("unexpected DNSEx count %q", fields[1])
	}
	pos := 2
	next := func() (int, error) {
		if pos >= len(fields) {
			return 0, fmt.Errorf("truncated DNSEx %q", dnsEx)
		}
		n, err := strconv.Atoi(fields[pos])
		pos++
		return n, err
	}
	var global []string
	for range count {
		ifindex, ifErr := next()
		_, familyErr := next()
		size, sizeErr := next()
		if err := errors.Join(ifErr, familyErr, sizeErr); err != nil {
			return nil, err
		}
		addr := make(net.IP, 0, size)
		for range size {
			b, byteErr := next()
			if byteErr != nil {
				return nil, byteErr
			}
			addr = append(addr, byte(b))
		}
		if _, portErr := next(); portErr != nil {
			return nil, portErr
		}
		if pos >= len(fields) {
			return nil, fmt.Errorf("truncated DNSEx %q", dnsEx)
		}
		pos++ // server name
		if ifindex == 0 {
			global = append(global, addr.String())
		}
	}
	return global, nil
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
		case "resolved_dns_ex":
			if servers, err := resolvedGlobalDNSServers(value); err == nil {
				for _, server := range servers {
					if ip := net.ParseIP(server); ip != nil && ip.IsLoopback() {
						state.GlobalLoopbackDNS = append(state.GlobalLoopbackDNS, server)
					}
				}
			}
		case "forward_queries":
			if n, err := strconv.ParseInt(value, 10, 64); err == nil {
				state.ForwardQueries = n
			}
		}
	}
	return state
}

// problems lists why this host's resolver could send public names to
// Privateer, or stall them behind it. forwardedInWindow is the number of
// non-.internal queries Privateer answered during the observation window, -1
// when it is unknown.
func (s privateerDNSState) problems(forwardedInWindow int64) []string {
	var out []string
	if !s.WG0Internal {
		out = append(out, "wg0 does not route ~internal to Privateer: .internal names do not resolve through the mesh")
	}
	if s.WG0DefaultRoute {
		out = append(out, "wg0 is a default DNS route: public lookups can be sent to Privateer")
	}
	if !s.Upstream && len(s.GlobalLoopbackDNS) > 0 {
		out = append(out, fmt.Sprintf("resolved's global DNS scope sends public names to %s, a loopback resolver, and Privateer has no upstream to forward them: those lookups are refused there and wait on the link servers", strings.Join(s.GlobalLoopbackDNS, ", ")))
	}
	if !s.Upstream && forwardedInWindow > 0 {
		out = append(out, fmt.Sprintf("Privateer has no upstream but answered %d non-.internal quer(ies) during the %s observation window: public lookups are routed to it", forwardedInWindow, privateerDNSObservationWindow))
	}
	return out
}

// forwardedDuring returns how many forward queries Privateer answered between
// two reads of its cumulative counter, or -1 when either read failed. A lower
// second read means Privateer restarted, so the second read is all growth.
func forwardedDuring(start, end int64) int64 {
	if start < 0 || end < 0 {
		return -1
	}
	if end < start {
		return end
	}
	return end - start
}

// checkPrivateerDNSScope probes every Privateer host and reports hosts whose
// resolver routes public names to Privateer. Hosts without an upstream are
// read twice, window apart, and judged on the forward queries answered in
// between. It is read-only and returns the number of hosts with a problem or
// that could not be probed.
func checkPrivateerDNSScope(ctx context.Context, out, errOut io.Writer, manifest *inventory.Manifest, runnerFor func(inventory.Host) (ssh.Runner, error), window time.Duration) int {
	names := meshCheckHostNames(manifest)
	if len(names) == 0 {
		fmt.Fprintln(out, "No Privateer hosts in this manifest.")
		return 0
	}
	fmt.Fprintln(out, "\nPrivateer DNS scope:")
	type probed struct {
		name   string
		runner ssh.Runner
		state  privateerDNSState
	}
	failures := 0
	var hosts []probed
	watch := false
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
		hosts = append(hosts, probed{name: name, runner: runner, state: state})
		if !state.Upstream && state.ForwardQueries >= 0 {
			watch = true
		}
	}
	if watch {
		timer := time.NewTimer(window)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	for _, h := range hosts {
		forwarded := int64(-1)
		if !h.state.Upstream && h.state.ForwardQueries >= 0 && ctx.Err() == nil {
			if result, err := h.runner.Run(ctx, privateerDNSForwardCounterProbe); err == nil && result != nil {
				forwarded = forwardedDuring(h.state.ForwardQueries, parsePrivateerDNSProbe(result.Stdout).ForwardQueries)
			}
		}
		problems := h.state.problems(forwarded)
		if len(problems) > 0 {
			failures++
			for _, problem := range problems {
				ux.Fail(errOut, fmt.Sprintf("%s: %s", h.name, problem))
			}
			continue
		}
		detail := "forward counter unreadable"
		switch {
		case h.state.Upstream && h.state.ForwardQueries >= 0:
			detail = fmt.Sprintf("%d forwarded since start", h.state.ForwardQueries)
		case forwarded >= 0:
			detail = fmt.Sprintf("%d forwarded in %s", forwarded, window)
		}
		ux.Success(out, fmt.Sprintf("%s: ~internal on wg0 without default route (upstream=%t, %s)", h.name, h.state.Upstream, detail))
	}
	return failures
}
