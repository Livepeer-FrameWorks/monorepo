//go:build schema_verify

package foghorndb

import (
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
)

func TestPublisherRecoverySelectionSkipsActiveSessions_RealPG(t *testing.T) {
	verifyPublisherRecoverySelection(t, startFoghornCatalogPostgres(t), false)
}

func TestPublisherRecoverySelectionSkipsActiveSessions_RealYugabyte(t *testing.T) {
	conn, ok := dockerpg.OpenSharedYugabyteBaseline(t, "publisher_recovery_plan", "foghorn")
	if !ok {
		t.Fatal("requires shared Yugabyte fixture")
	}
	verifyPublisherRecoverySelection(t, conn, true)
}

func verifyPublisherRecoverySelection(t *testing.T, conn *sql.DB, distributed bool) {
	t.Helper()
	ctx := t.Context()
	_, err := conn.ExecContext(ctx, `INSERT INTO foghorn.ingest_sessions
 (tenant_id,node_id,stream_internal_name,connector_pid,start_trigger_uuid,started_at_unix_millis,projection_state,publisher_admission_version)
 SELECT 'f41c946d-8d5f-4707-9049-55f9b5933a83'::uuid,'recovery-plan-'||i,'recovery-plan-'||i,i,'recovery-plan-'||i,1,'active',1
 FROM generate_series(1,5000) i`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `ANALYZE foghorn.ingest_sessions`); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	explain := "EXPLAIN (ANALYZE, FORMAT JSON) "
	if distributed {
		explain = "EXPLAIN (ANALYZE, DIST, FORMAT JSON) "
	}
	if err := conn.QueryRowContext(ctx, explain+listPublisherAdmissionsToSettle, int64(25500)).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var plans []struct{ Plan recoveryPlanNode }
	if err := json.Unmarshal(raw, &plans); err != nil {
		t.Fatal(err)
	}
	scanned, probes := 0.0, 0.0
	var walk func(recoveryPlanNode)
	walk = func(n recoveryPlanNode) {
		if n.Relation == "ingest_sessions" {
			probes += n.Loops
			examined := (n.Rows + n.Removed) * n.Loops
			storage := (n.TableRows + n.IndexRows) * n.Loops
			if storage > examined {
				examined = storage
			}
			scanned += examined
		}
		for _, child := range n.Plans {
			walk(child)
		}
	}
	for _, p := range plans {
		walk(p.Plan)
	}
	if scanned > 64 {
		t.Fatalf("idle recovery examined %.0f active session rows; want <=64:\n%s", scanned, raw)
	}
	rows, err := New(conn).ListPublisherAdmissionsToSettle(ctx, 25500)
	if err != nil || len(rows) != 0 {
		t.Fatalf("active publishers selected for recovery: %d %v", len(rows), err)
	}
	if _, err := conn.ExecContext(ctx, `UPDATE foghorn.ingest_sessions SET projection_state='pending',started_at=NOW()-INTERVAL '1 minute'
 WHERE tenant_id='f41c946d-8d5f-4707-9049-55f9b5933a83'::uuid AND connector_pid IN (4997,5000)`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `UPDATE foghorn.ingest_sessions SET publisher_admission_version=0
 WHERE tenant_id='f41c946d-8d5f-4707-9049-55f9b5933a83'::uuid AND connector_pid=4999`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO foghorn.ingest_admission_abandonments(node_id,start_trigger_uuid,connector_pid)
 SELECT 'recovery-plan-'||i,'recovery-plan-'||i,i FROM generate_series(4998,5000) i`); err != nil {
		t.Fatal(err)
	}
	rows, err = New(conn).ListPublisherAdmissionsToSettle(ctx, 25500)
	if err != nil || len(rows) != 4 {
		t.Fatalf("recovery candidates=%d, want three abandoned and one lapsed: %v", len(rows), err)
	}
	seen := make(map[string]string)
	for _, row := range rows {
		if _, duplicate := seen[row.StartTriggerUuid]; duplicate {
			t.Fatalf("execution selected twice: %s", row.StartTriggerUuid)
		}
		seen[row.StartTriggerUuid] = row.Reason
	}
	if seen["recovery-plan-4997"] != "admission_lapsed" || seen["recovery-plan-5000"] != "admission_abandoned" {
		t.Fatalf("pending marker did not supersede lapse: %v", seen)
	}

	if _, err := conn.ExecContext(ctx, `INSERT INTO foghorn.ingest_admission_abandonments(node_id,start_trigger_uuid,connector_pid,created_at)
 SELECT 'historical-marker-'||i,'historical-marker-'||i,i,NOW()-INTERVAL '2 hours' FROM generate_series(1,5000) i`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `ANALYZE foghorn.ingest_admission_abandonments`); err != nil {
		t.Fatal(err)
	}
	for _, complete := range []bool{false, true} {
		if complete {
			if _, err := conn.ExecContext(ctx, `UPDATE foghorn.ingest_admission_abandonments SET publisher_sessions_settled_at=NOW() WHERE node_id LIKE 'historical-marker-%'`); err != nil {
				t.Fatal(err)
			}
		}
		if err := conn.QueryRowContext(ctx, explain+listPublisherAdmissionsToSettle, int64(25500)).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &plans); err != nil {
			t.Fatal(err)
		}
		scanned, probes = 0, 0
		for _, p := range plans {
			walk(p.Plan)
		}
		if scanned > 128 || probes > 64 {
			t.Fatalf("completed=%v: historical recovery examined %.0f session rows with %.0f index probes; want bounded work <=128 rows/64probes", complete, scanned, probes)
		}
	}
}

type recoveryPlanNode struct {
	TableRows float64            `json:"Storage Table Rows Scanned"`
	IndexRows float64            `json:"Storage Index Rows Scanned"`
	Relation  string             `json:"Relation Name"`
	Rows      float64            `json:"Actual Rows"`
	Removed   float64            `json:"Rows Removed by Filter"`
	Loops     float64            `json:"Actual Loops"`
	Plans     []recoveryPlanNode `json:"Plans"`
}
