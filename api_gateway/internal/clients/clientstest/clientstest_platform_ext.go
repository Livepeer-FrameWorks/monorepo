// Fake methods backing the platform-operator god view resolvers
// (Query.platform). Same contract as the rest of clientstest: each method is
// backed by a func field and panics when unstubbed so a test that forgot a
// stub fails loudly instead of returning a zero value.

package clientstest

import (
	"context"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/periscope"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

// The platform resolvers fan calls out concurrently (per-cluster tenant
// lookups, content sub-reads), so these fakes guard the Calls counter with
// the fake's mutex. Reads of Calls after the resolver returns are ordered
// by the resolver's own WaitGroup.
func (f *FakePeriscope) incCalls() {
	f.mu.Lock()
	f.Calls++
	f.mu.Unlock()
}

func (f *FakeQuartermaster) incCalls() {
	f.mu.Lock()
	f.Calls++
	f.mu.Unlock()
}

func (f *FakePurser) incCalls() {
	f.mu.Lock()
	f.Calls++
	f.mu.Unlock()
}

func (f *FakeCommodore) incCalls() {
	f.mu.Lock()
	f.Calls++
	f.mu.Unlock()
}

func (f *FakePeriscope) ListTenantActivity(ctx context.Context, timeRange *periscope.TimeRangeOpts, tenantIDs []string, limit int32) (*periscopepb.ListTenantActivityResponse, error) {
	f.incCalls()
	if f.ListTenantActivityFn == nil {
		panic("FakePeriscope.ListTenantActivity not stubbed")
	}
	return f.ListTenantActivityFn(ctx, timeRange, tenantIDs, limit)
}

func (f *FakeQuartermaster) ListTenants(ctx context.Context, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListTenantsResponse, error) {
	f.incCalls()
	if f.ListTenantsFn == nil {
		panic("FakeQuartermaster.ListTenants not stubbed")
	}
	return f.ListTenantsFn(ctx, pagination)
}

func (f *FakeQuartermaster) ListClusters(ctx context.Context, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListClustersResponse, error) {
	f.incCalls()
	if f.ListClustersFn == nil {
		panic("FakeQuartermaster.ListClusters not stubbed")
	}
	return f.ListClustersFn(ctx, pagination)
}

func (f *FakeQuartermaster) GetTenantsByCluster(ctx context.Context, clusterID string, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.GetTenantsByClusterResponse, error) {
	f.incCalls()
	if f.GetTenantsByClusterFn == nil {
		panic("FakeQuartermaster.GetTenantsByCluster not stubbed")
	}
	return f.GetTenantsByClusterFn(ctx, clusterID, pagination)
}

func (f *FakePurser) ListTenantBillingSnapshots(ctx context.Context, tenantIDs []string, limit int32) (*purserpb.ListTenantBillingSnapshotsResponse, error) {
	f.incCalls()
	if f.ListTenantBillingSnapshotsFn == nil {
		panic("FakePurser.ListTenantBillingSnapshots not stubbed")
	}
	return f.ListTenantBillingSnapshotsFn(ctx, tenantIDs, limit)
}

func (f *FakeCommodore) GetTenantUserCount(ctx context.Context, tenantID string) (*commodorepb.GetTenantUserCountResponse, error) {
	// Called concurrently by DoPlatformTenantContent's fan-out.
	f.incCalls()
	if f.GetTenantUserCountFn == nil {
		panic("FakeCommodore.GetTenantUserCount not stubbed")
	}
	return f.GetTenantUserCountFn(ctx, tenantID)
}
