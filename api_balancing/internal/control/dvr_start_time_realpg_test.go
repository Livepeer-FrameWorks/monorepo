//go:build schema_verify

package control

import (
	"database/sql"
	"testing"
	"time"
)

func TestDVRStartTimeAnchorsChapters_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	const tenant = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	const hash = "recording-clock"
	start := time.Now().UTC().Add(-90 * time.Second).Truncate(time.Second)
	_, err := conn.ExecContext(t.Context(), `INSERT INTO foghorn.artifacts
		(artifact_hash, artifact_type, tenant_id, status, dvr_start_dispatch, dvr_chapter_mode, dvr_chapter_interval)
		VALUES ($1, 'dvr', $2, 'starting', '{"node_id":"owner"}', 'fixed_interval', 3600)`, hash, tenant)
	if err != nil {
		t.Fatal(err)
	}
	readStart := func() sql.NullTime {
		t.Helper()
		var got sql.NullTime
		if err := conn.QueryRowContext(t.Context(), `SELECT started_at FROM foghorn.artifacts WHERE tenant_id=$1 AND artifact_hash=$2`, tenant, hash).Scan(&got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	for _, tc := range []struct {
		tenant, node string
		start        int64
	}{
		{"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", "owner", start.Unix()},
		{tenant, "replica", start.Unix()},
		{tenant, "owner", 0},
	} {
		if err := recordDVRStartTime(t.Context(), hash, tc.tenant, tc.node, tc.start); err != nil {
			t.Fatal(err)
		}
		if readStart().Valid {
			t.Fatal("untrusted or absent start time was persisted")
		}
	}
	if err := recordDVRStartTime(t.Context(), hash, tenant, "owner", start.Unix()); err != nil {
		t.Fatal(err)
	}
	if err := recordDVRStartTime(t.Context(), hash, tenant, "owner", start.Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if got := readStart(); !got.Valid || !got.Time.Equal(start) {
		t.Fatalf("start anchor moved: %v", got)
	}
	policy, ok, err := ReadDVRChapterPolicy(t.Context(), hash)
	if err != nil || !ok || policy.StartedAtMs != start.UnixMilli() {
		t.Fatalf("chapter policy lacks recording anchor: %+v, %v, %v", policy, ok, err)
	}
	end := start.Add(85*time.Second + 987654*time.Microsecond)
	if _, err := conn.ExecContext(t.Context(), `UPDATE foghorn.artifacts SET ended_at=$1 WHERE tenant_id=$2 AND artifact_hash=$3`, end, tenant, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), `INSERT INTO foghorn.dvr_segments
		(artifact_hash,segment_name,sequence,media_start_ms,media_end_ms,duration_ms,s3_key,status)
		SELECT artifact_hash,'segment.ts',1,$3::bigint,$3::bigint+6000,6000,'test/segment.ts','uploaded'
		FROM foghorn.artifacts WHERE tenant_id=$1 AND artifact_hash=$2`, tenant, hash, start.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	result, err := FinalizeDVR(t.Context(), hash, FinalizeOptions{ReportingNodeID: "owner", StartedAtUnix: start.Unix()})
	if err != nil || result.ArtifactStatus != "completed" || result.NoOp {
		t.Fatalf("finalization failed: %+v, %v", result, err)
	}
	if _, err := FinalizeDVR(t.Context(), hash, FinalizeOptions{ReportingNodeID: "owner", StartedAtUnix: start.Unix()}); err != nil {
		t.Fatal(err)
	}
	var chapters int
	if err := conn.QueryRowContext(t.Context(), `SELECT count(*) FROM foghorn.dvr_chapters c JOIN foghorn.artifacts a USING(artifact_hash) WHERE a.tenant_id=$1 AND a.artifact_hash=$2 AND c.state='closed'`, tenant, hash).Scan(&chapters); err != nil {
		t.Fatal(err)
	}
	if chapters == 0 {
		t.Fatal("terminal recording produced no chapter")
	}
	policy, ok, err = ReadDVRChapterPolicy(t.Context(), hash)
	if err != nil || !ok || policy.EndedAtMs != end.UnixMilli() {
		t.Fatalf("chapter policy lacks terminal anchor: %+v, %v, %v; want %d", policy, ok, err, end.UnixMilli())
	}
	for _, bounds := range [][2]int64{{0, 0}, {start.UnixMilli(), end.UnixMilli()}} {
		listed, _, err := ListVirtualChaptersForArtifact(t.Context(), hash, "", 0, bounds[0], bounds[1], 100, "")
		if err != nil || len(listed) != chapters {
			t.Fatalf("default chapter listing: %d entries, %v; want %d", len(listed), err, chapters)
		}
		for _, chapter := range listed {
			if chapter.State != ChapterStateClosed {
				t.Fatalf("materialized chapter did not overlay listing: %+v", chapter)
			}
		}
	}
	for _, status := range []string{"completed", "completed_partial", "deleted", "failed"} {
		if _, err := conn.ExecContext(t.Context(), `UPDATE foghorn.artifacts SET status=$1, started_at=NULL WHERE tenant_id=$2 AND artifact_hash=$3`, status, tenant, hash); err != nil {
			t.Fatal(err)
		}
		if err := recordDVRStartTime(t.Context(), hash, tenant, "owner", start.Unix()); err != nil {
			t.Fatal(err)
		}
		want := status == "completed" || status == "completed_partial"
		if readStart().Valid != want {
			t.Fatalf("terminal timestamp mutation for %s", status)
		}
	}
}
