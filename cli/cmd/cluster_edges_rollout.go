package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/internal/controlplane"
	"frameworks/cli/internal/ux"
	fhclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/spf13/cobra"
)

const (
	edgeRolloutStatusCommand   = "frameworks cluster edges rollout status"
	edgeRolloutPollInterval    = 15 * time.Second
	edgeRolloutReleaseWait     = 10 * time.Minute
	edgeRolloutRPCTimeout      = 20 * time.Second
	edgeRolloutComponentSchema = "config_schema"
)

// edgeRolloutExpectation is what one cluster's edges should run: the release
// target Quartermaster holds and the component versions of that release.
type edgeRolloutExpectation struct {
	ClusterID     string
	TargetRelease string // channel:version, as Foghorn records it
	Paused        bool
	Components    map[string]string
}

// edgeRolloutSource is what the rollout check reads: the expectation and the
// enrolled edges from Quartermaster, and each node's update state from the
// cluster's Foghorn.
type edgeRolloutSource interface {
	Expectation(ctx context.Context, clusterID string) (edgeRolloutExpectation, error)
	EnrolledEdges(ctx context.Context, clusterID string) ([]string, error)
	NodeStatuses(ctx context.Context, clusterID string) ([]*foghorncontrolpb.NodeUpdateStatus, error)
}

type edgeNodeRolloutState string

const (
	edgeNodeConverged edgeNodeRolloutState = "converged"
	edgeNodeFailed    edgeNodeRolloutState = "failed"
	edgeNodePending   edgeNodeRolloutState = "not converged"
	edgeNodeUnmanaged edgeNodeRolloutState = "not updated automatically"
	edgeNodeMissing   edgeNodeRolloutState = "not connected to Foghorn"
	edgeNodePaused    edgeNodeRolloutState = "target paused"
)

type edgeNodeRollout struct {
	ClusterID string
	NodeID    string
	State     edgeNodeRolloutState
	Phase     string
	LastError string
	Deadline  string
	// Behind lists "component current->want" for each expected component the
	// node does not report at the target version.
	Behind []string
}

// edgeClusterRollout is one cluster's evaluated rollout; Err is set when the
// cluster's state could not be read.
type edgeClusterRollout struct {
	Expectation edgeRolloutExpectation
	Nodes       []edgeNodeRollout
	Err         error
}

type edgeRolloutOutcome struct {
	Clusters []edgeClusterRollout
}

func (o edgeRolloutOutcome) count(state edgeNodeRolloutState) int {
	n := 0
	for _, cluster := range o.Clusters {
		for _, node := range cluster.Nodes {
			if node.State == state {
				n++
			}
		}
	}
	return n
}

func (o edgeRolloutOutcome) unreadable() int {
	n := 0
	for _, cluster := range o.Clusters {
		if cluster.Err != nil {
			n++
		}
	}
	return n
}

// Converged is true only when every cluster was read and every node runs the
// target versions.
func (o edgeRolloutOutcome) Converged() bool {
	if o.unreadable() > 0 {
		return false
	}
	for _, cluster := range o.Clusters {
		for _, node := range cluster.Nodes {
			if node.State != edgeNodeConverged {
				return false
			}
		}
	}
	return true
}

// settled reports whether waiting longer cannot change the outcome: no node is
// still on its way to the target.
func (o edgeRolloutOutcome) settled() bool {
	if o.unreadable() > 0 {
		return false
	}
	for _, cluster := range o.Clusters {
		for _, node := range cluster.Nodes {
			if node.State == edgeNodePending || node.State == edgeNodeMissing {
				return false
			}
		}
	}
	return true
}

