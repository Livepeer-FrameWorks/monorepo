package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"frameworks/cli/pkg/inventory"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
)

// ProbeResult is what a ProbeFunc returns when running a command on a host.
// Stdout and ExitCode together tell a Gate whether the host is ready.
type ProbeResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// ProbeFunc runs a shell command on host (typically via SSH) and returns
// stdout + exit code. Indirection so tests can inject a stub instead of a
// real SSH pool — production wires this to fwssh.Pool.Run in cluster_apply.
type ProbeFunc func(ctx context.Context, host inventory.Host, cmd string) (ProbeResult, error)

// Gate is a readiness check the rolling-update executor runs between waves:
// the next wave does not start until every host in the current wave passes
// its gate. Returning nil = host is ready; non-nil = the executor decides
// whether to halt or retry the rollout. Each gate ships sensible default
// Timeout/Poll values; callers may override.
type Gate interface {
	// Wait blocks until the host is ready, ctx is cancelled, or the gate's
	// own internal timeout expires (whichever comes first).
	Wait(ctx context.Context, host inventory.Host, run ProbeFunc) error
	// Describe returns a short human-readable identity for logs / dry-run
	// output. Stable across runs.
	Describe() string
}

const (
	defaultGateTimeout = 30 * time.Second
	defaultGatePoll    = 1 * time.Second
)

// HTTPReady polls a health endpoint on the host (via SSH+curl bound to
// 127.0.0.1) until a 2xx response or the deadline expires. Localhost
// binding means the gate works regardless of the service's external
// listener configuration — internal-only health endpoints are reachable
// from the host itself.
type HTTPReady struct {
	Port    int
	Path    string
	Timeout time.Duration
	Poll    time.Duration
}

// Describe implements Gate.
func (g HTTPReady) Describe() string {
	return fmt.Sprintf("HTTPReady{127.0.0.1:%d%s}", g.Port, g.Path)
}

// Wait implements Gate. Polls the localhost health endpoint until 2xx or
// timeout. Treats SSH-level errors and non-2xx HTTP responses identically
// (both are "not ready yet") so a missed retry doesn't bypass the gate.
func (g HTTPReady) Wait(ctx context.Context, host inventory.Host, run ProbeFunc) error {
	if run == nil {
		return fmt.Errorf("HTTPReady: nil probe func")
	}
	timeout := g.Timeout
	if timeout <= 0 {
		timeout = defaultGateTimeout
	}
	poll := g.Poll
	if poll <= 0 {
		poll = defaultGatePoll
	}

	cmd := fmt.Sprintf("curl -fsS -o /dev/null -w '%%{http_code}' --max-time 5 http://127.0.0.1:%d%s", g.Port, g.Path)

	deadline := time.Now().Add(timeout)
	var lastDetail string
	for {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		result, err := run(ctx, host, cmd)
		switch {
		case err != nil:
			lastDetail = "ssh error: " + err.Error()
		case result.ExitCode == 0 && strings.HasPrefix(strings.TrimSpace(result.Stdout), "2"):
			return nil
		default:
			lastDetail = "http " + strings.TrimSpace(result.Stdout)
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("HTTPReady: %s:%d%s did not return 2xx within %s (last: %s)",
				host.Name, g.Port, g.Path, timeout, lastDetail)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}

// SystemdActive polls `systemctl is-active <Unit>` over SSH until it
// reports "active" or the deadline expires. For services without an HTTP
// health endpoint — the unit being in the active+running state is the
// closest cheap readiness signal we have.
type SystemdActive struct {
	Unit    string
	Timeout time.Duration
	Poll    time.Duration
}

// Describe implements Gate.
func (g SystemdActive) Describe() string {
	return fmt.Sprintf("SystemdActive{%s}", g.Unit)
}

// Wait implements Gate.
func (g SystemdActive) Wait(ctx context.Context, host inventory.Host, run ProbeFunc) error {
	if run == nil {
		return fmt.Errorf("SystemdActive: nil probe func")
	}
	timeout := g.Timeout
	if timeout <= 0 {
		timeout = defaultGateTimeout
	}
	poll := g.Poll
	if poll <= 0 {
		poll = defaultGatePoll
	}

	cmd := fmt.Sprintf("systemctl is-active %s", g.Unit)

	deadline := time.Now().Add(timeout)
	var lastDetail string
	for {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		result, err := run(ctx, host, cmd)
		switch {
		case err != nil:
			lastDetail = "ssh error: " + err.Error()
		case strings.TrimSpace(result.Stdout) == "active":
			return nil
		default:
			lastDetail = strings.TrimSpace(result.Stdout)
			if lastDetail == "" {
				lastDetail = "unknown"
			}
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("SystemdActive: %s on %s did not reach 'active' within %s (last: %s)",
				g.Unit, host.Name, timeout, lastDetail)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}

// GateForService returns the readiness gate appropriate for a service ID,
// auto-selected from pkg/servicedefs. Services with an HTTP health
// endpoint (HealthProtocol "http" + non-empty HealthPath + non-zero
// DefaultPort) get HTTPReady; everything else falls back to
// SystemdActive on `frameworks-<id>`. Unknown service IDs also fall back
// to the SystemdActive default — keeps the gate selection total without
// requiring a hand-maintained per-service table.
func GateForService(id string) Gate {
	s, ok := servicedefs.Lookup(id)
	if ok && s.HealthProtocol == "http" && s.HealthPath != "" && s.DefaultPort != 0 {
		return HTTPReady{Port: s.DefaultPort, Path: s.HealthPath}
	}
	return SystemdActive{Unit: "frameworks-" + id}
}
