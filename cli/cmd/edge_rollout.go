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

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/internal/controlplane"
	"frameworks/cli/internal/templates"
	"frameworks/cli/internal/xexec"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/provisioner"
	fwssh "frameworks/cli/pkg/ssh"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

// edgeRolloutOps is the per-node surface `edge provision` drives. Inspect is
// the silent check-mode precheck of an installed node, DryRun the verbose
// check-mode plan with diffs, Apply the real provisioning run.
// ControlConnected and RoutablePeers decide whether a drain can finish:
// Foghorn applies the mode change through Helmsman's control stream, and the
// node's sessions need another routable node of its cluster to move to.
// PendingRestarts names the services whose installed unit or env change
// waits for their next start. AwaitControlConnected waits, bounded, for
// Helmsman's control stream to Foghorn after an apply: a node Foghorn cannot
// reach is not provisioned, whatever HTTPS says.
type edgeRolloutOps interface {
	Inspect(ctx context.Context) (provisioner.EdgeInspection, error)
	DryRun(ctx context.Context) (provisioner.EdgeInspection, error)
	Apply(ctx context.Context) error
	Drain(ctx context.Context) (restore func(context.Context) error, err error)
	ControlConnected(ctx context.Context) (bool, error)
	RoutablePeers(ctx context.Context) (int, error)
	PendingRestarts(ctx context.Context) ([]string, error)
	AwaitControlConnected(ctx context.Context) error
}

// edgeRolloutNode is one node of an `edge provision` run. Live is true when
// the host already carries an enrolled edge install: it may be serving
// viewers, so it is prechecked, applied only when it drifted, never in
// parallel with another node, and drained first when the apply restarts
// MistServer or Caddy or recreates the edge container and the drain can
// finish. A fresh node has no sessions to protect and is applied
// unconditionally.
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
	// edgeActionInPlaceApply is a media-restarting apply run without a
	// drain: --no-drain, or a drain that could not finish.
	edgeActionInPlaceApply edgeRolloutAction = "apply-in-place"
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
	// PendingRestarts names the services whose installed unit or env change
	// waits for their next start.
	PendingRestarts []string
}

// edgeInPlaceReason returns why a media-restarting apply on this node cannot
// be preceded by a drain that finishes, or "" when the drain can run. A drain
// needs Helmsman's control stream (Foghorn applies the mode change through
// it) and another routable node in the cluster for the sessions to move to;
// a state that cannot be read counts as a drain that cannot finish.
func edgeInPlaceReason(ctx context.Context, ops edgeRolloutOps) string {
	connected, err := ops.ControlConnected(ctx)
	switch {
	case err != nil:
		return fmt.Sprintf("cannot read Helmsman's control stream state (%v)", err)
	case !connected:
		return "Helmsman's control stream to Foghorn is down, so Foghorn cannot route its sessions elsewhere"
	}
	peers, err := ops.RoutablePeers(ctx)
	switch {
	case err != nil:
		return fmt.Sprintf("cannot determine whether another node in its cluster can take its sessions (%v)", err)
	case peers == 0:
		return "it is the only routable node in its cluster, so its sessions have nowhere to drain to"
	}
	return ""
}

// edgePrecheckConcurrency bounds how many live nodes are checked at once; the
// check-mode run is read-only, so only the SSH fan-out needs a limit.
const edgePrecheckConcurrency = 8

// runEdgeRollout plans and applies an `edge provision` run without --no-drain.
func runEdgeRollout(ctx context.Context, w io.Writer, nodes []edgeRolloutNode, parallel int, dryRun bool) []edgeRolloutResult {
	return runEdgeRolloutWith(ctx, w, nodes, parallel, dryRun, false)
}