// evaluateEdgeNode classifies one node against its cluster's expectation. A
// node counts as converged only when it reports every expected component at
// the target version; a phase that failed for this target is a failure even
// when some components already moved.
func evaluateEdgeNode(exp edgeRolloutExpectation, status *foghorncontrolpb.NodeUpdateStatus) edgeNodeRollout {
	node := edgeNodeRollout{
		ClusterID: exp.ClusterID,
		NodeID:    status.GetNodeId(),
		Phase:     status.GetPhase(),
		LastError: status.GetLastError(),
		Deadline:  status.GetPhaseDeadline(),
	}
	reported := map[string]string{}
	for _, cv := range status.GetComponentVersions() {
		reported[cv.GetComponent()] = cv.GetVersion()
	}
	components := make([]string, 0, len(exp.Components))
	for component := range exp.Components {
		components = append(components, component)
	}
	sort.Strings(components)
	for _, component := range components {
		want := exp.Components[component]
		if got := reported[component]; got != want {
			node.Behind = append(node.Behind, fmt.Sprintf("%s %s->%s", component, dashIfEmpty(got), want))
		}
	}
	switch {
	case status.GetPhase() == "failed" && status.GetTargetRelease() == exp.TargetRelease:
		node.State = edgeNodeFailed
	case len(node.Behind) == 0:
		node.State = edgeNodeConverged
	case !status.GetAutomaticUpdates():
		node.State = edgeNodeUnmanaged
	case exp.Paused:
		node.State = edgeNodePaused
	default:
		node.State = edgeNodePending
	}
	return node
}

func evaluateEdgeCluster(ctx context.Context, src edgeRolloutSource, clusterID string) edgeClusterRollout {
	exp, err := src.Expectation(ctx, clusterID)
	if err != nil {
		return edgeClusterRollout{Expectation: edgeRolloutExpectation{ClusterID: clusterID}, Err: err}
	}
	result := edgeClusterRollout{Expectation: exp}
	statuses, err := src.NodeStatuses(ctx, clusterID)
	if err != nil {
		result.Err = fmt.Errorf("read node update state from Foghorn: %w", err)
		return result
	}
	enrolled, err := src.EnrolledEdges(ctx, clusterID)
	if err != nil {
		result.Err = fmt.Errorf("list enrolled edges from Quartermaster: %w", err)
		return result
	}
	seen := map[string]bool{}
	for _, status := range statuses {
		if status == nil {
			continue
		}
		seen[status.GetNodeId()] = true
		result.Nodes = append(result.Nodes, evaluateEdgeNode(exp, status))
	}
	// Foghorn forgets a node that stays disconnected past its eviction grace,
	// so an edge whose new Helmsman never came back can be absent from its
	// report entirely. Quartermaster's enrollment is the expected set.
	for _, nodeID := range enrolled {
		if seen[nodeID] {
			continue
		}
		result.Nodes = append(result.Nodes, edgeNodeRollout{ClusterID: clusterID, NodeID: nodeID, State: edgeNodeMissing})
	}
	sort.Slice(result.Nodes, func(i, j int) bool { return result.Nodes[i].NodeID < result.Nodes[j].NodeID })
	return result
}

func evaluateEdgeRollout(ctx context.Context, src edgeRolloutSource, clusterIDs []string) edgeRolloutOutcome {
	outcome := edgeRolloutOutcome{}
	for _, clusterID := range clusterIDs {
		outcome.Clusters = append(outcome.Clusters, evaluateEdgeCluster(ctx, src, clusterID))
	}
	return outcome
}

// waitEdgeRollout polls until every node is converged or settled in a state
// waiting cannot change, a node fails, or wait passes. A zero wait reads once.
func waitEdgeRollout(ctx context.Context, w io.Writer, src edgeRolloutSource, clusterIDs []string, wait, interval time.Duration) edgeRolloutOutcome {
	deadline := time.Now().Add(wait)
	for {
		outcome := evaluateEdgeRollout(ctx, src, clusterIDs)
		if outcome.Converged() || outcome.settled() || outcome.count(edgeNodeFailed) > 0 || !time.Now().Before(deadline) || ctx.Err() != nil {
			return outcome
		}
		_, _ = fmt.Fprintf(w, "  waiting for edges: %d converged, %d not converged, %d failed (up to %s more)\n",
			outcome.count(edgeNodeConverged), outcome.count(edgeNodePending)+outcome.count(edgeNodeMissing), outcome.count(edgeNodeFailed), time.Until(deadline).Round(time.Second))
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return evaluateEdgeRollout(context.WithoutCancel(ctx), src, clusterIDs)
		case <-timer.C:
		}
	}
}

