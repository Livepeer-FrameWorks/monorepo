package cmd

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/gitops"
	"frameworks/cli/pkg/remoteaccess"
	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"
	"frameworks/pkg/servicedefs"

	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func newClusterReleasesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "releases",
		Short: "Manage edge release catalog and cluster rollout targets",
	}
	cmd.AddCommand(newClusterReleasesListCmd())
	cmd.AddCommand(newClusterReleasesPublishCmd())
	cmd.AddCommand(newClusterReleaseTargetCmd())
	return cmd
}

func newClusterReleasesPublishCmd() *cobra.Command {
	var version string
	var remoteOS string
	var remoteArch string
	cmd := &cobra.Command{
		Use:   "publish --version <version>",
		Short: "Repair or backfill a GitOps edge release row in Quartermaster",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(version) == "" {
				return fmt.Errorf("--version is required")
			}
			qm, ctxCfg, cleanup, err := clusterNodesQMClientFromContext(cmd.Context())
			if err != nil {
				return err
			}
			defer cleanup()
			defer func() { _ = qm.Close() }()
			resp, err := publishEdgeReleaseFromGitOps(cmd, qm, ctxCfg, version, remoteOS, remoteArch)
			if err != nil {
				return err
			}
			release := resp.GetRelease()
			ux.Result(cmd.OutOrStdout(), []ux.ResultField{{
				Key:    "edge-release",
				OK:     true,
				Detail: fmt.Sprintf("%s/%s", release.GetChannel(), release.GetVersion()),
			}})
			return nil
		},
	}
	cmd.Flags().StringVar(&version, "version", "", "GitOps release version or channel")
	cmd.Flags().StringVar(&remoteOS, "os", "", "optional artifact operating system filter")
	cmd.Flags().StringVar(&remoteArch, "arch", "", "optional artifact architecture filter")
	return cmd
}

func newClusterReleasesListCmd() *cobra.Command {
	var channel string
	var version string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List edge releases",
		RunE: func(cmd *cobra.Command, args []string) error {
			qm, ctxCfg, cleanup, err := clusterNodesQMClientFromContext(cmd.Context())
			if err != nil {
				return err
			}
			defer cleanup()
			defer func() { _ = qm.Close() }()
			cctx, cancel := clusterNodesRPCContext(cmd.Context(), ctxCfg, 15*time.Second)
			defer cancel()
			resp, err := qm.ListEdgeReleases(cctx, &pb.ListEdgeReleasesRequest{
				Channel: strings.TrimSpace(channel),
				Version: strings.TrimSpace(version),
			})
			if err != nil {
				return err
			}
			if len(resp.GetReleases()) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No edge releases found.")
				return nil
			}
			for _, release := range resp.GetReleases() {
				if release == nil {
					continue
				}
				when := "-"
				if release.GetPublishedAt() != nil {
					when = release.GetPublishedAt().AsTime().Format(time.RFC3339)
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), " - %s/%s published=%s components=%s\n", release.GetChannel(), release.GetVersion(), when, release.GetComponentsJson())
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&channel, "channel", "", "release channel filter")
	cmd.Flags().StringVar(&version, "version", "", "release version filter")
	return cmd
}

func newClusterReleaseTargetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "target",
		Short: "Inspect or override a cluster edge release target",
	}
	cmd.AddCommand(newClusterReleaseTargetSetCmd())
	cmd.AddCommand(newClusterReleaseTargetGetCmd())
	return cmd
}

