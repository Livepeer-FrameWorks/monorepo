package grpc

import (
	"context"
	"strings"
	"time"
	"unicode"

	"frameworks/api_tenants/internal/database/quartermasterdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (s *QuartermasterServer) GetMediaPlacementInventory(ctx context.Context, req *quartermasterpb.GetMediaPlacementInventoryRequest) (*quartermasterpb.MediaPlacementInventory, error) {
	if ctxkeys.GetAuthType(ctx) != "service" {
		return nil, status.Error(codes.PermissionDenied, "placement inventory requires service token auth")
	}
	id, err := uuid.Parse(req.GetTenantId())
	if err != nil || id == uuid.Nil || id.String() != req.GetTenantId() || !placementInventoryID(req.GetControlCellId()) || len(req.GetClusterIds()) == 0 || len(req.GetClusterIds()) > 4096 {
		return nil, status.Error(codes.InvalidArgument, "bounded tenant and cell inventory are required")
	}
	requested := make(map[string]bool, len(req.GetClusterIds()))
	for _, clusterID := range req.GetClusterIds() {
		if !placementInventoryID(clusterID) || requested[clusterID] {
			return nil, status.Error(codes.InvalidArgument, "invalid or duplicate inventory cluster")
		}
		requested[clusterID] = true
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	rows, err := quartermasterdb.New(s.db).GetMediaPlacementInventory(ctx, quartermasterdb.GetMediaPlacementInventoryParams{
		TenantID: req.GetTenantId(), ControlCellID: req.GetControlCellId(), ClusterIds: req.GetClusterIds(),
	})
	if err != nil {
		return nil, status.Error(codes.Unavailable, "placement inventory is unavailable")
	}
	if len(rows) > 8192 {
		return nil, status.Error(codes.ResourceExhausted, "placement inventory exceeds node bound")
	}
	out := &quartermasterpb.MediaPlacementInventory{TenantId: req.GetTenantId(), ControlCellId: req.GetControlCellId()}
	seenClusters, seenNodes := map[string]bool{}, map[string]bool{}
	for _, row := range rows {
		if !requested[row.ClusterID] || !placementInventoryID(row.ClusterID) || row.ObservedAt.IsZero() || !timestamppb.New(row.ObservedAt).IsValid() {
			return nil, status.Error(codes.Internal, "inconsistent placement inventory")
		}
		if out.ObservedAt == nil {
			out.ObservedAt = timestamppb.New(row.ObservedAt)
		} else if !out.ObservedAt.AsTime().Equal(row.ObservedAt) {
			return nil, status.Error(codes.Internal, "inconsistent placement inventory snapshot")
		}
		if !seenClusters[row.ClusterID] {
			seenClusters[row.ClusterID] = true
			out.ClusterIds = append(out.ClusterIds, row.ClusterID)
		}
		if row.NodeID == "" {
			continue
		}
		if !placementInventoryID(row.NodeID) || seenNodes[row.NodeID] {
			return nil, status.Error(codes.Internal, "ambiguous placement node identity")
		}
		seenNodes[row.NodeID] = true
		out.Nodes = append(out.Nodes, &quartermasterpb.MediaPlacementInventoryNode{ClusterId: row.ClusterID, NodeId: row.NodeID, AdmissionEnabled: row.AdmissionEnabled})
		if len(out.Nodes) > 4096 {
			return nil, status.Error(codes.ResourceExhausted, "placement inventory exceeds node bound")
		}
	}
	if len(seenClusters) != len(requested) {
		// Missing access, revoked access and a moved control cell are deliberately indistinguishable.
		return nil, status.Error(codes.PermissionDenied, "requested placement inventory is not authorized")
	}
	out.Complete = true
	return out, nil
}

func placementInventoryID(value string) bool {
	return value != "" && len(value) <= 100 && strings.TrimSpace(value) == value && strings.IndexFunc(value, unicode.IsControl) < 0
}
