package provisioner

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fakeBaselineYsqlsh is a ysqlsh stand-in that records each run's arguments and then fails with the error the
// matching line of the outcomes file names, or succeeds once the outcomes run out.
const fakeBaselineYsqlsh = `#!/bin/sh
dir=$(dirname "$0")
printf '%s\n' "$*" >>"$dir/runs"
run=$(wc -l <"$dir/runs" | tr -d ' ')
outcome=$(sed -n "${run}p" "$dir/outcomes")
case "$outcome" in
  "") echo "applied"; exit 0 ;;
  *) echo "NOTICE:  00000: constraint does not exist, skipping"; echo "ysqlsh:/tmp/baseline.sql:512: ERROR:  $outcome" >&2; exit 3 ;;
esac
`

// applyBaselineWithFakeYsqlsh runs yugabyte-apply-baseline.sh with a fake ysqlsh whose runs fail with outcomes in
// order, and returns the script's exit status, its output, and the arguments of every ysqlsh run.
func applyBaselineWithFakeYsqlsh(t *testing.T, outcomes ...string) (int, string, []string) {
	t.Helper()
	dir := t.TempDir()
	ysqlsh := filepath.Join(dir, "ysqlsh")
	if err := os.WriteFile(ysqlsh, []byte(fakeBaselineYsqlsh), 0o755); err != nil {
		t.Fatal(err)
	}
	var lines strings.Builder
	for _, outcome := range outcomes {
		lines.WriteString(outcome + "\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "outcomes"), []byte(lines.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	script := ansibleTreePath(t, "collections", "ansible_collections", "frameworks", "infra", "roles", "yugabyte", "files", "yugabyte-apply-baseline.sh")
	cmd := exec.CommandContext(t.Context(), "sh", script, ysqlsh, "/tmp/baseline.sql", "-h", "127.0.0.1", "-d", "commodore")
	cmd.Env = append(os.Environ(), "BASELINE_APPLY_RETRY_DELAY=0", "BASELINE_APPLY_ATTEMPTS=3")
	out, err := cmd.CombinedOutput()
	status := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		status = exitErr.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	runs, readErr := os.ReadFile(filepath.Join(dir, "runs"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	return status, string(out), strings.Split(strings.TrimSpace(string(runs)), "\n")
}

func TestYugabyteApplyBaselineRerunsTheFileAfterARetryableFailure(t *testing.T) {
	status, out, runs := applyBaselineWithFakeYsqlsh(t, "40001: Restart read required", "40P01: deadlock detected")
	if status != 0 || len(runs) != 3 || !strings.Contains(out, "applied") {
		t.Fatalf("status=%d runs=%d, want 0 after the third run applied the file:\n%s", status, len(runs), out)
	}
	if !strings.Contains(out, "Restart read required") || !strings.Contains(out, "attempt 2 of 3") {
		t.Fatalf("the failed run and the replay are not reported:\n%s", out)
	}
	for _, run := range runs {
		for _, want := range []string{"-X", "-v ON_ERROR_STOP=1", "-v VERBOSITY=verbose", "-h 127.0.0.1 -d commodore", "-f /tmp/baseline.sql"} {
			if !strings.Contains(run, want) {
				t.Fatalf("ysqlsh run %q lacks %q", run, want)
			}
		}
	}
}

func TestYugabyteApplyBaselineStopsAtANonRetryableFailure(t *testing.T) {
	status, out, runs := applyBaselineWithFakeYsqlsh(t, `42P07: relation "streams" already exists`)
	if status != 3 || len(runs) != 1 {
		t.Fatalf("status=%d runs=%d, want ysqlsh's status 3 after one run:\n%s", status, len(runs), out)
	}
}

func TestYugabyteApplyBaselineGivesUpAfterItsAttempts(t *testing.T) {
	outcomes := make([]string, 4)
	for i := range outcomes {
		outcomes[i] = "40001: Restart read required (" + strconv.Itoa(i) + ")"
	}
	status, out, runs := applyBaselineWithFakeYsqlsh(t, outcomes...)
	if status != 3 || len(runs) != 3 {
		t.Fatalf("status=%d runs=%d, want status 3 after BASELINE_APPLY_ATTEMPTS=3 runs:\n%s", status, len(runs), out)
	}
}
