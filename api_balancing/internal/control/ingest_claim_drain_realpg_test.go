//go:build schema_verify

package control

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
)

func TestRetiredClaimPersistsGenerationFencedDrain_RealPG(t *testing.T) {
	testRetiredClaimDrain(t, startRealPG(t))
}
func testRetiredClaimDrain(t *testing.T, conn *sql.DB) {
	t.Helper()
	previous := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(previous) })
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	const node, stream, claim = "retired-claim-node", "retired-claim-stream", "retired-claim-proof"
	generation := seedOpenIngestSession(t, ingA, node, stream, claim, 777, time.Now().UnixMilli())
	projectAged(t, generation, time.Minute)
	gotNode, retired, err := RetireIngestSessionByClaim(ctx, ingA, stream, claim, logging.NewLogger())
	if err != nil || !retired || gotNode != node {
		t.Fatalf("retire claim: node=%q retired=%v err=%v", gotNode, retired, err)
	}
	var drain, storedGeneration string
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(to_jsonb(e)->>'drain_publisher',''), source_generation::text FROM foghorn.ingest_offline_effects e WHERE tenant_id=$1::uuid AND stream_internal_name=$2`, ingA, stream).Scan(&drain, &storedGeneration); err != nil {
		t.Fatal(err)
	}
	if drain != "true" || storedGeneration != generation {
		t.Fatalf("retired claim lost its durable fenced drain: drain=%q generation=%q, want true / %q", drain, storedGeneration, generation)
	}
}

func TestPublisherDrainSurvivesReconnectAndLateAcknowledgement_RealPG(t *testing.T) {
	testPublisherDrainReconnect(t, startRealPG(t))
}
func testPublisherDrainReconnect(t *testing.T, conn *sql.DB) {
	t.Helper()
	previous := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(previous) })
	ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), ctxkeys.KeyTenantID, ingA), 45*time.Second)
	defer cancel()
	const node, stream = "old-publisher-node", "claim-drain-reconnect"
	generation := seedOpenIngestSession(t, ingA, node, stream, "old-claim", 777, time.Now().UnixMilli()-1000)
	projectAged(t, generation, time.Minute)
	if _, retired, err := RetireIngestSessionByClaim(ctx, ingA, stream, "old-claim", logging.NewLogger()); err != nil || !retired {
		t.Fatalf("retire: retired=%v err=%v", retired, err)
	}
	// Isolate dispatch and settlement from retirement's separately tested intent construction.
	if _, err := conn.ExecContext(ctx, `UPDATE foghorn.ingest_offline_effects SET drain_publisher=TRUE,drain_done=FALSE,drain_runtime_name=$3,attempts=3,ack_wait_attempts=3 WHERE tenant_id=$1::uuid AND stream_internal_name=$2`, ingA, stream, "live+"+stream); err != nil {
		t.Fatal(err)
	}
	// Dispatch must remain possible after ordinary ended-session retention removes the row.
	if _, err := conn.ExecContext(ctx, `DELETE FROM foghorn.ingest_sessions WHERE tenant_id=$1::uuid AND id=$2::uuid AND ended_at IS NOT NULL`, ingA, generation); err != nil {
		t.Fatal(err)
	}
	newer := seedOpenIngestSession(t, ingA, "new-publisher-node", stream, "new-claim", 778, time.Now().UnixMilli())
	effects, err := ClaimOfflineEffects(ctx, 1, time.Minute, "drain-worker")
	if err != nil || len(effects) != 1 {
		t.Fatalf("unconfirmed drain was not retried within its budget: effects=%d err=%v", len(effects), err)
	}
	refusal := errors.New("edge refused drain while its Mist API was unavailable")
	dispatched := false
	complete, err := ApplyClaimedOfflineEffect(ctx, effects[0], func(_ context.Context, effect OfflineEffect) (OfflineEffectLegResults, error) {
		dispatched = true
		if !effect.StateSuperseded || effect.SourceGeneration != generation || effect.DrainRuntimeName != "live+"+stream {
			t.Errorf("drain lost immutable identity or superseded state: %+v", effect)
		}
		return OfflineEffectLegResults{}, refusal
	})
	if !dispatched || complete || !errors.Is(err, refusal) {
		t.Fatalf("reconnect cancelled unacknowledged publisher stop: dispatched=%v complete=%v err=%v", dispatched, complete, err)
	}
	if err := FailOfflineEffect(ctx, effects[0], refusal); err != nil {
		t.Fatal(err)
	}
	dead, err := FailExhaustedOfflineEffects(ctx)
	if err != nil || len(dead) != 0 {
		t.Fatalf("unconfirmed drain dead-lettered: dead=%v err=%v", dead, err)
	}
	// A late acknowledgement can settle a retained failed effect without retrying its drain.
	if _, err := conn.ExecContext(ctx, `UPDATE foghorn.ingest_offline_effects SET state='failed',updated_at=NOW()-INTERVAL '1 day' WHERE tenant_id=$1::uuid AND stream_internal_name=$2`, ingA, stream); err != nil {
		t.Fatal(err)
	}
	if n, err := PurgeTerminalOfflineEffects(ctx, time.Hour); err != nil || n != 0 {
		t.Fatalf("purged owed drain: n=%d err=%v", n, err)
	}
	if dead, err := FailExhaustedOfflineEffects(ctx); err != nil || len(dead) != 0 {
		t.Fatalf("failed drain unexpectedly revived: dead=%v err=%v", dead, err)
	}
	for _, ack := range [][2]string{{"another-node", generation}, {node, newer}} {
		if err := MarkOfflinePublisherDrainDone(ctx, ack[0], ack[1]); err != nil {
			t.Fatal(err)
		}
	}
	var done bool
	if err := conn.QueryRowContext(ctx, `SELECT drain_done FROM foghorn.ingest_offline_effects WHERE tenant_id=$1::uuid AND stream_internal_name=$2`, ingA, stream).Scan(&done); err != nil || done {
		t.Fatalf("unrelated acknowledgement settled drain: done=%v err=%v", done, err)
	}
	if err := MarkOfflinePublisherDrainDone(ctx, node, generation); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `UPDATE foghorn.ingest_offline_effects SET next_attempt_at=NOW()-INTERVAL '1 second' WHERE tenant_id=$1::uuid AND stream_internal_name=$2`, ingA, stream); err != nil {
		t.Fatal(err)
	}
	effects, err = ClaimOfflineEffects(ctx, 1, time.Minute, "replacement-worker")
	if err != nil || len(effects) != 1 {
		t.Fatalf("retry after reconnect: count=%d err=%v", len(effects), err)
	}
	complete, err = ApplyClaimedOfflineEffect(ctx, effects[0], func(context.Context, OfflineEffect) (OfflineEffectLegResults, error) {
		// The acknowledgement can race the worker's unlocked network dispatch.
		return OfflineEffectLegResults{}, MarkOfflinePublisherDrainDone(ctx, node, generation)
	})
	if err != nil || !complete {
		t.Fatalf("matching acknowledgement did not settle drain: complete=%v err=%v", complete, err)
	}
	var state string
	if err := conn.QueryRowContext(ctx, `SELECT state FROM foghorn.ingest_offline_effects WHERE tenant_id=$1::uuid AND stream_internal_name=$2`, ingA, stream).Scan(&state); err != nil || state != "superseded" {
		t.Fatalf("settled drain: state=%q err=%v", state, err)
	}
	var newerLive bool
	if err := conn.QueryRowContext(ctx, `SELECT ended_at IS NULL FROM foghorn.ingest_sessions WHERE tenant_id=$1::uuid AND id=$2::uuid`, ingA, newer).Scan(&newerLive); err != nil || !newerLive {
		t.Fatalf("old drain ended replacement: live=%v err=%v", newerLive, err)
	}
}

func TestPublisherClaimDrain_RealYugabyte(t *testing.T) {
	for _, proof := range []struct {
		name string
		run  func(*testing.T, *sql.DB)
	}{{"retirement", testRetiredClaimDrain}, {"reconnect", testPublisherDrainReconnect}} {
		t.Run(proof.name, func(t *testing.T) {
			conn, _, ok := dockerpg.OpenSharedYugabyteBaselineWithDSN(t, "claim_drain_"+proof.name, "foghorn")
			if !ok {
				t.Fatal("requires shared Yugabyte fixture")
			}
			proof.run(t, conn)
		})
	}
}
