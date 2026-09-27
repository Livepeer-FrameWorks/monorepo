package federation

import (
	"context"
	"errors"
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

func policyRuntimeFixture(t *testing.T) (*PlacementDestination, *PolicyBoundPlacementRuntime, *discoveryFixture, *placementpb.PreparePlacementRequest) {
	t.Helper()
	store, _, req := placementReceiptFixture(t)
	f := newDiscoveryFixture(t)
	f.discovery.Now = store.Now
	req.Query, req.NodeId = f.query, "node-00"
	transport := PlacementTransport{LocalCellID: store.CellID, Local: &PlacementDestination{Discovery: f.discovery}}
	runtime := &PolicyBoundPlacementRuntime{Policy: &PlacementPolicyGate{
		CellID: store.CellID, Authority: f.discovery.Authority, Router: transport.Router(), Now: store.Now,
	}}
	destination := &PlacementDestination{Discovery: f.discovery, Receipts: store, Runtime: runtime, Now: store.Now}
	return destination, runtime, f, req
}

func TestPolicyBoundRuntimeProducesExactRefusalsWithoutMedia(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(map[bool]string{false: "full", true: "unavailable"}[unavailable], func(t *testing.T) {
			destination, _, f, req := policyRuntimeFixture(t)
			f.snapshot.Nodes[0].UpSpeed = f.snapshot.Nodes[0].BWLimit
			want := placementpb.PreparationOutcome_PREPARATION_OUTCOME_CAPACITY_EXHAUSTED
			if unavailable {
				f.snapshot.Nodes[0].IsActive = false
				want = placementpb.PreparationOutcome_PREPARATION_OUTCOME_NODE_UNAVAILABLE
			}
			response, err := destination.PreparePlacement(context.Background(), req)
			if err != nil || response.GetOutcome() != want || response.GetEndpoint() != "" || response.GetReady() {
				t.Fatalf("exact refusal not produced: %v, %v", response, err)
			}
			if err = placement.ValidatePreparationResponse(req, response, destination.now()); err != nil {
				t.Fatal(err)
			}
			receipt, err := destination.Receipts.Begin(context.Background(), req)
			if err != nil || receipt.Pull != nil || receipt.Response.GetOutcome() != want {
				t.Fatalf("refusal created media or lost receipt: %v, %v", receipt, err)
			}
			f.snapshot.Nodes[0].UpSpeed = 0
			f.snapshot.Nodes[0].IsActive = true
			if response, err = destination.PreparePlacement(context.Background(), req); response != nil || err == nil {
				t.Fatalf("recovered capacity replayed stale refusal: %v, %v", response, err)
			}
		})
	}
}

func TestPolicyBoundRuntimeNeverTurnsUnknownFactsIntoCapacityRefusal(t *testing.T) {
	for _, unknown := range []string{"metrics", "missing-node", "missing-path", "inventory", "source", "authority"} {
		t.Run(unknown, func(t *testing.T) {
			destination, runtime, f, req := policyRuntimeFixture(t)
			switch unknown {
			case "metrics":
				f.snapshot.Nodes[0].MetricsObservedAt = time.Time{}
			case "missing-node":
				req.NodeId = "missing"
			case "missing-path":
				delete(f.paths.Paths, req.NodeId)
			case "inventory":
				f.inventory.Complete = false
				f.inventory.Nodes = nil
			case "source":
				f.paths.Paths[req.NodeId] = balancer.PlacementNodePath{Presence: placement.Absent}
			case "authority":
				f.pair.Tenant.Ready = false
			}
			runtime.Media = placementRuntimeFixture{
				validate: func(context.Context, *placementpb.PreparePlacementRequest, PlacementReceipt) error {
					t.Fatal("unknown policy reached media admission")
					return nil
				},
				reconcile: func(context.Context, *placementpb.PreparePlacementRequest, PlacementReceipt, func(*PlacementPullBinding) error) (*placementpb.Preparation, error) {
					t.Fatal("unknown policy started media")
					return nil, nil
				},
			}
			if response, err := destination.PreparePlacement(context.Background(), req); response != nil || err == nil {
				t.Fatalf("unknown %s became a typed refusal: %v, %v", unknown, response, err)
			}
			receipt, err := destination.Receipts.Begin(context.Background(), req)
			if err != nil || receipt.Response != nil || receipt.Pull != nil {
				t.Fatalf("unknown facts completed or started work: %v, %v", receipt, err)
			}
		})
	}
}

