package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"frameworks/api_dns/internal/logic"
	"frameworks/pkg/proto"

	"github.com/sirupsen/logrus"
)

type trackingQMClient struct {
	listClustersCount int
}

func (t *trackingQMClient) ListHealthyNodesForDNS(_ context.Context, _ string, _ int) (*proto.ListHealthyNodesForDNSResponse, error) {
	return nil, errors.New("intentional test stub")
}

func (t *trackingQMClient) ListClusters(_ context.Context, _ *proto.CursorPaginationRequest) (*proto.ListClustersResponse, error) {
	t.listClustersCount++
	return &proto.ListClustersResponse{}, nil
}

func TestReconciler_CallsSyncServiceByClusterForClusterScopedTypes(t *testing.T) {
	qm := &trackingQMClient{}
	logger := logrus.New()
	logger.SetLevel(logrus.FatalLevel)

	dnsManager := logic.NewDNSManager(nil, qm, logger, "example.com", 60, 60, 5*time.Minute, logic.MonitorConfig{})

	reconciler := NewDNSReconciler(dnsManager, nil, qm, logger, time.Hour, "example.com", "", []string{
		"edge-egress",
		"edge-ingest",
		"foghorn",
		"gateway",
		"chartroom",
	})

	reconciler.reconcile(context.Background())

	// SyncServiceByCluster calls ListClusters once per cluster-scoped type.
	// Only edge-egress, edge-ingest, foghorn trigger it (3 calls).
	// gateway and chartroom do not.
	if qm.listClustersCount != 3 {
		t.Fatalf("expected ListClusters called 3 times (edge-egress, edge-ingest, foghorn), got %d", qm.listClustersCount)
	}
}
