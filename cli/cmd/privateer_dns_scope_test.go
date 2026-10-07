package cmd

import (
	"bytes"
	"context"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

// dnsProbeRunner answers the Privateer DNS probe per host and records every
// command so the test can prove the check stays read-only.
type dnsProbeRunner struct {
	host     string
	outputs  map[string]string
	commands *[]string
}

func (r *dnsProbeRunner) Run(_ context.Context, command string) (*ssh.CommandResult, error) {
	*r.commands = append(*r.commands, r.host+": "+command)
	return &ssh.CommandResult{Stdout: r.outputs[r.host]}, nil
}

func (r *dnsProbeRunner) RunScript(context.Context, string) (*ssh.CommandResult, error) {
	return &ssh.CommandResult{ExitCode: 1}, nil
}
func (r *dnsProbeRunner) Upload(context.Context, ssh.UploadOptions) error { return nil }
func (r *dnsProbeRunner) Close() error                                    { return nil }

func TestPrivateerDNSProbeIsValidShell(t *testing.T) {
	if out, err := exec.CommandContext(t.Context(), "sh", "-n", "-c", privateerDNSProbe).CombinedOutput(); err != nil {
		t.Fatalf("probe does not parse: %v\n%s", err, out)
	}
}

func TestPrivateerDNSStateProblems(t *testing.T) {
	cases := []struct {
		name  string
		probe string
		want  []string
	}{
		{"scoped to wg0, nothing forwarded", "wg0_internal=yes\nwg0_default_route=no\nupstream=no\nforward_queries=0\n", nil},
		{"upstream configured may forward", "wg0_internal=yes\nwg0_default_route=no\nupstream=yes\nforward_queries=12\n", nil},
		{"metrics unreadable is not a verdict", "wg0_internal=yes\nwg0_default_route=no\nupstream=no\nforward_queries=unknown\n", nil},
		{"public names reach privateer", "wg0_internal=yes\nwg0_default_route=no\nupstream=no\nforward_queries=2\n", []string{"no upstream but answered 2"}},
		{"wg0 is a default route", "wg0_internal=yes\nwg0_default_route=yes\nupstream=no\nforward_queries=0\n", []string{"default DNS route"}},
		{"no internal routing", "wg0_internal=no\nwg0_default_route=no\nupstream=no\nforward_queries=0\n", []string{"does not route ~internal"}},
	}
	for _, tc := range cases {
		state := parsePrivateerDNSProbe(tc.probe)
		got := state.problems(state.ForwardQueries)
		if len(got) != len(tc.want) {
			t.Fatalf("%s: problems = %q, want %d matching %q", tc.name, got, len(tc.want), tc.want)
		}
		for i, want := range tc.want {
			if !strings.Contains(got[i], want) {
				t.Fatalf("%s: problem %q does not mention %q", tc.name, got[i], want)
			}
		}
	}
}

// The DNSEx values below are what `busctl get-property ... Manager DNSEx`
// printed on fw-stg-eu-1 (systemd 255). resolved reports wg0's link server
// 127.0.0.1 under the loopback ifindex 1, which resolvectl lists as "Global";
// only an ifindex-0 entry is a real global server.
const (
	stagingDNSEx            = `a(iiayqs) 3 1 2 4 127 0 0 1 0 "" 2 2 4 192 168 10 1 0 "" 2 10 16 42 2 34 160 187 186 219 1 0 0 0 0 0 0 0 0 0 ""`
	globalPrivateerDNSEx    = `a(iiayqs) 2 0 2 4 127 0 0 1 0 "" 2 2 4 192 168 10 1 0 ""`
	globalPublicServerDNSEx = `a(iiayqs) 1 0 2 4 9 9 9 9 853 "dns.quad9.net"`
)

func TestPrivateerDNSStateFlagsGlobalLoopbackResolver(t *testing.T) {
	cases := []struct {
		name     string
		dnsEx    string
		upstream bool
		want     string
	}{
		{"wg0 link server shown as global is not a global route", stagingDNSEx, false, ""},
		{"global scope points at privateer without upstream", globalPrivateerDNSEx, false, "global DNS scope sends public names to 127.0.0.1"},
		{"privateer with upstream may be global", globalPrivateerDNSEx, true, ""},
		{"public global server is the operator's choice", globalPublicServerDNSEx, false, ""},
	}
	for _, tc := range cases {
		probe := "wg0_internal=yes\nwg0_default_route=no\nupstream=" + map[bool]string{true: "yes", false: "no"}[tc.upstream] + "\nforward_queries=0\nresolved_dns_ex=" + tc.dnsEx + "\n"
		state := parsePrivateerDNSProbe(probe)
		got := state.problems(0)
		if tc.want == "" {
			if len(got) != 0 {
				t.Fatalf("%s: problems = %q, want none", tc.name, got)
			}
			continue
		}
		if len(got) != 1 || !strings.Contains(got[0], tc.want) {
			t.Fatalf("%s: problems = %q, want one mentioning %q", tc.name, got, tc.want)
		}
	}
}

func TestParseResolvedDNSExGlobalServers(t *testing.T) {
	got, err := resolvedGlobalDNSServers(stagingDNSEx)
	if err != nil || len(got) != 0 {
		t.Fatalf("staging DNSEx global servers = %v, %v; want none", got, err)
	}
	got, err = resolvedGlobalDNSServers(globalPrivateerDNSEx)
	if err != nil || len(got) != 1 || got[0] != "127.0.0.1" {
		t.Fatalf("global servers = %v, %v; want [127.0.0.1]", got, err)
	}
	if _, err := resolvedGlobalDNSServers("a(iiayqs) 2 0 2 4 127"); err == nil {
		t.Fatal("truncated DNSEx parsed without error")
	}
}

func TestPrivateerDNSScopeReportsLeakingHostsReadOnly(t *testing.T) {
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{
			"core-1": {Name: "core-1", ExternalIP: "10.0.0.1"},
			"edge-1": {Name: "edge-1", ExternalIP: "10.0.0.2"},
		},
		WireGuard: &inventory.WireGuardConfig{Enabled: true},
	}
	outputs := map[string]string{
		"core-1": "wg0_internal=yes\nwg0_default_route=no\nupstream=no\nforward_queries=0\n",
		"edge-1": "wg0_internal=yes\nwg0_default_route=yes\nupstream=no\nforward_queries=7\n",
	}
	var commands []string
	var out, errOut bytes.Buffer
	failures := checkPrivateerDNSScope(context.Background(), &out, &errOut, manifest, func(host inventory.Host) (ssh.Runner, error) {
		return &dnsProbeRunner{host: host.Name, outputs: outputs, commands: &commands}, nil
	}, time.Millisecond)
	if failures != 1 {
		t.Fatalf("failures = %d, want only edge-1\nstdout:\n%s\nstderr:\n%s", failures, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "core-1: ~internal on wg0 without default route") {
		t.Fatalf("core-1 not reported healthy:\n%s", out.String())
	}
	for _, want := range []string{"edge-1: wg0 is a default DNS route"} {
		if !strings.Contains(errOut.String(), want) {
			t.Fatalf("missing %q in:\n%s", want, errOut.String())
		}
	}
	for _, command := range commands {
		for _, write := range []string{"resolvectl dns", "resolvectl domain", "systemctl restart", " > /etc", "tee "} {
			if strings.Contains(command, write) {
				t.Fatalf("probe must stay read-only, found %q in %q", write, command)
			}
		}
	}
}

// counterRunner answers the full probe with a fixed state and every later
// counter read with the next value, like a live Privateer whose cumulative
// forward counter only grows while public names keep reaching it.
type counterRunner struct {
	state    string
	counters []int64
	reads    int
}

func (r *counterRunner) Run(_ context.Context, command string) (*ssh.CommandResult, error) {
	value := r.counters[min(r.reads, len(r.counters)-1)]
	r.reads++
	if command == privateerDNSProbe {
		return &ssh.CommandResult{Stdout: r.state + "forward_queries=" + strconv.FormatInt(value, 10) + "\n"}, nil
	}
	return &ssh.CommandResult{Stdout: "forward_queries=" + strconv.FormatInt(value, 10) + "\n"}, nil
}

func (r *counterRunner) RunScript(context.Context, string) (*ssh.CommandResult, error) {
	return &ssh.CommandResult{ExitCode: 1}, nil
}
func (r *counterRunner) Upload(context.Context, ssh.UploadOptions) error { return nil }
func (r *counterRunner) Close() error                                    { return nil }

// A refused forward query counted once since Privateer started must not fail
// the check forever: only queries forwarded during the observation window are
// evidence that public names are still routed to Privateer now.
func TestPrivateerDNSScopeJudgesForwardRateOverWindow(t *testing.T) {
	manifest := &inventory.Manifest{
		Hosts:     map[string]inventory.Host{"eu-1": {Name: "eu-1", ExternalIP: "10.0.0.1"}},
		WireGuard: &inventory.WireGuardConfig{Enabled: true},
	}
	state := "wg0_internal=yes\nwg0_default_route=no\nupstream=no\n"
	cases := []struct {
		name     string
		counters []int64
		failures int
		want     string
	}{
		{"one historic refusal, none during the window", []int64{1, 1}, 0, "eu-1: ~internal on wg0"},
		{"public names forwarded during the window", []int64{1, 4}, 1, "answered 3 non-.internal quer(ies) during the"},
		{"privateer restarted during the window", []int64{9, 2}, 1, "answered 2 non-.internal quer(ies) during the"},
	}
	for _, tc := range cases {
		var out, errOut bytes.Buffer
		runner := &counterRunner{state: state, counters: tc.counters}
		failures := checkPrivateerDNSScope(context.Background(), &out, &errOut, manifest, func(inventory.Host) (ssh.Runner, error) {
			return runner, nil
		}, time.Millisecond)
		if failures != tc.failures {
			t.Fatalf("%s: failures = %d, want %d\nstdout:\n%s\nstderr:\n%s", tc.name, failures, tc.failures, out.String(), errOut.String())
		}
		if !strings.Contains(out.String()+errOut.String(), tc.want) {
			t.Fatalf("%s: missing %q\nstdout:\n%s\nstderr:\n%s", tc.name, tc.want, out.String(), errOut.String())
		}
	}
}
