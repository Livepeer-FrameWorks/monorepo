package triggers

import (
	"context"
	"strings"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

func TestStreamSourcePullResolutionUsesCurrentSharedState(t *testing.T) {
	for _, prefix := range []string{"live+", "pull+", "dvr+", ""} {
		t.Run(prefix, func(t *testing.T) {
			server := miniredis.RunT(t)
			client := goredis.NewClient(&goredis.Options{Addr: server.Addr(), MaxRetries: -1})
			t.Cleanup(func() { _ = client.Close() })
			store := control.NewRedisRegistryStore(client, "cell")
			registry := control.NewStreamRegistry(nil, "cell", time.Minute)
			previous := control.StreamRegistryInstance
			control.SetStreamRegistry(registry)
			t.Cleanup(func() { registry.DisableRedisSync(); control.SetStreamRegistry(previous) })
			// Stop changelog delivery so the local cache deliberately lags Redis.
			stopped, stop := context.WithCancel(context.Background())
			stop()
			if _, _, err := registry.EnableRedisSync(stopped, store, "lagging", logging.NewLogger()); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			sm := state.ResetDefaultManagerForTests()
			t.Cleanup(sm.Shutdown)
			sm.SetNodeConnectionInfo(ctx, "edge", "", "owner", "dest-cluster", nil)
			storageName := "stream"
			if prefix == "dvr+" {
				storageName = prefix + "stream"
			}
			original, err := registry.RecordInboundPull(ctx, storageName, control.InboundPull{
				TenantID: "tenant", SourceClusterID: "source-cell", SourceNodeID: "publisher",
				DestClusterID: "dest-cluster", DestNodeID: "edge", DTSCURL: "dtsc://source/" + prefix + "stream",
			})
			if err != nil {
				t.Fatal(err)
			}
			processor := newTestProcessor(t)
			admitPreparedSourceForTest(t, processor, prefix+"stream", original)
			trigger := &ipcpb.MistTrigger{NodeId: "edge", TriggerPayload: &ipcpb.MistTrigger_StreamSource{StreamSource: &ipcpb.StreamSourceTrigger{StreamName: prefix + "stream"}}}
			if response, abort, sourceErr := processor.handleStreamSource(trigger); sourceErr != nil || abort || response != original.DTSCURL {
				t.Fatalf("prepared source unavailable: %q, %v", response, sourceErr)
			}
			if _, _, err = store.MutateInboundPull(ctx, storageName, "edge", "other-replica", func(pull control.InboundPull, found bool) (control.InboundPull, error) {
				if !found {
					t.Fatal("shared pull missing")
				}
				pull.Cleared = true
				return pull, nil
			}); err != nil {
				t.Fatal(err)
			}
			if _, found := registry.InboundPullForNode(storageName, "edge"); !found {
				t.Fatal("fixture did not preserve a stale cached pull")
			}
			if response, abort, sourceErr := processor.handleStreamSource(trigger); sourceErr != nil || abort || response != control.OfflineNotPlaced {
				t.Fatalf("cleared pull enabled cached URL or configured fallback: %q, %v", response, sourceErr)
			}
			server.SetError("coordination unavailable")
			if response, abort, sourceErr := processor.handleStreamSource(trigger); sourceErr != nil || abort || response != control.OfflineUnavailable {
				t.Fatalf("storage outage enabled cached URL or configured fallback: %q, %v", response, sourceErr)
			}
			server.SetError("")
		})
	}
}

func TestStreamSourcePreparedPullRejectsDestinationReassignment(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	sm.SetNodeConnectionInfo(context.Background(), "edge", "", "owner", "new-cluster", nil)
	registry := control.NewStreamRegistry(nil, "cell", time.Minute)
	previous := control.StreamRegistryInstance
	control.SetStreamRegistry(registry)
	t.Cleanup(func() { control.SetStreamRegistry(previous) })
	if _, err := registry.RecordInboundPull(context.Background(), "stream", control.InboundPull{
		TenantID: "tenant", SourceClusterID: "source", SourceNodeID: "publisher", DestClusterID: "old-cluster", DestNodeID: "edge", DTSCURL: "dtsc://source/live+stream",
	}); err != nil {
		t.Fatal(err)
	}
	processor := newTestProcessor(t)
	if response, handled := processor.federationOriginPullDTSC(context.Background(), "live+stream", "edge"); !handled || response != control.OfflineNotPlaced {
		t.Fatalf("reassigned destination used old placement: %q, %v", response, handled)
	}
}

func TestStreamSourceOtherDestinationUsesBoundResolverNotConfiguredFallback(t *testing.T) {
	t.Setenv("BRAND_DOMAIN", "frameworks.network")
	t.Setenv("FOGHORN_BALANCER_CAPABILITY_SECRET", "test-capability-secret")
	registry := control.NewStreamRegistry(nil, "cell", time.Minute)
	previous := control.StreamRegistryInstance
	control.SetStreamRegistry(registry)
	t.Cleanup(func() { control.SetStreamRegistry(previous) })
	markReplicatingForTest(t, registry, "stream", "source", "dtsc://source/live+stream", "other-edge", "https://other.example", "publisher")
	state.DefaultManager().SetNodeConnectionInfo(context.Background(), "new-edge", "new-edge:18090", "", "cell", nil)
	processor := newTestProcessor(t)
	processor.clusterID = "cell"
	if response, handled := processor.federationOriginPullDTSC(context.Background(), "live+stream", "new-edge"); !handled || !strings.HasPrefix(response, "balance:") {
		t.Fatalf("another edge's pull prevented exact-node resolution or enabled configured fallback: %q, %v", response, handled)
	}
}

func TestStreamSourceCurrentPublisherIgnoresRetiredReplicaPull(t *testing.T) {
	registry := control.NewStreamRegistry(nil, "cell", time.Minute)
	previous := control.StreamRegistryInstance
	control.SetStreamRegistry(registry)
	t.Cleanup(func() { control.SetStreamRegistry(previous) })
	ctx := context.Background()
	pull, err := registry.RecordInboundPull(ctx, "stream", control.InboundPull{
		TenantID: "tenant", SourceClusterID: "source", SourceNodeID: "old-publisher", DestNodeID: "edge", DTSCURL: "dtsc://source/live+stream",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = registry.ClearInboundPull(ctx, "stream", "edge", pull.AttemptID); err != nil {
		t.Fatal(err)
	}
	if _, applied, projectErr := registry.ProjectSource("stream", "edge", 1, "publisher-event", "publisher-generation", 7); projectErr != nil || !applied {
		t.Fatalf("publisher projection failed: %v", projectErr)
	}
	processor := newTestProcessor(t)
	if response, handled := processor.federationOriginPullDTSC(ctx, "live+stream", "edge"); handled || response != "" {
		t.Fatalf("retired replica pull blocked current publisher source: %q, %v", response, handled)
	}
}

// markReplicatingForTest builds the pre-placement replication record these
// fixtures rely on: an inbound pull with no owner tenant and no destination
// cluster, which prepared-source admission can never accept.
func markReplicatingForTest(t *testing.T, r *control.StreamRegistry, internalName, peerClusterID, pullDTSCURL, destNodeID, destNodeBaseURL, pullSourceNodeID string) {
	t.Helper()
	if _, err := r.RecordInboundPull(context.Background(), internalName, control.InboundPull{
		SourceClusterID: peerClusterID, SourceNodeID: pullSourceNodeID,
		DestNodeID: destNodeID, DestNodeBaseURL: destNodeBaseURL, DTSCURL: pullDTSCURL,
	}); err != nil {
		t.Fatalf("record inbound pull for %s: %v", internalName, err)
	}
}
