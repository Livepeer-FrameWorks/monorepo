package cmd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

// hostReplyRunner answers each command through reply and records it, so a
// test can stand in for a host with a fixed system state.
type hostReplyRunner struct {
	host     string
	reply    func(host, command string) (stdout string, exitCode int)
	commands *[]string
}

func (r *hostReplyRunner) Run(_ context.Context, command string) (*ssh.CommandResult, error) {
	if r.commands != nil {
		*r.commands = append(*r.commands, r.host+": "+command)
	}
	stdout, exitCode := r.reply(r.host, command)
	result := &ssh.CommandResult{Command: command}
	var runErr error
	if exitCode != 0 {
		runErr = fmt.Errorf("exit status %d", exitCode)
	}
	// CompleteRun applies the production trimming and exit-code semantics.
	result, err := ssh.CompleteRun(result, r.host, command, []byte(stdout), nil, runErr)
	if exitCode != 0 {
		result.ExitCode = exitCode
	}
	return result, err
}

func (r *hostReplyRunner) RunScript(context.Context, string) (*ssh.CommandResult, error) {
	return &ssh.CommandResult{ExitCode: 1}, nil
}
func (r *hostReplyRunner) Upload(context.Context, ssh.UploadOptions) error { return nil }
func (r *hostReplyRunner) Close() error                                    { return nil }

func replyRunnerFor(reply func(host, command string) (string, int), commands *[]string) func(inventory.Host) (ssh.Runner, error) {
	return func(host inventory.Host) (ssh.Runner, error) {
		return &hostReplyRunner{host: host.Name, reply: reply, commands: commands}, nil
	}
}

func TestDiagnoseHostTargetsIncludeEdgeNodesBesideTheManifest(t *testing.T) {
	dir := t.TempDir()
	edgeYAML := "version: v1\nroot_domain: example.com\nmode: docker\nnodes:\n  - name: edge-eu\n    ssh: ops@203.0.113.7\n  - name: edge-us\n    ssh: ops@203.0.113.8\n    mode: native\n"
	if err := os.WriteFile(filepath.Join(dir, "edge.yaml"), []byte(edgeYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	rc := &resolvedCluster{
		Manifest:     &inventory.Manifest{Hosts: map[string]inventory.Host{"core": {Name: "core", ExternalIP: "10.0.0.1", User: "root"}}},
		ManifestPath: filepath.Join(dir, "cluster.yaml"),
	}
	targets := diagnoseHostTargets(io.Discard, rc, "")
	var edge, native *diagnoseHost
	for i := range targets {
		switch targets[i].Name {
		case "edge-eu":
			edge = &targets[i]
		case "edge-us":
			native = &targets[i]
		}
	}
	if edge == nil || native == nil {
		t.Fatalf("edge nodes from edge.yaml missing from diagnose targets: %+v", targets)
	}
	if !edge.Edge || edge.Host.ExternalIP != "203.0.113.7" || edge.Host.User != "ops" {
		t.Fatalf("edge target = %+v, want edge host ops@203.0.113.7", *edge)
	}
	// The manifest's deprecated "docker" default resolves to container; the
	// per-node override wins.
	if edge.EdgeMode != "container" || native.EdgeMode != "native" {
		t.Fatalf("edge modes = %q/%q, want container/native", edge.EdgeMode, native.EdgeMode)
	}
}

func TestDiagnoseNetworkProbesEdgesAndCountsUnreachableHosts(t *testing.T) {
	targets := []diagnoseHost{
		{Name: "core", Host: inventory.Host{Name: "core", ExternalIP: "10.0.0.1"}},
		{Name: "edge-eu", Host: inventory.Host{Name: "edge-eu", ExternalIP: "203.0.113.7"}, Edge: true},
	}
	var commands []string
	reply := func(host, command string) (string, int) {
		if host == "edge-eu" && strings.Contains(command, "10.0.0.1") {
			return "", 1
		}
		return "1 packets transmitted, 1 received", 0
	}
	var stdout, stderr bytes.Buffer
	failures := diagnoseNetwork(context.Background(), &stdout, &stderr, targets, replyRunnerFor(reply, &commands))
	if failures != 1 {
		t.Fatalf("failures = %d, want 1 (edge-eu cannot reach core)\nstdout:\n%s\nstderr:\n%s", failures, stdout.String(), stderr.String())
	}
	if !strings.Contains(strings.Join(commands, "\n"), "edge-eu: ping") {
		t.Fatalf("edge host was never a ping source: %q", commands)
	}
}

// portHost answers both the per-port netstat probe and an ss listing for a
// host whose only listeners are listening.
func portHostReply(listening map[int]bool) func(host, command string) (string, int) {
	portArg := regexp.MustCompile(`':(\d+) '`)
	return func(_, command string) (string, int) {
		if m := portArg.FindStringSubmatch(command); m != nil {
			port, _ := strconv.Atoi(m[1])
			if listening[port] {
				return fmt.Sprintf("tcp        0      0 0.0.0.0:%d            0.0.0.0:*               LISTEN\n", port), 0
			}
			return "free\n", 0
		}
		if command == hostListenerShell {
			var b strings.Builder
			for port := range listening {
				fmt.Fprintf(&b, "tcp   LISTEN 0      4096               *:%d            *:*\n", port)
			}
			return b.String(), 0
		}
		return "", 127
	}
}

func TestDiagnosePortsReportsPlannedPortsThatAreNotListening(t *testing.T) {
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{"core": {Name: "core", ExternalIP: "10.0.0.1"}},
		Services: map[string]inventory.ServiceConfig{
			"quartermaster": {Enabled: true, Host: "core"},
			"purser":        {Enabled: true, Host: "core"},
		},
	}
	targets := []diagnoseHost{{Name: "core", Host: manifest.Hosts["core"]}}
	// quartermaster HTTP and gRPC listen; purser is down.
	reply := portHostReply(map[int]bool{18002: true, 19002: true})
	var stdout, stderr bytes.Buffer
	failures := diagnosePorts(context.Background(), &stdout, &stderr, manifest, targets, replyRunnerFor(reply, nil))
	out := stdout.String() + stderr.String()
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "18003") && strings.Contains(line, "IN USE") {
			t.Fatalf("purser's free port 18003 reported in use: %q\n%s", line, out)
		}
		if strings.Contains(line, "3000") && strings.Contains(line, "grafana") && strings.Contains(line, "IN USE") {
			t.Fatalf("free grafana port reported in use on a host that does not run it: %q", line)
		}
	}
	if !strings.Contains(out, "18003") || !strings.Contains(out, "NOT LISTENING") {
		t.Fatalf("purser's planned port 18003 not reported as not listening:\n%s", out)
	}
	if failures != 2 {
		t.Fatalf("failures = %d, want 2 (purser HTTP and gRPC down)\n%s", failures, out)
	}
}

