//go:build schema_verify

package control

import (
	"context"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestReviewPublisherDrainRetryBudgetIsTerminal_RealPG(t *testing.T) {
	conn := startRealPG(t)
	previous := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(previous) })
	tenant := uuid.NewString()
	for _, row := range []struct {
		name, state string
		attempts    int
		age         string
	}{{"exhausted", "pending", 12, "0 seconds"}, {"old-failed", "failed", 12, "35 days"}} {
		_, err := conn.ExecContext(t.Context(), `INSERT INTO foghorn.ingest_offline_effects(tenant_id,stream_internal_name,source_node_id,source_generation,source_revision,drain_publisher,drain_done,drain_runtime_name,state,attempts,updated_at) VALUES($1::uuid,$2,'node',$3::uuid,1,true,false,'live+old',$4,$5,NOW()-$6::interval)`, tenant, row.name, uuid.NewString(), row.state, row.attempts, row.age)
		if err != nil {
			t.Fatal(err)
		}
	}
	dead, err := FailExhaustedOfflineEffects(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(dead) != 1 {
		t.Errorf("publisher drain bypassed retry budget: dead letters=%d, want 1", len(dead))
	}
	var state string
	if err := conn.QueryRowContext(t.Context(), `SELECT state FROM foghorn.ingest_offline_effects WHERE tenant_id=$1::uuid AND stream_internal_name='old-failed'`, tenant).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "failed" {
		t.Errorf("failed unconfirmed drain revived itself: state=%q", state)
	}
	claimed, err := ClaimOfflineEffects(t.Context(), 10, time.Minute, "review-worker")
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 0 {
		t.Errorf("terminal drains were re-dispatched: %d", len(claimed))
	}
	if n, err := PurgeTerminalOfflineEffects(t.Context(), time.Hour); err != nil || n != 1 {
		t.Errorf("old failed drain was never cleaned up: purged=%d error=%v", n, err)
	}
}
func TestReviewPublisherDrainAcknowledgementIsTenantBound_RealPG(t *testing.T) {
	conn := startRealPG(t)
	previous := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(previous) })
	tenant, foreign, generation := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, id := range []string{tenant, foreign} {
		_, err := conn.ExecContext(t.Context(), `INSERT INTO foghorn.ingest_offline_effects(tenant_id,stream_internal_name,source_node_id,source_generation,source_revision,drain_publisher,drain_done,drain_runtime_name) VALUES($1::uuid,'same-stream','node',$2::uuid,1,true,false,'live+same-stream')`, id, generation)
		if err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.WithValue(t.Context(), ctxkeys.KeyTenantID, tenant)
	if err := MarkOfflinePublisherDrainDone(ctx, "node", generation); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{tenant, foreign} {
		var done bool
		if err := conn.QueryRowContext(t.Context(), `SELECT drain_done FROM foghorn.ingest_offline_effects WHERE tenant_id=$1::uuid AND source_generation=$2::uuid`, id, generation).Scan(&done); err != nil {
			t.Fatal(err)
		}
		if done != (id == tenant) {
			t.Errorf("drain acknowledgement crossed tenant boundary: tenant=%s done=%v", id, done)
		}
	}
}
