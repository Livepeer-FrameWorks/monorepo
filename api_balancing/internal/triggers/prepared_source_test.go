package triggers

import (
	"context"
	"errors"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/federation"
	localauthority "frameworks/api_balancing/internal/mediaauthority"
	"frameworks/api_balancing/internal/state"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	goredis "github.com/redis/go-redis/v9"
)

func TestPushSourceAdapterRejectsUnregisteredIdentityBeforeAuthorityRead(t *testing.T) {
	t.Cleanup(control.SetupTestRegistry("", nil))
	reads := 0
	adapter := &MediaSourcePlacementAdapter{
		Authority: viewerNamedAuthorityFunc(func(context.Context, string, string) (localauthority.PlacementPair, error) {
			reads++
			return localauthority.PlacementPair{}, errors.New("must not read authority without a connection")
		}),
		Push: &federation.LivePushPreparationRuntime{}, Receipts: &federation.PlacementReceiptStore{},
	}
	result, err := adapter.ResolveSource(context.Background(), PreparedSourceConnection{
		TenantID: "tenant", InternalName: "stream", ClusterID: "cluster", NodeID: "edge",
	})
	if err == nil || result.DTSCURL != "" || reads != 0 {
		t.Fatalf("unregistered source reached authority or escaped: %+v, %v, reads=%d", result, err, reads)
	}
}

func TestEverySourceRequiresAdmission(t *testing.T) {
	for _, scenario := range []string{"unmarked", "marked-missing", "retained-missing", "marked-accepted", "marked-denied"} {
		t.Run(scenario, func(t *testing.T) {
			processor := newTestProcessor(t)
			pull := control.InboundPull{TenantID: "tenant", DestClusterID: "us", DestNodeID: "edge", DTSCURL: "dtsc://source/live+stream", AttemptID: "attempt"}
			if scenario == "retained-missing" {
				pull.PlacementDemand = "retained-request"
			} else if scenario != "unmarked" {
				pull.PlacementRequired = true
			}
			calls := 0
			if scenario == "unmarked" || scenario == "marked-accepted" || scenario == "marked-denied" {
				processor.preparedSourceAdmission = func(context.Context, PreparedSourceConnection) (federation.PreparedPlacementSource, error) {
					calls++
					if scenario != "marked-accepted" {
						return federation.PreparedPlacementSource{}, errors.New("no receipt")
					}
					return federation.PreparedPlacementSource{DTSCURL: pull.DTSCURL, AttemptID: pull.AttemptID, ExpiresAt: time.Now().Add(time.Second)}, nil
				}
			}
			err := processor.checkPreparedSource(context.Background(), "live+stream", "edge", pull)
			allowed := scenario == "marked-accepted"
			if (err == nil) != allowed || scenario == "unmarked" && calls != 1 {
				t.Fatalf("source bypassed admission: err=%v, calls=%d", err, calls)
			}
		})
	}
}

func TestConfigureLivePreparedSourceAdmissionUsesDestinationRuntime(t *testing.T) {
	for _, missing := range []string{"none", "destination", "discovery", "receipts", "policy", "media", "serve", "authority", "already-installed", "receipt-cell", "receipt-store", "path-cell", "path-registry"} {
		t.Run(missing, func(t *testing.T) {
			processor := newTestProcessor(t)
			registry := control.NewStreamRegistry(nil, "cell", time.Minute)
			push := &federation.LivePushPlacementPaths{CellID: "cell", Registry: registry}
			paths := &federation.MediaPlacementPaths{Push: push}
			arrange := &federation.ArrangeOriginPullDeps{Registry: registry}
			serve := &federation.MediaServePreparationRuntime{CellID: "cell", Paths: paths,
				Registry: registry, Arrange: arrange,
				Push: &federation.LivePushPreparationRuntime{Authority: viewerPlacementPairReader{}, Registry: registry, Paths: push, Arrange: arrange}}
			media := &federation.PlacementMediaRuntime{Serve: serve}
			policy := &federation.PolicyBoundPlacementRuntime{Policy: &federation.PlacementPolicyGate{}, Media: media}
			client := goredis.NewClient(&goredis.Options{Addr: "unused:6379"})
			t.Cleanup(func() { _ = client.Close() })
			destination := &federation.PlacementDestination{Discovery: &federation.PlacementDiscovery{CellID: "cell", Paths: paths, Authority: viewerPlacementPairReader{}}, Runtime: policy, Receipts: &federation.PlacementReceiptStore{CellID: "cell", Client: client}}
			switch missing {
			case "destination":
				destination = nil
			case "discovery":
				destination.Discovery = nil
			case "receipts":
				destination.Receipts = nil
			case "policy":
				policy.Policy = nil
			case "media":
				policy.Media = nil
			case "serve":
				media.Serve = nil
			case "authority":
				destination.Discovery.Authority = nil
			case "receipt-cell":
				destination.Receipts.CellID = "other"
			case "receipt-store":
				destination.Receipts.Client = nil
			case "path-cell":
				push.CellID = "other"
			case "path-registry":
				push.Registry = nil
			case "already-installed":
				processor.SetPreparedSourceAdmission(func(context.Context, PreparedSourceConnection) (federation.PreparedPlacementSource, error) {
					return federation.PreparedPlacementSource{}, errors.New("existing")
				})
			}
			err := processor.ConfigureLivePreparedSourceAdmission(destination)
			if missing == "none" {
				if err != nil || processor.preparedSourceAdmission == nil || processor.preparedSourceRequired || processor.preparedSourceRegistry != registry {
					t.Fatalf("startup did not install scoped source runtime: %v", err)
				}
			} else if err == nil || processor.preparedSourceRegistry != nil || missing != "already-installed" && processor.preparedSourceAdmission != nil {
				t.Fatalf("incomplete startup installed source admission: %v", err)
			}
		})
	}
}

