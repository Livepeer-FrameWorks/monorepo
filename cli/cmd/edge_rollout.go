package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"frameworks/cli/internal/templates"
	"frameworks/cli/internal/xexec"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/provisioner"
	fwssh "frameworks/cli/pkg/ssh"
)

// edgeRolloutOps is the per-node surface `edge provision` drives. Inspect is
// the silent check-mode precheck of an installed node, DryRun the verbose
// check-mode plan with diffs, Apply the real provisioning run.
type edgeRolloutOps interface {
	Inspect(ctx context.Context) (provisioner.EdgeInspection, error)
	DryRun(ctx context.Context) (provisioner.EdgeInspection, error)
	Apply(ctx context.Context) error
	Drain(ctx context.Context) (restore func(context.Context) error, err error)
}

// edgeRolloutNode is one node of an `edge provision` run. Live is true when
// the host already carries an enrolled edge install: it may be serving
// viewers, so it is prechecked, applied only when it drifted, never in
// parallel with another node, and drained first when the apply restarts
// MistServer or Caddy or recreates the edge container. A fresh node has no
// sessions to protect and is applied unconditionally.
type edgeRolloutNode struct {
	Name string
	Live bool
	Ops  edgeRolloutOps
}

type edgeRolloutAction string

const (
	edgeActionInstall      edgeRolloutAction = "install"
	edgeActionSkip         edgeRolloutAction = "skip"
	edgeActionApply        edgeRolloutAction = "apply"
	edgeActionDrainedApply edgeRolloutAction = "drain+apply"
)

// edgeRolloutResult is the outcome for one node.
type edgeRolloutResult struct {
	Name       string
	Action     edgeRolloutAction
	Inspection provisioner.EdgeInspection
	Err        error
	// LeftDraining is set when the node was drained and the apply failed;
	// it stays out of routing until an operator resumes it.
	LeftDraining bool
}

// runEdgeRollout plans and applies an `edge provision` run. The precheck
// runs for every live node before anything is applied. Fresh nodes are
// installed first, up to parallel at a time; live nodes that drifted are
// then applied one at a time. dryRun prints each node's plan and diff and
// changes nothing.
func runEdgeRollout(ctx context.Context, w io.Writer, nodes []edgeRolloutNode, parallel int, dryRun bool) []edgeRolloutResult {
	return runEdgeRolloutWith(ctx, w, nodes, parallel, dryRun, false)
}

// runEdgeRolloutWith is runEdgeRollout with `edge provision --no-drain`: when
// noDrain is set, a node whose apply restarts media services is applied in
// place instead of drained first. The only edge in a cluster can never drain,
// because its sessions have nowhere else to go.
func runEdgeRolloutWith(ctx context.Context, w io.Writer, nodes []edgeRolloutNode, parallel int, dryRun, noDrain bool) []edgeRolloutResult {
	results := make([]edgeRolloutResult, len(nodes))
	for i, n := range nodes {
		results[i] = edgeRolloutResult{Name: n.Name}
	}

	if dryRun {
		for i, n := range nodes {
			fmt.Fprintf(w, "\n[%s] Checking drift (dry-run)...\n", n.Name)
			inspection, err := n.Ops.DryRun(ctx)
			results[i].Inspection = inspection
			results[i].Action = plannedEdgeAction(n.Live, inspection)
			results[i].Err = err
			if err != nil {
				fmt.Fprintf(w, "[%s] CHECK FAILED: %v\n", n.Name, err)
				continue
			}
			fmt.Fprintf(w, "[%s] plan: %s — %s\n", n.Name, results[i].Action, describeEdgePlan(n.Live, inspection))
		}
		return results
	}

	var live, fresh []int
	for i, n := range nodes {
		if !n.Live {
			results[i].Action = edgeActionInstall
			fresh = append(fresh, i)
			continue
		}
		fmt.Fprintf(w, "\n[%s] Checking drift...\n", n.Name)
		inspection, err := n.Ops.Inspect(ctx)
		results[i].Inspection = inspection
		if err != nil {
			results[i].Err = fmt.Errorf("precheck: %w", err)
			fmt.Fprintf(w, "[%s] PRECHECK FAILED: %v\n", n.Name, err)
			continue
		}
		results[i].Action = plannedEdgeAction(true, inspection)
		fmt.Fprintf(w, "[%s] plan: %s — %s\n", n.Name, results[i].Action, describeEdgePlan(true, inspection))
		if results[i].Action != edgeActionSkip {
			live = append(live, i)
		}
	}

	if parallel < 1 {
		parallel = 1
	}
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for _, i := range fresh {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			fmt.Fprintf(w, "\n[%s] Installing...\n", nodes[i].Name)
			results[i].Err = nodes[i].Ops.Apply(ctx)
		}(i)
	}
	wg.Wait()

	for _, i := range live {
		n := nodes[i]
		if results[i].Action == edgeActionDrainedApply && noDrain {
			fmt.Fprintf(w, "\n[%s] Applying without draining (--no-drain); media services restart and live sessions on this node reconnect...\n", n.Name)
			results[i].Err = n.Ops.Apply(ctx)
			continue
		}
		if results[i].Action != edgeActionDrainedApply {
			fmt.Fprintf(w, "\n[%s] Applying (no media restart)...\n", n.Name)
			results[i].Err = n.Ops.Apply(ctx)
			continue
		}
		fmt.Fprintf(w, "\n[%s] Draining before the apply restarts media services...\n", n.Name)
		restore, err := n.Ops.Drain(ctx)
		if err != nil {
			results[i].Err = fmt.Errorf("drain: %w (node not changed)", err)
			continue
		}
		fmt.Fprintf(w, "[%s] Applying...\n", n.Name)
		if err := n.Ops.Apply(ctx); err != nil {
			results[i].Err = err
			results[i].LeftDraining = true
			continue
		}
		if err := restore(ctx); err != nil {
			results[i].Err = fmt.Errorf("applied, but restoring the operational mode failed: %w", err)
			results[i].LeftDraining = true
		}
	}
	return results
}

