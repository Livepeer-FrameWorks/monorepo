package cmd

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"frameworks/cli/pkg/health"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
)

func TestDoctorYugabyteFlagsTServersTheMastersReportDead(t *testing.T) {
	orig, origLiveness := yugabyteDoctorNodeProbeFn, yugabyteTServerLivenessFn
	t.Cleanup(func() { yugabyteDoctorNodeProbeFn, yugabyteTServerLivenessFn = orig, origLiveness })
	yugabyteDoctorNodeProbeFn = func(context.Context, *ssh.Pool, inventory.Host, *inventory.PostgresConfig) *health.CheckResult {
		return &health.CheckResult{OK: true, Status: "healthy", Latency: 20 * time.Millisecond, Metadata: map[string]string{}}
	}
	listing := `Tablet Server UUID               RPC Host/Port        Heartbeat delay  Status   Reads/s  Writes/s  Uptime
8eda5d7e0f1a4c5b9a0b1c2d3e4f5a6b 10.88.0.1:9100       0.35s            ALIVE    0.00     0.00      1000
9fdb6e8f1a2b4c5d8e9f0a1b2c3d4e5f 10.88.0.2:9100       120.10s          DEAD     0.00     0.00      0`
	yugabyteTServerLivenessFn = func(context.Context, *ssh.Pool, *inventory.Manifest, *inventory.PostgresConfig) ([]string, error) {
		return deadYugabyteTServers(parseYugabyteTServerList(listing), map[string]string{
			"10.88.0.1:9100": "yb-1", "10.88.0.2:9100": "yb-2", "10.88.0.3:9100": "yb-3",
		}), nil
	}
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{"yb-1": {Name: "yb-1"}},
		Infrastructure: inventory.InfrastructureConfig{Postgres: &inventory.PostgresConfig{
			Enabled: true, Engine: "yugabyte", Nodes: []inventory.PostgresNode{{Host: "yb-1", ID: 1}},
		}},
	}
	result := checkYugabyteCluster(context.Background(), nil, manifest, manifest.Infrastructure.Postgres)
	if result.OK || result.Status != "degraded" {
		t.Fatalf("dead tservers passed: %+v", result)
	}
	for _, want := range []string{"yb-2: tserver 10.88.0.2:9100 is DEAD", "yb-3: tserver 10.88.0.3:9100 not registered"} {
		if !strings.Contains(result.Error, want) {
			t.Fatalf("result %q missing %q", result.Error, want)
		}
	}
}

func TestDoctorYugabyteHealthyWhenEveryTServerIsFastAndAlive(t *testing.T) {
	orig, origLiveness := yugabyteDoctorNodeProbeFn, yugabyteTServerLivenessFn
	t.Cleanup(func() { yugabyteDoctorNodeProbeFn, yugabyteTServerLivenessFn = orig, origLiveness })
	yugabyteDoctorNodeProbeFn = func(context.Context, *ssh.Pool, inventory.Host, *inventory.PostgresConfig) *health.CheckResult {
		return &health.CheckResult{OK: true, Status: "healthy", Latency: 40 * time.Millisecond, Metadata: map[string]string{}}
	}
	yugabyteTServerLivenessFn = func(context.Context, *ssh.Pool, *inventory.Manifest, *inventory.PostgresConfig) ([]string, error) {
		return nil, nil
	}
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{"yb-1": {Name: "yb-1"}, "yb-2": {Name: "yb-2"}},
		Infrastructure: inventory.InfrastructureConfig{Postgres: &inventory.PostgresConfig{
			Enabled: true, Engine: "yugabyte", Nodes: []inventory.PostgresNode{{Host: "yb-1", ID: 1}, {Host: "yb-2", ID: 2}},
		}},
	}
	result := checkYugabyteCluster(context.Background(), nil, manifest, manifest.Infrastructure.Postgres)
	if !result.OK || !strings.Contains(result.Message, "2/2 tservers") {
		t.Fatalf("healthy cluster not reported healthy: %+v", result)
	}
}