func newClusterReleaseTargetSetCmd() *cobra.Command {
	var clusterID string
	var channel string
	var version string
	var paused bool
	var rolloutPlan string
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Override a cluster edge release target",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(channel) == "" {
				return fmt.Errorf("--channel is required")
			}
			ctxCfg, err := loadActiveContextLax()
			if err != nil {
				return err
			}
			if clusterID == "" {
				clusterID = ctxCfg.ClusterID
			}
			if strings.TrimSpace(clusterID) == "" {
				selected, selectErr := promptSelectCluster(cmd)
				if selectErr != nil {
					return selectErr
				}
				clusterID = selected.GetClusterId()
			}
			qm, rpcCtxCfg, cleanup, err := clusterNodesQMClientFromContext(cmd.Context())
			if err != nil {
				return err
			}
			defer cleanup()
			defer func() { _ = qm.Close() }()
			channel, err = normalizeReleaseTargetChannel(channel)
			if err != nil {
				return err
			}
			targetVersion := normalizeReleaseTargetVersion(version)
			if shouldPublishReleaseForTarget(rpcCtxCfg) {
				if _, publishErr := publishEdgeReleaseFromGitOpsResolved(cmd, qm, rpcCtxCfg, channel, firstNonEmpty(targetVersion, "latest"), "", ""); publishErr != nil {
					return fmt.Errorf("publish selected GitOps release before target update: %w", publishErr)
				}
			} else if ensureErr := ensureReleaseTargetExists(cmd, qm, rpcCtxCfg, channel, targetVersion); ensureErr != nil {
				return ensureErr
			}
			cctx, cancel := clusterNodesRPCContext(cmd.Context(), rpcCtxCfg, 15*time.Second)
			defer cancel()
			resp, err := qm.SetClusterReleaseTarget(cctx, &pb.SetClusterReleaseTargetRequest{Target: &pb.ClusterReleaseTarget{
				ClusterId:       clusterID,
				Channel:         channel,
				TargetVersion:   targetVersion,
				RolloutPlanJson: firstNonEmpty(strings.TrimSpace(rolloutPlan), "{}"),
				Paused:          paused,
			}})
			if err != nil {
				return err
			}
			target := resp.GetTarget()
			ux.Result(cmd.OutOrStdout(), []ux.ResultField{{
				Key:    "release-target",
				OK:     true,
				Detail: fmt.Sprintf("cluster=%s track=%s version=%s paused=%t", target.GetClusterId(), target.GetChannel(), firstNonEmpty(target.GetTargetVersion(), "latest"), target.GetPaused()),
			}})
			return nil
		},
	}
	cmd.Flags().StringVar(&clusterID, "cluster-id", "", "cluster ID or slug (defaults to active context)")
	cmd.Flags().StringVar(&channel, "channel", "", "release track")
	cmd.Flags().StringVar(&version, "version", "", "target version (empty follows channel head)")
	cmd.Flags().BoolVar(&paused, "paused", false, "save the target without automatic reconciliation")
	cmd.Flags().StringVar(&rolloutPlan, "rollout-plan", "{}", "rollout plan JSON")
	return cmd
}

func shouldPublishReleaseForTarget(ctxCfg fwcfg.Context) bool {
	return ctxCfg.Persona == fwcfg.PersonaPlatform && strings.TrimSpace(ctxCfg.Auth.ServiceToken) != ""
}

func normalizeReleaseTargetVersion(version string) string {
	version = strings.TrimSpace(version)
	if strings.EqualFold(version, "latest") {
		return ""
	}
	return version
}

func normalizeReleaseTargetChannel(channel string) (string, error) {
	channel = strings.ToLower(strings.TrimSpace(channel))
	switch channel {
	case "stable", "rc":
		return channel, nil
	default:
		return "", fmt.Errorf("unsupported release channel %q", channel)
	}
}

func ensureReleaseTargetExists(cmd *cobra.Command, qm *qmclient.GRPCClient, ctxCfg fwcfg.Context, channel, version string) error {
	cctx, cancel := clusterNodesRPCContext(cmd.Context(), ctxCfg, 15*time.Second)
	defer cancel()
	resp, err := qm.ListEdgeReleases(cctx, &pb.ListEdgeReleasesRequest{
		Channel: channel,
		Version: version,
	})
	if err != nil {
		return fmt.Errorf("verify edge release catalog: %w", err)
	}
	if len(resp.GetReleases()) > 0 {
		return nil
	}
	if version == "" {
		return fmt.Errorf("no edge releases published for channel %q; a provider context must publish the GitOps release before this owner context can target it", channel)
	}
	return fmt.Errorf("edge release %s/%s is not published; a provider context must publish the GitOps release before this owner context can target it", channel, version)
}

