package logic

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_dns/internal/provider/bunny"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

// membershipTestManager returns a manager whose Bunny zones and Cloudflare
// cleanup bookkeeping are already settled, so a sync only reads Quartermaster
// and writes Bunny record sets.
func membershipTestManager(qm *fakeQuartermasterClient, bc *fakeBunnyClient, zones ...string) *DNSManager {
	m := newTestManager(&fakeCloudflareClient{})
	m.qmClient = qm
	m.bunnyClient = bc
	for i, zone := range zones {
		key := bunnyZoneCacheKey(zone)
		m.bunnyZoneCache[key] = &bunny.Zone{ID: int64(100 + i), Domain: zone}
		m.bunnyDelegationCheckedAt[key] = time.Now().Add(time.Hour)
		m.cloudflareCleanupAt[key] = time.Now().Add(time.Hour)
	}
	return m
}

func publishedA(name, ip string) bunny.Record {
	return bunny.Record{ID: int64(len(ip) + len(name)), Type: bunny.RecordTypeA, Name: name, Value: ip, TTL: 60}
}

func recordValues(records []bunny.Record) []string {
	out := make([]string, 0, len(records))
	for _, record := range records {
		out = append(out, record.Value)
	}
	slices.Sort(out)
	return out
}

// Production 2026-10-08: the only edge of a cell had a disconnected Helmsman, so
// Quartermaster reported one candidate and no healthy node. The targeted wake
// cleared the cell's edge records, its edge-<node> record and the global root
// for about an hour. An all-unhealthy set must leave every record in place.
func TestSyncServiceForClusterPreservesRecordsWhenCandidatesAreUnhealthy(t *testing.T) {
	qm := &fakeQuartermasterClient{
		clustersResponse: &quartermasterpb.ListClustersResponse{Clusters: []*quartermasterpb.InfrastructureCluster{{
			ClusterId:          "media-eu-1",
			ClusterType:        "edge",
			IsActive:           true,
			IsPlatformOfficial: true,
		}}},
		response: &quartermasterpb.ListHealthyNodesForDNSResponse{Nodes: []*quartermasterpb.InfrastructureNode{}, TotalNodes: 1},
	}
	nodeLabel := edgeNodeRecordLabel("edge-eu-1")
	var mu sync.Mutex
	var writes []string
	bc := &fakeBunnyClient{
		listRecords: func(ctx context.Context, zoneID int64) ([]bunny.Record, error) {
			return []bunny.Record{
				publishedA("edge-egress", "198.51.100.10"),
				publishedA(nodeLabel, "198.51.100.10"),
				publishedA("", "198.51.100.10"),
			}, nil
		},
		reconcileRecordSet: func(ctx context.Context, zoneID int64, name string, recordType int, desired []bunny.Record) error {
			mu.Lock()
			defer mu.Unlock()
			writes = append(writes, name)
			if len(desired) == 0 {
				t.Errorf("ReconcileRecordSet(zone=%d, name=%q, nil): an unhealthy-only set cleared a published record", zoneID, name)
			}
			return nil
		},
	}
	manager := membershipTestManager(qm, bc, "media-eu-1.example.com", "edge-egress.example.com")

	partialErrors, err := manager.SyncServiceForCluster(context.Background(), "edge-egress", "media-eu-1")
	if err != nil {
		t.Fatalf("SyncServiceForCluster returned error: %v", err)
	}
	if len(partialErrors) > 0 {
		t.Fatalf("unexpected partial errors: %v", partialErrors)
	}
	if len(writes) > 0 {
		t.Fatalf("Bunny writes for an all-unhealthy set = %v, want none", writes)
	}
}

func TestSyncBunnyRootServiceAuthoritativePreservesWhenCandidatesAreUnhealthy(t *testing.T) {
	qm := &fakeQuartermasterClient{
		clustersResponse: &quartermasterpb.ListClustersResponse{Clusters: []*quartermasterpb.InfrastructureCluster{{
			ClusterId:          "media-eu-1",
			IsActive:           true,
			IsPlatformOfficial: true,
		}}},
		response: &quartermasterpb.ListHealthyNodesForDNSResponse{Nodes: []*quartermasterpb.InfrastructureNode{}, TotalNodes: 1},
	}
	bc := &fakeBunnyClient{
		reconcileRecordSet: func(ctx context.Context, zoneID int64, name string, recordType int, desired []bunny.Record) error {
			t.Fatalf("ReconcileRecordSet(name=%q, desired=%v): an unhealthy-only set touched the global root", name, desired)
			return nil
		},
	}
	manager := membershipTestManager(qm, bc, "edge-ingest.example.com")

	if _, err := manager.syncBunnyRootService(context.Background(), "edge-ingest", true); err != nil {
		t.Fatalf("syncBunnyRootService returned error: %v", err)
	}
}

