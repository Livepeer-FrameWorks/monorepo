//go:build schema_verify

package control

import (
	"fmt"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestDVRAndThumbnailCutoffsPreserveElapsedTime_RealPG(t *testing.T) {
	for _, offset := range []int{-12 * 3600, 14 * 3600} {
		t.Run(fmt.Sprint(offset), func(t *testing.T) {
			conn := startRealPG(t)
			previousDB, previousZone := db, time.Local
			SetDB(conn)
			time.Local = time.FixedZone("worker", offset)
			t.Cleanup(func() { SetDB(previousDB); time.Local = previousZone })
			tenant := uuid.NewString()
			const hash = "timezonecutoff000000000000000001"
			if _, err := conn.Exec(`INSERT INTO foghorn.artifacts(artifact_hash,artifact_type,tenant_id,status) VALUES($1,'dvr',$2::uuid,'completed')`, hash, tenant); err != nil {
				t.Fatal(err)
			}
			for index, age := range []string{"2 hours", "30 minutes"} {
				name := fmt.Sprint(index)
				if _, err := conn.Exec(`INSERT INTO foghorn.dvr_segments(artifact_hash,segment_name,sequence,media_start_ms,media_end_ms,duration_ms,s3_key,status,created_at) VALUES($1,$2,$3,0,6000,6000,$2,'pending',NOW()-$4::interval)`, hash, name, index+1, age); err != nil {
					t.Fatal(err)
				}
				if ok, err := ClaimThumbnailAttempt(t.Context(), conn, "attempt-"+name, tenant, "asset-"+name, "node", "cluster", []string{"poster.jpg"}, time.Now().Add(time.Hour)); err != nil || !ok {
					t.Fatalf("claim thumbnail: %v %v", ok, err)
				}
				if _, err := conn.Exec(`UPDATE foghorn.thumbnail_task_assignment SET updated_at=NOW()-$1::interval WHERE attempt_id=$2 AND tenant_id=$3`, age, "attempt-"+name, tenant); err != nil {
					t.Fatal(err)
				}
			}
			pending, err := ListPendingDVRSegments(t.Context(), hash, time.Hour, 100)
			if err != nil || len(pending) != 1 || pending[0].SegmentName != "0" {
				t.Fatalf("zone %d changed DVR cutoff: %+v %v", offset, pending, err)
			}
			count, err := ThumbnailRecoveryBacklog(t.Context(), conn, time.Now().Add(-time.Hour))
			if err != nil || count != 1 {
				t.Fatalf("zone %d changed thumbnail cutoff: count=%d err=%v", offset, count, err)
			}
			if _, err := conn.Exec(`UPDATE foghorn.thumbnail_task_assignment SET status='published',publish_lease_token='utc-test' WHERE tenant_id=$1`, tenant); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"0", "1"} {
				if _, err := conn.Exec(`INSERT INTO foghorn.thumbnail_active_pointer(asset_key,tenant_id,active_version,active_token) VALUES($1,$2,$3,'utc-test')`, "asset-"+name, tenant, "attempt-"+name); err != nil {
					t.Fatal(err)
				}
			}
			count, err = UnprojectedThumbnailRecoveryBacklog(t.Context(), conn, time.Now().Add(-time.Hour))
			if err != nil || count != 1 {
				t.Fatalf("zone %d changed unprojected thumbnail cutoff: count=%d err=%v", offset, count, err)
			}

		})
	}
}
