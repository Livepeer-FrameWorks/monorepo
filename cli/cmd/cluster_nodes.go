package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/internal/controlplane"
	fwcredentials "frameworks/cli/internal/credentials"
	"frameworks/cli/internal/platformauth"
	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/gitops"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/provisioner"
	fwssh "frameworks/cli/pkg/ssh"
	fhclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const defaultClusterNodeDrainDeadline = 4 * time.Hour

type edgeProbeAction string

const (
	edgeProbeActionDeploy       edgeProbeAction = "deploy"
	edgeProbeActionAlreadyAdded edgeProbeAction = "already-added"
)

func newClusterNodesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "nodes",
		Short: "Manage edge nodes in the active cluster",
		Long: `Manage edge nodes in the active cluster.

This is the lifecycle surface for operators who manage edge nodes in clusters
registered with the FrameWorks control plane. Platform contexts use service-token
control-plane access. BYO edge and hosted user contexts use Bridge workflows,
not direct node lifecycle changes.`,
	}
	cmd.AddCommand(newClusterNodesAddCmd())
	cmd.AddCommand(newClusterNodesListCmd())
	cmd.AddCommand(newClusterNodesModeCmd("drain", "draining", "Cordon a node and let existing sessions finish"))
	cmd.AddCommand(newClusterNodesModeCmd("resume", "normal", "Return a drained node to normal routing"))
	cmd.AddCommand(newClusterNodesFenceCmd("remove", "Gracefully take a node out of service"))
	cmd.AddCommand(newClusterNodesFenceCmd("evict", "Immediately fence a node out of service"))
	return cmd
}