// runEdgeRolloutWith plans and applies an `edge provision` run. Every live
// node is prechecked, concurrently, before anything is applied. Fresh nodes
// are installed first, up to parallel at a time; live nodes that drifted are
// then applied one at a time. dryRun prints each node's plan and diff,
// including whether a media-restarting change would drain or apply in place,
// and changes nothing. noDrain (`edge provision --no-drain`) applies a
// media-restarting change in place even when another node could take the
// node's sessions. A node is only reported provisioned once Helmsman's
// control stream to Foghorn is connected after its apply.
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
			if results[i].Action == edgeActionDrainedApply {
				if reason := edgeDrainDecision(ctx, n.Ops, noDrain); reason != "" {
					results[i].Action = edgeActionInPlaceApply
					fmt.Fprintf(w, "[%s] plan: %s — would apply without draining: %s\n", n.Name, results[i].Action, reason)
				} else {
					fmt.Fprintf(w, "[%s] would drain first (up to %s): another routable node can take its sessions\n", n.Name, edgeDrainDeadline)
				}
			}
		}
		return results
	}

	var live, fresh []int
	for i, n := range nodes {
		if n.Live {
			live = append(live, i)
		} else {
			results[i].Action = edgeActionInstall
			fresh = append(fresh, i)
		}
	}
	if len(live) > 0 {
		fmt.Fprintf(w, "\nChecking drift on %d enrolled node(s)...\n", len(live))
	}
	precheck := make(chan struct{}, edgePrecheckConcurrency)
	var checks sync.WaitGroup
	for _, i := range live {
		checks.Add(1)
		go func(i int) {
			defer checks.Done()
			precheck <- struct{}{}
			defer func() { <-precheck }()
			inspection, err := nodes[i].Ops.Inspect(ctx)
			results[i].Inspection = inspection
			if err != nil {
				results[i].Err = fmt.Errorf("precheck: %w", err)
				return
			}
			results[i].Action = plannedEdgeAction(true, inspection)
		}(i)
	}
	checks.Wait()
	var drifted []int
	for _, i := range live {
		if results[i].Err != nil {
			fmt.Fprintf(w, "[%s] PRECHECK FAILED: %v\n", nodes[i].Name, results[i].Err)
			continue
		}
		fmt.Fprintf(w, "[%s] plan: %s — %s\n", nodes[i].Name, results[i].Action, describeEdgePlan(true, results[i].Inspection))
		if results[i].Action != edgeActionSkip {
			drifted = append(drifted, i)
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
			if err := nodes[i].Ops.Apply(ctx); err != nil {
				results[i].Err = err
				return
			}
			if err := nodes[i].Ops.AwaitControlConnected(ctx); err != nil {
				results[i].Err = fmt.Errorf("installed, but %w", err)
			}
		}(i)
	}
	wg.Wait()

	for _, i := range drifted {
		applyLiveEdgeNode(ctx, w, nodes[i], &results[i], noDrain)
		if results[i].Err != nil {
			continue
		}
		pending, err := nodes[i].Ops.PendingRestarts(ctx)
		if err != nil {
			fmt.Fprintf(w, "[%s] could not read pending restarts: %v\n", nodes[i].Name, err)
		}
		results[i].PendingRestarts = pending
	}
	return results
}

// edgeDrainDecision returns why a media-restarting apply runs in place
// instead of drained first, or "" when it drains.
func edgeDrainDecision(ctx context.Context, ops edgeRolloutOps, noDrain bool) string {
	if noDrain {
		return "--no-drain"
	}
	return edgeInPlaceReason(ctx, ops)
}

