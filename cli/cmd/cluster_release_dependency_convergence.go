package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"

	"frameworks/cli/pkg/orchestrator"
)

// Release dependency convergence applies the roles of managed dependencies
// one host at a time, in the order of their task types. Their roles carry the
// safety of each change: vmagent, vmalert, and vmauth validate a changed
// config with -dryRun and reload it, restarting only for a new binary or unit;
// nginx and caddy test a changed config before they reload. The role's
// validation of a changed host must pass before the next host starts.

func describeObservabilityConvergence(out io.Writer, tasks []*orchestrator.Task) {
	describeDependencyConvergence(out, "vmagent, vmalert, and vmauth (configs validated, then reloaded)", tasks)
}

func describeProxyConvergence(out io.Writer, tasks []*orchestrator.Task) {
	describeDependencyConvergence(out, "control-plane nginx and caddy (config tested, then reloaded)", tasks)
}

func describeDependencyConvergence(out io.Writer, label string, tasks []*orchestrator.Task) {
	entries := make([]string, 0, len(tasks))
	for _, task := range tasks {
		entries = append(entries, task.Type+"@"+task.Host)
	}
	fmt.Fprintf(out, "     · %s, one host at a time: %s\n", label, strings.Join(entries, " -> "))
}

func runObservabilityConvergence(ctx context.Context, c *releaseHostConvergence, tasks []*orchestrator.Task, dryRun bool) error {
	return c.runDependencyConvergence(ctx, "Observability agents", tasks, dryRun)
}

func runProxyConvergence(ctx context.Context, c *releaseHostConvergence, tasks []*orchestrator.Task, dryRun bool) error {
	return c.runDependencyConvergence(ctx, "Control-plane proxies", tasks, dryRun)
}

// runDependencyConvergence converges each task in turn and stops at the first
// failure, leaving later hosts untouched.
func (c *releaseHostConvergence) runDependencyConvergence(ctx context.Context, label string, tasks []*orchestrator.Task, dryRun bool) error {
	out := c.cmd.OutOrStdout()
	fmt.Fprintf(out, "\n%s (%d host(s))\n", label, len(tasks))
	for _, task := range tasks {
		fmt.Fprintf(out, "  %s on %s\n", task.Type, task.Host)
		if err := c.convergeTask(ctx, task, dryRun, out); err != nil {
			return fmt.Errorf("%s on %s: %w", task.Type, task.Host, err)
		}
	}
	return nil
}
