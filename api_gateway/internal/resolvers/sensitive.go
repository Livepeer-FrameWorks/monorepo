package resolvers

import (
	"context"
	"strings"

	"frameworks/api_gateway/internal/loaders"
	"frameworks/api_gateway/internal/middleware"
	pb "frameworks/pkg/proto"
)

func isPrivilegedRole(role string) bool {
	switch strings.ToLower(role) {
	case "admin", "owner":
		return true
	default:
		return false
	}
}

// CanViewSensitiveTenantData gates tenant-scoped sensitive fields (paths/URLs/metadata).
func (r *Resolver) CanViewSensitiveTenantData(ctx context.Context) bool {
	if middleware.IsDemoMode(ctx) || middleware.HasServiceToken(ctx) {
		return true
	}
	user := middleware.GetUserFromContext(ctx)
	if user == nil {
		return false
	}
	return isPrivilegedRole(user.Role)
}

// CanViewSensitiveInfraData gates infra-sensitive fields for a given cluster.
// Allowed for cluster owners (tenant owns cluster) or privileged roles.
func (r *Resolver) CanViewSensitiveInfraData(ctx context.Context, clusterID string) bool {
	if middleware.IsDemoMode(ctx) || middleware.HasServiceToken(ctx) {
		return true
	}
	user := middleware.GetUserFromContext(ctx)
	if user == nil {
		return false
	}
	if isPrivilegedRole(user.Role) {
		return true
	}
	if clusterID == "" {
		return false
	}
	cluster, err := r.loadCluster(ctx, clusterID)
	if err != nil || cluster == nil || cluster.OwnerTenantId == nil {
		return false
	}
	return *cluster.OwnerTenantId == user.TenantID
}

// CanViewSensitiveNodeData gates node-scoped sensitive fields by resolving cluster ownership.
func (r *Resolver) CanViewSensitiveNodeData(ctx context.Context, nodeID string) bool {
	if middleware.IsDemoMode(ctx) || middleware.HasServiceToken(ctx) {
		return true
	}
	user := middleware.GetUserFromContext(ctx)
	if user == nil {
		return false
	}
	if isPrivilegedRole(user.Role) {
		return true
	}
	if nodeID == "" {
		return false
	}
	node, err := r.loadNode(ctx, nodeID)
	if err != nil || node == nil {
		return false
	}
	return r.CanViewSensitiveInfraData(ctx, node.ClusterId)
}

func (r *Resolver) loadCluster(ctx context.Context, clusterID string) (*pb.InfrastructureCluster, error) {
	if clusterID == "" {
		return nil, nil
	}
	if l := loaders.FromContext(ctx); l != nil && l.Cluster != nil {
		return l.Cluster.Load(ctx, clusterID)
	}
	resp, err := r.Clients.Quartermaster.GetCluster(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	return resp.Cluster, nil
}

func (r *Resolver) loadNode(ctx context.Context, nodeID string) (*pb.InfrastructureNode, error) {
	if nodeID == "" {
		return nil, nil
	}
	if l := loaders.FromContext(ctx); l != nil && l.Node != nil {
		return l.Node.Load(ctx, nodeID)
	}
	resp, err := r.Clients.Quartermaster.GetNode(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	return resp.Node, nil
}
