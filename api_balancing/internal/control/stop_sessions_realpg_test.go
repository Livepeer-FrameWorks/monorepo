//go:build schema_verify

package control

import (
	"testing"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/google/uuid"
)

func TestStopSessionsGenerationIsTenantNodeAndStreamScoped_RealPG(t *testing.T) {
	useRealPG(t)
	q := foghorndb.New(db)
	node, stream := uuid.NewString(), uuid.NewString()
	owned := seedOpenIngestSession(t, ingA, node, stream, uuid.NewString(), 101, time.Now().UnixMilli())
	seedOpenIngestSession(t, ingB, node, uuid.NewString(), uuid.NewString(), 102, time.Now().UnixMilli())
	for _, tc := range []struct {
		tenant, node, stream, want string
	}{
		{ingA, node, stream, owned},
		{ingB, node, stream, ""},
		{ingA, "other-node", stream, ""},
		{ingA, node, "other-stream", ""},
	} {
		got, err := q.GetStopSessionsGeneration(t.Context(), foghorndb.GetStopSessionsGenerationParams{TenantID: tc.tenant, NodeID: tc.node, InternalName: tc.stream})
		if err != nil || got != tc.want {
			t.Fatalf("generation tenant=%s node=%s stream=%s: got=%s want=%s err=%v", tc.tenant, tc.node, tc.stream, got, tc.want, err)
		}
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE foghorn.ingest_sessions SET ended_at=NOW(),ended_reason='stream_end' WHERE tenant_id=$1::uuid AND id=$2::uuid`, ingA, owned); err != nil {
		t.Fatal(err)
	}
	got, err := q.GetStopSessionsGeneration(t.Context(), foghorndb.GetStopSessionsGenerationParams{TenantID: ingA, NodeID: node, InternalName: stream})
	if err != nil || got != "" {
		t.Fatalf("ended generation reused for destructive stop: got=%s err=%v", got, err)
	}
}
