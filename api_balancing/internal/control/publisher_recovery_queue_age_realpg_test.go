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

func TestPublisherRecoveryQueueAgeUsesEligibility_RealPG(t *testing.T) {
	testPublisherRecoveryQueueAgeUsesEligibility(t, startRealPG(t))
}

func TestPublisherRecoveryQueueAgeUsesEligibility_RealYugabyte(t *testing.T) {
	conn, _, ok := dockerpg.OpenSharedYugabyteBaselineWithDSN(t, "publisher_queue_age", "foghorn")
	if !ok {
		t.Fatal("requires shared Yugabyte fixture")
	}
	testPublisherRecoveryQueueAgeUsesEligibility(t, conn)
}

func testPublisherRecoveryQueueAgeUsesEligibility(t *testing.T, conn *sql.DB) {
	q := foghorndb.New(conn)
	for _, abandoned := range []bool{true, false} {
		t.Run(map[bool]string{true: "abandonment starts at marker enqueue", false: "lapse starts after admission window"}[abandoned], func(t *testing.T) {
			tenant, node, stream, trigger := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
			generation, err := q.InsertIngestSession(t.Context(), foghorndb.InsertIngestSessionParams{TenantID: tenant, NodeID: node, StreamInternalName: stream, ConnectorPid: 123, StartTriggerUuid: trigger, StartedAtUnixMillis: time.Now().UnixMilli()})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = conn.ExecContext(t.Context(), `UPDATE foghorn.ingest_sessions SET started_at=NOW()-INTERVAL '3 hours',publisher_admission_version=1 WHERE tenant_id=$1::uuid AND id=$2::uuid`, tenant, generation); err != nil {
				t.Fatal(err)
			}
			var want time.Time
			if abandoned {
				if err = q.RecordIngestAdmissionAbandonment(t.Context(), foghorndb.RecordIngestAdmissionAbandonmentParams{NodeID: node, StartTriggerUuid: trigger, ConnectorPid: 123}); err != nil {
					t.Fatal(err)
				}
				err = conn.QueryRowContext(t.Context(), `SELECT created_at FROM foghorn.ingest_admission_abandonments WHERE node_id=$1 AND start_trigger_uuid=$2`, node, trigger).Scan(&want)
			} else {
				err = conn.QueryRowContext(t.Context(), `SELECT started_at+($3::bigint*INTERVAL '1 millisecond') FROM foghorn.ingest_sessions WHERE tenant_id=$1::uuid AND id=$2::uuid`, tenant, generation, IngestAdmissionWindow.Milliseconds()).Scan(&want)
			}
			if err != nil {
				t.Fatal(err)
			}
			rows, err := q.ListPublisherAdmissionsToSettle(t.Context(), IngestAdmissionWindow.Milliseconds())
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				if row.NodeID == node {
					if !row.QueuedAt.Equal(want) {
						t.Fatalf("recovery age used publisher lifetime instead of queue eligibility: got=%s want=%s", row.QueuedAt, want)
					}
					return
				}
			}
			t.Fatal("pending recovery item missing")
		})
	}
}