// A member that leaves the healthy set stays published until it has been out
// for the removal grace, including when Navigator has just restarted and has
// no memory of the member.
func TestSyncServiceForClusterKeepsUnhealthyMemberWithinRemovalGrace(t *testing.T) {
	qm := &fakeQuartermasterClient{
		clustersResponse: &quartermasterpb.ListClustersResponse{Clusters: []*quartermasterpb.InfrastructureCluster{{
			ClusterId:   "media-eu-1",
			ClusterType: "edge",
			IsActive:    true,
		}}},
		response: &quartermasterpb.ListHealthyNodesForDNSResponse{
			Nodes:        []*quartermasterpb.InfrastructureNode{{NodeId: "edge-eu-1", ClusterId: "media-eu-1", ExternalIp: strPtr("198.51.100.10")}},
			TotalNodes:   2,
			HealthyNodes: 1,
		},
	}
	published := []bunny.Record{
		publishedA("edge-ingest", "198.51.100.10"),
		publishedA("edge-ingest", "198.51.100.11"),
	}
	var got []bunny.Record
	bc := &fakeBunnyClient{
		listRecords: func(ctx context.Context, zoneID int64) ([]bunny.Record, error) {
			return published, nil
		},
		reconcileRecordSet: func(ctx context.Context, zoneID int64, name string, recordType int, desired []bunny.Record) error {
			if name == "edge-ingest" {
				got = append([]bunny.Record(nil), desired...)
			}
			return nil
		},
	}
	manager := membershipTestManager(qm, bc, "media-eu-1.example.com")
	now := time.Date(2026, 10, 8, 17, 52, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	resync := func() []string {
		t.Helper()
		got = nil
		if _, err := manager.SyncServiceForCluster(context.Background(), "edge-ingest", "media-eu-1"); err != nil {
			t.Fatalf("SyncServiceForCluster returned error: %v", err)
		}
		return recordValues(got)
	}
	both := []string{"198.51.100.10", "198.51.100.11"}

	if members := resync(); !slices.Equal(members, both) {
		t.Fatalf("edge-ingest members right after the node left the healthy set = %v, want %v", members, both)
	}
	now = now.Add(minMemberRemovalGrace - time.Second)
	if members := resync(); !slices.Equal(members, both) {
		t.Fatalf("edge-ingest members just inside the removal grace = %v, want %v", members, both)
	}
	now = now.Add(time.Second)
	if members := resync(); !slices.Equal(members, []string{"198.51.100.10"}) {
		t.Fatalf("edge-ingest members after the removal grace = %v, want only the healthy node", members)
	}

	// A member that comes back before the grace ends restarts its clock the
	// next time it leaves.
	manager = membershipTestManager(qm, bc, "media-eu-1.example.com")
	manager.now = func() time.Time { return now }
	resync()
	now = now.Add(time.Minute)
	healthy := qm.response
	qm.response = &quartermasterpb.ListHealthyNodesForDNSResponse{
		Nodes: []*quartermasterpb.InfrastructureNode{
			{NodeId: "edge-eu-1", ClusterId: "media-eu-1", ExternalIp: strPtr("198.51.100.10")},
			{NodeId: "edge-eu-2", ClusterId: "media-eu-1", ExternalIp: strPtr("198.51.100.11")},
		},
		TotalNodes:   2,
		HealthyNodes: 2,
	}
	resync()
	qm.response = healthy
	now = now.Add(minMemberRemovalGrace - time.Minute)
	if members := resync(); !slices.Equal(members, both) {
		t.Fatalf("edge-ingest members after a recovered member left again = %v, want %v (grace restarted)", members, both)
	}
}

// Concurrent wakes for one (cluster, service type) must not interleave their
// Quartermaster reads and Bunny writes; waiting wakes fold into one rerun.
func TestSyncServiceForClusterSerializesConcurrentWakes(t *testing.T) {
	qm := &fakeQuartermasterClient{
		clustersResponse: &quartermasterpb.ListClustersResponse{Clusters: []*quartermasterpb.InfrastructureCluster{{
			ClusterId:   "media-eu-1",
			ClusterType: "edge",
			IsActive:    true,
		}}},
		response: &quartermasterpb.ListHealthyNodesForDNSResponse{
			Nodes:        []*quartermasterpb.InfrastructureNode{{NodeId: "edge-eu-1", ClusterId: "media-eu-1", ExternalIp: strPtr("198.51.100.10")}},
			TotalNodes:   1,
			HealthyNodes: 1,
		},
	}
	var inFlight, maxInFlight atomic.Int32
	entered := make(chan struct{}, 16)
	bc := &fakeBunnyClient{
		listRecords: func(ctx context.Context, zoneID int64) ([]bunny.Record, error) {
			return []bunny.Record{publishedA("edge-ingest", "198.51.100.10")}, nil
		},
		reconcileRecordSet: func(ctx context.Context, zoneID int64, name string, recordType int, desired []bunny.Record) error {
			if name != "edge-ingest" {
				return nil
			}
			now := inFlight.Add(1)
			defer inFlight.Add(-1)
			for {
				prev := maxInFlight.Load()
				if now <= prev || maxInFlight.CompareAndSwap(prev, now) {
					break
				}
			}
			entered <- struct{}{}
			// Hold the write open long enough for every other wake to arrive.
			time.Sleep(200 * time.Millisecond)
			return nil
		},
	}
	manager := membershipTestManager(qm, bc, "media-eu-1.example.com")

	const wakes = 8
	var wg sync.WaitGroup
	for range wakes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := manager.SyncServiceForCluster(context.Background(), "edge-ingest", "media-eu-1"); err != nil {
				t.Errorf("SyncServiceForCluster returned error: %v", err)
			}
		}()
	}
	wg.Wait()
	close(entered)

	if got := maxInFlight.Load(); got != 1 {
		t.Fatalf("concurrent Bunny writes for one (cluster, service type) = %d, want 1", got)
	}
	qm.mu.Lock()
	runs := qm.getClusterCalls
	qm.mu.Unlock()
	if runs > 2 {
		t.Fatalf("syncs run for %d concurrent wakes = %d, want the waiting wakes folded into one rerun (<= 2)", wakes, runs)
	}
}
