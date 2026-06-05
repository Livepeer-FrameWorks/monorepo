package resolvers

import (
	"context"
	"fmt"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"strings"
)

func (r *Resolver) ownedClusterIDs(ctx context.Context) (map[string]struct{}, error) {
	tenantID := tenantIDFromContext(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	resp, err := r.Clients.Quartermaster.ListClustersByOwner(ctx, tenantID, &commonpb.CursorPaginationRequest{First: infraMaxLimit})
	if err != nil {
		return nil, fmt.Errorf("failed to load owned clusters: %w", err)
	}

	out := make(map[string]struct{})
	for _, cluster := range resp.GetClusters() {
		if cluster == nil {
			continue
		}
		if strings.TrimSpace(cluster.GetClusterId()) != "" {
			out[cluster.GetClusterId()] = struct{}{}
		}
	}
	return out, nil
}

func (r *Resolver) requireClusterOperatorTenant(ctx context.Context) (string, map[string]struct{}, error) {
	tenantID := tenantIDFromContext(ctx)
	if tenantID == "" {
		return "", nil, fmt.Errorf("tenant context required")
	}

	owned, err := r.ownedClusterIDs(ctx)
	if err != nil {
		return "", nil, err
	}
	if len(owned) == 0 {
		return "", nil, fmt.Errorf("cluster owner access required")
	}
	return tenantID, owned, nil
}

func (r *Resolver) RequireClusterOperatorTenant(ctx context.Context) error {
	_, _, err := r.requireClusterOperatorTenant(ctx)
	return err
}

func (r *Resolver) requireOwnedCluster(ctx context.Context, clusterID string) error {
	clusterID = strings.TrimSpace(clusterID)
	if clusterID == "" {
		_, _, err := r.requireClusterOperatorTenant(ctx)
		return err
	}

	owned, err := r.ownedClusterIDs(ctx)
	if err != nil {
		return err
	}
	if _, ok := owned[clusterID]; !ok {
		return fmt.Errorf("cluster owner access required")
	}
	return nil
}

func (r *Resolver) requireOwnedNode(ctx context.Context, nodeID string) (*quartermasterpb.InfrastructureNode, error) {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return nil, fmt.Errorf("node_id required")
	}

	resp, err := r.Clients.Quartermaster.GetNode(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("failed to get node: %w", err)
	}
	node := resp.GetNode()
	if node == nil {
		return nil, fmt.Errorf("node not found")
	}
	if strings.TrimSpace(node.GetClusterId()) == "" {
		return nil, fmt.Errorf("node cluster missing")
	}
	if err := r.requireOwnedCluster(ctx, node.GetClusterId()); err != nil {
		return nil, err
	}
	return node, nil
}
