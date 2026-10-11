package grpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"

	"frameworks/api_tenants/internal/database/quartermasterdb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	placementMembershipMaxRows     = 8192
	placementMembershipMaxNodes    = 4096
	placementMembershipMaxClusters = 4096
)

func (s *QuartermasterServer) GetPlacementMembership(ctx context.Context, req *quartermasterpb.GetPlacementMembershipRequest) (*quartermasterpb.PlacementMembership, error) {
	if ctxkeys.GetAuthType(ctx) != "service" {
		return nil, status.Error(codes.PermissionDenied, "placement membership requires service token auth")
	}
	if !placementInventoryID(req.GetControlCellId()) {
		return nil, status.Error(codes.InvalidArgument, "control cell is required")
	}
	return s.readPlacementMembership(ctx, req.GetControlCellId())
}

func (s *QuartermasterServer) WatchPlacementMembership(req *quartermasterpb.GetPlacementMembershipRequest, stream quartermasterpb.ClusterService_WatchPlacementMembershipServer) error {
	if ctxkeys.GetAuthType(stream.Context()) != "service" {
		return status.Error(codes.PermissionDenied, "placement membership requires service token auth")
	}
	if !placementInventoryID(req.GetControlCellId()) {
		return status.Error(codes.InvalidArgument, "control cell is required")
	}
	if s.membershipWatch == nil {
		return status.Error(codes.Unavailable, "placement membership watch is not available")
	}
	cellID := req.GetControlCellId()
	return serveHubWatch(s.membershipWatch, stream.Context(), func(ctx context.Context, wake <-chan struct{}) error {
		last := ""
		send := func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			current, err := s.readPlacementMembership(ctx, cellID)
			if err != nil {
				return err
			}
			// The fingerprint is global, so a wake for another cell's change
			// re-reads this cell and sends nothing when its revision is unchanged.
			if current.GetRevision() == last {
				return nil
			}
			if err := stream.Send(current); err != nil {
				return err
			}
			last = current.GetRevision()
			return nil
		}
		if err := send(); err != nil {
			return err
		}
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-wake:
				if err := send(); err != nil {
					return err
				}
			}
		}
	})
}

// RunPlacementMembershipWatcher wakes membership subscribers when any cell's
// membership may have changed. It holds no subscriber state.
func (s *QuartermasterServer) RunPlacementMembershipWatcher(ctx context.Context, logger logging.Logger) {
	if s == nil || s.db == nil || s.membershipWatch == nil {
		return
	}
	runHubWatcher(ctx, s.membershipWatch, quartermasterdb.New(s.db).PlacementMembershipFingerprint, func(err error) {
		if logger != nil {
			logger.WithError(err).Warn("Placement membership fingerprint unavailable; subscribers fall back to their own reconciliation")
		}
	})
}

// readPlacementMembership returns one cell's complete membership or an error;
// it never returns a truncated or ambiguous set as complete.
func (s *QuartermasterServer) readPlacementMembership(ctx context.Context, cellID string) (*quartermasterpb.PlacementMembership, error) {
	readCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	rows, err := quartermasterdb.New(s.db).GetPlacementMembership(readCtx, cellID)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "placement membership is unavailable")
	}
	if len(rows) > placementMembershipMaxRows {
		return nil, status.Error(codes.ResourceExhausted, "placement membership exceeds row bound")
	}
	out := &quartermasterpb.PlacementMembership{ControlCellId: cellID}
	clusters, nodes := map[string]bool{}, map[string]bool{}
	digest := sha256.New()
	write := func(parts ...string) {
		for _, part := range parts {
			_, _ = digest.Write([]byte(strconv.Itoa(len(part))))
			_, _ = digest.Write([]byte{':'})
			_, _ = digest.Write([]byte(part))
		}
		_, _ = digest.Write([]byte{'\n'})
	}
	write("cell", cellID)
	for _, row := range rows {
		if !placementInventoryID(row.ClusterID) || row.ObservedAt.IsZero() || !timestamppb.New(row.ObservedAt).IsValid() {
			return nil, status.Error(codes.Internal, "inconsistent placement membership")
		}
		if out.ObservedAt == nil {
			out.ObservedAt = timestamppb.New(row.ObservedAt)
		} else if !out.ObservedAt.AsTime().Equal(row.ObservedAt) {
			return nil, status.Error(codes.Internal, "inconsistent placement membership snapshot")
		}
		if !clusters[row.ClusterID] {
			clusters[row.ClusterID] = true
			out.Clusters = append(out.Clusters, &quartermasterpb.PlacementMembershipCluster{ClusterId: row.ClusterID, IsActive: row.IsActive})
			write("cluster", row.ClusterID, strconv.FormatBool(row.IsActive))
			if len(out.Clusters) > placementMembershipMaxClusters {
				return nil, status.Error(codes.ResourceExhausted, "placement membership exceeds cluster bound")
			}
		}
		if row.NodeID == "" {
			continue
		}
		if !placementInventoryID(row.NodeID) || nodes[row.NodeID] {
			return nil, status.Error(codes.Internal, "ambiguous placement node identity")
		}
		nodes[row.NodeID] = true
		out.Nodes = append(out.Nodes, &quartermasterpb.MediaPlacementInventoryNode{ClusterId: row.ClusterID, NodeId: row.NodeID, AdmissionEnabled: row.AdmissionEnabled})
		write("node", row.ClusterID, row.NodeID, strconv.FormatBool(row.AdmissionEnabled))
		if len(out.Nodes) > placementMembershipMaxNodes {
			return nil, status.Error(codes.ResourceExhausted, "placement membership exceeds node bound")
		}
	}
	if out.ObservedAt == nil {
		// A cell with no clusters is a complete, empty membership.
		out.ObservedAt = timestamppb.Now()
	}
	out.Revision = hex.EncodeToString(digest.Sum(nil))
	out.Complete = true
	return out, nil
}
