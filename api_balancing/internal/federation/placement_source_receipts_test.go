package federation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func sourceReceiptQuery(req *placementpb.PreparePlacementRequest, response *placementpb.Preparation, pull *PlacementPullBinding) PlacementSourceReceiptQuery {
	return PlacementSourceReceiptQuery{TenantID: req.Query.TenantId, InternalName: req.Query.InternalName,
		ClusterID: req.ClusterId, NodeID: req.NodeId, TenantAuthorityVersion: response.TenantAuthorityVersion,
		ObjectAuthorityVersion: response.ObjectAuthorityVersion, Pull: *pull}
}

func TestPlacementSourceReceiptRequiresCompletedExactEvidence(t *testing.T) {
	store, engine, req := placementReceiptFixture(t)
	ctx := context.Background()
	pull, response := placementReceiptPull(), preparationWireResponse(req)
	response.TenantAuthorityVersion, response.ObjectAuthorityVersion = 103, 105
	query := sourceReceiptQuery(req, response, pull)
	keys := len(engine.Keys())
	if _, err := store.CompletedPull(ctx, query); err == nil || len(engine.Keys()) != keys {
		t.Fatal("missing evidence created intent or authorized a source")
	}
	if _, err := store.Begin(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := store.BindPull(ctx, req, pull); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompletedPull(ctx, query); err == nil {
		t.Fatal("pending physical work became source evidence")
	}
	if err := store.Finish(ctx, req, response, pull); err != nil {
		t.Fatal(err)
	}
	second := &PlacementReceiptStore{Client: store.Client, CellID: store.CellID, Now: store.Now}
	receipt, err := second.CompletedPull(ctx, query)
	if err != nil || receipt.Fresh || !proto.Equal(receipt.Response, response) || receipt.Pull == nil || *receipt.Pull != *pull {
		t.Fatalf("replica lost completed source evidence: %+v, %v", receipt, err)
	}
	expected, _, err := placement.PreparationIdentity(req)
	if err != nil {
		t.Fatal(err)
	}
	actual, _, err := placement.PreparationIdentity(receipt.Request)
	if err != nil || actual != expected {
		t.Fatalf("receipt lost canonical request identity: %v", err)
	}
	for name, change := range map[string]func(*PlacementSourceReceiptQuery){
		"tenant":                func(q *PlacementSourceReceiptQuery) { q.TenantID = "another-tenant" },
		"internal":              func(q *PlacementSourceReceiptQuery) { q.InternalName = "another-stream" },
		"cluster":               func(q *PlacementSourceReceiptQuery) { q.ClusterID = "another-cluster" },
		"node":                  func(q *PlacementSourceReceiptQuery) { q.NodeID = "another-node" },
		"tenant-authority":      func(q *PlacementSourceReceiptQuery) { q.TenantAuthorityVersion++ },
		"object-authority":      func(q *PlacementSourceReceiptQuery) { q.ObjectAuthorityVersion++ },
		"physical-attempt":      func(q *PlacementSourceReceiptQuery) { q.Pull.AttemptID = "10000000-0000-4000-8000-000000000002" },
		"source-cell":           func(q *PlacementSourceReceiptQuery) { q.Pull.SourceCellID = "another-cell" },
		"source-cluster":        func(q *PlacementSourceReceiptQuery) { q.Pull.SourceClusterID = "another-cluster" },
		"source-node":           func(q *PlacementSourceReceiptQuery) { q.Pull.SourceNodeID = "another-node" },
		"source-generation":     func(q *PlacementSourceReceiptQuery) { q.Pull.SourceGeneration = "another-generation" },
		"source-revision":       func(q *PlacementSourceReceiptQuery) { q.Pull.SourceRevision++ },
		"destination-reconnect": func(q *PlacementSourceReceiptQuery) { q.Pull.DestinationFence++ },
		"destination-unfenced":  func(q *PlacementSourceReceiptQuery) { q.Pull.DestinationFence = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			changed := query
			change(&changed)
			if _, lookupErr := second.CompletedPull(ctx, changed); lookupErr == nil {
				t.Fatal("source evidence escaped exact binding")
			}
		})
	}
	index, err := store.sourceReceiptKey(query)
	if err != nil {
		t.Fatal(err)
	}
	ttl := engine.TTL(index)
	engine.FastForward(time.Second)
	if _, err := second.CompletedPull(ctx, query); err != nil || engine.TTL(index) != ttl-time.Second {
		t.Fatalf("read renewed source evidence: %v", err)
	}
	engine.Del(placementReceiptKey(store, req))
	keys = len(engine.Keys())
	if _, err := second.CompletedPull(ctx, query); !errors.Is(err, ErrPlacementReceiptMissing) || len(engine.Keys()) != keys {
		t.Fatalf("dangling index recreated a receipt: %v", err)
	}
}

