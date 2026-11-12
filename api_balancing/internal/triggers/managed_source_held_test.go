package triggers

import (
	"context"
	"testing"
	"time"

	"frameworks/api_balancing/internal/appconfig"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"

	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// A running always-on source that restarts asks STREAM_SOURCE before it boots.
// At a spent balance that is the same stream continuing, not a new one: an
// offline answer would end it, and /source refuses a spent tenant's scope. An
// empty answer keeps the source configured on the node. Every other caller is
// still refused, and suspension still ends the source.
func TestStreamSourceRestartsHeldManagedSourceOnlyWhereItRuns(t *testing.T) {
	const (
		tenantID = "tenant-spent"
		name     = "60546679b497415db2338cd5cae54992"
		running  = "edge-running"
		other    = "edge-other"
	)
	for label, tc := range map[string]struct {
		node              string
		suspended         bool
		balanceNegative   bool
		retired           bool
		wantConfigured    bool
		wantAdmittedAtAll bool
	}{
		"spent on its node":     {node: running, balanceNegative: true, wantConfigured: true},
		"spent on another node": {node: other, balanceNegative: true},
		"suspended on its node": {node: running, balanceNegative: true, suspended: true},
		"retired on its node":   {node: running, balanceNegative: true, retired: true},
		"funded on its node":    {node: running, wantAdmittedAtAll: true},
	} {
		t.Run(label, func(t *testing.T) {
			useFoghornConfig(t, &appconfig.Foghorn{PlatformRootDomain: "frameworks.network", BalancerCapabilitySecret: "test-capability-secret"})
			state.DefaultManager().SetNodeConnectionInfo(context.Background(), tc.node, tc.node+":18090", "", "media-eu-1", nil)
			prior := control.StreamRegistryInstance
			registry := control.NewStreamRegistry(nil, "media-eu-1", time.Minute)
			control.SetStreamRegistry(registry)
			t.Cleanup(func() { control.SetStreamRegistry(prior) })
			registry.ManagedSetVerifiedFromHeartbeat(running, []*ipcpb.AppliedManagedStream{{
				StreamId: "stream-1", Name: name, Source: "ts-exec:cat /dev/null", AlwaysOn: true, IngestMode: "mist_native", TenantId: tenantID, PlacementRetired: tc.retired,
			}})
			commodoreClient, cleanup, stub := setupCommodoreClientWithStub(t, nil, nil)
			t.Cleanup(cleanup)
			admitted := &commodorepb.ResolveStreamContextResponse{
				Admitted: tc.wantAdmittedAtAll, IngestMode: "mist_native", StreamId: "stream-1", InternalName: name, TenantId: tenantID,
			}
			stub.resolveStreamContextByKey = map[string]*commodorepb.ResolveStreamContextResponse{"internal_name:" + name: admitted}

			processor := newTestProcessor(t)
			processor.commodoreClient = commodoreClient
			processor.clusterID = "media-eu-1"
			processor.streamCache.Set(streamContextCacheKey("live+"+name), streamContext{
				TenantID: tenantID, BillingModel: "prepaid", IsSuspended: tc.suspended, IsBalanceNegative: tc.balanceNegative,
			}, time.Minute)

			response, abort, err := processor.handleStreamSource(&ipcpb.MistTrigger{
				NodeId: tc.node, TriggerPayload: &ipcpb.MistTrigger_StreamSource{StreamSource: &ipcpb.StreamSourceTrigger{StreamName: name}},
			})
			if err != nil || abort {
				t.Fatalf("STREAM_SOURCE failed: response=%q abort=%v err=%v", response, abort, err)
			}
			if tc.wantConfigured && response != "" {
				t.Fatalf("held source restart answered %q, want the configured source", response)
			}
			if !tc.wantConfigured && response == "" {
				t.Fatal("STREAM_SOURCE handed the configured source to a stream that may not run it")
			}
		})
	}
}