func TestDiagnoseResourcesPrintsEachMetricOnItsOwnLineAndChecksDataMounts(t *testing.T) {
	targets := []diagnoseHost{{Name: "db-1", Host: inventory.Host{Name: "db-1", ExternalIP: "10.0.0.2"}}}
	reply := func(_, command string) (string, int) {
		switch {
		case strings.Contains(command, "/proc/stat"):
			return strings.Join([]string{
				"ncpu 4",
				"cpu1 cpu  1000 0 500 8000 100 0 0 0 0 0",
				"cpu2 cpu  1100 0 550 8300 150 0 0 0 0 0",
				"memtotal 12000000000",
				"memavailable 6000000000",
				"load 0.18 0.18 0.08",
				"df /dev/mapper/vg-root 32000000000 17000000000 15000000000 /",
				"df /dev/sdb1 100000000000 95000000000 5000000000 /var/lib/yugabyte",
				"inode /dev/mapper/vg-root 2000000 200000 1800000 /",
				"inode /dev/sdb1 6000000 60000 5940000 /var/lib/yugabyte",
			}, "\n") + "\n", 0
		case strings.Contains(command, "top -bn1"):
			return "2.3\n", 0
		case strings.Contains(command, "free -h"):
			return "  Memory: 6.6Gi / 11Gi (60.00%)\n", 0
		case strings.Contains(command, "df -h /"):
			return "  Disk: 16G / 30G (56% used)\n", 0
		case strings.Contains(command, "uptime"):
			return " 0.18, 0.18, 0.08\n", 0
		}
		return "", 127
	}
	var stdout, stderr bytes.Buffer
	failures := diagnoseResources(context.Background(), &stdout, &stderr, targets, replyRunnerFor(reply, nil))
	out := stdout.String() + stderr.String()
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "Memory") && (strings.Contains(line, "Disk") || strings.Contains(line, "Load")) {
			t.Fatalf("metrics run together on one line: %q\n%s", line, out)
		}
	}
	if !strings.Contains(out, "/var/lib/yugabyte") {
		t.Fatalf("data mount /var/lib/yugabyte not checked:\n%s", out)
	}
	if failures != 1 {
		t.Fatalf("failures = %d, want 1 (the 95%% full data disk)\n%s", failures, out)
	}
}

