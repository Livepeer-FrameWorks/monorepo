package control

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"frameworks/api_balancing/internal/balancer"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

type ingestPlacementPreparerFunc func(context.Context, IngestPlacementRequest) (balancer.PlacementPreparationResult, error)

func (fn ingestPlacementPreparerFunc) PrepareIngest(ctx context.Context, request IngestPlacementRequest) (balancer.PlacementPreparationResult, error) {
	return fn(ctx, request)
}

func preparedIngestFixture(t *testing.T, request IngestPlacementRequest) balancer.PlacementPreparationResult {
	t.Helper()
	now := time.Now()
	attempt, err := placement.NewPreparationAttemptID(now)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := map[string]string{"whip": "https://publish.example/webrtc/$", "rtmp": "rtmps://publish.example:2935/live/$", "srt": "srt://publish.example:9889?streamid=$"}[request.Protocol]
	return balancer.PlacementPreparationResult{Outcome: balancer.PlacementAccepted, TenantID: request.TenantID, ObjectID: sharedauthority.LiveStreamAuthorityID(request.StreamID),
		ClusterID: "selected-cluster", NodeID: "selected-node", Protocol: request.Protocol, Endpoint: endpoint, PublicBaseURL: "https://display.example:8443",
		AttemptID: attempt, ExpiresAt: now.Add(10 * time.Second)}
}

func TestPreparedIngestEndpointsBindOnlyConfirmedProtocols(t *testing.T) {
	for _, protocol := range []string{"", "whip", "rtmp", "srt"} {
		t.Run(protocol, func(t *testing.T) {
			var seen []string
			deps := &IngestDependencies{Protocol: protocol, GeoLat: math.NaN(), GeoLon: math.NaN(), Placement: ingestPlacementPreparerFunc(func(ctx context.Context, request IngestPlacementRequest) (balancer.PlacementPreparationResult, error) {
				seen = append(seen, request.Protocol)
				if request.TenantID != "tenant" || request.StreamID != "stream" || request.InternalName != "internal" || request.Location != nil {
					t.Fatal("publisher identity or unknown location changed")
				}
				if until, ok := ctx.Deadline(); !ok || time.Until(until) > 5*time.Second {
					t.Fatal("preparation has no end-to-end bound")
				}
				return preparedIngestFixture(t, request), nil
			})}
			stream := &commodorepb.ResolveStreamContextResponse{Admitted: true, IngestMode: "push", TenantId: "tenant", StreamId: "stream", InternalName: "internal"}
			response, err := ResolveIngestEndpoints(context.Background(), deps, stream, "private-key")
			if err != nil || response.Primary.NodeId != "selected-node" || response.Primary.BaseUrl != "https://display.example:8443" || len(response.Fallbacks) != 0 {
				t.Fatalf("prepared endpoints: %v, %v", response, err)
			}
			want := protocol
			if protocol == "" {
				want = "whip,rtmp,srt"
			}
			if strings.Join(seen, ",") != want {
				t.Fatalf("prepared protocols %v, want %s", seen, want)
			}
			for p, value := range map[string]string{"whip": response.Primary.GetWhipUrl(), "rtmp": response.Primary.GetRtmpUrl(), "srt": response.Primary.GetSrtUrl()} {
				if (protocol == "" || protocol == p) != strings.Contains(value, "private-key") {
					t.Fatalf("unconfirmed or unbound %s URL: %s", p, value)
				}
			}
		})
	}
}

func TestPreparedIngestEndpointsNeverFallThroughOnInvalidPreparation(t *testing.T) {
	for _, change := range []string{"missing", "error", "tenant", "object", "protocol", "expired", "long-lived", "attempt", "ready", "source", "base", "concrete-key", "cancelled"} {
		t.Run(change, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			deps := &IngestDependencies{Protocol: "rtmp", Placement: ingestPlacementPreparerFunc(func(_ context.Context, request IngestPlacementRequest) (balancer.PlacementPreparationResult, error) {
				p := preparedIngestFixture(t, request)
				switch change {
				case "error":
					return p, errors.New("ambiguous preparation")
				case "tenant":
					p.TenantID = "another"
				case "object":
					p.ObjectID = "live_stream:replacement"
				case "protocol":
					p.Protocol = "srt"
				case "expired":
					p.ExpiresAt = time.Now().Add(-time.Second)
				case "long-lived":
					p.ExpiresAt = time.Now().Add(time.Hour)
				case "attempt":
					p.AttemptID = "not-an-attempt"
				case "ready":
					p.Ready = true
				case "source":
					p.SourceGeneration = "source"
				case "base":
					p.PublicBaseURL = "rtmp://guessed.example:1935"
				case "concrete-key":
					p.Endpoint = "rtmps://node.example/live/already-bound"
				case "cancelled":
					cancel()
				}
				return p, nil
			})}
			if change == "missing" {
				deps.Placement = nil
			}
			stream := &commodorepb.ResolveStreamContextResponse{Admitted: true, IngestMode: "push", TenantId: "tenant", StreamId: "stream", InternalName: "internal"}
			if response, err := ResolveIngestEndpoints(ctx, deps, stream, "private-key"); response != nil || err == nil || err.Error() == "load balancer not available" {
				t.Fatalf("invalid preparation escaped into legacy selection: %v, %v", response, err)
			}
		})
	}
}

func TestPreparedIngestEndpointsDoNotBroadenAmbiguousProtocolResults(t *testing.T) {
	for _, ambiguous := range []bool{false, true} {
		calls := 0
		deps := &IngestDependencies{Placement: ingestPlacementPreparerFunc(func(_ context.Context, request IngestPlacementRequest) (balancer.PlacementPreparationResult, error) {
			calls++
			if request.Protocol == "rtmp" {
				if ambiguous {
					return balancer.PlacementPreparationResult{}, errors.New("reply lost")
				}
				return balancer.PlacementPreparationResult{}, balancer.ErrPlacementUnavailable
			}
			p := preparedIngestFixture(t, request)
			p.NodeID = request.Protocol + "-node"
			return p, nil
		})}
		stream := &commodorepb.ResolveStreamContextResponse{Admitted: true, IngestMode: "push", TenantId: "tenant", StreamId: "stream", InternalName: "internal"}
		response, err := ResolveIngestEndpoints(context.Background(), deps, stream, "key")
		if ambiguous {
			if err == nil || response != nil || calls != 2 {
				t.Fatalf("ambiguous preparation continued: %v, %v, %d", response, err, calls)
			}
		} else if err != nil || len(response.Fallbacks) != 1 || response.Primary.GetRtmpUrl() != "" || response.Fallbacks[0].GetSrtUrl() == "" || calls != 3 {
			t.Fatalf("independent protocol destinations changed: %v, %v, %d", response, err, calls)
		}
	}
}
