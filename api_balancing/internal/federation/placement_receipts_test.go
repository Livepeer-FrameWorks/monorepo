package federation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"github.com/alicebob/miniredis/v2"
	redisserver "github.com/alicebob/miniredis/v2/server"
	goredis "github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func placementReceiptRequest(t testing.TB, now time.Time) *placementpb.PreparePlacementRequest {
	t.Helper()
	attempt, err := placement.NewPreparationAttemptID(now)
	if err != nil {
		t.Fatal(err)
	}
	return &placementpb.PreparePlacementRequest{
		Query: &placementpb.CandidateQuery{TenantId: "tenant", ObjectId: "object", InternalName: "internal",
			Verb: placementpb.Verb_VERB_SERVE, Protocol: "hls", SourceGeneration: "publisher-session",
			ClusterIds: []string{"us", "eu"}, PolicyRevision: 4, ParentRevision: 8, PolicyDigest: strings.Repeat("a", 64)},
		ClusterId: "us", NodeId: "edge", AttemptId: attempt, ExpiresAt: timestamppb.New(now.Add(20 * time.Second)),
	}
}

func placementReceiptFixture(t *testing.T) (*PlacementReceiptStore, *miniredis.Miniredis, *placementpb.PreparePlacementRequest) {
	t.Helper()
	now := time.Unix(1800000000, 123000000).UTC()
	server := miniredis.RunT(t)
	placementReceiptEngineInfo(server, "a", "b", "0", "master")
	server.SetTime(now)
	client := goredis.NewClient(&goredis.Options{Addr: server.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	store := &PlacementReceiptStore{Client: client, CellID: "us-cell", Now: func() time.Time { return now }}
	primePlacementReceiptEpoch(t, store)
	now = now.Add(2*placement.PreparationClockSkew + time.Millisecond)
	server.SetTime(now)
	return store, server, placementReceiptRequest(t, now)
}

// Miniredis does not implement engine identity or replication INFO sections.
// Actual script compatibility and restart fencing are tested on pinned Valkey.
func placementReceiptEngineInfo(server *miniredis.Miniredis, runID, replID, evicted, role string) {
	server.Server().SetPreHook(func(peer *redisserver.Peer, command string, args ...string) bool {
		if command != "INFO" || len(args) != 1 {
			return false
		}
		switch args[0] {
		case "server":
			peer.WriteBulk("# Server\r\nrun_id:" + strings.Repeat(runID, 40) + "\r\n")
		case "replication":
			peer.WriteBulk("# Replication\r\nmaster_replid:" + strings.Repeat(replID, 40) + "\r\nrole:" + role + "\r\n")
		case "stats":
			peer.WriteBulk("# Stats\r\nevicted_keys:" + evicted + "\r\n")
		default:
			return false
		}
		return true
	})
}

func primePlacementReceiptEpoch(t testing.TB, store *PlacementReceiptStore) {
	t.Helper()
	req := placementReceiptRequest(t, store.now())
	if receipt, err := store.Begin(context.Background(), req); !errors.Is(err, ErrPlacementReceiptUnavailable) || receipt.Fresh {
		t.Fatalf("uninitialized coordination accepted an attempt: %+v, %v", receipt, err)
	}
}

func TestPlacementReceiptEpochCanBePreparedBeforeAdmission(t *testing.T) {
	now := time.Unix(1800000000, 123000000).UTC()
	server := miniredis.RunT(t)
	placementReceiptEngineInfo(server, "a", "b", "0", "master")
	server.SetTime(now)
	client := goredis.NewClient(&goredis.Options{Addr: server.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	store := &PlacementReceiptStore{Client: client, CellID: "us-cell", Now: func() time.Time { return now }}

	delay, err := store.epochReadyDelay(context.Background())
	if err != nil || delay != 2*placement.PreparationClockSkew+time.Millisecond {
		t.Fatalf("initial epoch delay = %s, %v", delay, err)
	}
	now = now.Add(delay)
	server.SetTime(now)
	delay, err = store.epochReadyDelay(context.Background())
	if err != nil || delay != 0 {
		t.Fatalf("prepared epoch delay = %s, %v", delay, err)
	}
	if receipt, beginErr := store.Begin(context.Background(), placementReceiptRequest(t, now)); beginErr != nil || !receipt.Fresh {
		t.Fatalf("prepared epoch rejected first placement: %+v, %v", receipt, beginErr)
	}
}

func placementReceiptPull() *PlacementPullBinding {
	return &PlacementPullBinding{AttemptID: "10000000-0000-4000-8000-000000000001", SourceCellID: "eu-cell",
		DestinationFence: 9007199254740993,
		SourceClusterID:  "eu", SourceNodeID: "publisher", SourceGeneration: "publisher-session", SourceRevision: 9007199254740993}
}

func placementReceiptKey(store *PlacementReceiptStore, req *placementpb.PreparePlacementRequest) string {
	return fmt.Sprintf("{%s}:placement_attempt:%d:%s:%s", store.CellID, len(req.Query.TenantId), req.Query.TenantId, req.AttemptId)
}

func TestPlacementReceiptReplicasFreezeIntentAndPhysicalPull(t *testing.T) {
	store, _, req := placementReceiptFixture(t)
	ctx := context.Background()
	second := &PlacementReceiptStore{Client: store.Client, CellID: store.CellID, Now: store.Now}
	const count = 24
	results := make(chan PlacementReceipt, count)
	errs := make(chan error, count)
	var group sync.WaitGroup
	for i := range count {
		group.Go(func() {
			replica := store
			if i%2 == 0 {
				replica = second
			}
			receipt, err := replica.Begin(ctx, req)
			results <- receipt
			errs <- err
		})
	}
	group.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	fresh := 0
	for receipt := range results {
		if receipt.Fresh {
			fresh++
		}
		if receipt.Pull != nil || receipt.Response != nil {
			t.Fatal("intent implied a pull or acknowledgement")
		}
	}
	if fresh != 1 {
		t.Fatalf("%d replicas created the attempt", fresh)
	}
	pull := placementReceiptPull()
	if err := store.BindPull(ctx, req, pull); err != nil {
		t.Fatal(err)
	}
	if err := second.BindPull(ctx, req, pull); err != nil {
		t.Fatalf("same pull was not idempotent: %v", err)
	}
	ack := preparationWireResponse(req)
	if err := second.Finish(ctx, req, ack, nil); !errors.Is(err, ErrPlacementReceiptConflict) {
		t.Fatalf("completion did not require recorded physical pull: %v", err)
	}
	if err := second.Finish(ctx, req, ack, pull); err != nil {
		t.Fatal(err)
	}
	if err := store.Finish(ctx, req, ack, pull); err != nil {
		t.Fatalf("same completion was not idempotent: %v", err)
	}
	receipt, err := second.Begin(ctx, req)
	if err != nil || receipt.Fresh || receipt.Pull == nil || *receipt.Pull != *pull || !proto.Equal(ack, receipt.Response) {
		t.Fatalf("cross-replica receipt lost exact physical pull or response: %+v, %v", receipt, err)
	}
	if receipt.Pull.AttemptID == receipt.Request.AttemptId {
		t.Fatal("viewer attempt relabeled a shared physical pull")
	}
	receipt.Pull.SourceRevision++
	receipt.Request.NodeId = "mutated"
	receipt.Response.Endpoint = "https://mutated.example"
	again, err := store.Begin(ctx, req)
	if err != nil || *again.Pull != *pull || again.Request.NodeId != req.NodeId || !proto.Equal(again.Response, ack) {
		t.Fatal("returned mutable objects changed stored receipt")
	}
	ack.Ready = true
	if err := store.Finish(ctx, req, ack, pull); !errors.Is(err, ErrPlacementReceiptConflict) {
		t.Fatalf("changed readiness overwrote immutable acknowledgement: %v", err)
	}
}

func TestPlacementReceiptIntentIdentityAndIsolation(t *testing.T) {
	store, server, req := placementReceiptFixture(t)
	ctx := context.Background()
	if _, err := store.Begin(ctx, req); err != nil {
		t.Fatal(err)
	}
	reordered := proto.CloneOf(req)
	reordered.Query.ClusterIds = []string{"eu", "us"}
	if receipt, err := store.Begin(ctx, reordered); err != nil || receipt.Fresh {
		t.Fatalf("cluster set order changed intent: %+v, %v", receipt, err)
	}
	for name, change := range map[string]func(*placementpb.PreparePlacementRequest){
		"node":          func(r *placementpb.PreparePlacementRequest) { r.NodeId = "another" },
		"cluster":       func(r *placementpb.PreparePlacementRequest) { r.ClusterId = "eu" },
		"object":        func(r *placementpb.PreparePlacementRequest) { r.Query.ObjectId = "another" },
		"internal_name": func(r *placementpb.PreparePlacementRequest) { r.Query.InternalName = "another" },
		"generation":    func(r *placementpb.PreparePlacementRequest) { r.Query.SourceGeneration = "successor" },
		"protocol":      func(r *placementpb.PreparePlacementRequest) { r.Query.Protocol = "dash" },
		"policy":        func(r *placementpb.PreparePlacementRequest) { r.Query.PolicyRevision++ },
		"parent":        func(r *placementpb.PreparePlacementRequest) { r.Query.ParentRevision++ },
		"digest":        func(r *placementpb.PreparePlacementRequest) { r.Query.PolicyDigest = strings.Repeat("b", 64) },
		"location": func(r *placementpb.PreparePlacementRequest) {
			r.Query.ClientLocation = &placementpb.Coordinates{Latitude: 40}
		},
		"shorten_expiry": func(r *placementpb.PreparePlacementRequest) {
			r.ExpiresAt = timestamppb.New(r.ExpiresAt.AsTime().Add(-time.Second))
		},
		"extend_expiry": func(r *placementpb.PreparePlacementRequest) {
			r.ExpiresAt = timestamppb.New(r.ExpiresAt.AsTime().Add(time.Second))
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := proto.CloneOf(req)
			change(changed)
			if _, err := store.Begin(ctx, changed); !errors.Is(err, ErrPlacementReceiptConflict) {
				t.Fatalf("changed request reused intent: %v", err)
			}
		})
	}
	other := proto.CloneOf(req)
	other.Query.TenantId = "another-tenant"
	if receipt, err := store.Begin(ctx, other); err != nil || !receipt.Fresh {
		t.Fatalf("another tenant shared receipt: %+v, %v", receipt, err)
	}
	otherCell := &PlacementReceiptStore{Client: store.Client, CellID: "another-cell", Now: store.Now}
	primePlacementReceiptEpoch(t, otherCell)
	later := store.now().Add(2*placement.PreparationClockSkew + time.Millisecond)
	otherCell.Now = func() time.Time { return later }
	server.SetTime(later)
	otherCellRequest := proto.CloneOf(req)
	otherCellRequest.AttemptId = testPreparationAttemptAt(t, later)
	store.Now = otherCell.Now
	if receipt, err := store.Begin(ctx, otherCellRequest); err != nil || !receipt.Fresh {
		t.Fatalf("first cell could not record isolated attempt: %+v, %v", receipt, err)
	}
	if receipt, err := otherCell.Begin(ctx, otherCellRequest); err != nil || !receipt.Fresh {
		t.Fatalf("another cell shared receipt: %+v, %v", receipt, err)
	}
}

func TestPlacementReceiptRequiresIntentAndImmutablePull(t *testing.T) {
	store, _, req := placementReceiptFixture(t)
	ctx := context.Background()
	pull := placementReceiptPull()
	ack := preparationWireResponse(req)
	if err := store.BindPull(ctx, req, pull); !errors.Is(err, ErrPlacementReceiptMissing) {
		t.Fatalf("pull without intent: %v", err)
	}
	if err := store.Finish(ctx, req, ack, nil); !errors.Is(err, ErrPlacementReceiptMissing) {
		t.Fatalf("completion without intent: %v", err)
	}
	if _, err := store.Begin(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := store.Finish(ctx, req, ack, pull); !errors.Is(err, ErrPlacementReceiptConflict) {
		t.Fatalf("completion implicitly attached a pull: %v", err)
	}
	if err := store.BindPull(ctx, req, pull); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*PlacementPullBinding){
		"attempt":           func(p *PlacementPullBinding) { p.AttemptID = "10000000-0000-4000-8000-000000000002" },
		"cell":              func(p *PlacementPullBinding) { p.SourceCellID = "other" },
		"cluster":           func(p *PlacementPullBinding) { p.SourceClusterID = "other" },
		"node":              func(p *PlacementPullBinding) { p.SourceNodeID = "other" },
		"revision":          func(p *PlacementPullBinding) { p.SourceRevision++ },
		"destination-fence": func(p *PlacementPullBinding) { p.DestinationFence++ },
		"generation":        func(p *PlacementPullBinding) { p.SourceGeneration = "successor" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := *pull
			change(&changed)
			if err := store.BindPull(ctx, req, &changed); !errors.Is(err, ErrPlacementReceiptConflict) {
				t.Fatalf("replaced bound physical pull: %v", err)
			}
			if err := store.Finish(ctx, req, ack, &changed); !errors.Is(err, ErrPlacementReceiptConflict) {
				t.Fatalf("completed against another physical pull: %v", err)
			}
		})
	}
	local := proto.CloneOf(req)
	local.AttemptId = testPreparationAttemptAt(t, store.now())
	if _, err := store.Begin(ctx, local); err != nil {
		t.Fatal(err)
	}
	if err := store.Finish(ctx, local, preparationWireResponse(local), nil); err != nil {
		t.Fatal(err)
	}
	if err := store.BindPull(ctx, local, pull); !errors.Is(err, ErrPlacementReceiptConflict) {
		t.Fatalf("completed local media acquired a pull afterward: %v", err)
	}
}

func testPreparationAttemptAt(t testing.TB, now time.Time) string {
	t.Helper()
	id, err := placement.NewPreparationAttemptID(now)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestPlacementReceiptExpiryAndNoRenewal(t *testing.T) {
	store, server, req := placementReceiptFixture(t)
	ctx := context.Background()
	key := placementReceiptKey(store, req)
	if _, err := store.Begin(ctx, req); err != nil {
		t.Fatal(err)
	}
	if ttl := server.TTL(key); ttl != 35*time.Second {
		t.Fatalf("receipt retention %v", ttl)
	}
	now := store.now().Add(5 * time.Second)
	store.Now = func() time.Time { return now }
	server.FastForward(5 * time.Second)
	server.SetTime(now)
	if _, err := store.Begin(ctx, req); err != nil {
		t.Fatal(err)
	}
	pull := placementReceiptPull()
	if err := store.BindPull(ctx, req, pull); err != nil {
		t.Fatal(err)
	}
	if err := store.Finish(ctx, req, preparationWireResponse(req), pull); err != nil {
		t.Fatal(err)
	}
	if ttl := server.TTL(key); ttl != 30*time.Second {
		t.Fatalf("replay or mutations renewed retention: %v", ttl)
	}
	// An independent storage clock fences a Foghorn whose clock falls behind.
	server.SetTime(req.ExpiresAt.AsTime())
	if _, err := store.Begin(ctx, req); !errors.Is(err, ErrPlacementReceiptExpired) {
		t.Fatalf("Redis accepted expired intent: %v", err)
	}
	if err := store.BindPull(ctx, req, pull); !errors.Is(err, ErrPlacementReceiptExpired) {
		t.Fatalf("Redis accepted expired pull binding: %v", err)
	}
	if err := store.Finish(ctx, req, preparationWireResponse(req), pull); !errors.Is(err, ErrPlacementReceiptExpired) {
		t.Fatalf("Redis accepted expired acknowledgement: %v", err)
	}
	now = now.Add(31 * time.Second)
	server.FastForward(31 * time.Second)
	server.SetTime(now)
	if server.Exists(key) {
		t.Fatal("receipt retained beyond absolute horizon")
	}
	renewed := proto.CloneOf(req)
	renewed.ExpiresAt = timestamppb.New(now.Add(time.Second))
	if _, err := store.Begin(ctx, renewed); err == nil || server.Exists(key) {
		t.Fatal("expired ID recreated receipt with a new deadline")
	}
}

func TestPlacementReceiptCorruptionFailsClosed(t *testing.T) {
	for _, field := range []string{"request", "pull", "response"} {
		t.Run(field, func(t *testing.T) {
			store, server, req := placementReceiptFixture(t)
			if _, err := store.Begin(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			server.HSet(placementReceiptKey(store, req), field, "corrupt")
			if receipt, err := store.Begin(context.Background(), req); err == nil || receipt.Fresh || receipt.Response != nil {
				t.Fatalf("corrupt receipt returned success: %+v, %v", receipt, err)
			}
		})
	}
}

func TestPlacementReceiptUnavailableHasNoFallback(t *testing.T) {
	store, _, req := placementReceiptFixture(t)
	if err := store.Client.Close(); err != nil {
		t.Fatal(err)
	}
	for _, unavailable := range []*PlacementReceiptStore{nil, {}, store, {Client: store.Client, CellID: "{bad}"}} {
		if receipt, err := unavailable.Begin(context.Background(), req); !errors.Is(err, ErrPlacementReceiptUnavailable) || receipt.Fresh {
			t.Fatalf("storage failure yielded local intent: %+v, %v", receipt, err)
		}
	}
}

func TestPlacementReceiptLostStateFencesEarlierAttempts(t *testing.T) {
	for _, scenario := range []string{"restart", "primary_change", "eviction", "missing_epoch"} {
		t.Run(scenario, func(t *testing.T) {
			store, server, req := placementReceiptFixture(t)
			ctx := context.Background()
			if _, err := store.Begin(ctx, req); err != nil {
				t.Fatal(err)
			}
			key := placementReceiptKey(store, req)
			server.Del(key)
			switch scenario {
			case "restart":
				placementReceiptEngineInfo(server, "c", "b", "0", "master")
			case "primary_change":
				placementReceiptEngineInfo(server, "a", "c", "0", "master")
			case "eviction":
				placementReceiptEngineInfo(server, "a", "b", "1", "master")
			case "missing_epoch":
				server.Del(fmt.Sprintf("{%s}:placement_attempt_epoch", store.CellID))
			}
			for range 2 {
				if receipt, err := store.Begin(ctx, req); !errors.Is(err, ErrPlacementReceiptUnavailable) || receipt.Fresh || server.Exists(key) {
					t.Fatalf("lost receipt authorized replay: %+v, %v", receipt, err)
				}
			}
			later := store.now().Add(2*placement.PreparationClockSkew + time.Millisecond)
			store.Now = func() time.Time { return later }
			server.SetTime(later)
			fresh := placementReceiptRequest(t, later)
			if receipt, err := store.Begin(ctx, fresh); err != nil || !receipt.Fresh {
				t.Fatalf("new epoch could not record new intent: %+v, %v", receipt, err)
			}
			if _, err := store.Begin(ctx, req); !errors.Is(err, ErrPlacementReceiptUnavailable) {
				t.Fatalf("new intent reopened old epoch: %v", err)
			}
		})
	}
}

func TestPlacementReceiptRequiresPrimaryAndIntactEpoch(t *testing.T) {
	for _, scenario := range []string{"replica", "missing_identity", "corrupt_watermark", "missing_digest"} {
		t.Run(scenario, func(t *testing.T) {
			store, server, req := placementReceiptFixture(t)
			ctx := context.Background()
			if _, err := store.Begin(ctx, req); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "replica":
				placementReceiptEngineInfo(server, "a", "b", "0", "slave")
			case "missing_identity":
				placementReceiptEngineInfo(server, "", "b", "0", "master")
			case "corrupt_watermark":
				server.HSet(fmt.Sprintf("{%s}:placement_attempt_epoch", store.CellID), "not_before", "nan")
			case "missing_digest":
				if err := store.Client.HDel(ctx, placementReceiptKey(store, req), "digest").Err(); err != nil {
					t.Fatal(err)
				}
			}
			if receipt, err := store.Begin(ctx, req); !errors.Is(err, ErrPlacementReceiptUnavailable) || receipt.Fresh {
				t.Fatalf("inconsistent storage yielded intent: %+v, %v", receipt, err)
			}
			if err := store.BindPull(ctx, req, placementReceiptPull()); !errors.Is(err, ErrPlacementReceiptUnavailable) {
				t.Fatalf("inconsistent storage accepted binding: %v", err)
			}
			if err := store.Finish(ctx, req, preparationWireResponse(req), nil); !errors.Is(err, ErrPlacementReceiptUnavailable) {
				t.Fatalf("inconsistent storage accepted acknowledgement: %v", err)
			}
		})
	}
}

func TestPlacementReceiptConcurrentPhysicalBindingsHaveOneWinner(t *testing.T) {
	store, _, req := placementReceiptFixture(t)
	ctx := context.Background()
	if _, err := store.Begin(ctx, req); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var group sync.WaitGroup
	for _, node := range []string{"publisher-a", "publisher-b"} {
		group.Go(func() {
			pull := placementReceiptPull()
			pull.SourceNodeID = node
			results <- store.BindPull(ctx, req, pull)
		})
	}
	group.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, ErrPlacementReceiptConflict) {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("%d distinct physical pulls were bound", winners)
	}
}

func TestPlacementReceiptRejectsMalformedPullBindings(t *testing.T) {
	store, _, req := placementReceiptFixture(t)
	for name, change := range map[string]func(*PlacementPullBinding){
		"missing_attempt":            func(p *PlacementPullBinding) { p.AttemptID = "" },
		"noncanonical_attempt":       func(p *PlacementPullBinding) { p.AttemptID = "10000000000040008000000000000001" },
		"missing_cell":               func(p *PlacementPullBinding) { p.SourceCellID = "" },
		"invalid_cluster":            func(p *PlacementPullBinding) { p.SourceClusterID = "eu\u0000cluster" },
		"invalid_node":               func(p *PlacementPullBinding) { p.SourceNodeID = " publisher" },
		"missing_revision":           func(p *PlacementPullBinding) { p.SourceRevision = 0 },
		"missing_destination_fence":  func(p *PlacementPullBinding) { p.DestinationFence = 0 },
		"negative_destination_fence": func(p *PlacementPullBinding) { p.DestinationFence = -1 },
		"missing_generation":         func(p *PlacementPullBinding) { p.SourceGeneration = "" },
	} {
		t.Run(name, func(t *testing.T) {
			pull := placementReceiptPull()
			change(pull)
			if err := store.BindPull(context.Background(), req, pull); !errors.Is(err, ErrPlacementReceiptConflict) {
				t.Fatalf("invalid physical binding reached storage: %v", err)
			}
		})
	}
	if err := store.BindPull(context.Background(), req, nil); !errors.Is(err, ErrPlacementReceiptConflict) {
		t.Fatalf("nil physical binding accepted: %v", err)
	}
	ingest := proto.CloneOf(req)
	ingest.Query.Verb = placementpb.Verb_VERB_INGEST
	if err := store.BindPull(context.Background(), ingest, placementReceiptPull()); !errors.Is(err, ErrPlacementReceiptConflict) {
		t.Fatalf("publisher admission acquired an upstream pull: %v", err)
	}
}

func TestPlacementReceiptClockSkewDoesNotReopenPreResetAttempts(t *testing.T) {
	store, server, _ := placementReceiptFixture(t)
	ctx := context.Background()
	base := store.now()
	issued := base.Add(placement.PreparationClockSkew)
	req := placementReceiptRequest(t, issued)
	if receipt, err := store.Begin(ctx, req); err != nil || !receipt.Fresh {
		t.Fatalf("bounded future issuance rejected: %+v, %v", receipt, err)
	}
	server.Del(placementReceiptKey(store, req))
	placementReceiptEngineInfo(server, "c", "d", "0", "master")
	// The replacement is one skew window behind the old engine. An already
	// accepted coordinator timestamp can consequently be two windows ahead.
	server.SetTime(base.Add(-placement.PreparationClockSkew))
	primePlacementReceiptEpoch(t, store)
	server.SetTime(base)
	for _, operation := range []string{"begin", "bind", "finish"} {
		var err error
		switch operation {
		case "begin":
			_, err = store.Begin(ctx, req)
		case "bind":
			err = store.BindPull(ctx, req, placementReceiptPull())
		case "finish":
			err = store.Finish(ctx, req, preparationWireResponse(req), nil)
		}
		if !errors.Is(err, ErrPlacementReceiptUnavailable) {
			t.Fatalf("skew let an old %s cross the reset watermark: %v", operation, err)
		}
	}
	if server.Exists(placementReceiptKey(store, req)) {
		t.Fatal("skew recreated a lost intent")
	}
	tooFar := placementReceiptRequest(t, base.Add(placement.PreparationClockSkew+time.Millisecond))
	store.Now = func() time.Time { return base.Add(placement.PreparationClockSkew) }
	if _, err := store.Begin(ctx, tooFar); !errors.Is(err, ErrPlacementReceiptExpired) {
		t.Fatalf("Redis did not independently reject excessive skew: %v", err)
	}
	later := issued.Add(time.Millisecond)
	server.SetTime(later)
	store.Now = func() time.Time { return later }
	if receipt, err := store.Begin(ctx, placementReceiptRequest(t, later)); err != nil || !receipt.Fresh {
		t.Fatalf("new post-watermark intent rejected: %+v, %v", receipt, err)
	}
	if _, err := store.Begin(ctx, req); !errors.Is(err, ErrPlacementReceiptUnavailable) {
		t.Fatalf("a later request unfenced the old skewed attempt: %v", err)
	}
}