func TestPolicyBoundRuntimeCapsMediaOutcomeAndRevalidatesReadiness(t *testing.T) {
	destination, runtime, _, req := policyRuntimeFixture(t)
	mediaChecks, reconciles := 0, 0
	ready := true
	mediaResponse := preparationWireResponse(req)
	mediaResponse.Ready = true
	runtime.Media = placementRuntimeFixture{
		validate: func(_ context.Context, _ *placementpb.PreparePlacementRequest, receipt PlacementReceipt) error {
			mediaChecks++
			if receipt.Response != nil && !ready {
				return errors.New("generation-bound media no longer present")
			}
			return nil
		},
		reconcile: func(_ context.Context, _ *placementpb.PreparePlacementRequest, _ PlacementReceipt, bind func(*PlacementPullBinding) error) (*placementpb.Preparation, error) {
			reconciles++
			pull := placementReceiptPull()
			pull.SourceGeneration = req.Query.SourceGeneration
			if err := bind(pull); err != nil {
				return nil, err
			}
			return mediaResponse, nil
		},
	}
	response, err := destination.PreparePlacement(context.Background(), req)
	if err != nil || response.GetOutcome() != placementpb.PreparationOutcome_PREPARATION_OUTCOME_ACCEPTED || mediaChecks != 2 || reconciles != 1 {
		t.Fatalf("policy/media integration failed: %v, %v checks=%d reconciles=%d", response, err, mediaChecks, reconciles)
	}
	if !response.ExpiresAt.AsTime().Before(mediaResponse.ExpiresAt.AsTime()) || !mediaResponse.ExpiresAt.AsTime().Equal(req.ExpiresAt.AsTime()) {
		t.Fatal("policy did not cap media lifetime or mutated media-owned response")
	}
	ready = false
	if response, err = destination.PreparePlacement(context.Background(), req); response != nil || err == nil || reconciles != 1 {
		t.Fatalf("cached readiness bypassed physical check: %v, %v", response, err)
	}
}

func TestPolicyBoundRuntimeRejectsInvalidMediaAckBeforeClamping(t *testing.T) {
	destination, runtime, _, req := policyRuntimeFixture(t)
	runtime.Media = placementRuntimeFixture{
		validate: func(context.Context, *placementpb.PreparePlacementRequest, PlacementReceipt) error { return nil },
		reconcile: func(context.Context, *placementpb.PreparePlacementRequest, PlacementReceipt, func(*PlacementPullBinding) error) (*placementpb.Preparation, error) {
			response := preparationWireResponse(req)
			response.ExpiresAt = timestamppb.New(req.ExpiresAt.AsTime().Add(time.Minute))
			return response, nil
		},
	}
	if response, err := destination.PreparePlacement(context.Background(), req); response != nil || err == nil {
		t.Fatalf("invalid media lifetime was silently repaired: %v, %v", response, err)
	}
}

func TestPolicyBoundRuntimeObservesBeforeAndAfterWorkWithoutDuplicatePreflight(t *testing.T) {
	destination, runtime, f, req := policyRuntimeFixture(t)
	observations := func() int {
		count := 0
		for _, call := range f.calls {
			if call == "inventory" {
				count++
			}
		}
		return count
	}
	starts := 0
	runtime.Media = placementRuntimeFixture{
		validate: func(context.Context, *placementpb.PreparePlacementRequest, PlacementReceipt) error { return nil },
		reconcile: func(_ context.Context, got *placementpb.PreparePlacementRequest, _ PlacementReceipt, bind func(*PlacementPullBinding) error) (*placementpb.Preparation, error) {
			if observations() != 1 {
				t.Fatalf("media preparation requires one initial census, got %d", observations())
			}
			pull := placementReceiptPull()
			pull.SourceGeneration = got.Query.SourceGeneration
			if err := bind(pull); err != nil {
				return nil, err
			}
			starts++
			return preparationWireResponse(got), nil
		},
	}
	if response, err := destination.PreparePlacement(context.Background(), req); err != nil || response == nil || observations() != 2 || starts != 1 {
		t.Fatalf("cold preparation: %v, %v, observations=%d starts=%d", response, err, observations(), starts)
	}
	f.calls = nil
	if response, err := destination.PreparePlacement(context.Background(), req); err != nil || response == nil || observations() != 1 || starts != 1 {
		t.Fatalf("replay: %v, %v, observations=%d starts=%d", response, err, observations(), starts)
	}
}

func TestPolicyBoundRuntimeChecksPhysicalAdmissionBeforeWork(t *testing.T) {
	for _, change := range []string{"refused", "canceled", "expired"} {
		t.Run(change, func(t *testing.T) {
			destination, runtime, _, req := policyRuntimeFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runtime.Media = placementRuntimeFixture{
				validate: func(_ context.Context, got *placementpb.PreparePlacementRequest, receipt PlacementReceipt) error {
					got.NodeId, receipt.Request.NodeId = "mutated", "mutated"
					switch change {
					case "refused":
						return errors.New("physical source changed")
					case "canceled":
						cancel()
					case "expired":
						runtime.Policy.Now = func() time.Time { return req.ExpiresAt.AsTime() }
					}
					return nil
				},
				reconcile: func(context.Context, *placementpb.PreparePlacementRequest, PlacementReceipt, func(*PlacementPullBinding) error) (*placementpb.Preparation, error) {
					t.Fatal("invalid physical admission reached media work")
					return nil, nil
				},
			}
			if response, err := destination.PreparePlacement(ctx, req); err == nil || response != nil || req.NodeId != "node-00" {
				t.Fatalf("physical refusal lost or request mutated: %v, %v, %s", response, err, req.NodeId)
			}
		})
	}
}

