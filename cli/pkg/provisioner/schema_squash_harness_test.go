//go:build schema_verify

// Schema-consolidation verification harness (shared helpers).
//
// Proves, against real database engines in throwaway Docker containers, that the
// baseline schema files equal the result of replaying every migration on top of
// the baseline — the invariant that makes the squash + baseline floor safe. Run
// via `make verify-schema-postgres` / `make verify-schema-clickhouse`; gated behind
// the `schema_verify` build tag so a plain `make test` needs no Docker.
package provisioner

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// docker runs a docker subcommand, optionally feeding stdin, and returns stdout.
func docker(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return out.String(), &dockerError{args: args, stderr: errb.String(), err: err}
	}
	return out.String(), nil
}

type dockerError struct {
	args   []string
	stderr string
	err    error
}

func (e *dockerError) Error() string {
	return "docker " + strings.Join(e.args, " ") + ": " + e.err.Error() + "\n" + e.stderr
}

// requireDocker skips the test if the docker daemon is not reachable.
func requireDocker(t *testing.T) {
	t.Helper()
	if _, err := docker(t, "", "version", "--format", "{{.Server.Version}}"); err != nil {
		t.Skipf("docker not available: %v", err)
	}
}

// rmContainer force-removes a container, ignoring errors (used in cleanup).
func rmContainer(t *testing.T, name string) {
	t.Helper()
	_, _ = docker(t, "", "rm", "-f", name)
}

// collapseWS collapses all runs of whitespace to a single space and trims.
func collapseWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// diffSchemas compares two name→normalized-DDL maps and reports every divergence.
// kind labels the object set ("clickhouse periscope", "postgres commodore", …).
func diffSchemas(t *testing.T, kind string, baseline, replayed map[string]string) {
	t.Helper()
	seen := map[string]bool{}
	var problems []string
	for name, bDDL := range baseline {
		seen[name] = true
		rDDL, ok := replayed[name]
		if !ok {
			problems = append(problems, "  only in baseline (replay is MISSING it): "+name)
			continue
		}
		if bDDL != rDDL {
			problems = append(problems,
				"  DIVERGES: "+name+"\n    baseline: "+bDDL+"\n    replayed: "+rDDL)
		}
	}
	for name := range replayed {
		if !seen[name] {
			problems = append(problems, "  only in replay (baseline is MISSING it): "+name)
		}
	}
	if len(problems) > 0 {
		t.Fatalf("%s: baseline != baseline+migrations across %d object(s):\n%s",
			kind, len(problems), strings.Join(problems, "\n"))
	}
}
