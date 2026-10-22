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
			return "renounced=35 leaderless=4 slow=198 max_slow_ms=10327 election_timeout_ms=5000\n", 0
		}
		return "ok", 0
	}
	cmd := &cobra.Command{}
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	err := diagnoseKafka(context.Background(), cmd, manifest, replyRunnerFor(reply, &commands), "48 hours ago")
	out := stdout.String() + stderr.String()
	if err == nil || !strings.Contains(out, "was left without a leader 4 time(s) since 48 hours ago") ||
		!strings.Contains(out, "31 unplanned leader change(s) since 48 hours ago") || !strings.Contains(out, "198 slow event(s), largest stall 10327 ms") {
		t.Fatalf("leadership losses not reported (err=%v):\n%s", err, out)
	}
	joined := strings.Join(commands, "\n")
	if !strings.Contains(joined, "unit='frameworks-kafka-controller'") || !strings.Contains(joined, "date -d '48 hours ago'") {
		t.Fatalf("controller log not read for the --since window: %q", commands)
	}
}

// runKafkaQuorumOnControllerLogs runs checkKafkaQuorum against one controller
// host simulated locally: systemctl reports the unit's LOG_DIR and its
// server.properties (election timeout 5000 ms, as the kafka role renders it),
// date resolves "1 hour ago" to cutoff, and logs holds LOG_DIR's files (a
// ".gz" name is written gzipped). The unit journal holds none of the
// QuorumController lines: log4j sends them only to controller.log.
func runKafkaQuorumOnControllerLogs(t *testing.T, cutoff string, logs map[string]string) *health.CheckResult {
	t.Helper()
	bin, logDir, confDir := t.TempDir(), t.TempDir(), t.TempDir()
	props := filepath.Join(confDir, "frameworks-kafka-controller.properties")
	if err := os.WriteFile(props, []byte("process.roles=controller\ncontroller.quorum.fetch.timeout.ms=10000\ncontroller.quorum.election.timeout.ms=5000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stubs := map[string]string{
		"journalctl": "#!/bin/sh\nexit 0\n",
		"systemctl": "#!/bin/sh\ncase \"$*\" in\n*ExecStart*) echo '{ path=/opt/kafka/bin/kafka-server-start.sh ; argv[]=/opt/kafka/bin/kafka-server-start.sh " + props + " ; ignore_errors=no }' ;;\n" +
			"*) echo \"LOG_DIR=" + logDir + " KAFKA_LOG4J_OPTS=-Dlog4j2.configurationFile=/opt/kafka/config/x.yaml\" ;;\nesac\n",
		// GNU date resolving "1 hour ago" on the host.
		"date": "#!/bin/sh\n[ \"$2\" = '1 hour ago' ] || exit 1\necho '" + cutoff + "'\n",
	}
	for name, body := range stubs {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range logs {
		data := []byte(content)
		if strings.HasSuffix(name, ".gz") {
			var gz bytes.Buffer
			zw := gzip.NewWriter(&gz)
			if _, err := zw.Write(data); err != nil {
				t.Fatal(err)
			}
			if err := zw.Close(); err != nil {
				t.Fatal(err)
			}
			data = gz.Bytes()
		}
		if err := os.WriteFile(filepath.Join(logDir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{"k1": {Name: "k1", ExternalIP: "10.0.0.5"}},
		Infrastructure: inventory.InfrastructureConfig{Kafka: &inventory.KafkaConfig{
			Enabled:     true,
			ClusterID:   "c1",
			Brokers:     []inventory.KafkaBroker{{Host: "k1", ID: 1, Port: 9092}},
			Controllers: []inventory.KafkaController{{Host: "k1", ID: 201, Port: 9093}},
		}},
	}
	reply := func(_, command string) (string, int) {
		if strings.Contains(command, "kafka-metadata-quorum") {
			return "LeaderId: 203\nLeaderEpoch: 1179\nMaxFollowerLagTimeMs: 0\nCurrentVoters: [201]\n", 0
		}
		cmd := exec.CommandContext(t.Context(), "sh", "-c", command)
		cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Logf("probe: %v: %s", err, stderr.String())
			return string(out), 1
		}
		return string(out), 0
	}
	var commands []string
	return checkKafkaQuorum(t.Context(), manifest, allKafkaClusters(manifest)[0], "native", "1 hour ago", replyRunnerFor(reply, &commands))
}

// TestKafkaControllerLogProbeReadsControllerLogInTheWindow reads the
// QuorumController lines from LOG_DIR's controller.log and its .gz rotations
// and the raft state transitions from server.log, counting only the window.
func TestKafkaControllerLogProbeReadsControllerLogInTheWindow(t *testing.T) {
	const leaderless = "WARN [QuorumController id=101] Renouncing the leadership due to a metadata log event. We were the leader at epoch 905, but in the new epoch 906, the leader is (none). Reverting to last stable offset 4370000. (org.apache.kafka.controller.QuorumController)"
	const handover = "WARN [QuorumController id=101] Renouncing the leadership due to a metadata log event. We were the leader at epoch 906, but in the new epoch 907, the leader is 102. Reverting to last stable offset 4370100. (org.apache.kafka.controller.QuorumController)"
	const laterLeaderless = "WARN [QuorumController id=101] Renouncing the leadership due to a metadata log event. We were the leader at epoch 907, but in the new epoch 908, the leader is (none). Reverting to last stable offset 4370200. (org.apache.kafka.controller.QuorumController)"
	const slow = "INFO [QuorumController id=101] Exceptionally slow controller event writeNoOpRecord took 2100 ms. (org.apache.kafka.controller.QuorumController)"
	current := strings.Join([]string{
		"[2026-10-16 18:10:00,001] " + leaderless, // before the window
		"[2026-10-16 18:12:00,001] " + slow,       // before the window
		"[2026-10-16 18:45:00,001] " + leaderless,
		"[2026-10-16 18:46:00,001] " + handover, // a minute later: past the election timeout
		"[2026-10-16 19:20:23,105] " + slow,
		"[2026-10-16 19:20:24,000] " + laterLeaderless,
	}, "\n") + "\n"
	result := runKafkaQuorumOnControllerLogs(t, "2026-10-16 18:30:00", map[string]string{
		"controller.log":                 current,
		"controller.log.2026-10-15-1.gz": "[2026-10-15 05:24:00,000] " + leaderless + "\n",
	})
	if result.OK ||
		!strings.Contains(result.Error, "controller 201 on k1 was left without a leader 2 time(s) since 1 hour ago") ||
		!strings.Contains(result.Error, "controller 201 on k1 lost leadership in 1 unplanned leader change(s) since 1 hour ago") ||
		!strings.Contains(result.Error, "controller 201: 1 slow event(s), largest stall 2100 ms") {
		t.Errorf("want the 2 leaderless renouncements, 1 handover and 1 slow event after 18:30: %+v", result)
	}

	// Staging us-2, 2026-10-22: a 10.3 s controller stall made the leader
	// resign; it passed through Unattached ("the leader is (none)") and
	// server.log shows it following the new leader 203 12 ms later.
	const (
		slow10s  = "[2026-10-22 20:23:18,195] ERROR [QuorumController id=201] Exceptionally slow controller event writeNoOpRecord(org.apache.kafka.controller.QuorumController$$Lambda/0x00007f3c2c4b1a30@6f1d3c2e) took 10327 ms. (org.apache.kafka.controller.QuorumController)"
		slow6s   = "[2026-10-22 20:23:07,868] ERROR [QuorumController id=201] Exceptionally slow controller event maybeFenceStaleBroker took 6012 ms. (org.apache.kafka.controller.QuorumController)"
		renounce = "[2026-10-22 20:23:18,892] WARN [QuorumController id=201] Renouncing the leadership due to a metadata log event. We were the leader at epoch 1178, but in the new epoch 1179, the leader is (none). Reverting to last stable offset 48211735. (org.apache.kafka.controller.QuorumController)"
		follower = "[2026-10-22 20:23:18,904] INFO [RaftManager id=201] Completed transition to FollowerState(fetchTimeoutMs=10000, epoch=1179, leader=203, leaderEndpoints=Endpoints(endpoints={ListenerName(CONTROLLER)=/10.88.0.3:9093}), voters=[201, 202, 203], highWatermark=Optional[LogOffsetMetadata(offset=48211736, metadata=Optional.empty)], fetchingSnapshot=Optional.empty) from UnattachedState(epoch=1179, leaderId=OptionalInt.empty, votedKey=Optional.empty, voters=[201, 202, 203], electionTimeoutMs=5871, highWatermark=Optional[LogOffsetMetadata(offset=48211736, metadata=Optional.empty)]) (org.apache.kafka.raft.QuorumState)"
		// The prior epoch's follower transition is no successor for epoch 1179.
		staleFollower = "[2026-10-22 20:23:18,700] INFO [RaftManager id=201] Completed transition to FollowerState(fetchTimeoutMs=10000, epoch=1177, leader=202, leaderEndpoints=Endpoints(endpoints={}), voters=[201, 202, 203], highWatermark=Optional.empty, fetchingSnapshot=Optional.empty) from UnattachedState(epoch=1177, leaderId=OptionalInt.empty, votedKey=Optional.empty, voters=[201, 202, 203], electionTimeoutMs=5000, highWatermark=Optional.empty) (org.apache.kafka.raft.QuorumState)"
		lateFollower  = "[2026-10-22 20:23:30,000] INFO [RaftManager id=201] Completed transition to FollowerState(fetchTimeoutMs=10000, epoch=1180, leader=203, leaderEndpoints=Endpoints(endpoints={}), voters=[201, 202, 203], highWatermark=Optional.empty, fetchingSnapshot=Optional.empty) from CandidateState(localId=201, epoch=1180, retries=2, voteStates={}, highWatermark=Optional.empty, electionTimeoutMs=5000) (org.apache.kafka.raft.QuorumState)"
	)
	controllerLog := slow6s + "\n" + slow10s + "\n" + renounce + "\n"
	result = runKafkaQuorumOnControllerLogs(t, "2026-10-22 19:30:00", map[string]string{
		"controller.log": controllerLog,
		"server.log":     staleFollower + "\n" + follower + "\n",
	})
	if result.OK ||
		!strings.Contains(result.Error, "controller 201 on k1 lost leadership in 1 unplanned leader change(s) since 1 hour ago") ||
		!strings.Contains(result.Error, "controller 201: 2 slow event(s), largest stall 10327 ms") ||
		strings.Contains(result.Error, "without a leader") || strings.Contains(result.Error, "no successor") {
		t.Errorf("want 1 unplanned leader change with a successor after a 10327 ms stall, not a leaderless quorum: %+v", result)
	}

	// No new leader within the 5000 ms election timeout: leaderless.
	for name, serverLog := range map[string]string{
		"no successor":    staleFollower + "\n",
		"late successor":  staleFollower + "\n" + lateFollower + "\n",
		"empty serverlog": "",
	} {
		result = runKafkaQuorumOnControllerLogs(t, "2026-10-22 19:30:00", map[string]string{
			"controller.log": controllerLog,
			"server.log":     serverLog,
		})
		if result.OK || !strings.Contains(result.Error, "controller 201 on k1 was left without a leader 1 time(s) since 1 hour ago") ||
			strings.Contains(result.Error, "unplanned leader change") {
			t.Errorf("%s: want a leaderless quorum: %+v", name, result)
		}
	}

	// A host without the log fails the probe rather than reporting zero.
	result = runKafkaQuorumOnControllerLogs(t, "2026-10-22 19:30:00", map[string]string{"server.log": follower + "\n"})
	if result.OK || !strings.Contains(result.Error, "controller log unreadable") {
		t.Fatalf("probe succeeded without a controller.log: %+v", result)
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
