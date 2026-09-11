//go:build schema_verify

package federation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockervalkey"
	"google.golang.org/protobuf/proto"
)

func TestPlacementReceipts_RealValkey(t *testing.T) {
	engine := dockervalkey.Start(t)
	ctx := context.Background()
	store := &PlacementReceiptStore{Client: engine.Client, CellID: "receipt-contract"}
	primePlacementReceiptEpoch(t, store)
	time.Sleep(2*placement.PreparationClockSkew + 2*time.Millisecond)
	req := placementReceiptRequest(t, time.Now())
	receipt, err := store.Begin(ctx, req)
	if err != nil || !receipt.Fresh {
		engineTime, clockErr := engine.Client.Time(ctx).Result()
		t.Fatalf("begin on real engine: %+v, %v; host=%v engine=%v clock_error=%v", receipt, err, time.Now().UTC(), engineTime, clockErr)
	}
	pull := placementReceiptPull()
	if bindErr := store.BindPull(ctx, req, pull); bindErr != nil {
		t.Fatal(bindErr)
	}
	ack := preparationWireResponse(req)
	ack.TenantAuthorityVersion, ack.ObjectAuthorityVersion = 103, 105
	if finishErr := store.Finish(ctx, req, ack, pull); finishErr != nil {
		t.Fatal(finishErr)
	}
	second := &PlacementReceiptStore{Client: engine.Client, CellID: store.CellID}
	query := sourceReceiptQuery(req, ack, pull)
	completed, err := second.CompletedPull(ctx, query)
	if err != nil || !proto.Equal(completed.Response, ack) || completed.Pull == nil || *completed.Pull != *pull {
		t.Fatalf("real engine lost completed source evidence: %+v, %v", completed, err)
	}
	replayed, err := second.Begin(ctx, req)
	if err != nil || replayed.Fresh || replayed.Pull == nil || *replayed.Pull != *pull || !proto.Equal(ack, replayed.Response) {
		t.Fatalf("replica lost exact attempt/revision: %+v, %v", replayed, err)
	}
	key := placementReceiptKey(store, req)
	before, err := engine.Client.HGetAll(ctx, key).Result()
	if err != nil {
		t.Fatal(err)
	}
	engine.Restart(t)
	store.Client = engine.Client
	after, err := engine.Client.HGetAll(ctx, key).Result()
	if err != nil || len(after) != len(before) {
		t.Fatalf("restart lost persisted receipt: before=%d after=%d, %v", len(before), len(after), err)
	}
	for field, value := range before {
		if after[field] != value {
			t.Fatalf("restart changed persisted %s", field)
		}
	}
	if _, err := store.CompletedPull(ctx, query); !errors.Is(err, ErrPlacementReceiptUnavailable) {
		t.Fatalf("persisted source index escaped the engine restart fence: %v", err)
	}
	if replayed, err := store.Begin(ctx, req); !errors.Is(err, ErrPlacementReceiptUnavailable) || replayed.Fresh {
		t.Fatalf("restarted engine reused a pre-restart attempt: %+v, %v", replayed, err)
	}
	time.Sleep(2*placement.PreparationClockSkew + 2*time.Millisecond)
	newRequest := placementReceiptRequest(t, time.Now())
	if receipt, err := store.Begin(ctx, newRequest); err != nil || !receipt.Fresh {
		engineTime, clockErr := engine.Client.Time(ctx).Result()
		issued, _ := placement.PreparationIssuedAt(newRequest.AttemptId)
		t.Fatalf("new engine epoch could not record intent: %+v, %v; issued=%v host=%v engine=%v clock_error=%v", receipt, err, issued, time.Now().UTC(), engineTime, clockErr)
	}
	if _, err := store.Begin(ctx, req); !errors.Is(err, ErrPlacementReceiptUnavailable) {
		t.Fatalf("new intent unfenced pre-restart attempt: %v", err)
	}
}
