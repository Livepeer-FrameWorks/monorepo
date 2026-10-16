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
	edgeYAML := "version: v1\nroot_domain: example.com\nnodes:\n  - name: edge-eu\n    ssh: ops@203.0.113.7\n"
	if err := os.WriteFile(filepath.Join(dir, "edge.yaml"), []byte(edgeYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	rc := &resolvedCluster{
		Manifest:     &inventory.Manifest{Hosts: map[string]inventory.Host{"core": {Name: "core", ExternalIP: "10.0.0.1", User: "root"}}},
		ManifestPath: filepath.Join(dir, "cluster.yaml"),
	}
	targets := diagnoseHostTargets(io.Discard, rc, "")
	var edge *diagnoseHost
	for i := range targets {
		if targets[i].Name == "edge-eu" {
			edge = &targets[i]
		}
	}
	if edge == nil {
		t.Fatalf("edge node from edge.yaml missing from diagnose targets: %+v", targets)
	}
	if !edge.Edge || edge.Host.ExternalIP != "203.0.113.7" || edge.Host.User != "ops" {
		t.Fatalf("edge target = %+v, want edge host ops@203.0.113.7", *edge)
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
		if strings.HasPrefix(command, "ss ") {
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