func publishEdgeReleaseFromGitOps(cmd *cobra.Command, qm *qmclient.GRPCClient, ctxCfg fwcfg.Context, version, remoteOS, remoteArch string) (*pb.EdgeReleaseResponse, error) {
	channel, resolved := gitops.ResolveVersion(version)
	return publishEdgeReleaseFromGitOpsResolved(cmd, qm, ctxCfg, channel, resolved, remoteOS, remoteArch)
}

func publishEdgeReleaseFromGitOpsResolved(cmd *cobra.Command, qm *qmclient.GRPCClient, ctxCfg fwcfg.Context, channel, resolved, remoteOS, remoteArch string) (*pb.EdgeReleaseResponse, error) {
	return publishEdgeReleaseFromGitOpsResolvedRepos(cmd, qm, ctxCfg, nil, channel, resolved, remoteOS, remoteArch)
}

func publishEdgeReleaseFromGitOpsResolvedRepos(cmd *cobra.Command, qm *qmclient.GRPCClient, ctxCfg fwcfg.Context, repos []string, channel, resolved, remoteOS, remoteArch string) (*pb.EdgeReleaseResponse, error) {
	manifest, err := gitops.FetchFromRepositories(gitops.FetchOptions{}, repos, channel, resolved)
	if err != nil {
		return nil, err
	}
	return upsertEdgeReleaseManifest(cmd, qm, ctxCfg, manifest, channel, remoteOS, remoteArch)
}

func upsertEdgeReleaseManifest(cmd *cobra.Command, qm *qmclient.GRPCClient, ctxCfg fwcfg.Context, manifest *gitops.Manifest, channel, remoteOS, remoteArch string) (*pb.EdgeReleaseResponse, error) {
	components, err := edgeReleaseComponentsFromManifest(manifest, remoteOS, remoteArch)
	if err != nil {
		return nil, err
	}
	componentsJSON, err := json.Marshal(components)
	if err != nil {
		return nil, err
	}
	cctx, cancel := clusterNodesRPCContext(cmd.Context(), ctxCfg, 15*time.Second)
	defer cancel()
	return qm.UpsertEdgeRelease(cctx, &pb.UpsertEdgeReleaseRequest{Release: &pb.EdgeRelease{
		Channel:        channel,
		Version:        manifest.PlatformVersion,
		ComponentsJson: string(componentsJSON),
	}})
}