func TestPolicyBoundRuntimeRechecksRevocationAfterMediaWork(t *testing.T) {
	destination, runtime, f, req := policyRuntimeFixture(t)
	worked := false
	runtime.Media = placementRuntimeFixture{
		validate: func(context.Context, *placementpb.PreparePlacementRequest, PlacementReceipt) error { return nil },
		reconcile: func(_ context.Context, got *placementpb.PreparePlacementRequest, _ PlacementReceipt, bind func(*PlacementPullBinding) error) (*placementpb.Preparation, error) {
			pull := placementReceiptPull()
			pull.SourceGeneration = got.Query.SourceGeneration
			if err := bind(pull); err != nil {
				return nil, err
			}
			worked = true
			f.pair.Tenant.Ready = false
			return preparationWireResponse(got), nil
		},
	}
	if response, err := destination.PreparePlacement(context.Background(), req); err == nil || response != nil || !worked {
		t.Fatalf("revocation during preparation was ignored: %v, %v, worked=%v", response, err, worked)
	}
	receipt, err := destination.Receipts.Begin(context.Background(), req)
	if err != nil || receipt.Response != nil || receipt.Pull == nil {
		t.Fatalf("revoked work was published or lost physical binding: %+v, %v", receipt, err)
	}
}

// The destination validates only its own facts. Signed authority versions
// that advance without changing what admits the node, during the work or
// before a replay, keep the preparation; a change that forbids the node
// refuses it, and so does destination authority older than the coordinator's.
func TestPolicyBoundRuntimeAcceptsAuthorityAdvanceThatStillAdmitsTheNode(t *testing.T) {
	for _, scenario := range []string{"advance-during-work", "advance-before-replay", "newer-destination-policy", "advance-to-deny", "older-destination-policy"} {
		t.Run(scenario, func(t *testing.T) {
			destination, runtime, f, req := policyRuntimeFixture(t)
			// Policy revisions deliberately differ from the signed envelope versions.
			f.pair.Tenant.Version, f.pair.Object.Version = 103, 105
			req.Query = proto.CloneOf(f.query)
			starts := 0
			runtime.Media = placementRuntimeFixture{
				validate: func(context.Context, *placementpb.PreparePlacementRequest, PlacementReceipt) error { return nil },
				reconcile: func(_ context.Context, got *placementpb.PreparePlacementRequest, _ PlacementReceipt, bind func(*PlacementPullBinding) error) (*placementpb.Preparation, error) {
					starts++
					pull := placementReceiptPull()
					pull.SourceGeneration = got.Query.SourceGeneration
					if err := bind(pull); err != nil {
						return nil, err
					}
					if scenario == "advance-during-work" {
						f.pair.Object.Version++
					}
					return preparationWireResponse(got), nil
				},
			}
			switch scenario {
			case "newer-destination-policy":
				// The destination already holds the next policy revision; it
				// answers under it, and the acknowledgement still names the
				// coordinator's revision.
				f.pair.Object.Authority.MediaPlacement.Revision++
				f.query.PolicyRevision++
			case "older-destination-policy":
				req.Query.PolicyRevision++
			case "advance-to-deny":
				f.pair.Object.Version++
				f.pair.Tenant.Authority.EffectiveClusterGrants[0].MediaConsent.AllowServe = false
			}
			response, err := destination.PreparePlacement(context.Background(), req)
			switch scenario {
			case "advance-to-deny", "older-destination-policy":
				if response != nil || status.Code(err) != codes.FailedPrecondition || starts != 0 {
					t.Fatalf("%s prepared: %v, %v, starts=%d", scenario, response, err, starts)
				}
				return
			}
			if err != nil || response.GetOutcome() != placementpb.PreparationOutcome_PREPARATION_OUTCOME_ACCEPTED || starts != 1 ||
				response.GetTenantAuthorityVersion() != 103 || response.GetObjectAuthorityVersion() != 105 ||
				response.GetPolicyRevision() != req.Query.PolicyRevision || response.GetParentRevision() != req.Query.ParentRevision {
				t.Fatalf("%s refused an admitted node: %v, %v, starts=%d", scenario, response, err, starts)
			}
			if scenario == "advance-before-replay" {
				f.pair.Object.Version++
			}
			replay, err := destination.PreparePlacement(context.Background(), req)
			if err != nil || !proto.Equal(replay, response) || starts != 1 {
				t.Fatalf("replay under advanced authority: %v, %v, starts=%d", replay, err, starts)
			}
			f.pair.Tenant.Authority.EffectiveClusterGrants[0].MediaConsent.AllowServe = false
			if replay, err = destination.PreparePlacement(context.Background(), req); replay != nil || err == nil {
				t.Fatalf("replay after the destination was withdrawn: %v, %v", replay, err)
			}
		})
	}
}
