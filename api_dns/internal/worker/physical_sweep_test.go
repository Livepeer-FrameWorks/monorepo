package worker

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_dns/internal/logic"

	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/sirupsen/logrus"
)

// slowListingQMClient lists one gateway instance, holding each listing for
// hold so concurrent sweeps overlap, and counts the listings and the per-node
// ingress lookups.
type slowListingQMClient struct {
	trackingQMClient
	hold           time.Duration
	instanceLists  atomic.Int32
	ingressLookups atomic.Int32
}

func (c *slowListingQMClient) ListServiceInstancesByType(_ context.Context, _ string, _ string, _ int32) (*quartermasterpb.ListServiceInstancesByTypeResponse, error) {
	c.instanceLists.Add(1)
	time.Sleep(c.hold)
	return &quartermasterpb.ListServiceInstancesByTypeResponse{Instances: []*quartermasterpb.PhysicalServiceInstance{{
		NodeId: "node-1", ClusterId: "cluster-a", PublicInstanceHost: "livepeer-gateway.node-1.infra.example.com", ExternalIp: "203.0.113.10",
	}}}, nil
}

func (c *slowListingQMClient) ListIngressSites(_ context.Context, _ string, _ string, _ *commonpb.CursorPaginationRequest) (*quartermasterpb.ListIngressSitesResponse, error) {
	c.ingressLookups.Add(1)
	return &quartermasterpb.ListIngressSitesResponse{}, nil
}

// TestSyncPhysicalInstanceEndpointsForTypeCoalescesConcurrentWakes sends the
// burst of SyncDNS wakes Quartermaster fires for one health change (one per
// served cluster). They share sweeps: at most the running one and the one
// queued behind it, not one full listing per wake.
func TestSyncPhysicalInstanceEndpointsForTypeCoalescesConcurrentWakes(t *testing.T) {
	qm := &slowListingQMClient{hold: 200 * time.Millisecond}
	logger := logrus.New()
	logger.SetLevel(logrus.FatalLevel)
	dnsManager := logic.NewDNSManager(nil, qm, logger, "example.com", 60, 60, 5*time.Minute, logic.MonitorConfig{})
	reconciler := NewDNSReconciler(dnsManager, nil, qm, logger, time.Hour, "example.com", "", []string{"livepeer-gateway"}, 300)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := reconciler.SyncPhysicalInstanceEndpointsForType(ctx, "livepeer-gateway"); err != nil {
				t.Errorf("sync: %v", err)
			}
		})
	}
	wg.Wait()

	if n := qm.instanceLists.Load(); n > 2 {
		t.Fatalf("8 concurrent wakes listed instances %d times, want at most 2 (the running sweep and one queued behind it)", n)
	}
	if lists, lookups := qm.instanceLists.Load(), qm.ingressLookups.Load(); lookups != lists {
		t.Fatalf("ingress lookups = %d for %d sweeps of one instance", lookups, lists)
	}
}