func syncClusterEdgeReleaseTargetFromGitOps(cmd *cobra.Command, rc *resolvedCluster, selector string, sharedEnv map[string]string) error {
	if rc == nil || rc.Manifest == nil {
		return nil
	}
	selector = strings.TrimSpace(selector)
	if selector == "" {
		selector = rc.Manifest.ResolvedChannel()
	}
	channel, resolved := gitops.ResolveVersion(selector)
	releaseManifest, err := gitops.FetchFromRepositories(gitops.FetchOptions{}, rc.ReleaseRepos, channel, resolved)
	if err != nil {
		return fmt.Errorf("fetch edge release manifest for target sync: %w", err)
	}
	targetVersion := releaseTargetVersionForSelector(selector, releaseManifest.PlatformVersion)

	qm, ctxCfg, cleanup, err := edgeReleaseQMClientForGitOpsSync(cmd, rc, sharedEnv)
	if err != nil {
		return fmt.Errorf("connect Quartermaster for edge release target sync: %w", err)
	}
	defer cleanup()
	defer func() { _ = qm.Close() }()

	if shouldPublishReleaseForTarget(ctxCfg) {
		if _, err := upsertEdgeReleaseManifest(cmd, qm, ctxCfg, releaseManifest, channel, "", ""); err != nil {
			return fmt.Errorf("publish edge release from GitOps manifest: %w", err)
		}
	} else if err := ensureReleaseTargetExists(cmd, qm, ctxCfg, channel, targetVersion); err != nil {
		return err
	}

	cctx, cancel := clusterNodesRPCContext(cmd.Context(), ctxCfg, 15*time.Second)
	defer cancel()
	clusterIDs := rc.Manifest.AllClusterIDs()
	for _, clusterID := range clusterIDs {
		rolloutPlan, paused, err := existingReleaseTargetControls(cctx, qm, clusterID)
		if err != nil {
			return err
		}
		if _, err := qm.SetClusterReleaseTarget(cctx, &pb.SetClusterReleaseTargetRequest{Target: &pb.ClusterReleaseTarget{
			ClusterId:       clusterID,
			Channel:         channel,
			TargetVersion:   targetVersion,
			RolloutPlanJson: rolloutPlan,
			Paused:          paused,
		}}); err != nil {
			return fmt.Errorf("set edge release target for cluster %s: %w", clusterID, err)
		}
	}
	ux.Result(cmd.OutOrStdout(), []ux.ResultField{{
		Key: "edge-release-target",
		OK:  true,
		Detail: fmt.Sprintf("track=%s version=%s clusters=%d",
			channel, firstNonEmpty(targetVersion, "latest"), len(clusterIDs)),
	}})
	return nil
}

func existingReleaseTargetControls(ctx context.Context, qm *qmclient.GRPCClient, clusterID string) (string, bool, error) {
	resp, err := qm.GetClusterReleaseTarget(ctx, &pb.GetClusterReleaseTargetRequest{ClusterId: clusterID})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return "{}", false, nil
		}
		return "", false, fmt.Errorf("load existing edge release target for cluster %s: %w", clusterID, err)
	}
	target := resp.GetTarget()
	if target == nil {
		return "{}", false, nil
	}
	rolloutPlan := strings.TrimSpace(target.GetRolloutPlanJson())
	if rolloutPlan == "" {
		rolloutPlan = "{}"
	}
	return rolloutPlan, target.GetPaused(), nil
}

func edgeReleaseQMClientForGitOpsSync(cmd *cobra.Command, rc *resolvedCluster, sharedEnv map[string]string) (*qmclient.GRPCClient, fwcfg.Context, func(), error) {
	qm, ctxCfg, cleanup, err := clusterNodesQMClientFromContext(cmd.Context())
	if err == nil {
		return qm, ctxCfg, cleanup, nil
	}
	if rc == nil || rc.Manifest == nil {
		return nil, fwcfg.Context{}, nil, err
	}

	serviceToken := strings.TrimSpace(sharedEnv["SERVICE_TOKEN"])
	if serviceToken == "" {
		env, envErr := rc.SharedEnv()
		if envErr != nil {
			return nil, fwcfg.Context{}, nil, fmt.Errorf("%w; fallback service-token load failed: %v", err, envErr)
		}
		serviceToken = strings.TrimSpace(env["SERVICE_TOKEN"])
	}
	if serviceToken == "" {
		return nil, fwcfg.Context{}, nil, fmt.Errorf("%w; SERVICE_TOKEN missing from manifest env_files", err)
	}

	port, ok := servicedefs.DefaultGRPCPort("quartermaster")
	if !ok {
		return nil, fwcfg.Context{}, nil, fmt.Errorf("quartermaster default gRPC port is not registered")
	}
	sess, sessErr := remoteaccess.OpenSession(remoteaccess.Options{
		Manifest:      rc.Manifest,
		SSHKeyPath:    stringFlag(cmd, "ssh-key").Value,
		AllowInsecure: isDevProfile(rc.Manifest),
	})
	if sessErr != nil {
		return nil, fwcfg.Context{}, nil, fmt.Errorf("%w; fallback remote access failed: %v", err, sessErr)
	}
	ep, epErr := sess.Endpoint(cmd.Context(), remoteaccess.ServiceTarget{
		Name:            "quartermaster",
		DefaultGRPCPort: port,
	})
	if epErr != nil {
		_ = sess.Close()
		return nil, fwcfg.Context{}, nil, fmt.Errorf("%w; fallback Quartermaster endpoint failed: %v", err, epErr)
	}
	fallbackQM, qmErr := qmclient.NewGRPCClient(qmclient.GRPCConfig{
		GRPCAddr:      ep.DialAddr,
		Timeout:       15 * time.Second,
		Logger:        logging.NewLogger(),
		ServiceToken:  serviceToken,
		AllowInsecure: ep.Insecure,
		ServerName:    ep.ServerName,
	})
	if qmErr != nil {
		_ = sess.Close()
		return nil, fwcfg.Context{}, nil, fmt.Errorf("%w; fallback Quartermaster client failed: %v", err, qmErr)
	}
	return fallbackQM, fwcfg.Context{
		Persona: fwcfg.PersonaPlatform,
		Auth:    fwcfg.Auth{ServiceToken: serviceToken},
	}, func() { _ = sess.Close() }, nil
}

