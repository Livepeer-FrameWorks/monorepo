package federation

import (
	"context"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	localauthority "frameworks/api_balancing/internal/mediaauthority"

	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
)

func pushSourceIdentity(req *placementpb.PreparePlacementRequest) PlacementSourceIdentity {
	return PlacementSourceIdentity{TenantID: req.Query.TenantId, ObjectID: req.Query.ObjectId, InternalName: req.Query.InternalName,
		DestinationFence: 9007199254740993,
		ClusterID:        req.ClusterId, NodeID: req.NodeId}
}

func TestPreparedPushSourceUsesCurrentLocalEvidenceWithoutRanking(t *testing.T) {
	destination, media, f, _, fed, req := pushRuntimeFixture(t)
	ctx := context.Background()
	response, err := destination.PreparePlacement(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	pull, found, err := media.Registry.CurrentInboundPull(ctx, req.Query.InternalName, req.NodeId)
	if err != nil || !found {
		t.Fatal("prepared pull missing")
	}
	if marked, markErr := media.Registry.MarkInboundDestinationObserved(ctx, req.Query.InternalName, req.NodeId, pull.AttemptID); markErr != nil || !marked {
		t.Fatalf("destination observation failed: %v", markErr)
	}
	f.calls = nil
	for range 2 {
		source, sourceErr := media.ResolvePreparedSource(ctx, destination.Receipts, pushSourceIdentity(req))
		if sourceErr != nil || control.SourcePullBaseURL(source.DTSCURL) != "dtsc://eu.example:14200/live+internal" || source.AttemptID != pull.AttemptID ||
			!source.ExpiresAt.Equal(response.ExpiresAt.AsTime()) {
			t.Fatalf("completed source was not resolved: %+v, %v", source, sourceErr)
		}
	}
	// Acceptance is still current, so resolution neither re-notifies the origin
	// nor arranges a second attempt for the same destination.
	if len(fed.calls) != 1 {
		t.Fatalf("source resolution re-notified the publisher: %d notifications", len(fed.calls))
	}
	for _, call := range fed.calls {
		if call.GetAttemptId() != pull.AttemptID {
			t.Fatalf("source resolution arranged a different pull attempt: %q", call.GetAttemptId())
		}
	}
	for _, call := range f.calls {
		if call == "inventory" || call == "paths" {
			t.Fatalf("source resolution repeated placement census: %v", f.calls)
		}
	}
}

func TestPreparedPushSourceRefusesChangedAuthorityAndPhysicalState(t *testing.T) {
	for _, change := range []string{"tenant-version", "object-version", "revoked", "consent", "policy", "cleared", "generation", "source-endpoint", "destination-listener", "destination-membership", "expired", "wrong-object", "wrong-tenant", "wrong-cell", "canceled"} {
		t.Run(change, func(t *testing.T) {
			destination, media, f, source, _, req := pushRuntimeFixture(t)
			ctx := context.Background()
			response, err := destination.PreparePlacement(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			input := pushSourceIdentity(req)
			switch change {
			case "tenant-version":
				f.pair.Tenant.Version++
			case "object-version":
				f.pair.Object.Version++
			case "revoked":
				f.pair.Object.Ready = false
			case "consent":
				f.pair.Tenant.Authority.EffectiveClusterGrants[0].MediaConsent.AllowExternalSource = false
			case "policy":
				f.pair.Object.Authority.MediaPlacement.Revision++
			case "cleared":
				pull, found, pullErr := media.Registry.CurrentInboundPull(ctx, input.InternalName, input.NodeID)
				if pullErr != nil || !found {
					t.Fatal("prepared pull missing")
				}
				if _, err = media.Registry.ClearInboundPull(ctx, input.InternalName, input.NodeID, pull.AttemptID); err != nil {
					t.Fatal(err)
				}
			case "generation", "source-endpoint":
				location := source.entry.Locations["eu-cell"]
				if change == "generation" {
					location.EdgeCandidates[0].SourceGeneration = "replacement"
					location.EdgeCandidates[0].SourceRevision++
				} else {
					location.EdgeCandidates[0].DTSCURL = "dtsc://new-source.example:14200/live+internal"
				}
				source.entry.Locations["eu-cell"] = location
			case "destination-listener":
				f.snapshot.Nodes[0].Outputs = nil
			case "destination-membership":
				f.snapshot.Nodes[0].ClusterID = "another-cluster"
			case "expired":
				media.Now = func() time.Time { return response.ExpiresAt.AsTime() }
			case "wrong-object":
				input.ObjectID = "another-object"
			case "wrong-tenant":
				input.TenantID = "another-tenant"
			case "wrong-cell":
				destination.Receipts = &PlacementReceiptStore{Client: destination.Receipts.Client, CellID: "another-cell", Now: destination.Now}
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			// The test reader returns the signed pair unchanged even for wrong input;
			// enforcement must independently compare the returned identity.
			media.Authority = placementAuthorityReaderFunc(func(context.Context, string, string, string) (localauthority.PlacementPair, error) {
				return f.pair, nil
			})
			resolved, resolveErr := media.ResolvePreparedSource(ctx, destination.Receipts, input)
			if resolveErr == nil || resolved.DTSCURL != "" {
				t.Fatalf("changed %s returned a source: %+v, %v", change, resolved, resolveErr)
			}
		})
	}
}

func TestPreparedPushSourceRechecksChangesDuringResolution(t *testing.T) {
	for _, change := range []string{"tenant-version", "object-version", "consent", "cleared", "canceled", "missing-epoch"} {
		t.Run(change, func(t *testing.T) {
			destination, media, f, _, _, req := pushRuntimeFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if _, err := destination.PreparePlacement(ctx, req); err != nil {
				t.Fatal(err)
			}
			reads := 0
			media.Authority = placementAuthorityReaderFunc(func(context.Context, string, string, string) (localauthority.PlacementPair, error) {
				reads++
				if reads == 3 {
					switch change {
					case "tenant-version":
						f.pair.Tenant.Version++
					case "object-version":
						f.pair.Object.Version++
					case "consent":
						f.pair.Tenant.Authority.EffectiveClusterGrants[0].MediaConsent.AllowExternalSource = false
					case "cleared":
						pull, found, err := media.Registry.CurrentInboundPull(ctx, req.Query.InternalName, req.NodeId)
						if err != nil || !found {
							t.Fatal("prepared pull missing")
						}
						if _, err = media.Registry.ClearInboundPull(ctx, req.Query.InternalName, req.NodeId, pull.AttemptID); err != nil {
							t.Fatal(err)
						}
					case "canceled":
						cancel()
					case "missing-epoch":
						if err := destination.Receipts.Client.Del(ctx, "{"+destination.Receipts.CellID+"}:placement_attempt_epoch").Err(); err != nil {
							t.Fatal(err)
						}
					}
				}
				return f.pair, nil
			})
			resolved, err := media.ResolvePreparedSource(ctx, destination.Receipts, pushSourceIdentity(req))
			if err == nil || resolved.DTSCURL != "" || reads != 3 {
				t.Fatalf("mid-read %s escaped resolution: %+v, %v, reads=%d", change, resolved, err, reads)
			}
		})
	}
}
