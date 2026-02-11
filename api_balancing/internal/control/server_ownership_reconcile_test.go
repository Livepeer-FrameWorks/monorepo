package control

import (
	"context"
	"errors"
	"testing"

	pb "frameworks/pkg/proto"

	"github.com/sirupsen/logrus"
)

func TestReconcileNodeCluster(t *testing.T) {
	original := getNodeOwnerFn
	t.Cleanup(func() { getNodeOwnerFn = original })

	logger := logrus.New()

	t.Run("uses quartermaster cluster when present", func(t *testing.T) {
		getNodeOwnerFn = func(ctx context.Context, nodeID string) (*pb.NodeOwnerResponse, error) {
			if nodeID != "node-1" {
				t.Fatalf("unexpected node id %q", nodeID)
			}
			return &pb.NodeOwnerResponse{OwnerTenantId: strPtr("tenant-new"), ClusterId: "cluster-new"}, nil
		}

		clusterID := reconcileNodeCluster(context.Background(), "node-1", "cluster-old", logger)
		if clusterID != "cluster-new" {
			t.Fatalf("expected cluster-new, got %q", clusterID)
		}
	})

	t.Run("keeps existing cluster when lookup fails", func(t *testing.T) {
		getNodeOwnerFn = func(context.Context, string) (*pb.NodeOwnerResponse, error) {
			return nil, errors.New("qm unavailable")
		}

		clusterID := reconcileNodeCluster(context.Background(), "node-1", "cluster-old", logger)
		if clusterID != "cluster-old" {
			t.Fatalf("expected cluster-old, got %q", clusterID)
		}
	})

	t.Run("fills missing cluster when quartermaster has it", func(t *testing.T) {
		getNodeOwnerFn = func(context.Context, string) (*pb.NodeOwnerResponse, error) {
			return &pb.NodeOwnerResponse{OwnerTenantId: strPtr("tenant-1"), ClusterId: "cluster-1"}, nil
		}

		clusterID := reconcileNodeCluster(context.Background(), "node-1", "", logger)
		if clusterID != "cluster-1" {
			t.Fatalf("expected cluster-1, got %q", clusterID)
		}
	})
}

func strPtr(v string) *string { return &v }