func releaseTargetVersionForSelector(selector, platformVersion string) string {
	switch strings.ToLower(strings.TrimSpace(selector)) {
	case "", "latest", "stable", "rc":
		return ""
	default:
		return strings.TrimSpace(platformVersion)
	}
}

func newClusterReleaseTargetGetCmd() *cobra.Command {
	var clusterID string
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Show a cluster edge release target",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctxCfg, err := loadActiveContextLax()
			if err != nil {
				return err
			}
			if clusterID == "" {
				clusterID = ctxCfg.ClusterID
			}
			if strings.TrimSpace(clusterID) == "" {
				selected, selectErr := promptSelectCluster(cmd)
				if selectErr != nil {
					return selectErr
				}
				clusterID = selected.GetClusterId()
			}
			qm, rpcCtxCfg, cleanup, err := clusterNodesQMClientFromContext(cmd.Context())
			if err != nil {
				return err
			}
			defer cleanup()
			defer func() { _ = qm.Close() }()
			cctx, cancel := clusterNodesRPCContext(cmd.Context(), rpcCtxCfg, 15*time.Second)
			defer cancel()
			resp, err := qm.GetClusterReleaseTarget(cctx, &pb.GetClusterReleaseTargetRequest{ClusterId: clusterID})
			if err != nil {
				return err
			}
			target := resp.GetTarget()
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "cluster=%s track=%s version=%s paused=%t rollout_plan=%s\n", target.GetClusterId(), target.GetChannel(), firstNonEmpty(target.GetTargetVersion(), "latest"), target.GetPaused(), target.GetRolloutPlanJson())
			return nil
		},
	}
	cmd.Flags().StringVar(&clusterID, "cluster-id", "", "cluster ID or slug (defaults to active context)")
	return cmd
}

type edgeReleaseArtifactSpec struct {
	ArtifactURL string `json:"artifact_url"`
	Checksum    string `json:"checksum"`
}

type edgeReleaseComponentSpec struct {
	Version   string                             `json:"version"`
	Artifacts map[string]edgeReleaseArtifactSpec `json:"artifacts,omitempty"`
}