// stagingNativeEdgeListeners is `ss -Hltnu` from staging edge fw-stg-edge-eu
// (native, vmagent installed) on 2026-10-16, before node_exporter rolled out.
const stagingNativeEdgeListeners = `tcp   LISTEN 0      4096         0.0.0.0:22         0.0.0.0:*
tcp   LISTEN 0      4096               *:1935             *:*
tcp   LISTEN 0      4096               *:4200             *:*
tcp   LISTEN 0      4096               *:443              *:*
tcp   LISTEN 0      4096               *:5554             *:*
tcp   LISTEN 0      4096               *:80               *:*
tcp   LISTEN 0      4096               *:8080             *:*
tcp   LISTEN 0      4096       127.0.0.1:18007      0.0.0.0:*
tcp   LISTEN 0      4096       127.0.0.1:18017      0.0.0.0:*
tcp   LISTEN 0      4096       127.0.0.1:2019       0.0.0.0:*
tcp   LISTEN 0      4096       127.0.0.1:4242       0.0.0.0:*
tcp   LISTEN 0      4096       127.0.0.1:8429       0.0.0.0:*
udp   UNCONN 0      0      [::ffff:127.0.0.1]:4242          *:*
udp   UNCONN 0      0                  *:443              *:*
udp   UNCONN 0      0            0.0.0.0:18203      0.0.0.0:*
udp   UNCONN 0      0            0.0.0.0:64142      0.0.0.0:*
udp   UNCONN 0      0            0.0.0.0:8889       0.0.0.0:*
`

func TestDiagnosePortsChecksEveryEdgeBundleListener(t *testing.T) {
	manifest := &inventory.Manifest{Hosts: map[string]inventory.Host{}}
	cases := []struct {
		name         string
		mode         string
		ss           string
		wantFailures int
		wantDown     []string
		wantUp       []string
	}{
		{
			name: "native edge with vmagent but no node_exporter",
			mode: "native",
			ss:   stagingNativeEdgeListeners,
			// vmagent runs, so the node_exporter installed beside it must too.
			wantFailures: 1,
			wantDown:     []string{"9110 node-exporter (tcp)"},
			wantUp: []string{
				"1935 mist-rtmp (tcp)", "8889 mist-srt (udp)", "18203 mist-webrtc (udp)",
				"18017 helmsman-management (tcp)", "4242 mist-api (tcp)", "2019 caddy-admin (tcp)", "443 caddy-http3 (udp)",
			},
		},
		{
			name:         "native edge without SRT",
			mode:         "native",
			ss:           strings.ReplaceAll(stagingNativeEdgeListeners, "udp   UNCONN 0      0            0.0.0.0:8889       0.0.0.0:*\n", "") + "tcp   LISTEN 0      4096       127.0.0.1:9110       0.0.0.0:*\n",
			wantFailures: 1,
			wantDown:     []string{"8889 mist-srt (udp)"},
			wantUp:       []string{"9110 node-exporter (tcp)"},
		},
		{
			name: "container edge where only Caddy and a Mist UDP socket on 4242 are up",
			mode: "container",
			ss: "tcp   LISTEN 0      4096               *:80               *:*\n" +
				"tcp   LISTEN 0      4096               *:443              *:*\n" +
				"udp   UNCONN 0      0                  *:443              *:*\n" +
				"udp   UNCONN 0      0      [::ffff:127.0.0.1]:4242          *:*\n",
			// Helmsman x2 and Mist HTTP, API, RTMP, DTSC, RTSP, SRT, WebRTC. No
			// Caddy admin port (a unix socket in the image) and no vmagent, so
			// no node_exporter either.
			wantFailures: 9,
			wantDown:     []string{"4242 mist-api (tcp)", "18007 helmsman (tcp)", "18203 mist-webrtc (udp)"},
			wantUp:       []string{"80 caddy-http (tcp)", "443 caddy-http3 (udp)"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			targets := []diagnoseHost{{Name: "edge-eu", Host: inventory.Host{Name: "edge-eu", ExternalIP: "203.0.113.7"}, Edge: true, EdgeMode: tc.mode}}
			reply := func(_, command string) (string, int) {
				if command == hostListenerShell {
					return tc.ss, 0
				}
				return "", 127
			}
			var stdout, stderr bytes.Buffer
			failures := diagnosePorts(context.Background(), &stdout, &stderr, manifest, targets, replyRunnerFor(reply, nil))
			out := stdout.String() + stderr.String()
			if failures != tc.wantFailures {
				t.Errorf("failures = %d, want %d", failures, tc.wantFailures)
			}
			for _, want := range tc.wantDown {
				if !strings.Contains(out, want+": NOT LISTENING") {
					t.Errorf("%q not reported down", want)
				}
			}
			for _, want := range tc.wantUp {
				if !strings.Contains(out, want+": listening") {
					t.Errorf("%q not reported listening", want)
				}
			}
			if tc.mode == "container" && strings.Contains(out, "caddy-admin") {
				t.Errorf("container edge planned the Caddy admin TCP port; its admin API is a unix socket")
			}
			if t.Failed() {
				t.Logf("output:\n%s", out)
			}
		})
	}
}