func renderEdgeRollout(w io.Writer, outcome edgeRolloutOutcome) {
	for _, cluster := range outcome.Clusters {
		exp := cluster.Expectation
		if cluster.Err != nil {
			ux.Warn(w, fmt.Sprintf("cluster %s: edge rollout state unknown: %v", exp.ClusterID, cluster.Err))
			continue
		}
		_, _ = fmt.Fprintf(w, "  cluster %s target=%s%s\n", exp.ClusterID, dashIfEmpty(exp.TargetRelease), pausedSuffix(exp.Paused))
		if len(cluster.Nodes) == 0 {
			_, _ = fmt.Fprintln(w, "    no edge nodes")
		}
		for _, node := range cluster.Nodes {
			line := fmt.Sprintf("    %s  %s", node.NodeID, node.State)
			if node.Phase != "" {
				line += " phase=" + node.Phase
			}
			if node.Deadline != "" && node.State == edgeNodePending {
				line += " deadline=" + node.Deadline
			}
			if len(node.Behind) > 0 {
				line += " behind=[" + strings.Join(node.Behind, ", ") + "]"
			}
			if node.LastError != "" {
				line += fmt.Sprintf(" last_error=%q", node.LastError)
			}
			_, _ = fmt.Fprintln(w, line)
		}
	}
}

func pausedSuffix(paused bool) string {
	if paused {
		return " (paused)"
	}
	return ""
}

// reportEdgeRollout renders the outcome and its verdict. It returns an error
// only when a node failed its update; an unconverged fleet is a warning that
// names the command to follow up with.
func reportEdgeRollout(w io.Writer, outcome edgeRolloutOutcome, waited time.Duration) error {
	renderEdgeRollout(w, outcome)
	failed := outcome.count(edgeNodeFailed)
	if failed > 0 {
		ux.Fail(w, fmt.Sprintf("Edge rollout failed on %d node(s); inspect with `%s` and the Foghorn logs", failed, edgeRolloutStatusCommand))
		return fmt.Errorf("edge rollout failed on %d node(s)", failed)
	}
	if outcome.Converged() {
		ux.Success(w, "All edges run the target release")
		return nil
	}
	notConverged := outcome.count(edgeNodePending) + outcome.count(edgeNodeMissing) + outcome.count(edgeNodeUnmanaged) + outcome.count(edgeNodePaused)
	detail := fmt.Sprintf("%d edge node(s) have not converged", notConverged)
	if unreadable := outcome.unreadable(); unreadable > 0 {
		detail += fmt.Sprintf(" and %d cluster(s) could not be read", unreadable)
	}
	if waited > 0 {
		detail += fmt.Sprintf(" after waiting %s", waited)
	}
	ux.Warn(w, fmt.Sprintf("EDGES NOT CONVERGED: %s. Follow up with `%s --wait 15m`.", detail, edgeRolloutStatusCommand))
	return nil
}

