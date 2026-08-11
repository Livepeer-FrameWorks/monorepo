// Package dockerpg provides context-bounded docker CLI helpers shared by the real-PG integration harnesses. It is a
// stdlib-only leaf package (no pkg/testutil transitive deps) so importing it does not pull websocket/jwt/auth into a
// service module's graph just to start a throwaway Postgres.
package dockerpg

import (
	"context"
	"database/sql"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Every docker invocation is deadline-bounded so no single call can hang a harness, but the budgets differ by command:
// only `docker run` may pull the image (nothing pre-pulls it in the Makefile or workflows) and needs minutes; the
// diagnostic/cleanup commands (inspect/logs/rm) and each `docker port` poll must stay short so a failure path cannot
// stack several multi-minute waits and collide with the package test timeout.
const (
	RunTimeout          = 3 * time.Minute        // docker run, incl. image pull
	cliTimeout          = 20 * time.Second       // inspect, logs, rm
	probeTimeout        = 5 * time.Second        // a single `docker port` poll
	portDiscoveryBudget = 30 * time.Second       // WALL-CLOCK deadline for a host port to appear, independent of attempt count
	pollInterval        = 100 * time.Millisecond // delay between port probes
)

// Run executes `docker run` (and any other image-pulling command) under RunTimeout and returns its combined output.
func Run(args ...string) (string, error) {
	return runDocker(context.Background(), RunTimeout, args...)
}

// CLI runs a short-lived docker command (inspect/logs/rm) under cliTimeout. A timeout surfaces as a non-nil error (the
// context deadline) so callers fail instead of blocking; it is deliberately NOT used for `docker run`, which may pull.
func CLI(args ...string) (string, error) {
	return runDocker(context.Background(), cliTimeout, args...)
}

// runDocker executes one docker command under min(parent deadline, timeout). Deriving each probe from the caller's
// outer deadline context is what keeps DiscoverPublishedHostPort/WaitReady honest: a probe can never run past the
// whole-wait budget.
func runDocker(parent context.Context, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	return string(out), err
}

// DiscoverPublishedHostPort polls `docker port <name> <containerPort>` (e.g. "5432/tcp") until a host mapping is
// published or portDiscoveryBudget elapses. Each probe is derived from the whole-wait deadline (and further capped at
// probeTimeout), and the inter-probe wait is context-aware, so no probe ever BEGINS after the budget — the deadline is
// exact, not budget+interval. On failure it returns an error carrying the container's state and recent logs, so a
// genuine startup failure is distinguishable from the publish race (publication under `docker run -P` is asynchronous,
// so an immediate probe can briefly report no mapping).
func DiscoverPublishedHostPort(name, containerPort string) (string, error) {
	deadline, cancel := context.WithTimeout(context.Background(), portDiscoveryBudget)
	defer cancel()
	var portOut string
	var err error
	for {
		portOut, err = runDocker(deadline, probeTimeout, "port", name, containerPort)
		if err == nil {
			if p := parseHostPort(portOut); p != "" {
				return p, nil
			}
		}
		select {
		case <-deadline.Done():
			if err == nil {
				err = fmt.Errorf("no host mapping in port output %q", portOut)
			}
			return "", fmt.Errorf("docker port did not publish %s within %s: %w\nstate: %s\nlogs:\n%s",
				containerPort, portDiscoveryBudget, err,
				cliDiagnostic("inspect", "-f", "{{.State.Status}} {{.State.Error}}", name),
				cliDiagnostic("logs", "--tail", "20", name))
		case <-time.After(pollInterval):
		}
	}
}

// Readiness bounds: the whole wait plus each individual ping, so a stalled TCP connect or PostgreSQL startup handshake
// cannot outlast the deadline (a context-free Ping would block past readinessBudget without ever rechecking it).
const (
	readinessBudget   = 90 * time.Second
	readinessProbe    = 3 * time.Second
	readinessInterval = 1 * time.Second
)

// WaitReady blocks until db answers a PingContext or readinessBudget elapses. Each ping is derived from the whole-wait
// deadline (and capped at readinessProbe), and the inter-ping wait is context-aware, so no ping BEGINS after the budget
// — the deadline is exact. On timeout it returns an error carrying the container's recent logs (name) so a real startup
// failure is diagnosable.
func WaitReady(db *sql.DB, name string) error {
	deadline, cancel := context.WithTimeout(context.Background(), readinessBudget)
	defer cancel()
	var last error
	for {
		probe, pcancel := context.WithTimeout(deadline, readinessProbe)
		last = db.PingContext(probe)
		pcancel()
		if last == nil {
			return nil
		}
		select {
		case <-deadline.Done():
			return fmt.Errorf("postgres not ready within %s: %w\nlogs:\n%s",
				readinessBudget, last, cliDiagnostic("logs", "--tail", "40", name))
		case <-time.After(readinessInterval):
		}
	}
}

// cliDiagnostic runs a best-effort docker diagnostic (container state, logs) for enriching a failure message. It folds
// any error into the returned string rather than discarding it, so a failed diagnostic never masks the original error
// and never leaves an unchecked return.
func cliDiagnostic(args ...string) string {
	out, err := CLI(args...)
	if err != nil {
		return fmt.Sprintf("%s(diagnostic %v failed: %v)", out, args, err)
	}
	return out
}

// parseHostPort extracts the host port from `docker port` output like "0.0.0.0:49153" (there may be an extra IPv6 line).
func parseHostPort(portOut string) string {
	for line := range strings.SplitSeq(strings.TrimSpace(portOut), "\n") {
		if i := strings.LastIndex(line, ":"); i >= 0 {
			return strings.TrimSpace(line[i+1:])
		}
	}
	return ""
}
