package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

type releaseComponent struct {
	Version     string                     `json:"version"`
	ArtifactURL string                     `json:"artifact_url"`
	Checksum    string                     `json:"checksum"`
	Artifacts   map[string]releaseArtifact `json:"artifacts"`
}

type releaseArtifact struct {
	ArtifactURL string `json:"artifact_url"`
	Checksum    string `json:"checksum"`
}

type rolloutPlan struct {
	Canary               bool   `json:"canary"`
	CanaryCount          int    `json:"canary_count"`
	BatchSize            int    `json:"batch_size"`
	CapacityFloor        int    `json:"capacity_floor"`
	CapacityFloorPercent int    `json:"capacity_floor_percent"`
	MaxFailed            int    `json:"max_failed"`
	ErrorAbort           bool   `json:"error_abort"`
	DrainDeadline        string `json:"drain_deadline"`
	Force                bool   `json:"force"`
}

func StartReleaseReconciler(ctx context.Context, qmProvider func() *qmclient.GRPCClient, interval time.Duration, logger logging.Logger) {
	if qmProvider == nil {
		return
	}
	if interval <= 0 {
		interval = time.Minute
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			if qm := qmProvider(); qm != nil {
				if err := ReconcileReleaseTargets(ctx, qm); err != nil && logger != nil {
					logger.WithError(err).Warn("Edge release target reconciliation failed")
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func ReconcileReleaseTargets(ctx context.Context, qm *qmclient.GRPCClient) error {
	targets, err := qm.ListClusterReleaseTargets(ctx, &pb.ListClusterReleaseTargetsRequest{})
	if err != nil {
		return err
	}
	var errs []error
	for _, target := range targets.GetTargets() {
		if target == nil || target.GetPaused() {
			continue
		}
		if err := reconcileTarget(ctx, qm, target); err != nil {
			errs = append(errs, fmt.Errorf("cluster %s: %w", target.GetClusterId(), err))
		}
	}
	return errors.Join(errs...)
}

func reconcileTarget(ctx context.Context, qm *qmclient.GRPCClient, target *pb.ClusterReleaseTarget) error {
	version := strings.TrimSpace(target.GetTargetVersion())
	releases, err := qm.ListEdgeReleases(ctx, &pb.ListEdgeReleasesRequest{
		Channel: strings.TrimSpace(target.GetChannel()),
		Version: version,
	})
	if err != nil {
		return err
	}
	if len(releases.GetReleases()) == 0 {
		return nil
	}
	release := releases.GetReleases()[0]
	plan, err := parseRolloutPlan(target.GetRolloutPlanJson())
	if err != nil {
		return err
	}
	targetRelease := fmt.Sprintf("%s:%s", release.GetChannel(), release.GetVersion())
	if rolloutFailed(ctx, target.GetClusterId(), targetRelease, plan) {
		return nil
	}
	components, err := parseReleaseComponents(release.GetComponentsJson())
	if err != nil {
		return err
	}
	_, snapshot := state.DefaultManager().GetClusterSnapshot()
	clusterNodes := nodesInCluster(snapshot, target.GetClusterId())
	nodes := eligibleNodes(clusterNodes, target.GetClusterId())
	if len(nodes) == 0 {
		return nil
	}
	if err := reconcileWarmups(ctx, target.GetClusterId(), targetRelease, nodes, plan); err != nil {
		return err
	}
	budget := rolloutBudget(ctx, clusterNodes, targetRelease, plan)
	if budget <= 0 {
		return nil
	}
	for _, node := range nodes {
		progress, err := loadProgress(ctx, node.NodeID)
		if err != nil {
			return err
		}
		if progress.Phase != "" && progress.Phase != "idle" && progress.Phase != "failed" {
			continue
		}
		current, err := currentNodeComponents(ctx, node.NodeID)
		if err != nil {
			return err
		}
		var direct []*pb.DesiredComponent
		for component, desired := range components {
			if component == "config_schema" {
				continue
			}
			if desired.Version == "" || current[component] == desired.Version {
				continue
			}
			selected, ok := releaseComponentForNode(desired, node)
			if !ok {
				if err := persistPhase(ctx, node.NodeID, targetRelease, "failed", fmt.Sprintf("release artifact for %s is not available for %s", component, nodePlatformKey(node)), time.Now()); err != nil {
					return err
				}
				continue
			}
			msg := desiredComponentMessage(component, selected)
			if component == "mist" {
				msg.SwapStrategy = "replace-all-usr1"
			} else {
				msg.SwapStrategy = "hot-reload"
				if component == "helmsman" {
					msg.SwapStrategy = "alongside-then-exec"
				}
			}
			direct = append(direct, msg)
		}
		if len(direct) == 0 {
			continue
		}
		if err := ApplyDirectUpdate(ctx, DirectUpdateRequest{
			NodeID:        node.NodeID,
			ClusterID:     target.GetClusterId(),
			TargetRelease: targetRelease,
			Components:    direct,
		}); err != nil {
			return err
		}
		budget--
		if budget <= 0 {
			return nil
		}
	}
	return nil
}

func reconcileWarmups(ctx context.Context, clusterID, targetRelease string, nodes []*state.NodeState, plan rolloutPlan) error {
	for _, node := range nodes {
		progress, err := loadProgress(ctx, node.NodeID)
		if err != nil {
			return err
		}
		if progress.TargetRelease != targetRelease || (progress.Phase != "warming" && progress.Phase != "warming_restore") {
			continue
		}
		warmupComponents := desiredComponentsFromExpected(progress.ExpectedComponents)
		if len(warmupComponents) == 0 {
			return persistPhase(ctx, node.NodeID, targetRelease, "failed", "warmup expected components missing", progress.Deadline)
		}
		if err := ApplyMistUpdate(ctx, MistUpdateRequest{
			NodeID:        node.NodeID,
			ClusterID:     clusterID,
			TargetRelease: targetRelease,
			Components:    warmupComponents,
			DrainDeadline: rolloutDrainDeadlineFromPlan(plan),
			Force:         plan.Force,
		}); err != nil {
			return err
		}
	}
	return nil
}

func desiredComponentsFromExpected(expected map[string]string) []*pb.DesiredComponent {
	out := make([]*pb.DesiredComponent, 0, len(expected))
	for component, version := range expected {
		component = strings.ToLower(strings.TrimSpace(component))
		version = strings.TrimSpace(version)
		if component == "" || version == "" {
			continue
		}
		out = append(out, &pb.DesiredComponent{Component: component, Version: version})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetComponent() < out[j].GetComponent() })
	return out
}

func parseReleaseComponents(raw string) (map[string]releaseComponent, error) {
	var components map[string]releaseComponent
	if err := json.Unmarshal([]byte(raw), &components); err != nil {
		return nil, fmt.Errorf("parse release components: %w", err)
	}
	return components, nil
}

func desiredComponentsForWarmup(components map[string]releaseComponent) []*pb.DesiredComponent {
	out := make([]*pb.DesiredComponent, 0, len(components))
	for component, desired := range components {
		if component == "config_schema" || desired.Version == "" {
			continue
		}
		out = append(out, desiredComponentMessage(component, desired))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetComponent() < out[j].GetComponent() })
	return out
}

func desiredComponentMessage(component string, desired releaseComponent) *pb.DesiredComponent {
	return &pb.DesiredComponent{
		Component:   component,
		Version:     desired.Version,
		ArtifactUrl: desired.ArtifactURL,
		Checksum:    desired.Checksum,
	}
}

func eligibleNodes(nodes []*state.NodeState, clusterID string) []*state.NodeState {
	out := make([]*state.NodeState, 0, len(nodes))
	for _, node := range nodes {
		if node == nil || node.ClusterID != clusterID || !node.IsHealthy || node.IsStale || !nodeAllowsAutomaticReleaseUpdate(node) {
			continue
		}
		out = append(out, node)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out
}

func nodesInCluster(nodes []*state.NodeState, clusterID string) []*state.NodeState {
	out := make([]*state.NodeState, 0, len(nodes))
	for _, node := range nodes {
		if node == nil || node.ClusterID != clusterID {
			continue
		}
		out = append(out, node)
	}
	return out
}

func nodeAllowsAutomaticReleaseUpdate(node *state.NodeState) bool {
	if node == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(node.DeployMode), "native") {
		return false
	}
	if nodePlatformKey(node) == "" {
		return false
	}
	return node.OperationalMode == "" || node.OperationalMode == state.NodeModeNormal
}

func releaseComponentForNode(component releaseComponent, node *state.NodeState) (releaseComponent, bool) {
	platform := nodePlatformKey(node)
	if platform == "" {
		return releaseComponent{}, false
	}
	for _, key := range platformAliases(platform) {
		if artifact, ok := component.Artifacts[key]; ok {
			component.ArtifactURL = artifact.ArtifactURL
			component.Checksum = artifact.Checksum
			return component, true
		}
	}
	if component.ArtifactURL != "" || component.Checksum != "" {
		return component, platform == "linux/amd64"
	}
	return releaseComponent{}, false
}

func nodePlatformKey(node *state.NodeState) string {
	if node == nil {
		return ""
	}
	osName := strings.ToLower(strings.TrimSpace(node.OS))
	arch := strings.ToLower(strings.TrimSpace(node.Arch))
	if osName == "" || arch == "" {
		return ""
	}
	return osName + "/" + arch
}

func platformAliases(platform string) []string {
	parts := strings.Split(platform, "/")
	if len(parts) != 2 {
		return []string{platform}
	}
	return []string{platform, parts[0] + "-" + parts[1]}
}

func parseRolloutPlan(raw string) (rolloutPlan, error) {
	plan := rolloutPlan{BatchSize: 1, CanaryCount: 1}
	if strings.TrimSpace(raw) != "" {
		var parsed rolloutPlan
		if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
			return rolloutPlan{}, fmt.Errorf("parse rollout plan: %w", err)
		}
		plan = parsed
	}
	if plan.CapacityFloor != 0 || plan.CapacityFloorPercent != 0 {
		return rolloutPlan{}, fmt.Errorf("rollout plan capacity_floor fields are not supported for edge release targets")
	}
	if plan.BatchSize <= 0 {
		plan.BatchSize = 1
	}
	if plan.CanaryCount <= 0 {
		plan.CanaryCount = 1
	}
	if plan.ErrorAbort && plan.MaxFailed <= 0 {
		plan.MaxFailed = 1
	}
	if strings.TrimSpace(plan.DrainDeadline) != "" {
		if _, err := time.ParseDuration(plan.DrainDeadline); err != nil {
			return rolloutPlan{}, fmt.Errorf("parse rollout plan drain_deadline: %w", err)
		}
	}
	return plan, nil
}

func rolloutBudget(ctx context.Context, nodes []*state.NodeState, targetRelease string, plan rolloutPlan) int {
	limit := plan.BatchSize
	if plan.Canary && completedTargetCount(ctx, nodes, targetRelease) == 0 && limit > plan.CanaryCount {
		limit = plan.CanaryCount
	}
	active := activeUpdateCount(ctx, nodes)
	if active >= limit {
		return 0
	}
	return limit - active
}

func rolloutFailed(ctx context.Context, clusterID, targetRelease string, plan rolloutPlan) bool {
	if !plan.ErrorAbort && plan.MaxFailed <= 0 {
		return false
	}
	_, snapshot := state.DefaultManager().GetClusterSnapshot()
	return failedTargetCount(ctx, nodesInCluster(snapshot, clusterID), targetRelease) >= plan.MaxFailed
}

func currentNodeComponents(ctx context.Context, nodeID string) (map[string]string, error) {
	out := map[string]string{}
	db := control.GetDB()
	if db == nil {
		return out, fmt.Errorf("component version database unavailable")
	}
	rows, err := db.QueryContext(ctx, `
		SELECT component, COALESCE(current_version, '')
		FROM foghorn.node_components
		WHERE node_id = $1
	`, nodeID)
	if err != nil {
		return out, fmt.Errorf("load node component versions for %s: %w", nodeID, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var component string
		var version string
		if err := rows.Scan(&component, &version); err == nil {
			out[component] = version
		} else {
			return out, fmt.Errorf("scan node component version for %s: %w", nodeID, err)
		}
	}
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("read node component versions for %s: %w", nodeID, err)
	}
	return out, nil
}

func updatingNode(ctx context.Context, nodeID string) bool {
	db := control.GetDB()
	if db == nil {
		return false
	}
	var phase string
	err := db.QueryRowContext(ctx, `
		SELECT phase
		FROM foghorn.node_update_state
		WHERE node_id = $1
	`, nodeID).Scan(&phase)
	if err == sql.ErrNoRows {
		return false
	}
	return err == nil && phase != "" && phase != "idle" && phase != "failed"
}

func activeUpdateCount(ctx context.Context, nodes []*state.NodeState) int {
	count := 0
	for _, node := range nodes {
		if updatingNode(ctx, node.NodeID) {
			count++
		}
	}
	return count
}

func failedTargetCount(ctx context.Context, nodes []*state.NodeState, targetRelease string) int {
	db := control.GetDB()
	if db == nil {
		return 0
	}
	count := 0
	for _, node := range nodes {
		var phase string
		err := db.QueryRowContext(ctx, `
			SELECT phase
			FROM foghorn.node_update_state
			WHERE node_id = $1 AND target_release = $2
		`, node.NodeID, targetRelease).Scan(&phase)
		if err == nil && phase == "failed" {
			count++
		}
	}
	return count
}

func completedTargetCount(ctx context.Context, nodes []*state.NodeState, targetRelease string) int {
	db := control.GetDB()
	if db == nil {
		return 0
	}
	count := 0
	for _, node := range nodes {
		var phase string
		err := db.QueryRowContext(ctx, `
			SELECT phase
			FROM foghorn.node_update_state
			WHERE node_id = $1 AND target_release = $2
		`, node.NodeID, targetRelease).Scan(&phase)
		if err == nil && phase == "idle" {
			count++
		}
	}
	return count
}

func rolloutDrainDeadlineFromPlan(plan rolloutPlan) time.Duration {
	if plan.DrainDeadline != "" {
		if d, err := time.ParseDuration(plan.DrainDeadline); err == nil {
			return d
		}
	}
	return 4 * time.Hour
}