// edgeReleaseExpectation turns a cluster release target and its catalog
// release into the versions every edge of the cluster must report. The
// reconciler applies the first release ListEdgeReleases returns for the
// target's channel and version; this reads the same one.
func edgeReleaseExpectation(ctx context.Context, qm edgeReleaseQMClient, clusterID string) (edgeRolloutExpectation, error) {
	target, err := existingReleaseTarget(ctx, qm, clusterID)
	if err != nil {
		return edgeRolloutExpectation{}, err
	}
	if target == nil {
		return edgeRolloutExpectation{}, fmt.Errorf("cluster %s has no edge release target", clusterID)
	}
	releases, err := qm.ListEdgeReleases(ctx, &quartermasterpb.ListEdgeReleasesRequest{
		Channel: strings.TrimSpace(target.GetChannel()),
		Version: strings.TrimSpace(target.GetTargetVersion()),
	})
	if err != nil {
		return edgeRolloutExpectation{}, fmt.Errorf("list edge releases: %w", err)
	}
	if len(releases.GetReleases()) == 0 {
		return edgeRolloutExpectation{}, fmt.Errorf("edge release %s/%s is not published", target.GetChannel(), firstNonEmpty(target.GetTargetVersion(), "latest"))
	}
	release := releases.GetReleases()[0]
	var components map[string]struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(release.GetComponentsJson()), &components); err != nil {
		return edgeRolloutExpectation{}, fmt.Errorf("parse edge release %s/%s components: %w", release.GetChannel(), release.GetVersion(), err)
	}
	exp := edgeRolloutExpectation{
		ClusterID:     clusterID,
		TargetRelease: release.GetChannel() + ":" + release.GetVersion(),
		Paused:        target.GetPaused(),
		Components:    map[string]string{},
	}
	for name, component := range components {
		if name == edgeRolloutComponentSchema || strings.TrimSpace(component.Version) == "" {
			continue
		}
		exp.Components[name] = strings.TrimSpace(component.Version)
	}
	return exp, nil
}

// liveEdgeRolloutSource reads Quartermaster through one client and each
// cluster's Foghorn through the lifecycle resolver.
type liveEdgeRolloutSource struct {
	qm       *qmclient.GRPCClient
	qmCtx    fwcfg.Context
	fhCtx    fwcfg.Context
	resolver *controlplane.Resolver
	foghorns map[string]*fhclient.GRPCClient
}

func (s *liveEdgeRolloutSource) Close() {
	for _, fh := range s.foghorns {
		_ = fh.Close()
	}
}

func (s *liveEdgeRolloutSource) Expectation(ctx context.Context, clusterID string) (edgeRolloutExpectation, error) {
	cctx, cancel := clusterNodesRPCContext(ctx, s.qmCtx, edgeRolloutRPCTimeout)
	defer cancel()
	return edgeReleaseExpectation(cctx, s.qm, clusterID)
}

func (s *liveEdgeRolloutSource) EnrolledEdges(ctx context.Context, clusterID string) ([]string, error) {
	cctx, cancel := clusterNodesRPCContext(ctx, s.qmCtx, edgeRolloutRPCTimeout)
	defer cancel()
	resp, err := s.qm.ListNodes(cctx, clusterID, "edge", "", nil)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, node := range resp.GetNodes() {
		if node == nil || node.GetClusterId() != clusterID {
			continue
		}
		if status := strings.TrimSpace(node.GetStatus()); status != "" && status != "active" {
			continue
		}
		out = append(out, node.GetNodeId())
	}
	return out, nil
}

func (s *liveEdgeRolloutSource) NodeStatuses(ctx context.Context, clusterID string) ([]*foghorncontrolpb.NodeUpdateStatus, error) {
	fh, ok := s.foghorns[clusterID]
	if !ok {
		var err error
		fh, err = clusterNodesFoghornClient(ctx, s.resolver, s.fhCtx, clusterID)
		if err != nil {
			return nil, err
		}
		s.foghorns[clusterID] = fh
	}
	cctx, cancel := clusterNodesRPCContext(ctx, s.fhCtx, edgeRolloutRPCTimeout)
	defer cancel()
	resp, err := fh.ListNodeUpdateStatus(cctx, clusterID)
	if err != nil {
		return nil, err
	}
	return resp.GetNodes(), nil
}

