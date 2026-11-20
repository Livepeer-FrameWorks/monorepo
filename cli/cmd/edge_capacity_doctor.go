package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"frameworks/cli/internal/controlplane"
	"frameworks/cli/internal/readiness"
	"frameworks/cli/internal/xexec"
	"frameworks/cli/pkg/health"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

func readLocalEdgeCapacity(ctx context.Context, dir, mode string) (*edgeCapacitySnapshot, error) {
	args := []string{"--silent", "--show-error", "--fail", "--max-time", "5", "http://localhost:18017/node/capacity"}
	var out, stderr string
	var err error
	if mode == "container" {
		out, stderr, err = runEdgeDocker(ctx, "", "", append([]string{"exec", "frameworks-edge", "curl"}, args...), dir)
	} else {
		_, out, stderr, err = xexec.Run(ctx, "curl", args, dir)
	}
	if err != nil {
		return nil, fmt.Errorf("local Helmsman capacity unavailable: %w (%s)", err, strings.TrimSpace(stderr))
	}
	var result edgeCapacitySnapshot
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		return nil, fmt.Errorf("decode local node capacity: %w", err)
	}
	return &result, nil
}

func evaluateEdgeCapacities(nodes []*quartermasterpb.InfrastructureNode, observations map[string]*foghorncontrolpb.GetNodeHealthResponse) *health.CheckResult {
	result := &health.CheckResult{Name: "edge_capacity", OK: true, Status: "healthy", CheckedAt: time.Now()}
	var warnings []string
	for _, node := range nodes {
		record := node.GetCapacity()
		if record == nil {
			warnings = append(warnings, node.GetNodeId()+": capacity unavailable")
			continue
		}
		if record.GetEgressSource() == quartermasterpb.CapacitySource_CAPACITY_SOURCE_DEFAULT || record.GetIngressSource() == quartermasterpb.CapacitySource_CAPACITY_SOURCE_DEFAULT {
			warnings = append(warnings, node.GetNodeId()+": default 1 Gbit/s budget")
		}
		observed := observations[node.GetNodeId()]
		if observed == nil || observed.CapacityRevisionApplied == nil {
			warnings = append(warnings, node.GetNodeId()+": applied capacity unknown (offline or Helmsman does not report it)")
		} else if observed.GetCapacityRevisionApplied() != record.GetRevision() {
			warnings = append(warnings, node.GetNodeId()+": capacity not applied yet")
		}
	}
	if len(warnings) > 0 {
		sort.Strings(warnings)
		result.OK = false
		result.Status = yugabyteLayoutWarning
		result.Message = strings.Join(warnings, "; ")
	} else {
		result.Message = fmt.Sprintf("%d edge node budgets applied", len(nodes))
	}
	return result
}

func doctorEdgeCapacity(ctx context.Context, rc *resolvedCluster) *health.CheckResult {
	notChecked := func(err error) *health.CheckResult {
		return &health.CheckResult{Name: "edge_capacity", Status: yugabyteLayoutWarning, CheckedAt: time.Now(), Message: "not checked: " + err.Error()}
	}
	cfg, err := lifecycleContextForResolved(ctx, rc)
	if err != nil {
		return notChecked(err)
	}
	resolver := controlplane.NewResolverWithManifest(cfg, rc.Manifest, rc.ManifestPath, rc.AgeKey)
	defer resolver.Close()
	endpoint, err := resolver.ResolveGRPC(ctx, "quartermaster")
	if err != nil {
		return notChecked(err)
	}
	qm, err := qmclient.NewGRPCClient(clusterNodesQuartermasterGRPCConfig(endpoint, cfg))
	if err != nil {
		return notChecked(err)
	}
	defer func() { _ = qm.Close() }()
	cctx, cancel := clusterNodesRPCContext(ctx, cfg, 30*time.Second)
	defer cancel()
	nodes, err := listCapacityNodes(cctx, qm, cfg.ClusterID)
	if err != nil {
		return notChecked(err)
	}
	observations, _, _ := collectNodeHealth(cctx, cfg, nodes, foghornNodeHealthDialer(resolver, cfg))
	return evaluateEdgeCapacities(nodes, observations)
}

func edgeReadinessCapacity(snapshot *edgeCapacitySnapshot) *readiness.EdgeCapacity {
	if snapshot == nil {
		return nil
	}
	return &readiness.EdgeCapacity{NodeID: snapshot.NodeID, EgressSource: snapshot.EgressSource, IngressSource: snapshot.IngressSource, Revision: snapshot.Revision, AppliedRevision: snapshot.AppliedRevision}
}