func edgeReleaseComponentsFromManifest(manifest *gitops.Manifest, remoteOS, remoteArch string) (map[string]edgeReleaseComponentSpec, error) {
	if (strings.TrimSpace(remoteOS) == "") != (strings.TrimSpace(remoteArch) == "") {
		return nil, fmt.Errorf("--os and --arch must be provided together")
	}
	components := map[string]edgeReleaseComponentSpec{}
	if info, err := manifest.GetServiceInfo("helmsman"); err == nil {
		artifacts, err := edgeServiceArtifacts(info, remoteOS, remoteArch)
		if err != nil {
			return nil, err
		}
		components["helmsman"] = edgeReleaseComponentSpec{
			Version:   strings.TrimSpace(info.Version),
			Artifacts: artifacts,
		}
		if err := validateEdgeReleaseComponent("helmsman", components["helmsman"]); err != nil {
			return nil, err
		}
	}
	if dep := manifest.GetExternalDependency("mistserver"); dep != nil {
		artifacts, err := edgeExternalArtifacts(dep, remoteOS, remoteArch)
		if err != nil {
			return nil, err
		}
		components["mist"] = edgeReleaseComponentSpec{
			Version:   firstNonEmpty(strings.TrimSpace(dep.ReleaseTag), strings.TrimSpace(dep.Digest), strings.TrimSpace(dep.ReleaseURL), strings.TrimSpace(dep.Image)),
			Artifacts: artifacts,
		}
		if err := validateEdgeReleaseComponent("mist", components["mist"]); err != nil {
			return nil, err
		}
	}
	if dep := manifest.GetExternalDependency("caddy"); dep != nil {
		artifacts, err := edgeExternalArtifacts(dep, remoteOS, remoteArch)
		if err != nil {
			if platformFilterSet(remoteOS, remoteArch) {
				return nil, err
			}
		} else {
			components["caddy"] = edgeReleaseComponentSpec{
				Version:   firstNonEmpty(strings.TrimSpace(dep.ReleaseTag), strings.TrimSpace(dep.Digest), strings.TrimSpace(dep.ReleaseURL), strings.TrimSpace(dep.Image)),
				Artifacts: artifacts,
			}
			if err := validateEdgeReleaseComponent("caddy", components["caddy"]); err != nil {
				return nil, err
			}
		}
	}
	if _, ok := components["caddy"]; !ok {
		if info, err := manifest.GetServiceInfo("caddy"); err == nil {
			artifacts, err := edgeServiceArtifacts(info, remoteOS, remoteArch)
			if err != nil {
				return nil, err
			}
			components["caddy"] = edgeReleaseComponentSpec{
				Version:   strings.TrimSpace(info.Version),
				Artifacts: artifacts,
			}
			if err := validateEdgeReleaseComponent("caddy", components["caddy"]); err != nil {
				return nil, err
			}
		}
	}
	if !edgeReleaseHasUpdateableComponent(components) {
		filter := "any platform"
		if platformFilterSet(remoteOS, remoteArch) {
			filter = platformArtifactName(remoteOS, remoteArch)
		}
		return nil, fmt.Errorf("release manifest has no updateable edge components for %s", filter)
	}
	return components, nil
}

func edgeServiceArtifacts(info *gitops.ServiceInfo, remoteOS, remoteArch string) (map[string]edgeReleaseArtifactSpec, error) {
	if info == nil {
		return nil, fmt.Errorf("service info missing")
	}
	if platformFilterSet(remoteOS, remoteArch) {
		artifact, err := info.GetBinary(remoteOS, remoteArch)
		if err != nil {
			return nil, err
		}
		return map[string]edgeReleaseArtifactSpec{
			platformKey(remoteOS, remoteArch): {
				ArtifactURL: artifact.URL,
				Checksum:    artifact.Checksum,
			},
		}, nil
	}
	artifacts := make(map[string]edgeReleaseArtifactSpec, len(info.Binaries))
	for name, artifact := range info.Binaries {
		key, ok := platformKeyFromArtifactName(name)
		if !ok {
			return nil, fmt.Errorf("%s binary has invalid platform key %q", info.Name, name)
		}
		artifacts[key] = edgeReleaseArtifactSpec{ArtifactURL: artifact.URL, Checksum: artifact.Checksum}
	}
	if len(artifacts) == 0 {
		return nil, fmt.Errorf("%s has no binary artifacts", info.Name)
	}
	return artifacts, nil
}

