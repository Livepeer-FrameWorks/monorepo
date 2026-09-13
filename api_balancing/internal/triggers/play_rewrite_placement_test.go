package triggers

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"frameworks/api_balancing/internal/federation"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestPlayRewritePlacementPrecedesMediaAndViewerEffects(t *testing.T) {
	for _, scenario := range []string{"allow", "deny", "expired", "wrong protocol", "unavailable", "unknown metadata protocol", "missing node", "missing cluster"} {
		t.Run(scenario, func(t *testing.T) {
			sm := resetStateTrigHandlers(t)
			p := newTestProcessor(t)
			p.streamCache.Set("tenant:stream", streamContext{TenantID: "tenant", BillingModel: "postpaid", RequiresAuthKnown: true}, time.Minute)
			viewerID := sm.CreateVirtualViewer("node", "stream", "203.0.113.1")
			calls := 0
			p.SetViewerPlacementAdmission(func(ctx context.Context, connection ViewerPlacementConnection) (federation.PlacementAdmissionDecision, error) {
				calls++
				if connection != (ViewerPlacementConnection{TenantID: "tenant", InternalName: "stream", ClusterID: "cluster", NodeID: "node", Connector: "HLS", ClientAddress: "203.0.113.1", PreSource: true}) {
					t.Fatalf("incorrect pre-source evidence: %+v", connection)
				}
				if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 3*time.Second {
					t.Fatal("pre-source admission exceeded trigger budget")
				}
				if stream := sm.GetStreamState("stream"); stream != nil && stream.Viewers != 0 {
					t.Fatal("viewer started before pre-source admission")
				}
				decision := federation.PlacementAdmissionDecision{TenantID: "tenant", ObjectID: "live_stream:stream", InternalName: "stream", ClusterID: "cluster", NodeID: "node", Protocol: "hls",
					SourceGeneration: "source", PolicyDigest: strings.Repeat("a", 64), PolicyRevision: 3, ParentRevision: 2, Verb: placement.Serve, ExpiresAt: time.Now().Add(10 * time.Second),
					TenantAuthorityVersion: 12, ObjectAuthorityVersion: 13}
				switch scenario {
				case "deny":
					return decision, errors.New("destination is not permitted")
				case "expired":
					decision.ExpiresAt = time.Now().Add(-time.Second)
				case "wrong protocol":
					decision.Protocol = "webrtc"
				}
				return decision, nil
			})
			trigger := &ipcpb.MistTrigger{NodeId: "node", TenantId: ptrTrigHandlers("tenant"), ClusterId: ptrTrigHandlers("cluster"), TriggerPayload: &ipcpb.MistTrigger_PlayRewrite{
				PlayRewrite: &ipcpb.ViewerResolveTrigger{RequestedStream: "live+stream", OutputType: "HLS", ViewerHost: "203.0.113.1",
					RequestUrl: "https://edge/hls/stream/index.m3u8?protocol=webrtc&tenant=other&policy_revision=999&fwcid=" + viewerID}}}
			switch scenario {
			case "unavailable":
				p.SetViewerPlacementAdmission(nil)
			case "unknown metadata protocol":
				trigger.GetPlayRewrite().OutputType = "UNKNOWN"
			case "missing node":
				trigger.NodeId = ""
			case "missing cluster":
				trigger.ClusterId = nil
			}
			response, abort, err := p.handlePlayRewrite(trigger)
			if scenario == "allow" {
				if err != nil || abort || response != "live+stream" {
					t.Fatalf("valid pre-source admission rejected: %q %t %v", response, abort, err)
				}
				if stream := sm.GetStreamState("stream"); stream == nil || stream.Viewers != 1 {
					t.Fatal("accepted request did not start its correlated viewer")
				}
			} else {
				if err == nil || response != "" {
					t.Fatalf("failed admission returned a usable source identity: %q %v", response, err)
				}
				if stream := sm.GetStreamState("stream"); stream != nil && stream.Viewers != 0 {
					t.Fatal("failed admission changed viewer state")
				}
				if trigger.GetPlayRewrite().ResolvedInternalName != nil {
					t.Fatal("failed admission reached analytics enrichment")
				}
			}
			wantCalls := 1
			if scenario == "unavailable" || scenario == "unknown metadata protocol" || scenario == "missing node" || scenario == "missing cluster" {
				wantCalls = 0
			}
			if calls != wantCalls {
				t.Fatalf("placement calls=%d want=%d", calls, wantCalls)
			}
		})
	}
}

func TestPlayRewriteMetadataRequiresPlacementWithoutStartingViewer(t *testing.T) {
	for _, scenario := range []string{"allow", "deny", "expired", "wrong protocol", "unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			sm := resetStateTrigHandlers(t)
			p := newTestProcessor(t)
			p.streamCache.Set("tenant:stream", streamContext{TenantID: "tenant", BillingModel: "postpaid", RequiresAuthKnown: true}, time.Minute)
			p.SetViewerPlacementAdmission(func(_ context.Context, connection ViewerPlacementConnection) (federation.PlacementAdmissionDecision, error) {
				if !connection.PreSource || connection.Connector != "HTTP" {
					t.Fatalf("metadata lost its trusted connector evidence: %+v", connection)
				}
				decision := placementDecisionForTest("tenant", "stream", "cluster", "node", "mist_html", placement.Serve)
				decision.SourceGeneration = "source"
				switch scenario {
				case "deny":
					return decision, errors.New("destination is not permitted")
				case "expired":
					decision.ExpiresAt = time.Now().Add(-time.Second)
				case "wrong protocol":
					decision.Protocol = "hls"
				}
				return decision, nil
			})
			if scenario == "unavailable" {
				p.SetViewerPlacementAdmission(nil)
			}
			trigger := &ipcpb.MistTrigger{NodeId: "node", TenantId: ptrTrigHandlers("tenant"), ClusterId: ptrTrigHandlers("cluster"), TriggerPayload: &ipcpb.MistTrigger_PlayRewrite{
				PlayRewrite: &ipcpb.ViewerResolveTrigger{RequestedStream: "live+stream", OutputType: "HTTP", ViewerHost: "203.0.113.1",
					RequestUrl: "https://edge/json_live+stream.js?protocol=hls"}}}
			response, _, err := p.handlePlayRewrite(trigger)
			if scenario == "allow" {
				if err != nil || response != "live+stream" {
					t.Fatalf("permitted metadata rejected: %q %v", response, err)
				}
			} else if err == nil || response != "" {
				t.Fatalf("metadata bypassed placement: %q %v", response, err)
			}
			if stream := sm.GetStreamState("stream"); stream != nil && stream.Viewers != 0 {
				t.Fatal("metadata request started a viewer")
			}
		})
	}
}
