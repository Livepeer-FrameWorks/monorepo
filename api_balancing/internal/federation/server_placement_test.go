package federation

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type placementRPCFixture struct {
	query   func(context.Context, *placementpb.CandidateQuery) (*placementpb.CandidateObservation, error)
	prepare func(context.Context, *placementpb.PreparePlacementRequest) (*placementpb.Preparation, error)
}

func testPreparationAttempt(t *testing.T) string {
	t.Helper()
	id, err := placement.NewPreparationAttemptID(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (fixture placementRPCFixture) QueryPlacementCandidates(ctx context.Context, query *placementpb.CandidateQuery) (*placementpb.CandidateObservation, error) {
	return fixture.query(ctx, query)
}
func (fixture placementRPCFixture) PreparePlacement(ctx context.Context, req *placementpb.PreparePlacementRequest) (*placementpb.Preparation, error) {
	return fixture.prepare(ctx, req)
}

func TestPlacementFederationAuthAndReadinessGates(t *testing.T) {
	query := &placementpb.CandidateQuery{TenantId: "tenant", ObjectId: "object", InternalName: "internal", Verb: placementpb.Verb_VERB_SERVE, Protocol: "hls", ClusterIds: []string{"us"}, PolicyDigest: strings.Repeat("a", 64)}
	prepare := &placementpb.PreparePlacementRequest{Query: query, ClusterId: "us", NodeId: "node", AttemptId: testPreparationAttempt(t), ExpiresAt: timestamppb.New(time.Now().Add(20 * time.Second))}
	for _, operation := range []string{"observe", "prepare"} {
		t.Run(operation, func(t *testing.T) {
			calls := 0
			check := func(ctx context.Context) {
				calls++
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 5*time.Second {
					t.Fatal("destination call lacks bounded deadline")
				}
			}
			delegate := placementRPCFixture{
				query: func(ctx context.Context, _ *placementpb.CandidateQuery) (*placementpb.CandidateObservation, error) {
					check(ctx)
					return &placementpb.CandidateObservation{}, nil
				},
				prepare: func(ctx context.Context, req *placementpb.PreparePlacementRequest) (*placementpb.Preparation, error) {
					check(ctx)
					return preparationWireResponse(req), nil
				},
			}
			server := NewFederationServer(FederationServerConfig{Placement: delegate})
			invoke := func(ctx context.Context) error {
				if operation == "observe" {
					_, err := server.QueryPlacementCandidates(ctx, query)
					return err
				}
				_, err := server.PreparePlacement(ctx, prepare)
				return err
			}
			if err := invoke(context.Background()); status.Code(err) != codes.PermissionDenied {
				t.Fatalf("anonymous call: %v", err)
			}
			if err := invoke(svcAuthCtx()); status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("disabled federation call: %v", err)
			}
			server.allowFederationMutations = true
			server.placement = nil
			if err := invoke(svcAuthCtx()); status.Code(err) != codes.Unavailable {
				t.Fatalf("unconfigured destination call: %v", err)
			}
			server.placement = delegate
			if calls != 0 {
				t.Fatal("authorization/readiness gate called destination")
			}
			if err := invoke(svcAuthCtx()); err != nil || calls != 1 {
				t.Fatalf("authorized call: %v (%d)", err, calls)
			}
		})
	}
}

func preparationWireResponse(req *placementpb.PreparePlacementRequest) *placementpb.Preparation {
	q := req.GetQuery()
	return &placementpb.Preparation{Outcome: placementpb.PreparationOutcome_PREPARATION_OUTCOME_ACCEPTED,
		TenantId: q.GetTenantId(), ObjectId: q.GetObjectId(), SourceGeneration: q.GetSourceGeneration(),
		ClusterId: req.GetClusterId(), NodeId: req.GetNodeId(), AttemptId: req.GetAttemptId(),
		Protocol: q.GetProtocol(), PolicyRevision: q.GetPolicyRevision(), ParentRevision: q.GetParentRevision(), PolicyDigest: q.GetPolicyDigest(),
		ExpiresAt: proto.CloneOf(req.GetExpiresAt()), Endpoint: "https://edge.example/hls/stream/index.m3u8"}
}

func TestPreparationServerRejectsExpiredAndUnboundAcknowledgements(t *testing.T) {
	for _, scenario := range []string{"expired_request", "renewed_ack", "wrong_node", "late_ack"} {
		t.Run(scenario, func(t *testing.T) {
			req := &placementpb.PreparePlacementRequest{Query: &placementpb.CandidateQuery{
				TenantId: "tenant", ObjectId: "object", InternalName: "internal", Verb: placementpb.Verb_VERB_SERVE, Protocol: "hls", ClusterIds: []string{"us"}, PolicyDigest: strings.Repeat("a", 64)},
				ClusterId: "us", NodeId: "node", AttemptId: testPreparationAttempt(t), ExpiresAt: timestamppb.New(time.Now().Add(20 * time.Second))}
			calls := 0
			delegate := placementRPCFixture{prepare: func(ctx context.Context, req *placementpb.PreparePlacementRequest) (*placementpb.Preparation, error) {
				calls++
				ack := preparationWireResponse(req)
				switch scenario {
				case "renewed_ack":
					ack.ExpiresAt = timestamppb.New(req.ExpiresAt.AsTime().Add(time.Nanosecond))
				case "wrong_node":
					ack.NodeId = "another"
				case "late_ack":
					<-ctx.Done()
				}
				return ack, nil
			}}
			server := NewFederationServer(FederationServerConfig{Placement: delegate, AllowFederationMutations: true})
			want := codes.FailedPrecondition
			if scenario == "expired_request" {
				req.ExpiresAt = timestamppb.New(time.Now().Add(-time.Second))
				want = codes.InvalidArgument
			}
			if scenario == "late_ack" {
				req.ExpiresAt = timestamppb.New(time.Now().Add(20 * time.Millisecond))
				want = codes.DeadlineExceeded
			}
			response, err := server.PreparePlacement(svcAuthCtx(), req)
			if response != nil || status.Code(err) != want || (scenario == "expired_request" && calls != 0) {
				t.Fatalf("invalid acknowledgement or dispatch accepted: %+v, %v, calls=%d", response, err, calls)
			}
			if scenario == "renewed_ack" || scenario == "wrong_node" {
				transport := PlacementTransport{LocalCellID: "cell", Local: delegate}
				_, err := transport.Prepare(context.Background(), balancer.PlacementCell{ID: "cell", ClusterIDs: []string{"us"}}, balancer.PlacementPreparationRequest{
					Route:  balancer.PlacementRouteRequest{TenantID: "tenant", ObjectID: "object", InternalName: "internal", Verb: placement.Serve, Protocol: "hls", PolicyDigest: req.Query.PolicyDigest},
					Choice: placement.Choice{ClusterID: "us", NodeID: "node"}, AttemptID: req.AttemptId, ExpiresAt: req.ExpiresAt.AsTime(),
				})
				if !errors.Is(err, balancer.ErrPlacementPreparation) {
					t.Fatalf("local transport bypassed exact acknowledgement validation: %v", err)
				}
			}
		})
	}
}

func TestPlacementTransportBindsObservationToExactIntent(t *testing.T) {
	req := balancer.PlacementRouteRequest{TenantID: "tenant", ObjectID: "object", InternalName: "internal", Verb: placement.Serve, Protocol: "hls", PolicyDigest: strings.Repeat("a", 64), PolicyRevision: 5, ParentRevision: 7, SourceGeneration: "source-1", Location: &placement.Coordinates{Latitude: 40, Longitude: -74}}
	cell := balancer.PlacementCell{ID: "cell", ClusterIDs: []string{"us"}}
	base := &placementpb.CandidateObservation{PolicyDigest: req.PolicyDigest, PolicyRevision: req.PolicyRevision, ParentRevision: req.ParentRevision, SourceGeneration: req.SourceGeneration, Complete: true,
		ObservedAt: timestamppb.New(time.Unix(1800000000, 0)), ExpiresAt: timestamppb.New(time.Unix(1800000020, 0)),
		Candidates: []*placementpb.ObservedCandidate{{TenantId: "tenant", ClusterId: "us", NodeId: "node"}},
	}
	for name, mutate := range map[string]func(*placementpb.CandidateObservation){
		"valid":               func(*placementpb.CandidateObservation) {},
		"policy":              func(o *placementpb.CandidateObservation) { o.PolicyRevision++ },
		"parent":              func(o *placementpb.CandidateObservation) { o.ParentRevision++ },
		"digest":              func(o *placementpb.CandidateObservation) { o.PolicyDigest = "wrong" },
		"source":              func(o *placementpb.CandidateObservation) { o.SourceGeneration = "source-2" },
		"tenant":              func(o *placementpb.CandidateObservation) { o.Candidates[0].TenantId = "other" },
		"cluster":             func(o *placementpb.CandidateObservation) { o.Candidates[0].ClusterId = "eu" },
		"missing_observed_at": func(o *placementpb.CandidateObservation) { o.ObservedAt = nil },
		"missing_expires_at":  func(o *placementpb.CandidateObservation) { o.ExpiresAt = nil },
		"invalid_expires_at":  func(o *placementpb.CandidateObservation) { o.ExpiresAt = &timestamppb.Timestamp{Seconds: 1 << 62} },
	} {
		t.Run(name, func(t *testing.T) {
			response := proto.CloneOf(base)
			mutate(response)
			transport := PlacementTransport{LocalCellID: "cell", Local: placementRPCFixture{query: func(_ context.Context, q *placementpb.CandidateQuery) (*placementpb.CandidateObservation, error) {
				if q.GetInternalName() != req.InternalName || q.GetClientLocation().GetLongitude() != req.Location.Longitude || q.GetProtocol() != req.Protocol {
					t.Fatal("lost request identity/location/protocol")
				}
				return response, nil
			}}}
			observed, err := transport.Observe(context.Background(), cell, req)
			if name == "valid" {
				if err != nil || len(observed.Candidates) != 1 || !observed.Complete {
					t.Fatalf("valid observation: %+v %v", observed, err)
				}
			} else if !errors.Is(err, balancer.ErrPlacementObservation) {
				t.Fatalf("substituted observation accepted: %v", err)
			}
		})
	}
}
