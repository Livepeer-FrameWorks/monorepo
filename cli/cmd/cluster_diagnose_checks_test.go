package cmd

import (
	"bytes"
	"compress/gzip"
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

// TestKafkaQuorumFollowerLagLimitMatchesStaging pins the lag limit to staging
// samples: 80 describe --status reads across both regions (2026-10-16) peaked
// at 497 ms, and a voter more than 2.5 fetch timeouts (2 s) behind is no
// longer counted by the leader's quorum check.
func TestKafkaQuorumFollowerLagLimitMatchesStaging(t *testing.T) {
	for _, tc := range []struct {
		lagMs    string
		degraded bool
	}{{"0", false}, {"497", false}, {"4000", false}, {"6000", true}, {"45000", true}} {
		status, err := parseKafkaQuorumStatus("LeaderId: 102\nLeaderEpoch: 910\nMaxFollowerLagTimeMs: " + tc.lagMs + "\nCurrentVoters: [101,102,103]\n")
		if err != nil {
			t.Fatal(err)
		}
		if got := len(status.problems(3)) > 0; got != tc.degraded {
			t.Errorf("lag %s ms: degraded = %v, want %v (%q)", tc.lagMs, got, tc.degraded, status.problems(3))
		}
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
		case strings.Contains(command, "controller.log"):
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
	joined := strings.Join(commands, "\n")
	if !strings.Contains(joined, "unit='frameworks-kafka-controller'") || !strings.Contains(joined, "date -d '48 hours ago'") {
		t.Fatalf("controller log not read for the --since window: %q", commands)
	}
}

// TestKafkaControllerLogProbeReadsControllerLogInTheWindow stands in for a
// staging controller host: the QuorumController lines are only in LOG_DIR's
// controller.log and its .gz rotations (log4j ControllerAppender), and the
// unit journal holds none of them.
func TestKafkaControllerLogProbeReadsControllerLogInTheWindow(t *testing.T) {
	bin, logDir := t.TempDir(), t.TempDir()
	stubs := map[string]string{
		// journald sees only the kafka-server-start.sh stdout, not ControllerAppender.
		"journalctl": "#!/bin/sh\nexit 0\n",
		"systemctl":  "#!/bin/sh\necho \"LOG_DIR=" + logDir + " KAFKA_LOG4J_OPTS=-Dlog4j2.configurationFile=/opt/kafka/config/x.yaml\"\n",
		// GNU date resolving "1 hour ago" on the host.
		"date": "#!/bin/sh\n[ \"$2\" = '1 hour ago' ] || exit 1\necho '2026-10-16 18:30:00'\n",
	}
	for name, body := range stubs {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	const leaderless = "WARN [QuorumController id=101] Renouncing the leadership due to a metadata log event. We were the leader at epoch 905, but in the new epoch 906, the leader is (none). Reverting to last stable offset 4370000. (org.apache.kafka.controller.QuorumController)"
	const handover = "WARN [QuorumController id=101] Renouncing the leadership due to a metadata log event. We were the leader at epoch 906, but in the new epoch 907, the leader is 102. Reverting to last stable offset 4370100. (org.apache.kafka.controller.QuorumController)"
	const slow = "INFO [QuorumController id=101] Exceptionally slow controller event writeNoOpRecord took 2100 ms. (org.apache.kafka.controller.QuorumController)"
	current := strings.Join([]string{
		"[2026-10-16 18:10:00,001] " + leaderless, // before the window
		"[2026-10-16 18:12:00,001] " + slow,       // before the window
		"[2026-10-16 18:45:00,001] " + leaderless,
		"[2026-10-16 18:46:00,001] " + handover,
		"[2026-10-16 19:20:23,105] " + slow,
		"[2026-10-16 19:20:24,000] " + leaderless,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(logDir, "controller.log"), []byte(current), 0o644); err != nil {
		t.Fatal(err)
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write([]byte("[2026-10-15 05:24:00,000] " + leaderless + "\n")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "controller.log.2026-10-15-1.gz"), gz.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.CommandContext(t.Context(), "sh", "-c", kafkaControllerLogProbe("frameworks-kafka-controller", "1 hour ago"))
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("probe failed: %v\n%s", err, out)
	}
	renounced, slowEvents, err := parseKafkaControllerLogProbe(string(out))
	if err != nil || renounced != 2 || slowEvents != 1 {
		t.Fatalf("renounced=%d slow=%d err=%v from %q; want the 2 leaderless renouncements and 1 slow event after 18:30", renounced, slowEvents, err, out)
	}

	// A host without the log fails the probe rather than reporting zero.
	stubs["systemctl"] = "#!/bin/sh\necho LOG_DIR=" + filepath.Join(logDir, "missing") + "\n"
	if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte(stubs["systemctl"]), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd = exec.CommandContext(t.Context(), "sh", "-c", kafkaControllerLogProbe("frameworks-kafka-controller", "1 hour ago"))
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("probe succeeded without a controller.log: %q", out)
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

// A Restart=always unit whose main process exits 0 (Helmsman after a
// self-update) is restarted by systemd without being a crash; a restart after
// a failed exit still is, even later in the same window.
func TestHostCrashProbeSkipsRestartsAfterCleanExit(t *testing.T) {
	probe := hostCrashProbe("24 hours ago")
	for _, pattern := range []string{"Deactivated successfully", "Succeeded"} {
		if !strings.Contains(probe, pattern) {
			t.Fatalf("crash probe does not collect %q lines:\n%s", pattern, probe)
		}
	}
	output := strings.Join([]string{
		"##systemd",
		"frameworks-helmsman.service: Deactivated successfully.",
		"frameworks-helmsman.service: Scheduled restart job, restart counter is at 1.",
		"legacy.service: Succeeded.",
		"legacy.service: Scheduled restart job, restart counter is at 1.",
		"frameworks-helmsman.service: Failed with result 'exit-code'.",
		"frameworks-helmsman.service: Scheduled restart job, restart counter is at 2.",
		"frameworks-helmsman.service: Scheduled restart job, restart counter is at 3.",
		"##coredump",
		"##kernel",
		"##end",
	}, "\n")
	findings := parseHostCrashProbe(output).findings()
	want := []string{"frameworks-helmsman.service: failed 1 time(s) (exit-code x1), restarted automatically 2 time(s)"}
	if strings.Join(findings, "\n") != strings.Join(want, "\n") {
		t.Fatalf("findings = %q, want %q", findings, want)
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