func plannedEdgeAction(live bool, inspection provisioner.EdgeInspection) edgeRolloutAction {
	switch {
	case !live:
		return edgeActionInstall
	case !inspection.Changed:
		return edgeActionSkip
	case inspection.Interrupting:
		return edgeActionDrainedApply
	default:
		return edgeActionApply
	}
}

func describeEdgePlan(live bool, inspection provisioner.EdgeInspection) string {
	if !live {
		return "fresh node; full install"
	}
	summary := inspection.Summary()
	if len(inspection.ChangedTasks) > 0 {
		summary += "; changed: " + strings.Join(inspection.ChangedTasks, ", ")
	}
	return summary
}

// provisionedEdgeNode drives one node through EdgeProvisioner. runningMode is
// the stack that serves the node now (it differs from config.Mode during a
// container/native migration) and decides where the drain reaches Helmsman.
type provisionedEdgeNode struct {
	ep          *provisioner.EdgeProvisioner
	host        inventory.Host
	config      provisioner.EdgeProvisionConfig
	sshTarget   string
	sshKey      string
	runningMode string
	name        string
	w           io.Writer
}

func newProvisionedEdgeNode(w io.Writer, name string, host inventory.Host, config provisioner.EdgeProvisionConfig, sshTarget, sshKey, runningMode string, live bool) edgeRolloutNode {
	return edgeRolloutNode{
		Name: name,
		Live: live,
		Ops: &provisionedEdgeNode{
			ep:          provisioner.NewEdgeProvisioner(fwssh.NewPool(30*time.Second, sshKey)),
			host:        host,
			config:      config,
			sshTarget:   sshTarget,
			sshKey:      sshKey,
			runningMode: templates.NormalizeEdgeMode(runningMode),
			name:        name,
			w:           w,
		},
	}
}

func (p *provisionedEdgeNode) Inspect(ctx context.Context) (provisioner.EdgeInspection, error) {
	return p.ep.Inspect(ctx, p.host, p.config)
}

func (p *provisionedEdgeNode) DryRun(ctx context.Context) (provisioner.EdgeInspection, error) {
	return p.ep.DryRun(ctx, p.host, p.config)
}

func (p *provisionedEdgeNode) Apply(ctx context.Context) error {
	return p.ep.Provision(ctx, p.host, p.config)
}

func (p *provisionedEdgeNode) Drain(ctx context.Context) (func(context.Context) error, error) {
	return newEdgeDrainer(p.w, p.name, newEdgeModeRunner(p.sshTarget, p.sshKey, p.runningMode)).Drain(ctx)
}

