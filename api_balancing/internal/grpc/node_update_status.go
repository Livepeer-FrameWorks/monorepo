package grpc

import (
	"context"
	"sort"
	"strings"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	"frameworks/api_balancing/internal/orchestrator"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ListNodeUpdateStatus reports the release update flow's recorded phase and
// the reported component versions of every node of one cluster this Foghorn
// tracks. Membership comes from the live node state, so a node that has been
// disconnected long enough to be evicted is absent; callers that know the
// cluster's expected node set report such nodes themselves.
func (s *FoghornGRPCServer) ListNodeUpdateStatus(ctx context.Context, req *foghorncontrolpb.ListNodeUpdateStatusRequest) (*foghorncontrolpb.ListNodeUpdateStatusResponse, error) {
	clusterID := strings.TrimSpace(req.GetClusterId())
	if clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_id is required")
	}
	callerTenant := ""
	if ctxkeys.GetAuthType(ctx) != "service" {
		callerTenant = ctxkeys.GetTenantID(ctx)
		if callerTenant == "" {
			return nil, status.Error(codes.Unauthenticated, "node lifecycle authentication required")
		}
	}

	_, snapshot := state.DefaultManager().GetClusterSnapshot()
	nodes := make([]*state.NodeState, 0, len(snapshot))
	for _, node := range snapshot {
		if node == nil || node.ClusterID != clusterID {
			continue
		}
		if callerTenant != "" && node.TenantID != callerTenant {
			continue
		}
		nodes = append(nodes, node)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].NodeID < nodes[j].NodeID })

	resp := &foghorncontrolpb.ListNodeUpdateStatusResponse{Nodes: make([]*foghorncontrolpb.NodeUpdateStatus, 0, len(nodes))}
	if len(nodes) == 0 {
		return resp, nil
	}
	if s.db == nil {
		return nil, status.Error(codes.Unavailable, "database not configured")
	}
	nodeIDs := make([]string, 0, len(nodes))
	for _, node := range nodes {
		nodeIDs = append(nodeIDs, node.NodeID)
	}
	q := foghorndb.New(s.db)
	updateRows, err := q.ListNodeUpdateStatuses(ctx, nodeIDs)
	if err != nil {
		s.logger.WithError(err).WithField("cluster_id", clusterID).Warn("Failed to load node update states")
		return nil, status.Error(codes.Internal, "failed to load node update states")
	}
	componentRows, err := q.ListComponentVersionsForNodes(ctx, nodeIDs)
	if err != nil {
		s.logger.WithError(err).WithField("cluster_id", clusterID).Warn("Failed to load node component versions")
		return nil, status.Error(codes.Internal, "failed to load node component versions")
	}
	updates := make(map[string]foghorndb.ListNodeUpdateStatusesRow, len(updateRows))
	for _, row := range updateRows {
		updates[row.NodeID] = row
	}
	components := make(map[string][]*foghorncontrolpb.NodeComponentVersion, len(nodes))
	for _, row := range componentRows {
		components[row.NodeID] = append(components[row.NodeID], &foghorncontrolpb.NodeComponentVersion{Component: row.Component, Version: row.CurrentVersion})
	}

	for _, node := range nodes {
		entry := &foghorncontrolpb.NodeUpdateStatus{
			NodeId:            node.NodeID,
			ClusterId:         node.ClusterID,
			ComponentVersions: components[node.NodeID],
			Connected:         node.IsHealthy && !node.IsStale,
			OperationalMode:   string(node.OperationalMode),
			AutomaticUpdates:  orchestrator.NodeSupportsAutomaticReleaseUpdate(node),
		}
		if entry.OperationalMode == "" {
			entry.OperationalMode = string(state.NodeModeNormal)
		}
		if row, ok := updates[node.NodeID]; ok {
			entry.TargetRelease = row.TargetRelease
			entry.Phase = row.Phase
			entry.LastError = row.LastError
			if row.Deadline.Valid {
				entry.PhaseDeadline = row.Deadline.Time.UTC().Format(time.RFC3339)
			}
			if !row.UpdatedAt.IsZero() {
				entry.PhaseUpdatedAt = row.UpdatedAt.UTC().Format(time.RFC3339)
			}
		}
		resp.Nodes = append(resp.Nodes, entry)
	}
	return resp, nil
}
