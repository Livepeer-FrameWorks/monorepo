package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"frameworks/api_dns/internal/logic"

	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/sirupsen/logrus"
)

type trackingQMClient struct {
	listClustersCount int
}

func (t *trackingQMClient) ListHealthyNodesForDNS(_ context.Context, _ int, _ string) (*quartermasterpb.ListHealthyNodesForDNSResponse, error) {
	return nil, errors.New("intentional test stub")
}

func (t *trackingQMClient) ListHealthyNodesForDNSForCluster(_ context.Context, _ int, _ string, _ string) (*quartermasterpb.ListHealthyNodesForDNSResponse, error) {
	return nil, errors.New("intentional test stub")
}

func (t *trackingQMClient) ListClusters(_ context.Context, _ *commonpb.CursorPaginationRequest) (*quartermasterpb.ListClustersResponse, error) {
	t.listClustersCount++
	return &quartermasterpb.ListClustersResponse{}, nil
}

func (t *trackingQMClient) GetCluster(_ context.Context, _ string) (*quartermasterpb.ClusterResponse, error) {
	return &quartermasterpb.ClusterResponse{}, nil
}

func (t *trackingQMClient) ListTLSBundles(_ context.Context, _ string, _ *commonpb.CursorPaginationRequest) (*quartermasterpb.ListTLSBundlesResponse, error) {
	return &quartermasterpb.ListTLSBundlesResponse{}, nil
}

func (t *trackingQMClient) ListServiceInstancesByType(_ context.Context, _ string, _ string, _ int32) (*quartermasterpb.ListServiceInstancesByTypeResponse, error) {
	return &quartermasterpb.ListServiceInstancesByTypeResponse{}, nil
}

func (t *trackingQMClient) ListIngressSites(_ context.Context, _ string, _ string, _ *commonpb.CursorPaginationRequest) (*quartermasterpb.ListIngressSitesResponse, error) {
	return &quartermasterpb.ListIngressSitesResponse{}, nil
}

func TestReconciler_CallsSyncServiceByClusterForClusterScopedTypes(t *testing.T) {
	qm := &trackingQMClient{}
	logger := logrus.New()
	logger.SetLevel(logrus.FatalLevel)

	dnsManager := logic.NewDNSManager(nil, qm, logger, "example.com", 60, 60, 5*time.Minute, logic.MonitorConfig{})

	reconciler := NewDNSReconciler(dnsManager, nil, qm, logger, time.Hour, "example.com", "", []string{
		"edge-egress",
		"edge-ingest",
		"edge-storage",
		"edge-processing",
		"foghorn",
		"livepeer-gateway",
		"bridge",
		"chartroom",
	}, 300)

	reconciler.reconcile(context.Background())

	// SyncServiceByCluster calls ListClusters once per cluster-scoped type.
	// edge-egress, edge-ingest, edge-storage, edge-processing, foghorn, and
	// livepeer-gateway trigger it (6 calls). bridge and chartroom do not.
	if qm.listClustersCount != 6 {
		t.Fatalf("expected ListClusters called 6 times (edge-egress, edge-ingest, edge-storage, edge-processing, foghorn, livepeer-gateway), got %d", qm.listClustersCount)
	}
}

func TestUsesBunnyClusterDNSOnlyForEdgeClusters(t *testing.T) {
	if !usesBunnyClusterDNS(&quartermasterpb.InfrastructureCluster{ClusterType: "edge"}) {
		t.Fatal("expected edge cluster to use Bunny DNS")
	}
	if usesBunnyClusterDNS(&quartermasterpb.InfrastructureCluster{ClusterType: "central"}) {
		t.Fatal("expected central cluster to stay out of Bunny DNS")
	}
}