// edgeDrainDeadline bounds how long a drain waits for sessions to end,
// matching `cluster nodes remove`.
const edgeDrainDeadline = defaultClusterNodeDrainDeadline

// edgeModeRunner runs curl against the node's loopback-only Helmsman
// management listener: inside the edge container in container mode, on the
// host otherwise, over SSH when sshTarget is set.
type edgeModeRunner func(ctx context.Context, curlArgs []string) (string, error)

func newEdgeModeRunner(sshTarget, sshKey, deployMode string) edgeModeRunner {
	return func(ctx context.Context, curlArgs []string) (string, error) {
		if deployMode == "container" {
			out, errOut, err := runEdgeDocker(ctx, sshTarget, sshKey, append([]string{"exec", "frameworks-edge", "curl"}, curlArgs...), "")
			if err != nil {
				return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(errOut))
			}
			return out, nil
		}
		var (
			out, errOut string
			err         error
		)
		if strings.TrimSpace(sshTarget) != "" && sshTarget != "localhost" && sshTarget != "127.0.0.1" {
			_, out, errOut, err = xexec.RunSSHWithKey(ctx, sshTarget, sshKey, "curl", curlArgs, "")
		} else {
			_, out, errOut, err = xexec.Run(ctx, "curl", curlArgs, "")
		}
		if err != nil {
			return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(errOut))
		}
		return out, nil
	}
}

const edgeHelmsmanManagementBase = "http://localhost:18017"

// edgeDrainer takes one node out of routing through Foghorn (Helmsman
// forwards the mode change on its control stream; Foghorn applies and
// persists it) and waits for its sessions to end.
type edgeDrainer struct {
	run      edgeModeRunner
	deadline time.Duration
	poll     time.Duration
	// confirm bounds the wait for Foghorn to push the requested mode back
	// and, after an apply, for Helmsman's control stream to reconnect.
	confirm time.Duration
	sleep   func(context.Context, time.Duration) error
	now     func() time.Time
	w       io.Writer
	name    string
}