func TestKafkaQuorumStatusParsesKafka4VoterObjectsAndPlainLists(t *testing.T) {
	status, err := parseKafkaQuorumStatus("ClusterId: x\nLeaderId: 101\nLeaderEpoch: 7\nMaxFollowerLagTimeMs: 0\nCurrentVoters: [{\"id\": 100, \"directoryId\": \"a\"}, {\"id\": 101, \"directoryId\": \"b\"}, {\"id\": 102, \"directoryId\": \"c\"}]\n")
	if err != nil || status.LeaderID != 101 || len(status.Voters) != 3 {
		t.Fatalf("status = %+v, err = %v", status, err)
	}
	if problems := status.problems(3); len(problems) != 0 {
		t.Fatalf("healthy quorum has problems %q", problems)
	}
	plain, err := parseKafkaQuorumStatus("LeaderId: 1\nLeaderEpoch: 3\nMaxFollowerLagTimeMs: 45000\nCurrentVoters: [1,2]\n")
	if err != nil {
		t.Fatal(err)
	}
	problems := strings.Join(plain.problems(3), "; ")
	if !strings.Contains(problems, "2 voter(s) [1 2], manifest declares 3") || !strings.Contains(problems, "45s behind") {
		t.Fatalf("problems = %q", problems)
	}
	if _, err := parseKafkaQuorumStatus("Error: timed out"); err == nil {
		t.Fatal("unreadable output parsed")
	}
}

func TestDiagnoseKafkaFailsOnControllerLeadershipLossInWindow(t *testing.T) {
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{"k1": {Name: "k1", ExternalIP: "10.0.0.5"}},
		Infrastructure: inventory.InfrastructureConfig{Kafka: &inventory.KafkaConfig{
			Enabled:     true,
			ClusterID:   "c1",
			Brokers:     []inventory.KafkaBroker{{Host: "k1", ID: 1, Port: 9092}},
			Controllers: []inventory.KafkaController{{Host: "k1", ID: 100, Port: 9093}},
		}},
	}
	var commands []string
	reply := func(_, command string) (string, int) {
		switch {
		case strings.Contains(command, "kafka-metadata-quorum"):
			return "LeaderId: 100\nLeaderEpoch: 9\nMaxFollowerLagTimeMs: 0\nCurrentVoters: [100]\n", 0
		case strings.Contains(command, "journalctl"):
			return "renounced=35 slow=198\n", 0
		}
		return "ok", 0
	}
	cmd := &cobra.Command{}
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	err := diagnoseKafka(context.Background(), cmd, manifest, replyRunnerFor(reply, &commands), "48 hours ago")
	out := stdout.String() + stderr.String()
	if err == nil || !strings.Contains(out, "gave up leadership with no successor 35 time(s) since 48 hours ago") || !strings.Contains(out, "198 slow event(s)") {
		t.Fatalf("leadership losses not reported (err=%v):\n%s", err, out)
	}
	if !strings.Contains(strings.Join(commands, "\n"), "journalctl -u 'frameworks-kafka-controller' --since '48 hours ago'") {
		t.Fatalf("controller journal not read for the --since window: %q", commands)
	}
}

func TestKafkaControllerJournalProbeCountsOnlyLeaderlessRenouncements(t *testing.T) {
	bin := t.TempDir()
	journal := `#!/bin/sh
cat <<'LOG'
[QuorumController id=100] Renouncing the leadership due to a metadata log event. We were the leader at epoch 5, but in the new epoch 6, the leader is (none). Reverting to last stable offset 10.
[QuorumController id=100] Renouncing the leadership due to a metadata log event. We were the leader at epoch 6, but in the new epoch 7, the leader is 101. Reverting to last stable offset 12.
[QuorumController id=100] Exceptionally slow controller event writeNoOpRecord took 2100 ms.
LOG
`
	if err := os.WriteFile(filepath.Join(bin, "journalctl"), []byte(journal), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "sh", "-c", kafkaControllerJournalProbe("frameworks-kafka-controller", "1 hour ago"))
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("probe failed: %v\n%s", err, out)
	}
	renounced, slow, err := parseKafkaControllerJournalProbe(string(out))
	if err != nil || renounced != 1 || slow != 1 {
		t.Fatalf("renounced=%d slow=%d err=%v from %q", renounced, slow, err, out)
	}
}

