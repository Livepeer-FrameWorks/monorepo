package cmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"frameworks/cli/pkg/health"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"
)

// slowSSHRunner charges every SSH operation a fixed round trip, like the
// operator-to-staging links where doctor reported 0.96-1.44 s of "YSQL
// latency" for a node whose ysqlsh connect and SELECT 1 take about 28 ms.
type slowSSHRunner struct {
	rtt       time.Duration
	hostTimed time.Duration
}

func (r *slowSSHRunner) Run(_ context.Context, command string) (*ssh.CommandResult, error) {
	time.Sleep(r.rtt)
	stdout := ""
	switch {
	case strings.Contains(command, "ysql_elapsed_us="):
		stdout = "ysql_elapsed_us=" + strconv.FormatInt(r.hostTimed.Microseconds(), 10) + "\n"
	case strings.Contains(command, "-f "):
		stdout = "1\n"
	}
	return &ssh.CommandResult{Command: command, Stdout: stdout}, nil
}

func (r *slowSSHRunner) RunScript(context.Context, string) (*ssh.CommandResult, error) {
	time.Sleep(r.rtt)
	return &ssh.CommandResult{}, nil
}

func (r *slowSSHRunner) Upload(context.Context, ssh.UploadOptions) error {
	time.Sleep(r.rtt)
	return nil
}
func (r *slowSSHRunner) Close() error { return nil }

// TestYugabyteYSQLTimingProbeRunsOnTheNode runs the real probe shell with a
// stub ysqlsh and a GNU-style date whose two reads are 28 ms apart.
func TestYugabyteYSQLTimingProbeRunsOnTheNode(t *testing.T) {
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	stubs := map[string]string{
		"ysqlsh": "#!/bin/sh\necho \"$*\" >\"" + filepath.Join(bin, "args") + "\"\necho 1\n",
		"date":   "#!/bin/sh\n[ \"$1\" = '+%s%N' ] || exit 1\nif [ -e \"" + calls + "\" ]; then echo 1792179325028000000; else : >\"" + calls + "\"; echo 1792179325000000000; fi\n",
	}
	for name, body := range stubs {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	probe := yugabyteYSQLTimingProbe(provisioner.ConnParams{Port: 5433, User: "yugabyte", Database: "yugabyte"})
	cmd := exec.CommandContext(t.Context(), "sh", "-c", probe)
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("probe failed: %v\n%s", err, out)
	}
	got, err := parseYugabyteYSQLTiming(string(out))
	if err != nil || got != 28*time.Millisecond {
		t.Fatalf("timing = %v, err %v from %q; want 28ms", got, err, out)
	}
	args, err := os.ReadFile(filepath.Join(bin, "args"))
	if err != nil || !strings.Contains(string(args), "-h localhost -p 5433 -U yugabyte -d yugabyte") || !strings.Contains(string(args), "SELECT 1") {
		t.Fatalf("ysqlsh args = %q (err %v)", args, err)
	}
}

func TestYugabyteLocalYSQLLatencyIsTimedOnTheNode(t *testing.T) {
	pg := &inventory.PostgresConfig{Port: 5433}
	cases := []struct {
		name      string
		hostTimed time.Duration
	}{
		{name: "healthy node behind a slow SSH link", hostTimed: 28 * time.Millisecond},
		{name: "stalled YSQL", hostTimed: 1500 * time.Millisecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := &slowSSHRunner{rtt: 300 * time.Millisecond, hostTimed: tc.hostTimed}
			result := probeYugabyteLocalYSQL(context.Background(), runner, pg, &health.CheckResult{Name: "yugabyte", Metadata: map[string]string{}})
			if result.Latency != tc.hostTimed {
				t.Errorf("Latency = %v, want the node-measured %v (SSH round trips are not YSQL latency)", result.Latency, tc.hostTimed)
			}
			// A slow node still serves; checkYugabyteCluster judges the latency.
			if !result.OK {
				t.Errorf("OK = false (status %q, error %q)", result.Status, result.Error)
			}
		})
	}
}