// openEdgeRolloutSource connects the rollout check for a manifest-selected
// cluster: Quartermaster as edge release target sync reaches it, and Foghorn
// through the lifecycle resolver.
func openEdgeRolloutSource(cmd *cobra.Command, rc *resolvedCluster) (*liveEdgeRolloutSource, func(), error) {
	qm, qmCtx, qmCleanup, err := edgeReleaseQMClientForGitOpsSync(cmd, rc, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("connect Quartermaster: %w", err)
	}
	fhCtx, resolver, fhCleanup, err := clusterLifecycleAccess(cmd)
	if err != nil {
		_ = qm.Close()
		qmCleanup()
		return nil, nil, fmt.Errorf("resolve Foghorn access: %w", err)
	}
	src := &liveEdgeRolloutSource{qm: qm, qmCtx: qmCtx, fhCtx: fhCtx, resolver: resolver, foghorns: map[string]*fhclient.GRPCClient{}}
	return src, func() {
		src.Close()
		fhCleanup()
		_ = qm.Close()
		qmCleanup()
	}, nil
}

// releaseVerifyEdgeRolloutFn waits for the edges after `release apply` synced
// their target. It reports whether every edge converged; the error is non-nil
// only when a node failed its update. Tests substitute it.
var releaseVerifyEdgeRolloutFn = verifyReleaseEdgeRollout

func verifyReleaseEdgeRollout(cmd *cobra.Command, rc *resolvedCluster, wait time.Duration) (bool, error) {
	out := cmd.OutOrStdout()
	if rc == nil || rc.Manifest == nil {
		return false, nil
	}
	src, cleanup, err := openEdgeRolloutSource(cmd, rc)
	if err != nil {
		ux.Warn(out, fmt.Sprintf("EDGES NOT VERIFIED: %v. Check with `%s --wait 15m`.", err, edgeRolloutStatusCommand))
		return false, nil
	}
	defer cleanup()
	outcome := waitEdgeRollout(cmd.Context(), out, src, rc.Manifest.AllClusterIDs(), wait, edgeRolloutPollInterval)
	if err := reportEdgeRollout(out, outcome, wait); err != nil {
		return false, err
	}
	return outcome.Converged(), nil
}

func newClusterEdgesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "edges",
		Short: "Inspect the cluster's edge fleet",
	}
	rollout := &cobra.Command{
		Use:   "rollout",
		Short: "Inspect edge release rollouts",
	}
	rollout.AddCommand(newClusterEdgesRolloutStatusCmd())
	cmd.AddCommand(rollout)
	return cmd
}

func newClusterEdgesRolloutStatusCmd() *cobra.Command {
	var clusterID string
	var wait time.Duration
	var interval time.Duration
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show whether every edge runs its cluster's release target",
		Long: `Compare each edge node's reported component versions with its cluster's
edge release target and print one line per node: converged, not converged,
failed (with the reconciler's last error), or absent from Foghorn.

With --wait, poll until every edge converges, a node fails, or the wait
passes. Exits non-zero only when a node's update failed.`,
		Example: `  frameworks cluster edges rollout status
  frameworks cluster edges rollout status --cluster-id media-eu --wait 15m`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			clusterIDs := rc.Manifest.AllClusterIDs()
			if id := strings.TrimSpace(clusterID); id != "" {
				clusterIDs = []string{id}
			}
			src, cleanup, err := openEdgeRolloutSource(cmd, rc)
			if err != nil {
				return err
			}
			defer cleanup()
			outcome := waitEdgeRollout(cmd.Context(), cmd.OutOrStdout(), src, clusterIDs, wait, interval)
			return reportEdgeRollout(cmd.OutOrStdout(), outcome, wait)
		},
	}
	cmd.Flags().StringVar(&clusterID, "cluster-id", "", "only this cluster (defaults to every manifest cluster)")
	cmd.Flags().DurationVar(&wait, "wait", 0, "poll until converged, a node fails, or this long passes (0 reads once)")
	cmd.Flags().DurationVar(&interval, "interval", edgeRolloutPollInterval, "poll interval with --wait")
	return cmd
}