func TestStreamSourcePreparedAdmissionRequiresExactCurrentResult(t *testing.T) {
	for _, outcome := range []string{"accepted", "marked", "fresh-receipt", "denied", "wrong-url", "wrong-attempt", "expired", "renewed", "canceled", "missing", "cleared-adapter"} {
		t.Run(outcome, func(t *testing.T) {
			sm := state.ResetDefaultManagerForTests()
			t.Cleanup(sm.Shutdown)
			sm.SetNodeConnectionInfo(context.Background(), "edge", "", "owner", "us", nil)
			registry := control.NewStreamRegistry(nil, "cell", time.Minute)
			previous := control.StreamRegistryInstance
			control.SetStreamRegistry(registry)
			t.Cleanup(func() { control.SetStreamRegistry(previous) })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			pull, err := registry.RecordInboundPull(ctx, "stream", control.InboundPull{
				TenantID: "tenant", SourceClusterID: "source-cell", SourceMediaClusterID: "eu", SourceNodeID: "publisher",
				DestClusterID: "us", DestNodeID: "edge", DTSCURL: "dtsc://source/live+stream",
				PlacementRequired: outcome == "marked",
			})
			if err != nil {
				t.Fatal(err)
			}
			processor := newTestProcessor(t)
			calls := 0
			processor.SetPreparedSourceAdmission(func(gotCtx context.Context, connection PreparedSourceConnection) (federation.PreparedPlacementSource, error) {
				calls++
				if connection != (PreparedSourceConnection{TenantID: "tenant", InternalName: "stream", ClusterID: "us", NodeID: "edge"}) {
					t.Fatalf("source admission lost trusted identity: %+v", connection)
				}
				if deadline, ok := gotCtx.Deadline(); !ok || time.Until(deadline) > 5*time.Second {
					t.Fatal("source admission lost bounded source-resolution context")
				}
				result := federation.PreparedPlacementSource{DTSCURL: pull.DTSCURL, AttemptID: pull.AttemptID, ExpiresAt: time.Now().Add(time.Second)}
				switch outcome {
				case "fresh-receipt":
					result.ExpiresAt = time.Now().Add(placement.PreparationLifetime)
				case "denied":
					return federation.PreparedPlacementSource{}, errors.New("policy revoked")
				case "wrong-url":
					result.DTSCURL = "dtsc://another/live+stream"
				case "wrong-attempt":
					result.AttemptID = "another-attempt"
				case "expired":
					result.ExpiresAt = time.Now().Add(-time.Second)
				case "renewed":
					result.ExpiresAt = time.Now().Add(time.Minute)
				case "canceled":
					cancel()
				}
				return result, nil
			})
			switch outcome {
			case "marked":
				processor.preparedSourceRequired = false
				processor.preparedSourceRegistry = registry
				control.SetStreamRegistry(control.NewStreamRegistry(nil, "foreign-cell", time.Minute))
			case "missing":
				processor = newTestProcessor(t)
				processor.SetPreparedSourceAdmission(nil)
			case "cleared-adapter":
				processor.SetPreparedSourceAdmission(nil)
			}
			result, handled := processor.federationOriginPullDTSC(ctx, "live+stream", "edge")
			want := control.OfflineUnavailable
			if outcome == "accepted" || outcome == "marked" || outcome == "fresh-receipt" {
				want = pull.DTSCURL
			}
			if !handled || result != want {
				t.Fatalf("source gate escaped explicit decision: %q, handled=%v", result, handled)
			}
			if outcome != "missing" && outcome != "cleared-adapter" && calls != 1 {
				t.Fatalf("source admission calls=%d", calls)
			}
			if outcome == "accepted" {
				processor.SetPreparedSourceAdmission(func(context.Context, PreparedSourceConnection) (federation.PreparedPlacementSource, error) {
					return federation.PreparedPlacementSource{}, errors.New("revoked after first source response")
				})
				trigger := &ipcpb.MistTrigger{NodeId: "edge", TriggerPayload: &ipcpb.MistTrigger_StreamSource{StreamSource: &ipcpb.StreamSourceTrigger{StreamName: "live+stream"}}}
				if result, abort, retryErr := processor.handleStreamSource(trigger); retryErr != nil || abort || result != control.OfflineUnavailable {
					t.Fatalf("source retry reused earlier admission: %q, %v", result, retryErr)
				}
			}
		})
	}
}
