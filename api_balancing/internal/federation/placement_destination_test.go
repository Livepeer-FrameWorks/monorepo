package federation

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type placementRuntimeFixture struct {
	validate  func(context.Context, *placementpb.PreparePlacementRequest, PlacementReceipt) error
	reconcile func(context.Context, *placementpb.PreparePlacementRequest, PlacementReceipt, func(*PlacementPullBinding) error) (*placementpb.Preparation, error)
}

func (runtime placementRuntimeFixture) Revalidate(ctx context.Context, req *placementpb.PreparePlacementRequest, receipt PlacementReceipt) error {
	return runtime.validate(ctx, req, receipt)
}

func (runtime placementRuntimeFixture) Reconcile(ctx context.Context, req *placementpb.PreparePlacementRequest, receipt PlacementReceipt, bind func(*PlacementPullBinding) error) (*placementpb.Preparation, error) {
	return runtime.reconcile(ctx, req, receipt, bind)
}

func TestPlacementDestinationBindsBeforeWorkAndRevalidatesReplay(t *testing.T) {
	store, _, req := placementReceiptFixture(t)
	var calls []string
	var refused bool
	runtime := placementRuntimeFixture{
		validate: func(ctx context.Context, got *placementpb.PreparePlacementRequest, receipt PlacementReceipt) error {
			calls = append(calls, "validate")
			if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 5*time.Second || !proto.Equal(got, req) {
				t.Fatal("runtime lost bound request/deadline")
			}
			if receipt.Response != nil && (receipt.Pull == nil || receipt.Pull.AttemptID != placementReceiptPull().AttemptID) {
				t.Fatal("completed receipt lost physical pull")
			}
			if refused {
				return status.Error(codes.PermissionDenied, "policy revoked")
			}
			// Runtime arguments must not alias durable request/receipt state.
			got.NodeId = "mutated"
			receipt.Request.NodeId = "mutated"
			return nil
		},
		reconcile: func(ctx context.Context, got *placementpb.PreparePlacementRequest, receipt PlacementReceipt, bind func(*PlacementPullBinding) error) (*placementpb.Preparation, error) {
			calls = append(calls, "reconcile")
			if !receipt.Fresh || !proto.Equal(got, req) {
				t.Fatal("new work did not receive immutable original intent")
			}
			if err := bind(placementReceiptPull()); err != nil {
				return nil, err
			}
			stored, err := store.Begin(ctx, req)
			if err != nil || stored.Pull == nil || stored.Response != nil {
				t.Fatal("physical work began without bound pending receipt")
			}
			return preparationWireResponse(req), nil
		},
	}
	destination := &PlacementDestination{Discovery: &PlacementDiscovery{CellID: store.CellID}, Receipts: store, Runtime: runtime, Now: store.Now}
	response, err := destination.PreparePlacement(context.Background(), req)
	if err != nil || !proto.Equal(response, preparationWireResponse(req)) || !reflect.DeepEqual(calls, []string{"reconcile", "validate"}) {
		t.Fatalf("preparation = %v, %v, %v", response, err, calls)
	}
	calls = nil
	response, err = destination.PreparePlacement(context.Background(), req)
	if err != nil || !proto.Equal(response, preparationWireResponse(req)) || !reflect.DeepEqual(calls, []string{"validate"}) {
		t.Fatalf("replay restarted work or skipped authority: %v, %v", calls, err)
	}
	refused = true
	if response, err = destination.PreparePlacement(context.Background(), req); response != nil || status.Code(err) != codes.PermissionDenied {
		t.Fatalf("old receipt bypassed revocation: %v, %v", response, err)
	}
}