// applyLiveEdgeNode applies one drifted live node: directly when the change
// keeps media running, in place when a drain cannot finish, and otherwise
// wrapped in drain → apply → restore. The apply only counts once Helmsman's
// control stream is connected again; a drained node whose stream does not
// come back stays draining.
func applyLiveEdgeNode(ctx context.Context, w io.Writer, n edgeRolloutNode, result *edgeRolloutResult, noDrain bool) {
	if result.Action != edgeActionDrainedApply {
		fmt.Fprintf(w, "\n[%s] Applying (no media restart)...\n", n.Name)
		result.Err = applyAndAwaitControl(ctx, n.Ops)
		return
	}
	if reason := edgeDrainDecision(ctx, n.Ops, noDrain); reason != "" {
		result.Action = edgeActionInPlaceApply
		fmt.Fprintf(w, "\n[%s] Applying in place without draining: %s. Media services restart and live sessions on this node reconnect.\n", n.Name, reason)
		result.Err = applyAndAwaitControl(ctx, n.Ops)
		return
	}
	fmt.Fprintf(w, "\n[%s] Draining before the apply restarts media services (up to %s)...\n", n.Name, edgeDrainDeadline)
	restore, err := n.Ops.Drain(ctx)
	if err != nil {
		result.Err = fmt.Errorf("drain: %w (node not changed)", err)
		return
	}
	fmt.Fprintf(w, "[%s] Applying...\n", n.Name)
	if err := applyAndAwaitControl(ctx, n.Ops); err != nil {
		result.Err = err
		result.LeftDraining = true
		return
	}
	if err := restore(ctx); err != nil {
		result.Err = fmt.Errorf("applied, but restoring the operational mode failed: %w", err)
		result.LeftDraining = true
	}
}