func newEdgeDrainer(w io.Writer, name string, run edgeModeRunner) *edgeDrainer {
	return &edgeDrainer{
		run:      run,
		deadline: edgeDrainDeadline,
		poll:     15 * time.Second,
		confirm:  3 * time.Minute,
		sleep:    sleepContext,
		now:      time.Now,
		w:        w,
		name:     name,
	}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (d *edgeDrainer) mode(ctx context.Context) (string, error) {
	out, err := d.run(ctx, []string{"-s", "-f", "--max-time", "10", edgeHelmsmanManagementBase + "/node/mode"})
	if err != nil {
		return "", err
	}
	var resp struct {
		Mode string `json:"mode"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &resp); err != nil {
		return "", fmt.Errorf("parse node mode: %w", err)
	}
	return resp.Mode, nil
}

func (d *edgeDrainer) setMode(ctx context.Context, mode string) error {
	body := fmt.Sprintf(`{"mode":%q,"reason":"edge_provision"}`, mode)
	_, err := d.run(ctx, []string{"-s", "-f", "--max-time", "10", "-X", "POST", "-H", "Content-Type: application/json", "-d", body, edgeHelmsmanManagementBase + "/node/mode"})
	return err
}

// sessions sums Helmsman's stream_viewers gauge: one sample per stream,
// counting every client connection Mist reports on that stream.
func (d *edgeDrainer) sessions(ctx context.Context) (int, error) {
	out, err := d.run(ctx, []string{"-s", "-f", "--max-time", "10", edgeHelmsmanManagementBase + "/metrics"})
	if err != nil {
		return 0, err
	}
	return sumStreamViewers(out)
}

func sumStreamViewers(metrics string) (int, error) {
	total := 0.0
	for line := range strings.SplitSeq(metrics, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "stream_viewers{") && !strings.HasPrefix(line, "stream_viewers ") {
			continue
		}
		fields := strings.Fields(line[strings.LastIndex(line, "}")+1:])
		if len(fields) == 0 {
			return 0, fmt.Errorf("malformed stream_viewers sample %q", line)
		}
		v, err := strconv.ParseFloat(fields[0], 64)
		if err != nil {
			return 0, fmt.Errorf("malformed stream_viewers sample %q: %w", line, err)
		}
		total += v
	}
	return int(total), nil
}

// waitForMode polls until Helmsman reports want, within d.confirm, retrying
// read errors (Helmsman restarting, control stream reconnecting).
func (d *edgeDrainer) waitForMode(ctx context.Context, want string, set bool) error {
	deadline := d.now().Add(d.confirm)
	var lastErr error
	for {
		if set {
			lastErr = d.setMode(ctx, want)
		}
		if lastErr == nil {
			got, err := d.mode(ctx)
			if err == nil && got == want {
				return nil
			}
			lastErr = err
			if err == nil {
				lastErr = fmt.Errorf("node reports mode %q", got)
			}
		}
		if !d.now().Before(deadline) {
			return fmt.Errorf("node did not reach mode %s within %s: %w", want, d.confirm, lastErr)
		}
		if err := d.sleep(ctx, 5*time.Second); err != nil {
			return err
		}
	}
}

// Drain moves the node to draining unless an operator already set a
// non-normal mode, waits for Foghorn to confirm, then waits for every
// session to end. A drain that cannot finish restores the prior mode and
// fails, so the apply that would drop the sessions does not run. The
// returned restore puts back the mode the node had before.
func (d *edgeDrainer) Drain(ctx context.Context) (func(context.Context) error, error) {
	prior, err := d.mode(ctx)
	if err != nil {
		return nil, fmt.Errorf("read node mode: %w", err)
	}
	restore := func(context.Context) error { return nil }
	if prior == "normal" {
		if err := d.waitForMode(ctx, "draining", true); err != nil {
			return nil, err
		}
		restore = func(ctx context.Context) error {
			fmt.Fprintf(d.w, "[%s] Restoring operational mode normal...\n", d.name)
			return d.waitForMode(ctx, "normal", true)
		}
	} else {
		fmt.Fprintf(d.w, "[%s] node already in %s mode; leaving it as set\n", d.name, prior)
	}

	deadline := d.now().Add(d.deadline)
	for {
		n, err := d.sessions(ctx)
		if err == nil && n == 0 {
			fmt.Fprintf(d.w, "[%s] drained: no active sessions\n", d.name)
			return restore, nil
		}
		if err == nil {
			fmt.Fprintf(d.w, "[%s] waiting for %d session(s) to end\n", d.name, n)
		}
		if !d.now().Before(deadline) {
			cause := fmt.Errorf("%d session(s) still active after %s", n, d.deadline)
			if err != nil {
				cause = fmt.Errorf("session count unavailable after %s: %w", d.deadline, err)
			}
			if restoreErr := restore(ctx); restoreErr != nil {
				return nil, errors.Join(cause, fmt.Errorf("restore mode: %w", restoreErr))
			}
			return nil, cause
		}
		if err := d.sleep(ctx, d.poll); err != nil {
			return nil, err
		}
	}
}

// summarizeEdgeRollout prints the per-node outcome and returns an error
// naming the failed nodes.
func summarizeEdgeRollout(w io.Writer, results []edgeRolloutResult, dryRun bool) error {
	fmt.Fprintf(w, "\n=== Provisioning Summary ===\n")
	counts := map[edgeRolloutAction]int{}
	var failed []string
	for _, r := range results {
		counts[r.Action]++
		if r.Err != nil {
			failed = append(failed, r.Name)
			fmt.Fprintf(w, "  %-24s FAILED (%s): %v\n", r.Name, r.Action, r.Err)
			if r.LeftDraining {
				fmt.Fprintf(w, "  %-24s left draining; after fixing it, resume with: frameworks edge mode normal --ssh <target>\n", "")
			}
			continue
		}
		verb := string(r.Action)
		if dryRun {
			verb = "would " + verb
		}
		fmt.Fprintf(w, "  %-24s %s\n", r.Name, verb)
	}
	fmt.Fprintf(w, "  install=%d apply=%d drain+apply=%d skip=%d failed=%d\n",
		counts[edgeActionInstall], counts[edgeActionApply], counts[edgeActionDrainedApply], counts[edgeActionSkip], len(failed))
	if len(failed) > 0 {
		return fmt.Errorf("%d node(s) failed: %s", len(failed), strings.Join(failed, ", "))
	}
	return nil
}
