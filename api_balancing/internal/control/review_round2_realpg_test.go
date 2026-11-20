//go:build schema_verify

package control

import (
	"context"
	"testing"
	"time"

	foghorndb "frameworks/api_balancing/internal/database/foghorndb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/google/uuid"
)

func seedReviewRound2Effect(t *testing.T, tenant, generation, state string) {
	t.Helper()
	_, err := db.ExecContext(t.Context(), `INSERT INTO foghorn.ingest_offline_effects(tenant_id,stream_internal_name,source_node_id,source_generation,source_revision,drain_publisher,drain_done,drain_runtime_name,teardown_stream,teardown_done,state) VALUES($1::uuid,'review','node',$2::uuid,1,true,false,'live+review',true,false,$3)`, tenant, generation, state)
	if err != nil {
		t.Fatal(err)
	}
}

func TestReviewRound2LegacyPublisherDrainAcknowledgement_RealPG(t *testing.T) {
	conn := startRealPG(t)
	previous := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(previous) })
	tenant, generation := uuid.NewString(), uuid.NewString()
	seedReviewRound2Effect(t, tenant, generation, "pending")
	cleanup := SetupTestRegistry("node", &fakeControlStream{})
	t.Cleanup(cleanup)
	session, ok := currentNodeSession("node")
	if !ok {
		t.Fatal("missing current node session")
	}
	processDrainStreamResponse(&ipcpb.DrainStreamResponse{RuntimeName: "live+review", SourceGeneration: generation}, session, logging.NewLogger())
	var done bool
	if err := conn.QueryRowContext(t.Context(), `SELECT drain_done FROM foghorn.ingest_offline_effects WHERE tenant_id=$1::uuid AND source_generation=$2::uuid`, tenant, generation).Scan(&done); err != nil {
		t.Fatal(err)
	}
	if !done {
		t.Fatal("successful legacy edge drain was not durably acknowledged")
	}
}

func TestReviewRound2TeardownAcknowledgementIsTenantBound_RealPG(t *testing.T) {
	for _, state := range []string{"pending", "failed"} {
		t.Run(state, func(t *testing.T) {
			conn := startRealPG(t)
			previous := db
			SetDB(conn)
			t.Cleanup(func() { SetDB(previous) })
			tenant, foreign, generation := uuid.NewString(), uuid.NewString(), uuid.NewString()
			seedReviewRound2Effect(t, tenant, generation, state)
			seedReviewRound2Effect(t, foreign, generation, state)
			if err := MarkOfflineTeardownDone(context.WithValue(t.Context(), ctxkeys.KeyTenantID, tenant), "node", generation); err != nil {
				t.Fatal(err)
			}
			var done bool
			if err := conn.QueryRowContext(t.Context(), `SELECT teardown_done FROM foghorn.ingest_offline_effects WHERE tenant_id=$1::uuid AND source_generation=$2::uuid`, foreign, generation).Scan(&done); err != nil {
				t.Fatal(err)
			}
			if done {
				t.Fatal("teardown acknowledgement crossed tenant boundary")
			}
		})
	}
}

func TestReviewRound2IncompleteEffectPreservesAttemptBudget_RealPG(t *testing.T) {
	conn := startRealPG(t)
	previous := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(previous) })
	tenant, generation := uuid.NewString(), uuid.NewString()
	seedReviewRound2Effect(t, tenant, generation, "pending")
	if _, err := conn.ExecContext(t.Context(), `UPDATE foghorn.ingest_offline_effects SET attempts=5 WHERE tenant_id=$1::uuid`, tenant); err != nil {
		t.Fatal(err)
	}
	effects, err := ClaimOfflineEffects(t.Context(), 1, time.Minute, "review-r2")
	if err != nil || len(effects) != 1 {
		t.Fatal(effects, err)
	}
	e := effects[0]
	_, err = foghorndb.New(conn).SettleOfflineEffect(t.Context(), foghorndb.SettleOfflineEffectParams{EffectID: e.ID, LeaseToken: e.LeaseToken, ReleaseLease: true})
	if err != nil {
		t.Fatal(err)
	}
	var attempts int
	if err = conn.QueryRowContext(t.Context(), `SELECT attempts FROM foghorn.ingest_offline_effects WHERE tenant_id=$1::uuid AND id=$2`, tenant, e.ID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts < 6 {
		t.Fatalf("successful incomplete dispatch reset retry budget: %d", attempts)
	}
	if _, err = conn.ExecContext(t.Context(), `UPDATE foghorn.ingest_offline_effects SET attempts=11,next_attempt_at=NOW(),state='pending',lease_token=NULL,leased_until=NULL WHERE tenant_id=$1::uuid AND id=$2`, tenant, e.ID); err != nil {
		t.Fatal(err)
	}
	final, err := ClaimOfflineEffects(t.Context(), 1, time.Minute, "review-r6")
	if err != nil || len(final) != 1 {
		t.Fatalf("claim final partial leg: %v %v", final, err)
	}
	_, err = foghorndb.New(conn).SettleOfflineEffect(t.Context(), foghorndb.SettleOfflineEffectParams{EffectID: final[0].ID, LeaseToken: final[0].LeaseToken, ReleaseLease: true, DecklogDone: true})
	if err != nil {
		t.Fatal(err)
	}
	var state string
	if err = conn.QueryRowContext(t.Context(), `SELECT state,attempts FROM foghorn.ingest_offline_effects WHERE tenant_id=$1::uuid AND id=$2`, tenant, e.ID).Scan(&state, &attempts); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || attempts != 12 {
		t.Fatalf("partial progress revived a persistently failing leg: state=%s attempts=%d", state, attempts)
	}

}

func TestReviewRound2ObsoleteDrainRetryIndexIsAbsent_RealPG(t *testing.T) {
	conn := startRealPG(t)
	var exists bool
	if err := conn.QueryRowContext(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_indexes WHERE schemaname='foghorn' AND indexname='idx_foghorn_ingest_offline_effects_drain_retry')`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("obsolete failed-drain revival index is still in the baseline")
	}
}

func TestReviewRound2AmbiguousLegacyAcknowledgementIsRefused_RealPG(t *testing.T) {
	conn := startRealPG(t)
	previous := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(previous) })
	generation := uuid.NewString()
	for range 2 {
		seedReviewRound2Effect(t, uuid.NewString(), generation, "pending")
	}
	if err := MarkOfflinePublisherDrainDone(t.Context(), "node", generation); err == nil {
		t.Fatal("ambiguous legacy drain acknowledgement was accepted")
	}
	if err := MarkOfflineTeardownDone(t.Context(), "node", generation); err == nil {
		t.Fatal("ambiguous legacy teardown acknowledgement was accepted")
	}
	var changed int
	if err := conn.QueryRowContext(t.Context(), `SELECT count(*) FROM foghorn.ingest_offline_effects WHERE source_node_id='node' AND source_generation=$1::uuid AND (drain_done OR teardown_done)`, generation).Scan(&changed); err != nil {
		t.Fatal(err)
	}
	if changed != 0 {
		t.Fatalf("ambiguous acknowledgement changed %d tenant rows", changed)
	}
}
