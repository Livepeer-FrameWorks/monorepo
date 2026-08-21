// Package dockerpg provides context-bounded docker CLI helpers shared by the real-PG integration harnesses. It is a
// stdlib-only leaf package (no pkg/testutil transitive deps) so importing it does not pull websocket/jwt/auth into a
// service module's graph just to start a throwaway Postgres.
package dockerpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// PostgresImage resolves the release-pinned PostgreSQL contract image from
// config/infrastructure.yaml. Real-engine service tests use this instead of
// silently drifting to an older hard-coded major version.
func PostgresImage() (string, error) {
	if override := strings.TrimSpace(os.Getenv("FRAMEWORKS_POSTGRES_TEST_IMAGE")); override != "" {
		return override, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for dir, depth := wd, 0; depth < 10; depth++ {
		path := filepath.Join(dir, "config", "infrastructure.yaml")
		if b, readErr := os.ReadFile(path); readErr == nil {
			image, digest, parseErr := infrastructureImage(string(b), "postgresql")
			if parseErr != nil {
				return "", fmt.Errorf("parse %s: %w", path, parseErr)
			}
			return image + "@" + digest, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", errors.New("config/infrastructure.yaml not found from test working directory")
}

func infrastructureImage(yaml, name string) (string, string, error) {
	active := false
	image, digest := "", ""
	for line := range strings.SplitSeq(yaml, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- name:") {
			if active {
				break
			}
			active = strings.TrimSpace(strings.TrimPrefix(trimmed, "- name:")) == name
			continue
		}
		if !active {
			continue
		}
		switch {
		case strings.HasPrefix(trimmed, "image:"):
			image = strings.TrimSpace(strings.TrimPrefix(trimmed, "image:"))
		case strings.HasPrefix(trimmed, "digest:"):
			digest = strings.TrimSpace(strings.TrimPrefix(trimmed, "digest:"))
		}
	}
	if image == "" || digest == "" {
		return "", "", fmt.Errorf("infrastructure/%s must declare image and digest", name)
	}
	return image, digest, nil
}

// Every docker invocation is deadline-bounded so no single call can hang a harness, but the budgets differ by command:
// only `docker run` may pull the image (nothing pre-pulls it in the Makefile or workflows) and needs minutes; the
// diagnostic/cleanup commands (inspect/logs/rm) and each published-port probe must stay short so a failure path cannot
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

// DiscoverPublishedHostPort polls Docker's inspected port map until a host mapping is published or the budget elapses.
// Inspect is authoritative and avoids `docker port` occasionally blocking under a loaded CI daemon even though a later
// inspect shows the mapping already exists. Each probe is bounded by both probeTimeout and the whole-wait deadline.
func DiscoverPublishedHostPort(name, containerPort string) (string, error) {
	deadline, cancel := context.WithTimeout(context.Background(), portDiscoveryBudget)
	defer cancel()
	var portOut string
	var err error
	for {
		portOut, err = runDocker(deadline, probeTimeout, "inspect", "-f", "{{json .NetworkSettings.Ports}}", name)
		if err == nil {
			if p := parseInspectedHostPort(portOut, containerPort); p != "" {
				return p, nil
			}
		}
		select {
		case <-deadline.Done():
			if err == nil {
				err = fmt.Errorf("no host mapping in inspected ports %q", portOut)
			}
			return "", fmt.Errorf("docker did not publish %s within %s: %w\nstate: %s\nlogs:\n%s",
				containerPort, portDiscoveryBudget, err,
				cliDiagnostic("inspect", "-f", "{{.State.Status}} {{.State.Error}}", name),
				cliDiagnostic("logs", "--tail", "20", name))
		case <-time.After(pollInterval):
		}
	}
}

func parseInspectedHostPort(portJSON, containerPort string) string {
	var ports map[string][]struct {
		HostPort string `json:"HostPort"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(portJSON)), &ports); err != nil {
		return ""
	}
	for _, binding := range ports[containerPort] {
		if binding.HostPort != "" {
			return binding.HostPort
		}
	}
	return ""
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