func TestHostCrashProbeParsesUnitFailuresCoreDumpsAndKernelFaults(t *testing.T) {
	output := strings.Join([]string{
		"##systemd",
		"frameworks-kafka-controller.service: Main process exited, code=dumped, status=6/ABRT",
		"frameworks-kafka-controller.service: Failed with result 'core-dump'.",
		"frameworks-kafka-controller.service: Scheduled restart job, restart counter is at 1.",
		"valkey-server.service: Failed with result 'exit-code'.",
		"##coredump",
		"Process 4242 (MistOutHLS) of user 997 dumped core.",
		"Process 4243 (MistOutHLS) of user 997 dumped core.",
		"##kernel",
		"MistOutHLS[4242]: segfault at 0 ip 00007f0000000000 sp 00007ffd00000000 error 4 in libc.so.6[7f0000000000+1000]",
		"traps: MistInBuffer[77] general protection fault ip:55 sp:7f error:0 in MistInBuffer[55+1000]",
		"Out of memory: Killed process 3131 (java) total-vm:1kB, anon-rss:1kB",
		"##end",
	}, "\n")
	report := parseHostCrashProbe(output)
	findings := strings.Join(report.findings(), "\n")
	for _, want := range []string{
		"frameworks-kafka-controller.service: failed 1 time(s) (core-dump x1), restarted automatically 1 time(s)",
		"valkey-server.service: failed 1 time(s) (exit-code x1)",
		"core dumps: MistOutHLS x2",
		"kernel-reported segfaults/protection faults: MistInBuffer x1, MistOutHLS x1",
		"OOM kills: java x1",
	} {
		if !strings.Contains(findings, want) {
			t.Fatalf("findings missing %q:\n%s", want, findings)
		}
	}
}

func TestDiagnoseCrashesProbesEdgesAndCountsFindings(t *testing.T) {
	targets := []diagnoseHost{
		{Name: "core", Host: inventory.Host{Name: "core"}},
		{Name: "edge-eu", Host: inventory.Host{Name: "edge-eu"}, Edge: true},
	}
	reply := func(host, command string) (string, int) {
		if !strings.Contains(command, "systemd-coredump") || !strings.Contains(command, "24 hours ago") {
			return "", 127
		}
		if host == "edge-eu" {
			return "##systemd\nfrequent.service: Scheduled restart job, restart counter is at 9.\n##coredump\nProcess 1 (MistOutHLS) of user 0 dumped core.\n##kernel\n##end\n", 0
		}
		return "##systemd\n##coredump\n##kernel\n##end\n", 0
	}
	var stdout, stderr bytes.Buffer
	failures := diagnoseCrashes(context.Background(), &stdout, &stderr, targets, "24 hours ago", replyRunnerFor(reply, nil))
	if failures != 2 {
		t.Fatalf("failures = %d, want 2 (edge restart loop and core dump)\n%s%s", failures, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "frequent.service: restarted automatically 1 time(s)") {
		t.Fatalf("restart not reported:\n%s", stdout.String())
	}
}

func TestDiagnoseProbesAreValidShell(t *testing.T) {
	scripts := map[string]string{
		"crash":      hostCrashProbe("24 hours ago"),
		"resource":   resourceProbeScript,
		"edge media": mediaDiagnosticScript([]string{"helmsman"}, mediaDiagnosticEdgePorts(), diagnoseOptions{}) + mediaDiagnosticEdgeContainerScript,
	}
	for name, script := range scripts {
		if out, err := exec.CommandContext(t.Context(), "sh", "-n", "-c", script).CombinedOutput(); err != nil {
			t.Fatalf("%s probe does not parse: %v\n%s", name, err, out)
		}
	}
}

func TestDiagnosePlannedPortsCoverRegionalKafkaRedisAndAliasedServices(t *testing.T) {
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{"eu-1": {Name: "eu-1"}, "us-1": {Name: "us-1"}},
		Infrastructure: inventory.InfrastructureConfig{
			Kafka: &inventory.KafkaConfig{
				Enabled: true, ClusterID: "eu",
				Brokers:  []inventory.KafkaBroker{{Host: "eu-1", ID: 1}},
				Regional: []inventory.RegionalKafkaCluster{{RegionID: "us-east", ClusterID: "us", Brokers: []inventory.KafkaBroker{{Host: "us-1", ID: 1}}, Controllers: []inventory.KafkaController{{Host: "us-1", ID: 100}}}},
			},
			Redis: &inventory.RedisConfig{Enabled: true, Instances: []inventory.RedisInstance{{
				Name: "foghorn", Host: "eu-1", ReplicaHosts: []string{"us-1"}, Sentinels: []inventory.RedisSentinelNode{{Host: "eu-1"}},
			}}},
		},
		Services: map[string]inventory.ServiceConfig{
			"foghorn-us": {Enabled: true, Deploy: "foghorn", Host: "us-1"},
		},
	}
	planned := diagnosePlannedPorts(manifest)
	for host, ports := range map[string][]int{"eu-1": {9092, 9093, 6379, 26379}, "us-1": {9092, 9093, 6379, 18008}} {
		for _, port := range ports {
			if planned[host][port] == "" {
				t.Fatalf("%s:%d not planned: %v", host, port, planned[host])
			}
		}
	}
}