func applyAndAwaitControl(ctx context.Context, ops edgeRolloutOps) error {
	if err := ops.Apply(ctx); err != nil {
		return err
	}
	if err := ops.AwaitControlConnected(ctx); err != nil {
		return fmt.Errorf("applied, but %w", err)
	}
	return nil
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
// routablePeers counts the other routable nodes of the node's cluster.
type provisionedEdgeNode struct {
	ep            *provisioner.EdgeProvisioner
	host          inventory.Host
	config        provisioner.EdgeProvisionConfig
	sshTarget     string
	sshKey        string
	runningMode   string
	name          string
	w             io.Writer
	routablePeers func(context.Context) (int, error)
}

func newProvisionedEdgeNode(w io.Writer, name string, host inventory.Host, config provisioner.EdgeProvisionConfig, sshTarget, sshKey, runningMode string, live bool, access edgeControlAccess) edgeRolloutNode {
	return edgeRolloutNode{
		Name: name,
		Live: live,
		Ops: &provisionedEdgeNode{
			ep:            provisioner.NewEdgeProvisioner(fwssh.NewPool(30*time.Second, sshKey)),
			host:          host,
			config:        config,
			sshTarget:     sshTarget,
			sshKey:        sshKey,
			runningMode:   templates.NormalizeEdgeMode(runningMode),
			name:          name,
			w:             w,
			routablePeers: edgeRoutablePeerCounter(access, config.ClusterID, config.NodeID),
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
	return p.drainer().Drain(ctx)
}

func (p *provisionedEdgeNode) ControlConnected(ctx context.Context) (bool, error) {
	return p.drainer().controlConnected(ctx)
}

func (p *provisionedEdgeNode) RoutablePeers(ctx context.Context) (int, error) {
	return p.routablePeers(ctx)
}

func (p *provisionedEdgeNode) AwaitControlConnected(ctx context.Context) error {
	return p.drainer().awaitControlConnected(ctx)
}

func (p *provisionedEdgeNode) PendingRestarts(ctx context.Context) ([]string, error) {
	return p.ep.PendingRestarts(ctx, p.host)
}

// edgeControlAccess opens the context and resolver the routability read
// authenticates and dials with; the caller runs the returned cleanup.
type edgeControlAccess func(ctx context.Context) (fwcfg.Context, *controlplane.Resolver, func(), error)

// edgeControlAccessFor reads routability with ctxCfg when it already carries a
// resolved service token (an edge manifest's cluster control plane), and with
// the active platform context otherwise.
func edgeControlAccessFor(ctxCfg fwcfg.Context) edgeControlAccess {
	return func(ctx context.Context) (fwcfg.Context, *controlplane.Resolver, func(), error) {
		cfg := ctxCfg
		if cfg.Persona != fwcfg.PersonaPlatform || strings.TrimSpace(cfg.Auth.ServiceToken) == "" {
			active, err := activeClusterLifecycleContextWithAuth(ctx)
			if err != nil {
				return fwcfg.Context{}, nil, nil, err
			}
			cfg = active
		}
		r := controlplane.NewResolver(cfg)
		return cfg, r, r.Close, nil
	}
}

// edgeRoutablePeerCounter counts the other edge nodes of clusterID that
// Quartermaster lists as active and Foghorn reports healthy in normal mode:
// the nodes a drained nodeID's sessions can move to.
func edgeRoutablePeerCounter(access edgeControlAccess, clusterID, nodeID string) func(context.Context) (int, error) {
	return func(ctx context.Context) (int, error) {
		if strings.TrimSpace(clusterID) == "" || strings.TrimSpace(nodeID) == "" {
			return 0, errors.New("node has no cluster or node ID")
		}
		ctxCfg, resolver, cleanup, err := access(ctx)
		if err != nil {
			return 0, err
		}
		defer cleanup()
		ep, err := resolver.ResolveGRPC(ctx, "quartermaster")
		if err != nil {
			return 0, err
		}
		qm, err := qmclient.NewGRPCClient(clusterNodesQuartermasterGRPCConfig(ep, ctxCfg))
		if err != nil {
			return 0, fmt.Errorf("connect to Quartermaster: %w", err)
		}
		defer func() { _ = qm.Close() }()
		cctx, cancel := clusterNodesRPCContext(ctx, ctxCfg, 15*time.Second)
		resp, err := qm.ListNodes(cctx, clusterID, "edge", "", nil)
		cancel()
		if err != nil {
			return 0, fmt.Errorf("list cluster %s edge nodes: %w", clusterID, err)
		}
		return countRoutablePeers(ctx, ctxCfg, resp.GetNodes(), nodeID, foghornNodeHealthDialer(resolver, ctxCfg))
	}
}

// countRoutablePeers counts the nodes other than nodeID that are active and
// that Foghorn reports healthy in normal mode. A node whose health cannot be
// read does not count; when no health can be read at all the count is
// unknown.
func countRoutablePeers(ctx context.Context, ctxCfg fwcfg.Context, nodes []*quartermasterpb.InfrastructureNode, nodeID string, dial nodeHealthDialer) (int, error) {
	var peers []*quartermasterpb.InfrastructureNode
	for _, n := range nodes {
		if n == nil || n.GetNodeId() == nodeID || n.GetNodeName() == nodeID || n.GetId() == nodeID {
			continue
		}
		if status := n.GetStatus(); status != "" && status != "active" {
			continue
		}
		peers = append(peers, n)
	}
	if len(peers) == 0 {
		return 0, nil
	}
	health, dialErrs, _ := collectNodeHealth(ctx, ctxCfg, peers, dial)
	if len(health) == 0 && len(dialErrs) > 0 {
		return 0, errors.Join(dialErrs...)
	}
	routable := 0
	for _, n := range peers {
		if h := health[n.GetNodeId()]; h != nil && h.GetIsHealthy() && h.GetOperationalMode() == "normal" {
			routable++
		}
	}
	return routable, nil
}

func (p *provisionedEdgeNode) drainer() *edgeDrainer {
	return newEdgeDrainer(p.w, p.name, newEdgeModeRunner(p.sshTarget, p.sshKey, p.runningMode))
}

// edgeDrainDeadline bounds how long `edge provision` waits for a drained
// node's sessions to end. It only drains when another routable node can take
// them, so sessions that have not moved by then are not going to; the node is
// then restored and failed rather than held out of routing for hours.
const edgeDrainDeadline = 10 * time.Minute

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
	// controlWait bounds the post-apply wait for the control stream.
	controlWait time.Duration
	sleep       func(context.Context, time.Duration) error
	now         func() time.Time
	w           io.Writer
	name        string
}

func newEdgeDrainer(w io.Writer, name string, run edgeModeRunner) *edgeDrainer {
	return &edgeDrainer{
		run:         run,
		deadline:    edgeDrainDeadline,
		poll:        15 * time.Second,
		confirm:     3 * time.Minute,
		controlWait: edgeControlStreamWait,
		sleep:       sleepContext,
		now:         time.Now,
		w:           w,
		name:        name,
	}
}

// edgeControlStreamWait bounds how long `edge provision` waits after an apply
// for Helmsman to (re)connect to Foghorn: a restarted Helmsman reconnects in
// seconds, so a stream still down after this is a fault to report.
const edgeControlStreamWait = 90 * time.Second

// edgeControlStreamPoll is the interval between control stream reads.
const edgeControlStreamPoll = 5 * time.Second

// awaitControlConnected polls Helmsman's control stream gauge until it reads
// connected, printing each poll, and fails once controlWait has passed.
func (d *edgeDrainer) awaitControlConnected(ctx context.Context) error {
	start := d.now()
	deadline := start.Add(d.controlWait)
	for {
		connected, err := d.controlConnected(ctx)
		if err == nil && connected {
			fmt.Fprintf(d.w, "[%s] Helmsman control stream to Foghorn connected\n", d.name)
			return nil
		}
		elapsed := fmt.Sprintf("%s elapsed of %s", d.now().Sub(start).Round(time.Second), d.controlWait)
		state := "disconnected"
		if err != nil {
			state = "unreadable: " + err.Error()
		}
		fmt.Fprintf(d.w, "[%s] waiting for Helmsman control stream to Foghorn: %s (%s)\n", d.name, state, elapsed)
		if !d.now().Before(deadline) {
			return fmt.Errorf("the Helmsman control stream to Foghorn is not connected after %s (%s); Foghorn cannot reach or route this node", d.controlWait, state)
		}
		if err := d.sleep(ctx, edgeControlStreamPoll); err != nil {
			return err
		}
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

// controlConnected reads Helmsman's helmsman_control_stream_connected gauge.
func (d *edgeDrainer) controlConnected(ctx context.Context) (bool, error) {
	out, err := d.run(ctx, []string{"-s", "-f", "--max-time", "10", edgeHelmsmanManagementBase + "/metrics"})
	if err != nil {
		return false, err
	}
	return parseControlStreamConnected(out)
}

func parseControlStreamConnected(metrics string) (bool, error) {
	for line := range strings.SplitSeq(metrics, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "helmsman_control_stream_connected" {
			continue
		}
		v, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			return false, fmt.Errorf("malformed helmsman_control_stream_connected sample %q: %w", line, err)
		}
		return v == 1, nil
	}
	return false, errors.New("helmsman_control_stream_connected not reported")
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

	start := d.now()
	deadline := start.Add(d.deadline)
	for {
		n, err := d.sessions(ctx)
		if err == nil && n == 0 {
			fmt.Fprintf(d.w, "[%s] drained: no active sessions\n", d.name)
			return restore, nil
		}
		elapsed := fmt.Sprintf("%s elapsed of %s", d.now().Sub(start).Round(time.Second), d.deadline)
		if err == nil {
			fmt.Fprintf(d.w, "[%s] draining: %d session(s) remaining (%s)\n", d.name, n, elapsed)
		} else {
			fmt.Fprintf(d.w, "[%s] draining: session count unavailable: %v (%s)\n", d.name, err, elapsed)
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
		if len(r.PendingRestarts) > 0 {
			fmt.Fprintf(w, "  %-24s restart pending: %s (an installed unit/env change applies at its next start; the running process keeps serving)\n", "", strings.Join(r.PendingRestarts, ", "))
		}
	}
	fmt.Fprintf(w, "  install=%d apply=%d drain+apply=%d apply-in-place=%d skip=%d failed=%d\n",
		counts[edgeActionInstall], counts[edgeActionApply], counts[edgeActionDrainedApply], counts[edgeActionInPlaceApply], counts[edgeActionSkip], len(failed))
	if len(failed) > 0 {
		return fmt.Errorf("%d node(s) failed: %s", len(failed), strings.Join(failed, ", "))
	}
	return nil
}
