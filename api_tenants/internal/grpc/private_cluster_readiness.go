package grpc

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"frameworks/api_tenants/internal/database/quartermasterdb"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

// Private clusters have no separate provisioning job to mark them ready.
// Their first healthy, active edge completes readiness in the same transaction
// that records its capabilities. Existing unknown clusters recover the same way.
func (s *QuartermasterServer) activateReportedPrivateClusters(ctx context.Context, tx *sql.Tx, nodes []*quartermasterpb.NodeAliveness) error {
	var nodeIDs []string
	for _, node := range nodes {
		caps := node.GetCapabilities()
		if id := strings.TrimSpace(node.GetNodeId()); id != "" && node.GetIsHealthy() &&
			(caps.GetIngest() || caps.GetEgress() || caps.GetStorage() || caps.GetProcessing()) {
			nodeIDs = append(nodeIDs, id)
		}
	}
	if len(nodeIDs) == 0 {
		return nil
	}
	clusters, err := quartermasterdb.New(tx).ActivateReportedPrivateClusters(ctx, nodeIDs)
	if err != nil {
		return fmt.Errorf("activate reported private clusters: %w", err)
	}
	for _, cluster := range clusters {
		if err := s.emitClusterEventTx(ctx, tx, eventClusterUpdated, cluster.TenantID, "", cluster.ClusterID, "cluster", cluster.ClusterID, "", "", "edge_ready"); err != nil {
			return fmt.Errorf("enqueue private cluster readiness: %w", err)
		}
	}
	return nil
}