func newClusterNodesAddCmd() *cobra.Command {
	var (
		clusterID     string
		nodeName      string
		sshTarget     string
		sshKey        string
		mode          string
		email         string
		version       string
		tokenTTL      string
		timeout       time.Duration
		applyTuning   bool
		skipPreflight bool
		skipProbe     bool
		forceReapply  bool
	)

	cmd := &cobra.Command{
		Use:   "add --ssh user@host",
		Short: "Add an edge node to the active cluster",
		Long: `Add an edge node to an existing cluster.

The command requires an existing cluster from --cluster-id or the active
context. It mints a short-lived enrollment token through Quartermaster, pipes
it into the existing edge deploy pipeline, and never prints the token.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctxCfg, err := loadActiveContextLax()
			if err != nil {
				return err
			}
			if guardErr := requireClusterLifecycleContext(ctxCfg); guardErr != nil {
				return guardErr
			}
			if clusterID == "" {
				clusterID = ctxCfg.ClusterID
				if clusterID != "" {
					ux.ContextNotice(cmd.OutOrStdout(), "cluster", clusterID)
				}
			}
			if strings.TrimSpace(clusterID) == "" {
				selectedCluster, selectErr := promptSelectCluster(cmd)
				if selectErr != nil {
					return selectErr
				}
				clusterID = selectedCluster.GetClusterId()
			}
			if strings.TrimSpace(sshTarget) == "" {
				return fmt.Errorf("--ssh is required for cluster node expansion")
			}
			if tokenTTL != "" {
				normalizedTTL, ttlErr := normalizeDuration(tokenTTL)
				if ttlErr != nil {
					return fmt.Errorf("--token-ttl: %w", ttlErr)
				}
				tokenTTL = normalizedTTL
			}

			versionExplicit := cmd.Flags().Changed("version")
			installVersion := version
			if !versionExplicit {
				resolvedVersion, versionErr := resolveClusterNodeInstallVersion(cmd, clusterID, version)
				if versionErr != nil {
					return versionErr
				}
				installVersion = resolvedVersion
			}

			probeAction := edgeProbeActionDeploy
			probeTargetVersion := ""
			if versionExplicit {
				probeTargetVersion = version
			}
			if !skipProbe {
				probedAction, probeErr := probeEdgeTarget(cmd, clusterID, sshTarget, sshKey, forceReapply, probeTargetVersion, installVersion)
				if probeErr != nil {
					return probeErr
				}
				probeAction = probedAction
			}
			if probeAction == edgeProbeActionAlreadyAdded {
				ux.PrintNextSteps(cmd.OutOrStdout(), []ux.NextStep{
					{Cmd: "frameworks cluster nodes list", Why: "Review the existing node registration and routing mode."},
					{Cmd: fmt.Sprintf("frameworks edge status --ssh %s", sshTarget), Why: "Check services directly on the node."},
				})
				return nil
			}

			ux.Heading(cmd.OutOrStdout(), fmt.Sprintf("Adding edge node to cluster %s", clusterID))
			cfg := deployConfig{
				clusterID:          clusterID,
				nodeName:           nodeName,
				sshTarget:          sshTarget,
				sshKey:             sshKey,
				mode:               mode,
				email:              email,
				applyTuning:        applyTuning,
				skipPreflight:      skipPreflight,
				version:            installVersion,
				timeout:            timeout,
				enrollmentTokenTTL: tokenTTL,
			}

			var nodeID, domain, clusterSlug string
			token, tokenErr := createClusterNodeEnrollmentToken(cmd, clusterID, nodeName, tokenTTL)
			if tokenErr != nil {
				return tokenErr
			}
			cfg.enrollmentToken = token
			err = runEdgeDeploy(cmd.Context(), cmd, ctxCfg, cfg, &nodeID, &domain, &clusterSlug)
			renderEdgeDeployResult(cmd, edgeDeployResultFields{
				modeA:         true,
				bridgeCreated: false,
				nodeID:        nodeID,
				domain:        domain,
				clusterSlug:   clusterSlug,
				provisioned:   err == nil,
				failed:        err,
			})
			if err != nil {
				return err
			}
			ux.PrintNextSteps(cmd.OutOrStdout(), []ux.NextStep{
				{Cmd: "frameworks cluster nodes list", Why: "Review node registration and current routing mode."},
				{Cmd: fmt.Sprintf("frameworks edge status --ssh %s", sshTarget), Why: "Check services directly on the node."},
			})
			return nil
		},
	}

	cmd.Flags().StringVar(&clusterID, "cluster-id", "", "cluster to expand (defaults to active context cluster_id)")
	cmd.Flags().StringVar(&nodeName, "node-name", "", "preferred node name/id")
	cmd.Flags().StringVar(&sshTarget, "ssh", "", "SSH target (user@host)")
	cmd.Flags().StringVar(&sshKey, "ssh-key", "", "SSH private key path")
	cmd.Flags().StringVar(&mode, "mode", "native", "deployment mode: native or docker")
	cmd.Flags().StringVar(&email, "email", "", "ACME email for TLS certificates")
	cmd.Flags().StringVar(&version, "version", "stable", "platform version for binary resolution")
	cmd.Flags().StringVar(&tokenTTL, "token-ttl", "5m", "short-lived enrollment token TTL")
	cmd.Flags().DurationVar(&timeout, "timeout", 3*time.Minute, "HTTPS verification timeout")
	cmd.Flags().BoolVar(&applyTuning, "tune", true, "apply sysctl/limits tuning")
	cmd.Flags().BoolVar(&skipPreflight, "skip-preflight", false, "skip deploy preflight checks")
	cmd.Flags().BoolVar(&skipProbe, "skip-probe", false, "skip the existing-edge state probe")
	cmd.Flags().BoolVar(&forceReapply, "force-reapply", false, "allow deployment over an existing detected edge stack")
	return cmd
}

func resolveClusterNodeInstallVersion(cmd *cobra.Command, clusterID, fallbackVersion string) (string, error) {
	qm, ctxCfg, cleanup, err := clusterNodesQMClientFromContext(cmd.Context())
	if err != nil {
		return "", err
	}
	defer cleanup()
	defer func() { _ = qm.Close() }()

	cctx, cancel := clusterNodesRPCContext(cmd.Context(), ctxCfg, 10*time.Second)
	defer cancel()
	resp, err := qm.GetClusterReleaseTarget(cctx, &pb.GetClusterReleaseTargetRequest{ClusterId: clusterID})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return fallbackVersion, nil
		}
		return "", fmt.Errorf("load cluster release target: %w", err)
	}
	target := resp.GetTarget()
	if target == nil {
		return fallbackVersion, nil
	}
	if version := strings.TrimSpace(target.GetTargetVersion()); version != "" {
		return version, nil
	}
	if channel := strings.TrimSpace(target.GetChannel()); channel != "" {
		return channel, nil
	}
	return fallbackVersion, nil
}

func createClusterNodeEnrollmentToken(cmd *cobra.Command, clusterID, nodeName, tokenTTL string) (string, error) {
	qm, ctxCfg, cleanup, err := clusterNodesQMClientFromContext(cmd.Context())
	if err != nil {
		return "", err
	}
	defer cleanup()
	defer func() { _ = qm.Close() }()

	tokenName := "cluster nodes add"
	if strings.TrimSpace(nodeName) != "" {
		tokenName = "cluster nodes add: " + strings.TrimSpace(nodeName)
	}
	req := &pb.CreateEnrollmentTokenRequest{
		ClusterId: clusterID,
		Name:      &tokenName,
	}
	if clusterNodesUseServiceAuth(ctxCfg) {
		tenantID := ctxCfg.SystemTenantID
		if tenantID == "" {
			return "", fmt.Errorf("platform context %q has no system_tenant_id for enrollment token minting", ctxCfg.Name)
		}
		req.TenantId = &tenantID
	}
	if strings.TrimSpace(tokenTTL) != "" {
		req.Ttl = &tokenTTL
	}
	cctx, cancel := clusterNodesRPCContext(cmd.Context(), ctxCfg, 15*time.Second)
	defer cancel()
	resp, err := qm.CreateEnrollmentToken(cctx, req)
	if err != nil {
		return "", err
	}
	token := resp.GetToken().GetToken()
	if token == "" {
		return "", fmt.Errorf("quartermaster returned an empty enrollment token")
	}
	return token, nil
}

func probeEdgeTarget(cmd *cobra.Command, clusterID, sshTarget, sshKey string, forceReapply bool, targetVersion, fallbackVersion string) (edgeProbeAction, error) {
	host := sshTargetToHost(sshTarget)
	pool := fwssh.NewPool(30*time.Second, sshKey)
	defer func() { _ = pool.Close() }()
	ep := provisioner.NewEdgeProvisioner(pool)
	state, err := ep.Detect(cmd.Context(), host)
	if err != nil {
		return edgeProbeActionDeploy, fmt.Errorf("probe existing edge state: %w", err)
	}
	envState, err := readRemoteEdgeEnv(cmd.Context(), ep, host)
	if err != nil {
		return edgeProbeActionDeploy, fmt.Errorf("probe existing edge environment: %w", err)
	}
	if (state == nil || !state.Exists) && envState.File == "" {
		ux.Result(cmd.OutOrStdout(), []ux.ResultField{{Key: "probe", OK: true, Detail: "clean edge target"}})
		return edgeProbeActionDeploy, nil
	}
	if envState.ClusterID != "" {
		if envState.ClusterID != clusterID {
			return edgeProbeActionDeploy, fmt.Errorf("target belongs to cluster %s, not %s; reclaim is intentionally not implicit", envState.ClusterID, clusterID)
		}
		if state == nil || !state.Exists {
			if !forceReapply {
				return edgeProbeActionDeploy, fmt.Errorf("target has cluster marker %s but no detectable edge stack; pass --force-reapply only after confirming ownership", envState.File)
			}
			ux.Result(cmd.OutOrStdout(), []ux.ResultField{{Key: "probe", OK: true, Detail: "same-cluster marker without detected stack, reapply forced"}})
			return edgeProbeActionDeploy, nil
		}
		if envState.NodeID == "" {
			if !forceReapply {
				return edgeProbeActionDeploy, fmt.Errorf("target has cluster marker %s but no node marker; pass --force-reapply only after confirming ownership", envState.File)
			}
			ux.Result(cmd.OutOrStdout(), []ux.ResultField{{Key: "probe", OK: true, Detail: "same-cluster marker without node marker, reapply forced"}})
			return edgeProbeActionDeploy, nil
		}
		if err := verifyExistingClusterNode(cmd, clusterID, envState.NodeID, targetVersion, fallbackVersion); err != nil {
			if !forceReapply {
				return edgeProbeActionDeploy, err
			}
			ux.Result(cmd.OutOrStdout(), []ux.ResultField{{Key: "probe", OK: true, Detail: "same-cluster marker not current, reapply forced"}})
			return edgeProbeActionDeploy, nil
		}
		detail := "same-cluster edge stack"
		if envState.NodeID != "" {
			detail = fmt.Sprintf("same-cluster edge stack node=%s", envState.NodeID)
		}
		ux.Result(cmd.OutOrStdout(), []ux.ResultField{{Key: "probe", OK: true, Detail: detail}})
		return edgeProbeActionAlreadyAdded, nil
	}
	if envState.HasFrameworksEnv() && !forceReapply {
		return edgeProbeActionDeploy, fmt.Errorf("target has Frameworks edge environment without cluster marker (%s); pass --force-reapply only after confirming ownership", envState.File)
	}
	if state == nil || !state.Exists {
		ux.Result(cmd.OutOrStdout(), []ux.ResultField{{Key: "probe", OK: true, Detail: "unmarked edge env found, reapply forced"}})
		return edgeProbeActionDeploy, nil
	}
	mode := state.Metadata["mode"]
	if mode == "" {
		mode = "unknown"
	}
	if !forceReapply {
		return edgeProbeActionDeploy, fmt.Errorf("target already has a Frameworks edge stack (%s) but no cluster marker; pass --force-reapply only after confirming ownership", mode)
	}
	ux.Result(cmd.OutOrStdout(), []ux.ResultField{{Key: "probe", OK: true, Detail: fmt.Sprintf("existing edge stack (%s), reapply forced", mode)}})
	return edgeProbeActionDeploy, nil
}

func verifyExistingClusterNode(cmd *cobra.Command, clusterID, nodeID, targetVersion, fallbackVersion string) error {
	qm, ctxCfg, cleanup, err := clusterNodesQMClientFromContext(cmd.Context())
	if err != nil {
		return err
	}
	defer cleanup()
	defer func() { _ = qm.Close() }()

	cctx, cancel := clusterNodesRPCContext(cmd.Context(), ctxCfg, 15*time.Second)
	resp, err := qm.ListNodes(cctx, clusterID, "edge", "", nil)
	cancel()
	if err != nil {
		return fmt.Errorf("verify existing node registration: %w", err)
	}
	var registered *pb.InfrastructureNode
	for _, node := range resp.GetNodes() {
		if node == nil {
			continue
		}
		if node.GetNodeId() == nodeID || node.GetNodeName() == nodeID || node.GetId() == nodeID {
			registered = node
			break
		}
	}
	if registered == nil {
		return fmt.Errorf("target has same-cluster marker node=%s but Quartermaster has no active edge node registration; pass --force-reapply only after confirming ownership", nodeID)
	}
	if registered.GetStatus() != "" && registered.GetStatus() != "active" {
		return fmt.Errorf("target node %s is registered with status=%s; re-add is not treated as current", nodeID, registered.GetStatus())
	}

	fh, fhCtxCfg, fhCleanup, err := clusterNodesFoghornClientFromContext(cmd.Context())
	if err != nil {
		return fmt.Errorf("verify existing node health: %w", err)
	}
	defer fhCleanup()
	defer func() { _ = fh.Close() }()
	hctx, hcancel := clusterNodesRPCContext(cmd.Context(), fhCtxCfg, 5*time.Second)
	health, _, err := fh.GetNodeHealth(hctx, &pb.GetNodeHealthRequest{NodeId: registered.GetNodeId()})
	hcancel()
	if err != nil {
		return fmt.Errorf("same-cluster node %s is registered but not reporting health through Foghorn: %w", registered.GetNodeId(), err)
	}
	if desired, err := desiredEdgeComponentVersionsForExistingNode(cmd.Context(), qm, ctxCfg, clusterID, targetVersion, fallbackVersion); err != nil {
		return err
	} else if len(desired) > 0 {
		reported := map[string]string{}
		for _, component := range health.GetComponentVersions() {
			if component != nil && strings.TrimSpace(component.GetComponent()) != "" {
				reported[component.GetComponent()] = strings.TrimSpace(component.GetVersion())
			}
		}
		var stale []string
		for component, target := range desired {
			if component == "config_schema" {
				continue
			}
			if target == "" {
				continue
			}
			current := reported[component]
			if current == "" || current != target {
				stale = append(stale, fmt.Sprintf("%s current=%s target=%s", component, firstNonEmpty(current, "unknown"), target))
			}
		}
		if len(stale) > 0 {
			sort.Strings(stale)
			return fmt.Errorf("same-cluster node %s is stale (%s); re-run with --force-reapply or use the release updater", registered.GetNodeId(), strings.Join(stale, ", "))
		}
	}
	ux.Result(cmd.OutOrStdout(), []ux.ResultField{{
		Key:    "probe",
		OK:     true,
		Detail: fmt.Sprintf("same-cluster registered node=%s mode=%s streams=%d", registered.GetNodeId(), health.GetOperationalMode(), health.GetActiveStreams()),
	}})
	return nil
}

func desiredEdgeComponentVersionsForExistingNode(ctx context.Context, qm *qmclient.GRPCClient, ctxCfg fwcfg.Context, clusterID, targetVersion, fallbackVersion string) (map[string]string, error) {
	if strings.TrimSpace(targetVersion) != "" {
		return desiredEdgeComponentVersions(targetVersion)
	}
	cctx, cancel := clusterNodesRPCContext(ctx, ctxCfg, 10*time.Second)
	defer cancel()
	targetResp, err := qm.GetClusterReleaseTarget(cctx, &pb.GetClusterReleaseTargetRequest{ClusterId: clusterID})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			if strings.TrimSpace(fallbackVersion) != "" {
				return desiredEdgeComponentVersions(fallbackVersion)
			}
			return nil, nil
		}
		return nil, fmt.Errorf("load cluster release target: %w", err)
	}
	if targetResp.GetTarget() == nil {
		if strings.TrimSpace(fallbackVersion) != "" {
			return desiredEdgeComponentVersions(fallbackVersion)
		}
		return nil, nil
	}
	target := targetResp.GetTarget()
	releaseReq := &pb.ListEdgeReleasesRequest{
		Channel: strings.TrimSpace(target.GetChannel()),
		Version: strings.TrimSpace(target.GetTargetVersion()),
	}
	cctx, cancel = clusterNodesRPCContext(ctx, ctxCfg, 10*time.Second)
	defer cancel()
	releases, err := qm.ListEdgeReleases(cctx, releaseReq)
	if err != nil {
		return nil, fmt.Errorf("load edge release: %w", err)
	}
	if len(releases.GetReleases()) == 0 {
		return nil, fmt.Errorf("cluster release target %s:%s has no published release", releaseReq.GetChannel(), releaseReq.GetVersion())
	}
	return desiredEdgeComponentVersionsFromJSON(releases.GetReleases()[0].GetComponentsJson())
}

func desiredEdgeComponentVersions(targetVersion string) (map[string]string, error) {
	if strings.TrimSpace(targetVersion) == "" {
		return nil, nil
	}
	channel, resolved := gitops.ResolveVersion(targetVersion)
	fetcher, err := gitops.NewFetcher(gitops.FetchOptions{})
	if err != nil {
		return nil, fmt.Errorf("resolve target release: %w", err)
	}
	manifest, err := fetcher.Fetch(channel, resolved)
	if err != nil {
		return nil, fmt.Errorf("fetch target release %s/%s: %w", channel, resolved, err)
	}
	desired := map[string]string{}
	if info, err := manifest.GetServiceInfo("helmsman"); err == nil {
		desired["helmsman"] = strings.TrimSpace(info.Version)
	}
	if dep := manifest.GetExternalDependency("mistserver"); dep != nil {
		desired["mist"] = firstNonEmpty(strings.TrimSpace(dep.ReleaseTag), strings.TrimSpace(dep.Digest), strings.TrimSpace(dep.ReleaseURL), strings.TrimSpace(dep.Image))
	}
	if dep := manifest.GetExternalDependency("caddy"); dep != nil {
		desired["caddy"] = firstNonEmpty(strings.TrimSpace(dep.ReleaseTag), strings.TrimSpace(dep.Digest), strings.TrimSpace(dep.ReleaseURL), strings.TrimSpace(dep.Image))
	}
	if desired["caddy"] == "" {
		if info, err := manifest.GetServiceInfo("caddy"); err == nil {
			desired["caddy"] = strings.TrimSpace(info.Version)
		}
	}
	return desired, nil
}

func desiredEdgeComponentVersionsFromJSON(raw string) (map[string]string, error) {
	var components map[string]struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(raw), &components); err != nil {
		return nil, fmt.Errorf("parse target release components: %w", err)
	}
	desired := make(map[string]string, len(components))
	for component, spec := range components {
		if component == "config_schema" {
			continue
		}
		desired[component] = strings.TrimSpace(spec.Version)
	}
	return desired, nil
}

type remoteEdgeEnv struct {
	File       string
	ClusterID  string
	NodeID     string
	DeployMode string
	Domain     string
	Foghorn    string
}

func (s remoteEdgeEnv) HasFrameworksEnv() bool {
	return s.File != "" || s.NodeID != "" || s.DeployMode != "" || s.Domain != "" || s.Foghorn != ""
}

func readRemoteEdgeEnv(ctx context.Context, ep *provisioner.EdgeProvisioner, host inventory.Host) (remoteEdgeEnv, error) {
	command := `found=0
for f in /opt/frameworks/edge/.edge.env /etc/frameworks/helmsman.env /usr/local/etc/frameworks/helmsman.env "$HOME/.config/frameworks/helmsman.env"; do
  if [ -r "$f" ]; then
    echo "__FRAMEWORKS_ENV_FILE=$f"
    grep -E '^(CLUSTER_ID|NODE_ID|DEPLOY_MODE|EDGE_DOMAIN|FOGHORN_CONTROL_ADDR)=' "$f" || true
    found=1
    break
  fi
done
if [ "$found" = 0 ]; then echo "__FRAMEWORKS_ENV_FILE="; fi`
	result, err := ep.RunCommand(ctx, host, command)
	if err != nil {
		return remoteEdgeEnv{}, err
	}
	out := remoteEdgeEnv{}
	for _, line := range strings.Split(result.Stdout, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "__FRAMEWORKS_ENV_FILE":
			out.File = strings.TrimSpace(value)
		case "CLUSTER_ID":
			out.ClusterID = strings.TrimSpace(value)
		case "NODE_ID":
			out.NodeID = strings.TrimSpace(value)
		case "DEPLOY_MODE":
			out.DeployMode = strings.TrimSpace(value)
		case "EDGE_DOMAIN":
			out.Domain = strings.TrimSpace(value)
		case "FOGHORN_CONTROL_ADDR":
			out.Foghorn = strings.TrimSpace(value)
		}
	}
	return out, nil
}

func promptSelectCluster(cmd *cobra.Command) (*pb.InfrastructureCluster, error) {
	if !isatty.IsTerminal(os.Stdin.Fd()) {
		return nil, fmt.Errorf("--cluster-id is required when interactive cluster selection is unavailable")
	}
	qm, ctxCfg, cleanup, err := clusterNodesQMClientFromContext(cmd.Context())
	if err != nil {
		return nil, err
	}
	defer cleanup()
	defer func() { _ = qm.Close() }()
	cctx, cancel := clusterNodesRPCContext(cmd.Context(), ctxCfg, 15*time.Second)
	defer cancel()
	resp, err := qm.ListClusters(cctx, nil)
	if err != nil {
		return nil, err
	}
	clusters := resp.GetClusters()
	if len(clusters) == 0 {
		return nil, fmt.Errorf("no clusters available to the active context")
	}
	sort.SliceStable(clusters, func(i, j int) bool {
		return clusterDisplayName(clusters[i]) < clusterDisplayName(clusters[j])
	})
	_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Select cluster:")
	for i, cluster := range clusters {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %d. %s\n", i+1, clusterDisplayName(cluster))
	}
	_, _ = fmt.Fprint(cmd.OutOrStdout(), "> ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("read selection: %w", err)
	}
	idx, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || idx < 1 || idx > len(clusters) {
		return nil, fmt.Errorf("invalid cluster selection")
	}
	selected := clusters[idx-1]
	ux.ContextNotice(cmd.OutOrStdout(), "cluster", selected.GetClusterId())
	return selected, nil
}

func clusterDisplayName(cluster *pb.InfrastructureCluster) string {
	if cluster == nil {
		return ""
	}
	name := cluster.GetClusterName()
	if name == "" || name == cluster.GetClusterId() {
		name = cluster.GetClusterId()
	} else {
		name = fmt.Sprintf("%s (%s)", name, cluster.GetClusterId())
	}
	tags := []string{}
	if cluster.GetClusterType() != "" {
		tags = append(tags, cluster.GetClusterType())
	}
	if cluster.GetDeploymentModel() != "" {
		tags = append(tags, cluster.GetDeploymentModel())
	}
	if cluster.GetIsPlatformOfficial() {
		tags = append(tags, "platform-official")
	}
	if len(tags) == 0 {
		return name
	}
	return fmt.Sprintf("%s [%s]", name, strings.Join(tags, ", "))
}

func newClusterNodesListCmd() *cobra.Command {
	var clusterID string
	var nodeType string
	var region string
	var withHealth bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List nodes in the active cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			active, err := loadActiveContextLax()
			if err != nil {
				return err
			}
			if guardErr := requireClusterLifecycleContext(active); guardErr != nil {
				return guardErr
			}
			qm, ctxCfg, cleanup, err := clusterNodesQMClientFromContext(cmd.Context())
			if err != nil {
				return err
			}
			defer cleanup()
			defer func() { _ = qm.Close() }()
			clusterID, err = resolveClusterIDForLifecycle(cmd, ctxCfg, clusterID)
			if err != nil {
				return err
			}
			cctx, cancel := clusterNodesRPCContext(cmd.Context(), ctxCfg, 15*time.Second)
			defer cancel()
			resp, err := qm.ListNodes(cctx, clusterID, nodeType, region, nil)
			if err != nil {
				return err
			}
			if output == "json" {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(resp)
			}

			var health map[string]*pb.GetNodeHealthResponse
			if withHealth {
				health = loadNodeHealth(cmd, resp.GetNodes())
			}

			ux.Heading(cmd.OutOrStdout(), fmt.Sprintf("Cluster nodes (%d)", len(resp.GetNodes())))
			for _, n := range resp.GetNodes() {
				mode := "-"
				streams := "-"
				if h := health[n.GetNodeId()]; h != nil {
					mode = h.GetOperationalMode()
					streams = fmt.Sprintf("%d", h.GetActiveStreams())
				}
				versions := "-"
				if h := health[n.GetNodeId()]; h != nil {
					versions = nodeComponentVersions(h.GetComponentVersions())
				}
				lastSeen := "-"
				if n.GetLastHeartbeat() != nil {
					lastSeen = n.GetLastHeartbeat().AsTime().Format(time.RFC3339)
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), " - %s node=%s type=%s cluster=%s mode=%s streams=%s versions=%s last_seen=%s\n",
					n.GetNodeName(), n.GetNodeId(), n.GetNodeType(), n.GetClusterId(), mode, streams, versions, lastSeen)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&clusterID, "cluster-id", "", "filter by cluster ID (defaults to active context cluster_id)")
	cmd.Flags().StringVar(&nodeType, "type", "edge", "filter by node type")
	cmd.Flags().StringVar(&region, "region", "", "filter by region")
	cmd.Flags().BoolVar(&withHealth, "health", true, "include Foghorn health/mode data when foghorn_grpc_addr is configured")
	return cmd
}

func loadNodeHealth(cmd *cobra.Command, nodes []*pb.InfrastructureNode) map[string]*pb.GetNodeHealthResponse {
	fh, ctxCfg, cleanup, err := clusterNodesFoghornClientFromContext(cmd.Context())
	if err != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: health unavailable: %v\n", err)
		return nil
	}
	defer cleanup()
	defer func() { _ = fh.Close() }()

	out := make(map[string]*pb.GetNodeHealthResponse, len(nodes))
	for _, n := range nodes {
		cctx, cancel := clusterNodesRPCContext(cmd.Context(), ctxCfg, 5*time.Second)
		resp, _, err := fh.GetNodeHealth(cctx, &pb.GetNodeHealthRequest{NodeId: n.GetNodeId()})
		cancel()
		if err == nil {
			out[n.GetNodeId()] = resp
		}
	}
	return out
}

func nodeComponentVersions(versions []*pb.NodeComponentVersion) string {
	if len(versions) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(versions))
	for _, version := range versions {
		if version == nil || version.GetComponent() == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%s", version.GetComponent(), version.GetVersion()))
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ",")
}

func resolveClusterNode(cmd *cobra.Command, clusterID, selector string) (*pb.InfrastructureNode, string, error) {
	qm, ctxCfg, cleanup, err := clusterNodesQMClientFromContext(cmd.Context())
	if err != nil {
		return nil, "", err
	}
	defer cleanup()
	defer func() { _ = qm.Close() }()

	if strings.TrimSpace(clusterID) == "" {
		clusterID, err = resolveClusterIDForLifecycle(cmd, ctxCfg, clusterID)
		if err != nil {
			return nil, "", err
		}
	}

	cctx, cancel := clusterNodesRPCContext(cmd.Context(), ctxCfg, 15*time.Second)
	defer cancel()
	resp, err := qm.ListNodes(cctx, clusterID, "edge", "", nil)
	if err != nil {
		return nil, "", err
	}
	nodes := resp.GetNodes()
	selector = strings.TrimSpace(selector)
	if selector == "" {
		if !isatty.IsTerminal(os.Stdin.Fd()) {
			return nil, "", fmt.Errorf("--node is required when interactive selection is unavailable")
		}
		selected, selectErr := promptSelectNode(cmd, nodes)
		if selectErr != nil {
			return nil, "", selectErr
		}
		return selected, clusterID, nil
	}

	var matches []*pb.InfrastructureNode
	for _, node := range nodes {
		if node == nil {
			continue
		}
		if selector == node.GetNodeId() || selector == node.GetNodeName() || selector == node.GetId() {
			matches = append(matches, node)
		}
	}
	if len(matches) == 1 {
		return matches[0], clusterID, nil
	}
	if len(matches) > 1 {
		names := make([]string, 0, len(matches))
		for _, node := range matches {
			names = append(names, nodeDisplayName(node))
		}
		sort.Strings(names)
		return nil, "", fmt.Errorf("node selector %q matched multiple nodes: %s", selector, strings.Join(names, ", "))
	}
	return nil, "", fmt.Errorf("node %q not found in cluster %s", selector, clusterID)
}

func resolveClusterIDForLifecycle(cmd *cobra.Command, ctxCfg fwcfg.Context, explicit string) (string, error) {
	if clusterID := strings.TrimSpace(explicit); clusterID != "" {
		return clusterID, nil
	}
	if clusterID := strings.TrimSpace(ctxCfg.ClusterID); clusterID != "" {
		ux.ContextNotice(cmd.OutOrStdout(), "cluster", clusterID)
		return clusterID, nil
	}
	selected, err := promptSelectCluster(cmd)
	if err != nil {
		return "", err
	}
	return selected.GetClusterId(), nil
}

func promptSelectNode(cmd *cobra.Command, nodes []*pb.InfrastructureNode) (*pb.InfrastructureNode, error) {
	if len(nodes) == 0 {
		return nil, fmt.Errorf("no edge nodes found in the selected cluster")
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		return nodeDisplayName(nodes[i]) < nodeDisplayName(nodes[j])
	})
	_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Select node:")
	for i, node := range nodes {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %d. %s status=%s\n", i+1, nodeDisplayName(node), node.GetStatus())
	}
	_, _ = fmt.Fprint(cmd.OutOrStdout(), "> ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("read selection: %w", err)
	}
	idx, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || idx < 1 || idx > len(nodes) {
		return nil, fmt.Errorf("invalid node selection")
	}
	return nodes[idx-1], nil
}

func nodeDisplayName(node *pb.InfrastructureNode) string {
	if node == nil {
		return ""
	}
	if node.GetNodeName() != "" && node.GetNodeName() != node.GetNodeId() {
		return fmt.Sprintf("%s (%s)", node.GetNodeName(), node.GetNodeId())
	}
	return node.GetNodeId()
}

func newClusterNodesModeCmd(name, mode, short string) *cobra.Command {
	var clusterID string
	var nodeSelector string
	var nodeID string
	var yes bool
	cmd := &cobra.Command{
		Use:   name + " [node]",
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctxCfg, err := loadActiveContextLax()
			if err != nil {
				return err
			}
			if guardErr := requireClusterLifecycleContext(ctxCfg); guardErr != nil {
				return guardErr
			}
			if nodeSelector == "" {
				nodeSelector = nodeID
			}
			if nodeSelector == "" && len(args) > 0 {
				nodeSelector = args[0]
			}
			node, selectedClusterID, err := resolveClusterNode(cmd, clusterID, nodeSelector)
			if err != nil {
				return err
			}
			display := nodeDisplayName(node)
			if mode == "normal" && !promptConfirm(fmt.Sprintf("Return node %s to normal routing?", display), yes) {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Cancelled")
				return nil
			}
			_ = selectedClusterID
			return setClusterNodeMode(cmd, node.GetNodeId(), mode)
		},
	}
	cmd.Flags().StringVar(&clusterID, "cluster-id", "", "cluster to manage (defaults to active context cluster_id)")
	cmd.Flags().StringVar(&nodeSelector, "node", "", "node name or id")
	cmd.Flags().StringVar(&nodeID, "node-id", "", "node id (deprecated; use --node)")
	mustMarkDeprecatedFlag(cmd, "node-id", "use --node with a node name or id")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip confirmation prompt")
	return cmd
}

func newClusterNodesFenceCmd(name, short string) *cobra.Command {
	var clusterID string
	var nodeSelector string
	var nodeID string
	var yes bool
	var wait time.Duration
	var uninstallSSH string
	var sshKey string
	cmd := &cobra.Command{
		Use:   name + " [node]",
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctxCfg, err := loadActiveContextLax()
			if err != nil {
				return err
			}
			if guardErr := requireClusterLifecycleContext(ctxCfg); guardErr != nil {
				return guardErr
			}
			if nodeSelector == "" {
				nodeSelector = nodeID
			}
			if nodeSelector == "" && len(args) > 0 {
				nodeSelector = args[0]
			}
			node, selectedClusterID, err := resolveClusterNode(cmd, clusterID, nodeSelector)
			if err != nil {
				return err
			}
			display := nodeDisplayName(node)
			if !promptConfirm(fmt.Sprintf("%s node %s? This fences routing and marks the registry status %s", commandVerbTitle(name), display, terminalNodeStatus(name)), yes) {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Cancelled")
				return nil
			}
			mode := "maintenance"
			if name == "remove" {
				mode = "draining"
			}
			if err := setClusterNodeMode(cmd, node.GetNodeId(), mode); err != nil {
				return err
			}
			if name == "remove" {
				if wait <= 0 {
					return fmt.Errorf("remove requires a positive --wait deadline; use cluster nodes evict for immediate fencing")
				}
				if err := waitForNodeStreams(cmd, node.GetNodeId(), wait); err != nil {
					return err
				}
				if err := setClusterNodeMode(cmd, node.GetNodeId(), "maintenance"); err != nil {
					return err
				}
			}
			if err := updateQuartermasterNodeStatus(cmd, node.GetNodeId(), selectedClusterID, terminalNodeStatus(name)); err != nil {
				return err
			}
			if uninstallSSH != "" {
				return stopEdgeStack(cmd, uninstallSSH, sshKey)
			}
			ux.PrintNextSteps(cmd.OutOrStdout(), []ux.NextStep{
				{Cmd: "frameworks cluster nodes list", Why: "Confirm the node is fenced from routing."},
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&clusterID, "cluster-id", "", "cluster to manage (defaults to active context cluster_id)")
	cmd.Flags().StringVar(&nodeSelector, "node", "", "node name or id")
	cmd.Flags().StringVar(&nodeID, "node-id", "", "node id (deprecated; use --node)")
	mustMarkDeprecatedFlag(cmd, "node-id", "use --node with a node name or id")
	cmd.Flags().DurationVar(&wait, "wait", defaultClusterNodeDrainDeadline, "for remove: wait for active streams to reach zero before maintenance mode")
	cmd.Flags().StringVar(&uninstallSSH, "uninstall-ssh", "", "SSH target to stop local edge services after fencing")
	cmd.Flags().StringVar(&sshKey, "ssh-key", "", "SSH private key path for --uninstall-ssh")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip confirmation prompt")
	return cmd
}

func setClusterNodeMode(cmd *cobra.Command, nodeID, mode string) error {
	fh, ctxCfg, cleanup, err := clusterNodesFoghornClientFromContext(cmd.Context())
	if err != nil {
		return err
	}
	defer cleanup()
	defer func() { _ = fh.Close() }()
	cctx, cancel := clusterNodesRPCContext(cmd.Context(), ctxCfg, 15*time.Second)
	defer cancel()
	resp, _, err := fh.SetNodeMode(cctx, &pb.SetNodeModeRequest{
		NodeId: nodeID,
		Mode:   mode,
		SetBy:  "frameworks-cli",
	})
	if err != nil {
		return err
	}
	ux.Result(cmd.OutOrStdout(), []ux.ResultField{{
		Key:    "mode",
		OK:     resp.GetStatus() == pb.SetNodeModeStatus_SET_NODE_MODE_STATUS_SUCCESS || resp.GetStatus() == pb.SetNodeModeStatus_SET_NODE_MODE_STATUS_ALREADY_IN_MODE,
		Detail: fmt.Sprintf("%s: %s", resp.GetMode(), resp.GetMessage()),
	}})
	return nil
}

func waitForNodeStreams(cmd *cobra.Command, nodeID string, timeout time.Duration) error {
	fh, ctxCfg, cleanup, err := clusterNodesFoghornClientFromContext(cmd.Context())
	if err != nil {
		return err
	}
	defer cleanup()
	defer func() { _ = fh.Close() }()
	deadline := time.Now().Add(timeout)
	for {
		cctx, cancel := clusterNodesRPCContext(cmd.Context(), ctxCfg, 5*time.Second)
		resp, _, err := fh.GetNodeHealth(cctx, &pb.GetNodeHealthRequest{NodeId: nodeID})
		cancel()
		if err != nil {
			return err
		}
		if resp.GetActiveStreams() == 0 {
			ux.Result(cmd.OutOrStdout(), []ux.ResultField{{Key: "drain", OK: true, Detail: "active streams reached zero"}})
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("drain deadline reached with %d active streams", resp.GetActiveStreams())
		}
		time.Sleep(15 * time.Second)
	}
}

func updateQuartermasterNodeStatus(cmd *cobra.Command, nodeID, clusterID, statusValue string) error {
	qm, ctxCfg, cleanup, err := clusterNodesQMClientFromContext(cmd.Context())
	if err != nil {
		return err
	}
	defer cleanup()
	defer func() { _ = qm.Close() }()
	cctx, cancel := clusterNodesRPCContext(cmd.Context(), ctxCfg, 15*time.Second)
	defer cancel()
	resp, err := qm.UpdateNodeStatus(cctx, &pb.UpdateNodeStatusRequest{
		NodeId:            nodeID,
		Status:            statusValue,
		ExpectedClusterId: &clusterID,
	})
	if err != nil {
		return err
	}
	ux.Result(cmd.OutOrStdout(), []ux.ResultField{{
		Key:    "registry",
		OK:     resp.GetNode().GetStatus() == statusValue,
		Detail: fmt.Sprintf("%s status=%s", resp.GetNode().GetNodeId(), resp.GetNode().GetStatus()),
	}})
	return nil
}

func terminalNodeStatus(action string) string {
	if action == "evict" {
		return "evicted"
	}
	return "retired"
}

func stopEdgeStack(cmd *cobra.Command, sshTarget, sshKey string) error {
	host := sshTargetToHost(sshTarget)
	pool := fwssh.NewPool(30*time.Second, sshKey)
	defer func() { _ = pool.Close() }()
	ep := provisioner.NewEdgeProvisioner(pool)
	commands := []string{
		"cd /opt/frameworks/edge && docker compose -f docker-compose.yml -f docker-compose.edge.yml down",
		"systemctl stop frameworks-caddy frameworks-helmsman frameworks-mistserver",
		"launchctl kill SIGTERM system/com.livepeer.frameworks.caddy",
		"launchctl kill SIGTERM system/com.livepeer.frameworks.helmsman",
		"launchctl kill SIGTERM system/com.livepeer.frameworks.mistserver",
	}
	var failures []error
	succeeded := 0
	for _, command := range commands {
		if _, runErr := ep.RunCommand(cmd.Context(), host, command); runErr != nil {
			failures = append(failures, fmt.Errorf("%s: %w", command, runErr))
			continue
		}
		succeeded++
	}
	if succeeded == 0 {
		return fmt.Errorf("stop edge stack over SSH: no stop command succeeded: %w", errors.Join(failures...))
	}
	ux.Result(cmd.OutOrStdout(), []ux.ResultField{{Key: "uninstall", OK: true, Detail: fmt.Sprintf("%d stop command(s) sent", succeeded)}})
	return nil
}

func activeClusterLifecycleContextWithAuth(ctx context.Context) (fwcfg.Context, error) {
	cfg, err := fwcfg.Load()
	if err != nil {
		return fwcfg.Context{}, err
	}
	rt := fwcfg.GetRuntimeOverrides()
	ctxCfg, err := fwcfg.ResolveActiveContext(rt, fwcfg.OSEnv{}, cfg)
	if err != nil {
		return fwcfg.Context{}, err
	}
	if guardErr := requireClusterLifecycleContext(ctxCfg); guardErr != nil {
		return fwcfg.Context{}, guardErr
	}

	if clusterNodesUseServiceAuth(ctxCfg) {
		token, tokenErr := platformauth.ResolveManifestServiceToken(ctx, ctxCfg, cfg)
		if tokenErr != nil {
			return fwcfg.Context{}, tokenErr
		}
		ctxCfg.Auth.ServiceToken = token
		ctxCfg.Auth.JWT = ""
		return ctxCfg, nil
	}

	jwt, err := fwcredentials.ResolveUserAuth(fwcfg.OSEnv{}, fwcredentials.DefaultStore())
	if err != nil {
		return fwcfg.Context{}, err
	}
	ctxCfg.Auth.JWT = jwt
	return ctxCfg, nil
}

func clusterNodesQMClientFromContext(ctx context.Context) (*qmclient.GRPCClient, fwcfg.Context, func(), error) {
	ctxCfg, err := activeClusterLifecycleContextWithAuth(ctx)
	if err != nil {
		return nil, fwcfg.Context{}, nil, err
	}
	ep, err := controlplane.ResolveGRPC(ctx, ctxCfg, "quartermaster")
	if err != nil {
		return nil, fwcfg.Context{}, nil, err
	}
	qm, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
		GRPCAddr:      ep.Address,
		Timeout:       15 * time.Second,
		Logger:        logging.NewLogger(),
		ServiceToken:  ctxCfg.Auth.ServiceToken,
		AllowInsecure: ep.AllowInsecure,
		ServerName:    ep.ServerName,
	})
	if err != nil {
		ep.Cleanup()
		return nil, fwcfg.Context{}, nil, fmt.Errorf("failed to connect to Quartermaster gRPC: %w", err)
	}
	return qm, ctxCfg, ep.Cleanup, nil
}

func clusterNodesFoghornClientFromContext(ctx context.Context) (*fhclient.GRPCClient, fwcfg.Context, func(), error) {
	ctxCfg, err := activeClusterLifecycleContextWithAuth(ctx)
	if err != nil {
		return nil, fwcfg.Context{}, nil, err
	}
	ep, err := controlplane.ResolveGRPC(ctx, ctxCfg, "foghorn")
	if err != nil {
		return nil, fwcfg.Context{}, nil, err
	}
	fh, err := fhclient.NewGRPCClient(fhclient.GRPCConfig{
		GRPCAddr:      ep.Address,
		Timeout:       30 * time.Second,
		Logger:        logging.NewLogger(),
		ServiceToken:  ctxCfg.Auth.ServiceToken,
		UseTLS:        !ep.AllowInsecure,
		ServerName:    ep.ServerName,
		AllowInsecure: ep.AllowInsecure,
	})
	if err != nil {
		ep.Cleanup()
		return nil, fwcfg.Context{}, nil, fmt.Errorf("failed to connect to Foghorn gRPC: %w", err)
	}
	return fh, ctxCfg, ep.Cleanup, nil
}

func clusterNodesRPCContext(parent context.Context, ctxCfg fwcfg.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	cctx, cancel := context.WithTimeout(parent, timeout)
	if !clusterNodesUseServiceAuth(ctxCfg) && ctxCfg.Auth.JWT != "" {
		cctx = context.WithValue(cctx, ctxkeys.KeyJWTToken, ctxCfg.Auth.JWT)
	}
	return cctx, cancel
}

func clusterNodesUseServiceAuth(ctxCfg fwcfg.Context) bool {
	return ctxCfg.Persona == fwcfg.PersonaPlatform
}

func requireClusterLifecycleContext(ctxCfg fwcfg.Context) error {
	switch ctxCfg.Persona {
	case fwcfg.PersonaPlatform:
		return nil
	case fwcfg.PersonaSelfHosted:
		return fmt.Errorf("cluster nodes requires a platform context; selfhosted contexts deploy BYO edges through Bridge with 'frameworks edge deploy'")
	case fwcfg.PersonaUser, fwcfg.PersonaEdge:
		return fmt.Errorf("cluster nodes requires a platform context; user contexts can inspect account and cluster insights but cannot mutate node lifecycle")
	case "":
		return fmt.Errorf("cluster nodes requires an explicit platform context")
	default:
		return fmt.Errorf("cluster nodes does not support persona %q; use platform for provider operations", ctxCfg.Persona)
	}
}

func mustMarkDeprecatedFlag(cmd *cobra.Command, name, message string) {
	if err := cmd.Flags().MarkDeprecated(name, message); err != nil {
		panic(err)
	}
}

func commandVerbTitle(value string) string {
	if value == "" {
		return ""
	}
	return strings.ToUpper(value[:1]) + value[1:]
}