func TestPlacementDestinationRecoversPendingWithoutFreshPermission(t *testing.T) {
	store, _, req := placementReceiptFixture(t)
	ambiguous := errors.New("reply lost after physical start")
	starts := 0
	checks := 0
	runtime := placementRuntimeFixture{
		validate: func(context.Context, *placementpb.PreparePlacementRequest, PlacementReceipt) error { return nil },
		reconcile: func(_ context.Context, got *placementpb.PreparePlacementRequest, receipt PlacementReceipt, bind func(*PlacementPullBinding) error) (*placementpb.Preparation, error) {
			checks++
			if starts == 0 {
				if err := bind(placementReceiptPull()); err != nil {
					return nil, err
				}
				starts++
				return nil, ambiguous
			}
			if receipt.Fresh || receipt.Pull == nil || receipt.Response != nil {
				t.Fatal("pending work was treated as a fresh permission")
			}
			return preparationWireResponse(got), nil
		},
	}
	destination := &PlacementDestination{Discovery: &PlacementDiscovery{CellID: store.CellID}, Receipts: store, Runtime: runtime, Now: store.Now}
	if response, err := destination.PreparePlacement(context.Background(), req); response != nil || !errors.Is(err, ambiguous) {
		t.Fatalf("ambiguous work became success: %v, %v", response, err)
	}
	if response, err := destination.PreparePlacement(context.Background(), req); err != nil || response == nil || starts != 1 || checks != 2 {
		t.Fatalf("pending reconciliation failed: %v, %v, starts=%d checks=%d", response, err, starts, checks)
	}
}

func TestPlacementDestinationRefusesChangedAuthorityAfterWork(t *testing.T) {
	for _, scenario := range []string{"revoked", "cancelled", "wrong-node", "expired"} {
		t.Run(scenario, func(t *testing.T) {
			store, server, req := placementReceiptFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			worked := false
			runtime := placementRuntimeFixture{
				validate: func(context.Context, *placementpb.PreparePlacementRequest, PlacementReceipt) error {
					if worked && scenario == "revoked" {
						return status.Error(codes.PermissionDenied, "revoked during work")
					}
					return nil
				},
				reconcile: func(_ context.Context, got *placementpb.PreparePlacementRequest, _ PlacementReceipt, bind func(*PlacementPullBinding) error) (*placementpb.Preparation, error) {
					worked = true
					if err := bind(placementReceiptPull()); err != nil {
						return nil, err
					}
					response := preparationWireResponse(got)
					switch scenario {
					case "cancelled":
						cancel()
					case "wrong-node":
						response.NodeId = "other"
					case "expired":
						server.SetTime(req.ExpiresAt.AsTime())
					}
					return response, nil
				},
			}
			destination := &PlacementDestination{Discovery: &PlacementDiscovery{CellID: store.CellID}, Receipts: store, Runtime: runtime, Now: store.Now}
			if response, err := destination.PreparePlacement(ctx, req); response != nil || err == nil {
				t.Fatalf("invalid work became success: %v, %v", response, err)
			}
			if server.HGet(placementReceiptKey(store, req), "response") != "" {
				t.Fatal("invalid response persisted")
			}
		})
	}
}

func TestPlacementDestinationRequiresEnforcementAndMatchingCell(t *testing.T) {
	store, _, req := placementReceiptFixture(t)
	for _, destination := range []*PlacementDestination{nil, {}, {Discovery: &PlacementDiscovery{CellID: store.CellID}, Receipts: store},
		{Discovery: &PlacementDiscovery{CellID: "other"}, Receipts: store, Runtime: placementRuntimeFixture{}}} {
		if response, err := destination.PreparePlacement(context.Background(), req); response != nil || status.Code(err) != codes.Unavailable {
			t.Fatalf("incomplete destination accepted work: %v, %v", response, err)
		}
	}
}

func TestPlacementDestinationReplayRechecksCoordinationAfterRuntime(t *testing.T) {
	for _, change := range []string{"expired", "engine-restarted", "outcome-lost"} {
		t.Run(change, func(t *testing.T) {
			store, server, req := placementReceiptFixture(t)
			ctx := context.Background()
			if _, err := store.Begin(ctx, req); err != nil {
				t.Fatal(err)
			}
			if err := store.Finish(ctx, req, preparationWireResponse(req), nil); err != nil {
				t.Fatal(err)
			}
			runtime := placementRuntimeFixture{validate: func(context.Context, *placementpb.PreparePlacementRequest, PlacementReceipt) error {
				switch change {
				case "expired":
					server.SetTime(req.ExpiresAt.AsTime())
				case "engine-restarted":
					placementReceiptEngineInfo(server, "c", "b", "0", "master")
				case "outcome-lost":
					server.Del(placementReceiptKey(store, req))
				}
				return nil
			}, reconcile: func(context.Context, *placementpb.PreparePlacementRequest, PlacementReceipt, func(*PlacementPullBinding) error) (*placementpb.Preparation, error) {
				t.Fatal("replay restarted work")
				return nil, nil
			}}
			destination := &PlacementDestination{Discovery: &PlacementDiscovery{CellID: store.CellID}, Receipts: store, Runtime: runtime, Now: store.Now}
			if response, err := destination.PreparePlacement(ctx, req); response != nil || err == nil {
				t.Fatalf("replay escaped coordination fence: %v, %v", response, err)
			}
		})
	}
}