func TestPlacementSourceReceiptWarmPlacementsSharePullWithoutRenewingOldEvidence(t *testing.T) {
	store, engine, first := placementReceiptFixture(t)
	ctx := context.Background()
	pull := placementReceiptPull()
	finish := func(req *placementpb.PreparePlacementRequest, until time.Time) PlacementSourceReceiptQuery {
		t.Helper()
		response := preparationWireResponse(req)
		response.TenantAuthorityVersion, response.ObjectAuthorityVersion = 103, 105
		response.ExpiresAt = timestamppb.New(until)
		if _, err := store.Begin(ctx, req); err != nil {
			t.Fatal(err)
		}
		if err := store.BindPull(ctx, req, pull); err != nil {
			t.Fatal(err)
		}
		if err := store.Finish(ctx, req, response, pull); err != nil {
			t.Fatal(err)
		}
		return sourceReceiptQuery(req, response, pull)
	}
	query := finish(first, store.now().Add(5*time.Second))
	second := placementReceiptRequest(t, store.now())
	second.Query.ClientLocation = &placementpb.Coordinates{Latitude: 40, Longitude: -74}
	finish(second, store.now().Add(10*time.Second))
	// Replaying the first viewer must not replace the longer-lived completed
	// evidence for the same physical pull and signed authority.
	finish(first, store.now().Add(5*time.Second))
	receipt, err := store.CompletedPull(ctx, query)
	if err != nil || receipt.Request.GetAttemptId() != second.AttemptId || receipt.Pull == nil || *receipt.Pull != *pull || !proto.Equal(receipt.Request.Query.ClientLocation, second.Query.ClientLocation) {
		t.Fatalf("warm placement lost its physical or client binding: %+v, %v", receipt, err)
	}
	index, err := store.sourceReceiptKey(query)
	if err != nil || engine.TTL(index) != 10*time.Second {
		t.Fatalf("index lifetime is not the selected completed evidence lifetime: %v", err)
	}
	engine.SetTime(store.now().Add(10 * time.Second))
	if _, err = store.CompletedPull(ctx, query); !errors.Is(err, ErrPlacementReceiptExpired) {
		t.Fatalf("warm reuse renewed expired authorization: %v", err)
	}
}

func TestPlacementSourceReceiptRejectsUnversionedAndRefusedResults(t *testing.T) {
	for _, kind := range []string{"unversioned", "refused"} {
		t.Run(kind, func(t *testing.T) {
			store, _, req := placementReceiptFixture(t)
			ctx := context.Background()
			pull, response := placementReceiptPull(), preparationWireResponse(req)
			response.TenantAuthorityVersion, response.ObjectAuthorityVersion = 103, 105
			query := sourceReceiptQuery(req, response, pull)
			if kind == "unversioned" {
				response.TenantAuthorityVersion, response.ObjectAuthorityVersion = 0, 0
			} else {
				response.Outcome, response.Endpoint = placementpb.PreparationOutcome_PREPARATION_OUTCOME_SOURCE_UNAVAILABLE, ""
			}
			if _, err := store.Begin(ctx, req); err != nil {
				t.Fatal(err)
			}
			if err := store.BindPull(ctx, req, pull); err != nil {
				t.Fatal(err)
			}
			if err := store.Finish(ctx, req, response, pull); err != nil {
				t.Fatal(err)
			}
			if _, err := store.CompletedPull(ctx, query); err == nil {
				t.Fatal("non-admission result published source evidence")
			}
		})
	}
}

func TestPlacementSourceReceiptRejectsExpiryEpochChangeAndCorruption(t *testing.T) {
	for _, change := range []string{"expired", "restart", "promotion", "eviction", "missing-epoch", "wrong-index", "wrong-response", "canceled", "unavailable"} {
		t.Run(change, func(t *testing.T) {
			store, engine, req := placementReceiptFixture(t)
			ctx := context.Background()
			pull, response := placementReceiptPull(), preparationWireResponse(req)
			response.TenantAuthorityVersion, response.ObjectAuthorityVersion = 103, 105
			response.ExpiresAt = timestamppb.New(store.now().Add(5 * time.Second))
			query := sourceReceiptQuery(req, response, pull)
			if _, err := store.Begin(ctx, req); err != nil {
				t.Fatal(err)
			}
			if err := store.BindPull(ctx, req, pull); err != nil {
				t.Fatal(err)
			}
			if err := store.Finish(ctx, req, response, pull); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "expired":
				// Keep the index and caller clock stale to exercise the engine-time fence.
				engine.SetTime(response.ExpiresAt.AsTime())
			case "restart":
				placementReceiptEngineInfo(engine, "c", "b", "0", "master")
			case "promotion":
				placementReceiptEngineInfo(engine, "a", "c", "0", "master")
			case "eviction":
				placementReceiptEngineInfo(engine, "a", "b", "1", "master")
			case "missing-epoch":
				engine.Del("{" + store.CellID + "}:placement_attempt_epoch")
			case "wrong-index":
				index, err := store.sourceReceiptKey(query)
				if err != nil {
					t.Fatal(err)
				}
				other := proto.CloneOf(req)
				other.Query.TenantId = "another-tenant"
				payload, err := proto.Marshal(other)
				if err != nil {
					t.Fatal(err)
				}
				engine.HSet(index, "request", string(payload))
			case "wrong-response":
				response.TenantAuthorityVersion++
				payload, err := proto.Marshal(response)
				if err != nil {
					t.Fatal(err)
				}
				engine.HSet(placementReceiptKey(store, req), "response", string(payload))
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "unavailable":
				engine.Close()
			}
			if _, err := store.CompletedPull(ctx, query); err == nil {
				t.Fatal("stale or corrupt source evidence was accepted")
			}
		})
	}
}
