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

// TestMediaDiagnosticLogScanCoversTheWholeSinceWindow runs the real probe with
// a journalctl that holds 500 lines in the window, the only error being the
// oldest. A line cap would drop it even though it is inside --since.
func TestMediaDiagnosticLogScanCoversTheWholeSinceWindow(t *testing.T) {
	bin := t.TempDir()
	journal := `#!/bin/sh
limit=0
while [ $# -gt 0 ]; do
  case "$1" in
    -n) limit="$2"; shift ;;
  esac
  shift
done
gen() {
  echo 'Oct 16 05:00:00 host svc[1]: {"level":"error","msg":"earliest failure in window"}'
  i=1
  while [ $i -lt 500 ]; do echo "Oct 16 05:00:01 host svc[1]: {\"level\":\"info\",\"msg\":\"ok $i\"}"; i=$((i+1)); done
}
if [ "$limit" -gt 0 ]; then gen | tail -n "$limit"; else gen; fi
`
	if err := os.WriteFile(filepath.Join(bin, "journalctl"), []byte(journal), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte("#!/bin/sh\necho active\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := mediaDiagnosticScript([]string{"foghorn"}, []int{18008}, diagnoseOptions{Since: "4 hours ago"})
	cmd := exec.CommandContext(t.Context(), "sh", "-c", script)
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("probe failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "earliest failure in window") {
		t.Fatalf("error logged inside --since was not reported:\n%s", out)
	}
}

func TestDiagnoseKafkaFailsWhenTheMetadataQuorumHasNoLeader(t *testing.T) {
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{"k1": {Name: "k1", ExternalIP: "10.0.0.5"}},
		Infrastructure: inventory.InfrastructureConfig{Kafka: &inventory.KafkaConfig{
			Enabled:     true,
			ClusterID:   "c1",
			Brokers:     []inventory.KafkaBroker{{Host: "k1", ID: 1, Port: 9092}},
			Controllers: []inventory.KafkaController{{Host: "k1", ID: 100, Port: 9093}},
		}},
	}
	reply := func(_, command string) (string, int) {
		switch {
		case strings.Contains(command, "kafka-metadata-quorum"):
			return "ClusterId:              c1\nLeaderId:               -1\nLeaderEpoch:            42\nHighWatermark:          900\nMaxFollowerLag:         0\nMaxFollowerLagTimeMs:   -1\nCurrentVoters:          [{\"id\": 100, \"directoryId\": \"x\", \"endpoints\": [\"CONTROLLER://k1:9093\"]}]\nCurrentObservers:       [{\"id\": 1, \"directoryId\": \"y\"}]\n", 0
		case strings.Contains(command, "journalctl"):
			return "", 0
		}
		return "ok", 0
	}
	cmd := &cobra.Command{}
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	err := diagnoseKafka(context.Background(), cmd, manifest, replyRunnerFor(reply, nil), "4 hours ago")
	out := stdout.String() + stderr.String()
	if err == nil {
		t.Fatalf("kafka diagnostics passed with a leaderless metadata quorum:\n%s", out)
	}
	if !strings.Contains(out, "no leader") {
		t.Fatalf("leaderless quorum not named in output:\n%s", out)
	}
}

func TestDoctorYugabyteProbesEveryTServerAndFlagsSlowYSQL(t *testing.T) {
	orig, origLiveness := yugabyteDoctorNodeProbeFn, yugabyteTServerLivenessFn
	t.Cleanup(func() { yugabyteDoctorNodeProbeFn, yugabyteTServerLivenessFn = orig, origLiveness })
	yugabyteTServerLivenessFn = func(context.Context, *ssh.Pool, *inventory.Manifest, *inventory.PostgresConfig) ([]string, error) {
		return nil, nil
	}
	yugabyteDoctorNodeProbeFn = func(_ context.Context, _ *ssh.Pool, host inventory.Host, _ *inventory.PostgresConfig) *health.CheckResult {
		if host.Name == "yb-1" {
			return &health.CheckResult{Name: "yugabyte", OK: true, Status: "healthy", Latency: 1440 * time.Millisecond, Metadata: map[string]string{}}
		}
		return &health.CheckResult{Name: "yugabyte", OK: true, Status: "healthy", Latency: 20 * time.Millisecond, Metadata: map[string]string{}}
	}
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{"yb-1": {Name: "yb-1"}, "yb-2": {Name: "yb-2"}},
		Infrastructure: inventory.InfrastructureConfig{Postgres: &inventory.PostgresConfig{
			Enabled: true, Engine: "yugabyte",
			Nodes: []inventory.PostgresNode{{Host: "yb-1", ID: 1}, {Host: "yb-2", ID: 2}},
		}},
	}
	result := checkYugabyteCluster(context.Background(), nil, manifest, manifest.Infrastructure.Postgres)
	if result.OK {
		t.Fatalf("1.44s YSQL round trip on yb-1 passed: %+v", result)
	}
	if !strings.Contains(result.Message+result.Error, "yb-1") {
		t.Fatalf("slow tserver not named: %+v", result)
	}
}
