//go:build schema_verify

package control

import (
	"database/sql"
	"testing"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/google/uuid"
)

func TestStreamIngestStates_RealPG(t *testing.T) { testStreamIngestStates(t, startRealPG(t)) }
func TestStreamIngestStates_RealYugabyte(t *testing.T) {
	conn, _, ok := dockerpg.OpenSharedYugabyteBaselineWithDSN(t, "stream_ingest_states", "foghorn")
	if !ok {
		t.Fatal("requires shared Yugabyte fixture")
	}
	testStreamIngestStates(t, conn)
}
func testStreamIngestStates(t *testing.T, conn *sql.DB) {
	tenant, other, stream, foreign, group := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	start := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Minute)
	for i, node := range []string{"first", "second"} {
		var end any
		if i == 0 {
			end = start.Add(20 * time.Second)
		}
		if _, err := conn.ExecContext(t.Context(), `INSERT INTO foghorn.pull_ingest_sessions(tenant_id,stream_id,cell_session_id,stream_internal_name,node_id,ingest_cluster_id,admission_state,buffer_pid,start_event_id,started_at,started_at_unix_millis,playable_at,playable_at_unix_millis,ended_at) VALUES($1,$2,$3,'pull-state',$4,'cell','active',$5,$6,$7,$8,$7,$8,$9)`, tenant, stream, group, node, 100+i, uuid.NewString(), start.Add(time.Duration(i)*time.Second), start.Add(time.Duration(i)*time.Second).UnixMilli(), end); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.ExecContext(t.Context(), `INSERT INTO foghorn.ingest_sessions(tenant_id,stream_id,stream_internal_name,node_id,connector_pid,start_trigger_uuid,started_at_unix_millis,projection_state) VALUES($1,$2,'foreign','foreign',900,$3,1000,'active')`, other, foreign, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	q := foghorndb.New(conn)
	rows, err := q.GetStreamIngestStates(t.Context(), foghorndb.GetStreamIngestStatesParams{TenantID: tenant, StreamIds: []string{stream, foreign}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != group || rows[0].EndedAt.Valid || !rows[0].StartedAt.Equal(start) {
		t.Fatalf("two-node cell session must stay live with its first identity/time; tenant isolation must hold: %+v", rows)
	}
	if _, err := conn.ExecContext(t.Context(), `UPDATE foghorn.pull_ingest_sessions SET ended_at=$3 WHERE tenant_id=$1 AND stream_id=$2 AND ended_at IS NULL`, tenant, stream, start.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	rows, err = q.GetStreamIngestStates(t.Context(), foghorndb.GetStreamIngestStatesParams{TenantID: tenant, StreamIds: []string{stream}})
	if err != nil || len(rows) != 1 || !rows[0].EndedAt.Valid || !rows[0].EndedAt.Time.Equal(start.Add(30*time.Second)) {
		t.Fatalf("last buffer end state=%+v err=%v", rows, err)
	}
}
