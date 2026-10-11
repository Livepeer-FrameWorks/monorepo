package federation

import (
	"context"
	"errors"
	"testing"
	"time"

	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type placementStreamContextFunc func(context.Context, string, string, string, string) (*commodorepb.ResolveStreamContextResponse, error)

func (fn placementStreamContextFunc) ResolveStreamContext(ctx context.Context, stream, playback, internal, cluster string) (*commodorepb.ResolveStreamContextResponse, error) {
	return fn(ctx, stream, playback, internal, cluster)
}

func fenceCluster(ctx context.Context, reader *ConnectedPlacementIngestFence, identity PlacementIngestIdentity) (string, error) {
	ownership, err := reader.IngestOwnership(ctx, identity)
	if err == nil && ownership.Source != PlacementIngestOwnershipConnected {
		return "", errors.New("ownership did not come from a connected read: " + string(ownership.Source))
	}
	return ownership.Cluster, err
}

// An unreachable Commodore yields the signed outage owner, and the outage
// backoff answers following requests without asking Commodore until it lapses.
// A slow answer opens the backoff only after consecutive timeouts.
func TestConnectedPlacementIngestFenceFallsBackToSignedOutageOwner(t *testing.T) {
	for _, failure := range []codes.Code{codes.Unavailable, codes.DeadlineExceeded} {
		t.Run(failure.String(), func(t *testing.T) {
			now := time.Unix(1800000000, 0)
			reads := 0
			answer := failure
			reader := &ConnectedPlacementIngestFence{Now: func() time.Time { return now },
				Client: placementStreamContextFunc(func(context.Context, string, string, string, string) (*commodorepb.ResolveStreamContextResponse, error) {
					reads++
					if answer == codes.OK {
						return &commodorepb.ResolveStreamContextResponse{Admitted: true, TenantId: "tenant", StreamId: "stream", InternalName: "internal", IngestMode: "push", ActiveIngestClusterId: proto.String("eu")}, nil
					}
					return nil, status.Error(answer, "commodore unreachable")
				})}
			identity := PlacementIngestIdentity{TenantID: "tenant", ObjectID: sharedauthority.LiveStreamAuthorityID("stream"), InternalName: "internal", OutageIngestClusterID: "us"}
			wantReadsBeforeBackoff := 1
			if failure == codes.DeadlineExceeded {
				wantReadsBeforeBackoff = commodoreTimeoutsToBackOff
			}
			for range wantReadsBeforeBackoff + 2 {
				ownership, err := reader.IngestOwnership(context.Background(), identity)
				if err != nil || ownership != (PlacementIngestOwnership{Cluster: "us", Source: PlacementIngestOwnershipOutageOwner}) {
					t.Fatalf("unreachable Commodore = %+v, %v; want signed outage owner", ownership, err)
				}
			}
			if reads != wantReadsBeforeBackoff {
				t.Fatalf("Commodore reads = %d, want %d before the outage backoff answers", reads, wantReadsBeforeBackoff)
			}
			now = now.Add(commodoreOutageBackoff)
			answer = codes.OK
			ownership, err := reader.IngestOwnership(context.Background(), identity)
			if err != nil || ownership != (PlacementIngestOwnership{Cluster: "eu", Source: PlacementIngestOwnershipConnected}) || reads != wantReadsBeforeBackoff+1 {
				t.Fatalf("after the backoff = %+v, %v, reads=%d; want Commodore's lease", ownership, err, reads)
			}
		})
	}
}

func TestConnectedPlacementIngestFenceWithoutOutageOwnerFailsClosed(t *testing.T) {
	reader := &ConnectedPlacementIngestFence{Client: placementStreamContextFunc(func(context.Context, string, string, string, string) (*commodorepb.ResolveStreamContextResponse, error) {
		return nil, status.Error(codes.Unavailable, "commodore unreachable")
	})}
	for _, owner := range []string{"", " us"} {
		ownership, err := reader.IngestOwnership(context.Background(), PlacementIngestIdentity{TenantID: "tenant", ObjectID: sharedauthority.LiveStreamAuthorityID("stream"),
			InternalName: "internal", OutageIngestClusterID: owner})
		if status.Code(err) != codes.Unavailable || ownership != (PlacementIngestOwnership{}) {
			t.Fatalf("outage owner %q = %+v, %v; want Unavailable", owner, ownership, err)
		}
	}
}

func TestConnectedPlacementIngestFenceFollowsClientReplacement(t *testing.T) {
	var current PlacementStreamContextClient
	reads := 0
	reader := &ConnectedPlacementIngestFence{
		Client: placementStreamContextFunc(func(context.Context, string, string, string, string) (*commodorepb.ResolveStreamContextResponse, error) {
			t.Fatal("snapshot provider fell back to stale initial client")
			return nil, nil
		}),
		ClientSnapshot: func() PlacementStreamContextClient { reads++; return current },
	}
	identity := PlacementIngestIdentity{TenantID: "tenant", ObjectID: sharedauthority.LiveStreamAuthorityID("stream"), InternalName: "internal"}
	for _, owner := range []string{"", "eu", "us", ""} {
		current = nil
		calls := 0
		if owner != "" {
			current = placementStreamContextFunc(func(context.Context, string, string, string, string) (*commodorepb.ResolveStreamContextResponse, error) {
				calls++
				return &commodorepb.ResolveStreamContextResponse{Admitted: true, TenantId: "tenant", StreamId: "stream", InternalName: "internal", IngestMode: "push", ActiveIngestClusterId: proto.String(owner)}, nil
			})
		}
		before := reads
		got, err := fenceCluster(context.Background(), reader, identity)
		if reads != before+1 {
			t.Fatal("request did not capture exactly one client snapshot")
		}
		if owner == "" {
			if status.Code(err) != codes.Unavailable || got != "" || calls != 0 {
				t.Fatalf("disconnected client became known unowned: %q, %v", got, err)
			}
		} else if err != nil || got != owner || calls != 1 {
			t.Fatalf("replacement client not used: %q, %v, calls=%d", got, err, calls)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := reads
	if _, err := fenceCluster(ctx, reader, identity); status.Code(err) != codes.Canceled || reads != before {
		t.Fatal("canceled request consulted client provider")
	}
}

func TestConnectedPlacementIngestFenceIsScopedAndNonClaiming(t *testing.T) {
	for _, owned := range []bool{false, true} {
		response := &commodorepb.ResolveStreamContextResponse{Admitted: true, TenantId: "tenant", StreamId: "stream", InternalName: "internal", IngestMode: "push"}
		if owned {
			response.ActiveIngestClusterId = proto.String("eu")
		}
		reader := &ConnectedPlacementIngestFence{Client: placementStreamContextFunc(func(ctx context.Context, stream, playback, internal, cluster string) (*commodorepb.ResolveStreamContextResponse, error) {
			if stream != "" || playback != "" || internal != "internal" || cluster != "" {
				t.Fatal("ownership read asserted a candidate or another stream")
			}
			if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > time.Second {
				t.Fatal("ownership lookup has no bounded deadline")
			}
			return response, nil
		})}
		got, err := fenceCluster(context.Background(), reader, PlacementIngestIdentity{TenantID: "tenant", ObjectID: sharedauthority.LiveStreamAuthorityID("stream"), InternalName: "internal"})
		if err != nil || got != response.GetActiveIngestClusterId() {
			t.Fatalf("ownership=%q, %v", got, err)
		}
	}
}

func TestConnectedPlacementIngestFenceRejectsUnknownAndDeniedContext(t *testing.T) {
	for _, invalid := range []string{"nil", "denied", "tenant", "internal", "stream", "different-stream", "managed-source", "missing-mode", "suspended", "negative", "empty-owner", "padded-owner", "lookup-error", "cancelled"} {
		t.Run(invalid, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reader := &ConnectedPlacementIngestFence{Client: placementStreamContextFunc(func(context.Context, string, string, string, string) (*commodorepb.ResolveStreamContextResponse, error) {
				response := &commodorepb.ResolveStreamContextResponse{Admitted: true, TenantId: "tenant", StreamId: "stream", InternalName: "internal", IngestMode: "push"}
				switch invalid {
				case "nil":
					return nil, nil
				case "denied":
					response.Admitted = false
				case "tenant":
					response.TenantId = "other"
				case "internal":
					response.InternalName = "other"
				case "stream":
					response.StreamId = ""
				case "different-stream":
					response.StreamId = "replacement-stream"
				case "managed-source":
					response.IngestMode = "pull"
				case "missing-mode":
					response.IngestMode = ""
				case "suspended":
					response.IsSuspended = true
				case "negative":
					response.IsBalanceNegative = true
				case "empty-owner":
					response.ActiveIngestClusterId = proto.String("")
				case "padded-owner":
					response.ActiveIngestClusterId = proto.String(" eu")
				case "lookup-error":
					return nil, errors.New("control plane unavailable")
				case "cancelled":
					cancel()
				}
				return response, nil
			})}
			// A signed outage owner is present, so only an unreachable
			// Commodore could select it; a denied, mismatched or failed
			// answer stays a refusal.
			if got, err := fenceCluster(ctx, reader, PlacementIngestIdentity{TenantID: "tenant", ObjectID: sharedauthority.LiveStreamAuthorityID("stream"), InternalName: "internal",
				OutageIngestClusterID: "us"}); got != "" || err == nil {
				t.Fatalf("invalid context became owned or unowned: %q, %v", got, err)
			}
		})
	}
}

func TestConnectedPlacementIngestFenceCancelledBeforeLookup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reader := &ConnectedPlacementIngestFence{Client: placementStreamContextFunc(func(context.Context, string, string, string, string) (*commodorepb.ResolveStreamContextResponse, error) {
		t.Fatal("cancelled ownership read performed I/O")
		return nil, nil
	})}
	if _, err := fenceCluster(ctx, reader, PlacementIngestIdentity{TenantID: "tenant", ObjectID: sharedauthority.LiveStreamAuthorityID("stream"), InternalName: "internal"}); err == nil {
		t.Fatal("cancelled ownership read succeeded")
	}
}

func TestIngestPreparationBindsConnectedLeaseToSignedObject(t *testing.T) {
	for _, wrongObject := range []bool{false, true} {
		destination, _, _, req := ingestRuntimeFixture(t, "rtmp")
		calls := 0
		gate := destination.Runtime.(*PolicyBoundPlacementRuntime).Policy
		gate.IngestFence = &ConnectedPlacementIngestFence{Client: placementStreamContextFunc(func(_ context.Context, stream, playback, internal, cluster string) (*commodorepb.ResolveStreamContextResponse, error) {
			calls++
			if stream != "" || playback != "" || internal != req.Query.InternalName || cluster != "" {
				t.Fatal("preparation claimed an owner or changed lookup identity")
			}
			id := "stream"
			if wrongObject {
				id = "another-stream"
			}
			return &commodorepb.ResolveStreamContextResponse{Admitted: true, TenantId: req.Query.TenantId, InternalName: internal, StreamId: id, IngestMode: "push"}, nil
		})}
		response, err := destination.PreparePlacement(context.Background(), req)
		if wrongObject {
			if err == nil || response != nil || calls != 1 {
				t.Fatalf("foreign object ownership crossed gate: %v, %v, calls=%d", response, err, calls)
			}
		} else if err != nil || response == nil || calls < 2 {
			t.Fatalf("exact unclaimed object failed preparation/recheck: %v, %v, calls=%d", response, err, calls)
		}
	}
}

func TestConnectedPlacementIngestFenceRequiresCompleteIdentityBeforeIO(t *testing.T) {
	reader := &ConnectedPlacementIngestFence{Client: placementStreamContextFunc(func(context.Context, string, string, string, string) (*commodorepb.ResolveStreamContextResponse, error) {
		t.Fatal("incomplete signed identity performed an ownership lookup")
		return nil, nil
	})}
	for _, identity := range []PlacementIngestIdentity{
		{TenantID: "tenant", InternalName: "internal"},
		{ObjectID: sharedauthority.LiveStreamAuthorityID("stream"), InternalName: "internal"},
		{TenantID: "tenant", ObjectID: sharedauthority.LiveStreamAuthorityID("stream")},
	} {
		if _, err := fenceCluster(context.Background(), reader, identity); err == nil {
			t.Fatal("incomplete identity was accepted")
		}
	}
}