func edgeExternalArtifacts(dep *gitops.ExternalDependency, remoteOS, remoteArch string) (map[string]edgeReleaseArtifactSpec, error) {
	if dep == nil {
		return nil, fmt.Errorf("external dependency missing")
	}
	if platformFilterSet(remoteOS, remoteArch) {
		name := platformArtifactName(remoteOS, remoteArch)
		artifact := dep.GetBinary(name)
		if artifact == nil {
			return nil, fmt.Errorf("%s has no binary for %s", dep.Name, name)
		}
		return map[string]edgeReleaseArtifactSpec{
			platformKey(remoteOS, remoteArch): {
				ArtifactURL: artifact.URL,
				Checksum:    artifact.Checksum,
			},
		}, nil
	}
	artifacts := make(map[string]edgeReleaseArtifactSpec, len(dep.Binaries))
	for _, artifact := range dep.Binaries {
		key, ok := platformKeyFromArtifactName(artifact.Name)
		if !ok {
			return nil, fmt.Errorf("%s binary has invalid platform key %q", dep.Name, artifact.Name)
		}
		artifacts[key] = edgeReleaseArtifactSpec{ArtifactURL: artifact.URL, Checksum: artifact.Checksum}
	}
	if len(artifacts) == 0 {
		return nil, fmt.Errorf("%s has no binary artifacts", dep.Name)
	}
	return artifacts, nil
}

func platformFilterSet(remoteOS, remoteArch string) bool {
	return strings.TrimSpace(remoteOS) != "" || strings.TrimSpace(remoteArch) != ""
}

func platformArtifactName(osName, arch string) string {
	return strings.ToLower(strings.TrimSpace(osName)) + "-" + strings.ToLower(strings.TrimSpace(arch))
}

func platformKey(osName, arch string) string {
	return strings.ToLower(strings.TrimSpace(osName)) + "/" + strings.ToLower(strings.TrimSpace(arch))
}

func platformKeyFromArtifactName(name string) (string, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return "", false
	}
	if strings.Contains(name, "/") {
		parts := strings.Split(name, "/")
		if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
			return parts[0] + "/" + parts[1], true
		}
		return "", false
	}
	parts := strings.SplitN(name, "-", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	return parts[0] + "/" + parts[1], true
}

func edgeReleaseHasUpdateableComponent(components map[string]edgeReleaseComponentSpec) bool {
	for _, component := range []string{"helmsman", "mist", "caddy"} {
		if _, ok := components[component]; ok {
			return true
		}
	}
	return false
}

func validateEdgeReleaseComponent(component string, values edgeReleaseComponentSpec) error {
	if strings.TrimSpace(values.Version) == "" {
		return fmt.Errorf("%s version required", component)
	}
	if len(values.Artifacts) == 0 {
		return fmt.Errorf("%s artifacts required", component)
	}
	for platform, artifact := range values.Artifacts {
		if _, ok := platformKeyFromArtifactName(platform); !ok {
			return fmt.Errorf("%s artifact platform %q invalid", component, platform)
		}
		if strings.TrimSpace(artifact.ArtifactURL) == "" {
			return fmt.Errorf("%s artifact_url required for %s", component, platform)
		}
		if strings.TrimSpace(artifact.Checksum) == "" {
			return fmt.Errorf("%s checksum required for %s", component, platform)
		}
		if err := validateEdgeReleaseChecksum(artifact.Checksum); err != nil {
			return fmt.Errorf("%s checksum invalid for %s: %w", component, platform, err)
		}
	}
	return nil
}

func validateEdgeReleaseChecksum(value string) error {
	value = strings.TrimSpace(value)
	algo, digest, ok := strings.Cut(value, ":")
	if !ok {
		algo, digest = "sha256", value
	}
	var hexLen int
	switch strings.ToLower(strings.TrimSpace(algo)) {
	case "sha256":
		hexLen = sha256.Size * 2
	case "sha512":
		hexLen = sha512.Size * 2
	default:
		return fmt.Errorf("unsupported checksum algorithm %q", algo)
	}
	digest = strings.TrimSpace(digest)
	if len(digest) != hexLen {
		return fmt.Errorf("digest must be %d hex characters", hexLen)
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return fmt.Errorf("digest must be hex: %w", err)
	}
	return nil
}
