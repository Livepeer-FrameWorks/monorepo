package control

import (
	"context"
	"errors"
	"testing"

	pb "frameworks/pkg/proto"

	"github.com/sirupsen/logrus"
)

func TestReconcileNodeOwnership(t *testing.T) {
	original := getNodeOwnerFn
	t.Cleanup(func() { getNodeOwnerFn = original })

	logger := logrus.New()

	t.Run("uses quartermaster ownership when present", func(t *testing.T) {
		getNodeOwnerFn = func(ctx context.Context, nodeID string) (*pb.NodeOwnerResponse, error) {
			if nodeID != "node-1" {
				t.Fatalf("unexpected node id %q", nodeID)
			}
			return &pb.NodeOwnerResponse{OwnerTenantId: strPtr("tenant-new"), ClusterId: "cluster-new"}, nil
		}

		tenantID, clusterID := reconcileNodeOwnership(context.Background(), "node-1", "tenant-old", "cluster-old", logger)
		if tenantID != "tenant-new" {
			t.Fatalf("expected tenant-new, got %q", tenantID)
		}
		if clusterID != "cluster-new" {
			t.Fatalf("expected cluster-new, got %q", clusterID)
		}
	})

	t.Run("keeps existing ownership when lookup fails", func(t *testing.T) {
		getNodeOwnerFn = func(context.Context, string) (*pb.NodeOwnerResponse, error) {
			return nil, errors.New("qm unavailable")
		}

		tenantID, clusterID := reconcileNodeOwnership(context.Background(), "node-1", "tenant-old", "cluster-old", logger)
		if tenantID != "tenant-old" {
			t.Fatalf("expected tenant-old, got %q", tenantID)
		}
		if clusterID != "cluster-old" {
			t.Fatalf("expected cluster-old, got %q", clusterID)
		}
	})

	t.Run("fills missing cluster when quartermaster has it", func(t *testing.T) {
		getNodeOwnerFn = func(context.Context, string) (*pb.NodeOwnerResponse, error) {
			return &pb.NodeOwnerResponse{OwnerTenantId: strPtr("tenant-1"), ClusterId: "cluster-1"}, nil
		}

		tenantID, clusterID := reconcileNodeOwnership(context.Background(), "node-1", "tenant-1", "", logger)
		if tenantID != "tenant-1" {
			t.Fatalf("expected tenant-1, got %q", tenantID)
		}
		if clusterID != "cluster-1" {
			t.Fatalf("expected cluster-1, got %q", clusterID)
		}
	})
}

func strPtr(v string) *string { return &v }
