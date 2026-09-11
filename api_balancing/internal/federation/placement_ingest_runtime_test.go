package federation

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
)

func ingestRuntimeFixture(t *testing.T, protocol string) (*PlacementDestination, *LiveIngestPreparationRuntime, *discoveryFixture, *placementpb.PreparePlacementRequest) {
	t.Helper()
	store, _, req := placementReceiptFixture(t)
	f := newDiscoveryFixture(t)
	f.query.Verb, f.query.Protocol, f.query.SourceGeneration = placementpb.Verb_VERB_INGEST, protocol, ""
	f.paths.SourceGeneration = ""
	authority, err := balancer.CompilePlacementAuthority(f.pair, placement.Ingest, f.now)
	if err != nil {
		t.Fatal(err)
	}
	f.query.PolicyDigest = authority.PolicyDigest
	f.discovery.Now = store.Now
	for i := range f.snapshot.Nodes {
		f.snapshot.Nodes[i].Outputs = map[string]any{"RTMP": "rtmps://HOST:2935/play/$", "TSSRT": "srt://HOST:9889?streamid=$", "WebRTC": "https://HOST:8443/webrtc/$"}
	}
	req.Query, req.NodeId = f.query, "node-00"
	media := &LiveIngestPreparationRuntime{CellID: store.CellID, Authority: f.discovery.Authority, Snapshot: f.discovery.Snapshot, Now: store.Now}
	destination := &PlacementDestination{Discovery: f.discovery, Receipts: store, Now: store.Now}
	transport := PlacementTransport{LocalCellID: store.CellID, Local: destination}
	destination.Runtime = &PolicyBoundPlacementRuntime{Policy: &PlacementPolicyGate{CellID: store.CellID,
		Authority: f.discovery.Authority, Router: transport.Router(), Now: store.Now,
		IngestFence: placementFenceFunc(func(context.Context, string, string) (string, error) { return "", nil }),
	}, Media: &PlacementMediaRuntime{Ingest: media}}
	return destination, media, f, req
}

func TestIngestPreparationConfirmsExactListenerWithoutClaimOrCredential(t *testing.T) {
	for _, protocol := range []string{"rtmp", "srt", "whip"} {
		t.Run(protocol, func(t *testing.T) {
			destination, media, _, req := ingestRuntimeFixture(t, protocol)
			ctx := context.Background()
			response, err := destination.PreparePlacement(ctx, req)
			if err != nil || response.GetReady() || response.GetOutcome() != placementpb.PreparationOutcome_PREPARATION_OUTCOME_ACCEPTED || response.GetNodeId() != req.NodeId || response.GetClusterId() != "us" || !strings.Contains(response.GetEndpoint(), "node-00.example") {
				t.Fatalf("exact ingest preparation failed: %v, %v", response, err)
			}
			if !mist.ValidIngestEndpointTemplate(response.Endpoint, protocol) || strings.Count(response.Endpoint, "$") != 1 || response.TenantAuthorityVersion != 3 || response.ObjectAuthorityVersion != 5 {
				t.Fatalf("listener template or signed version binding changed: %v", response)
			}
			if bound := mist.BindIngestEndpointTemplate(response.Endpoint, protocol, "owner-secret"); bound == "" || !strings.Contains(bound, "owner-secret") {
				t.Fatal("credential-owning front door could not bind confirmed endpoint")
			}
			replay, err := destination.PreparePlacement(ctx, req)
			if err != nil || !proto.Equal(response, replay) {
				t.Fatalf("replay changed endpoint or extended receipt: %v, %v", replay, err)
			}
			receipt, err := destination.Receipts.Begin(ctx, req)
			if err != nil || receipt.Pull != nil {
				t.Fatal("ingest preparation created a media pull")
			}
			if _, err := media.Reconcile(ctx, req, PlacementReceipt{}, func(*PlacementPullBinding) error {
				t.Fatal("ingest preparation attempted a physical pull binding")
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestIngestPreparationReplayRequiresCurrentListenerAndPolicy(t *testing.T) {
	for _, change := range []string{"listener", "public-address", "public-origin-only", "inactive", "capability", "heartbeat", "metrics", "full", "membership", "authority-version", "consent", "active-owner", "unknown-owner", "cancel"} {
		t.Run(change, func(t *testing.T) {
			destination, _, f, req := ingestRuntimeFixture(t, "rtmp")
			if change == "public-origin-only" {
				f.snapshot.Nodes[0].Outputs["RTMP"] = "rtmps://fixed-listener.example:2935/play/$"
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if _, err := destination.PreparePlacement(ctx, req); err != nil {
				t.Fatal(err)
			}
			gate := destination.Runtime.(*PolicyBoundPlacementRuntime).Policy
			switch change {
			case "listener":
				f.snapshot.Nodes[0].Outputs["RTMP"] = "rtmps://HOST:3935/play/$"
			case "public-address", "public-origin-only":
				f.snapshot.Nodes[0].Host = "https://another.example"
			case "inactive":
				f.snapshot.Nodes[0].IsActive = false
			case "capability":
				f.snapshot.Nodes[0].CapIngest = false
			case "heartbeat":
				f.snapshot.Nodes[0].LastHeartbeat = f.now.Add(-31 * time.Second)
			case "metrics":
				f.snapshot.Nodes[0].MetricsObservedAt = f.now.Add(-31 * time.Second)
			case "full":
				f.snapshot.Nodes[0].DownSpeed = f.snapshot.Nodes[0].BWLimit
			case "membership":
				f.inventory.Nodes[0].AdmissionEnabled = false
			case "authority-version":
				f.pair.Tenant.Version++
			case "consent":
				f.pair.Tenant.Authority.EffectiveClusterGrants[0].MediaConsent.AllowIngest = false
			case "active-owner":
				gate.IngestFence = placementFenceFunc(func(context.Context, string, string) (string, error) { return "empty", nil })
			case "unknown-owner":
				gate.IngestFence = placementFenceFunc(func(context.Context, string, string) (string, error) { return "", errors.New("ownership unavailable") })
			case "cancel":
				cancel()
			}
			if response, err := destination.PreparePlacement(ctx, req); err == nil || response != nil {
				t.Fatalf("changed %s replayed stale endpoint: %v, %v", change, response, err)
			}
		})
	}
}

func TestIngestPreparationRejectsPullReceiptsAndWrongVerb(t *testing.T) {
	_, media, _, req := ingestRuntimeFixture(t, "rtmp")
	ctx := context.Background()
	for _, receipt := range []PlacementReceipt{{Pull: placementReceiptPull()}, {Response: &placementpb.Preparation{Ready: true}}} {
		if err := media.Revalidate(ctx, req, receipt); err == nil {
			t.Fatal("ingest accepted a pull or publisher readiness claim")
		}
	}
	if _, err := media.Reconcile(ctx, req, PlacementReceipt{Pull: placementReceiptPull()}, nil); err == nil {
		t.Fatal("ingest reconciled a source pull")
	}
	req.Query.Verb = placementpb.Verb_VERB_SERVE
	if _, err := media.Reconcile(ctx, req, PlacementReceipt{}, nil); err == nil {
		t.Fatal("serve dispatched to ingest")
	}
	mux := &PlacementMediaRuntime{Ingest: media}
	if _, err := mux.Reconcile(ctx, req, PlacementReceipt{}, nil); err == nil {
		t.Fatal("missing serve handler fell back to ingest")
	}
	if err := mux.Revalidate(ctx, nil, PlacementReceipt{}); err == nil {
		t.Fatal("missing verb was accepted")
	}
}