func TestPlacementDestinationReplicasReconcileOnePhysicalPull(t *testing.T) {
	store, _, req := placementReceiptFixture(t)
	var physicalMu sync.Mutex
	starts := 0
	runtime := placementRuntimeFixture{
		validate: func(context.Context, *placementpb.PreparePlacementRequest, PlacementReceipt) error { return nil },
		reconcile: func(_ context.Context, request *placementpb.PreparePlacementRequest, _ PlacementReceipt, bind func(*PlacementPullBinding) error) (*placementpb.Preparation, error) {
			physicalMu.Lock()
			defer physicalMu.Unlock()
			if err := bind(placementReceiptPull()); err != nil {
				return nil, err
			}
			if starts == 0 {
				starts++
			}
			return preparationWireResponse(request), nil
		},
	}
	errs := make(chan error, 12)
	var callers sync.WaitGroup
	for range 12 {
		callers.Go(func() {
			replicaStore := &PlacementReceiptStore{Client: store.Client, CellID: store.CellID, Now: store.Now}
			destination := &PlacementDestination{Discovery: &PlacementDiscovery{CellID: store.CellID}, Receipts: replicaStore, Runtime: runtime, Now: store.Now}
			response, err := destination.PreparePlacement(context.Background(), req)
			if err == nil && !proto.Equal(response, preparationWireResponse(req)) {
				err = errors.New("replica returned another outcome")
			}
			errs <- err
		})
	}
	callers.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if starts != 1 {
		t.Fatalf("physical starts=%d", starts)
	}
}

// Each assessment is an independent observation: the post-work check can see
// evidence that ends before the evidence the preparation was built from. A
// fresh preparation takes the shorter lifetime; a persisted receipt cannot.
func TestPlacementDestinationShortensFreshPreparationToCurrentEvidence(t *testing.T) {
	store, _, req := placementReceiptFixture(t)
	until := store.Now().Add(time.Second).Truncate(time.Millisecond)
	shortenedEvidence := true
	runtime := placementRuntimeFixture{
		validate: func(_ context.Context, _ *placementpb.PreparePlacementRequest, receipt PlacementReceipt) error {
			if receipt.Response == nil || !shortenedEvidence {
				return nil
			}
			if prepared := receipt.Response.GetExpiresAt().AsTime(); prepared.After(until) {
				return &PlacementEvidenceShortenedError{Prepared: prepared, Until: until}
			}
			return nil
		},
		reconcile: func(_ context.Context, got *placementpb.PreparePlacementRequest, _ PlacementReceipt, bind func(*PlacementPullBinding) error) (*placementpb.Preparation, error) {
			if err := bind(placementReceiptPull()); err != nil {
				return nil, err
			}
			return preparationWireResponse(got), nil
		},
	}
	destination := &PlacementDestination{Discovery: &PlacementDiscovery{CellID: store.CellID}, Receipts: store, Runtime: runtime, Now: store.Now}
	response, err := destination.PreparePlacement(context.Background(), req)
	if err != nil || response == nil {
		t.Fatalf("shortened evidence refused a fresh preparation: %v, %v", response, err)
	}
	if got := response.GetExpiresAt().AsTime(); !got.Equal(until) {
		t.Fatalf("expiry = %s, want the current evidence's %s", got, until)
	}
	// The persisted receipt now carries the shorter expiry, so a replay under
	// the same evidence is accepted unchanged.
	if replay, err := destination.PreparePlacement(context.Background(), req); err != nil || !proto.Equal(replay, response) {
		t.Fatalf("replay = %v, %v", replay, err)
	}
	// Evidence that shrinks after persistence refuses the immutable receipt.
	until = until.Add(-500 * time.Millisecond)
	if replay, err := destination.PreparePlacement(context.Background(), req); replay != nil || status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("persisted receipt outlived current evidence: %v, %v", replay, err)
	}
}
