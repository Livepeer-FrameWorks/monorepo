//go:build schema_verify

package jobs

import (
	"database/sql"
	"errors"
	"testing"

	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func TestRetentionKeepsPermanentMedia_RealPG(t *testing.T) {
	db := startRealPGForCleanup(t)
	cases := []struct {
		hash, kind, state, origin, deadline, want string
	}{
		{"permanent-vod", "vod", "ready", "", "", "ready"},
		{"permanent-clip", "clip", "ready", "", "", "ready"},
		{"processing-vod", "vod", "processing", "", "", "processing"},
		{"failed-clip", "clip", "failed", "", "", "deleted"},
		{"expired-vod", "vod", "ready", "", "1 day", "deleted"},
		{"permanent-chapter", "vod", "ready", "dvr_chapter", "", "ready"},
		{"permanent-dvr", "dvr", "completed", "", "", "completed"},
	}
	for _, test := range cases {
		retentionExec(t, db, `INSERT INTO foghorn.artifacts
			(artifact_hash,artifact_type,tenant_id,status,origin_type,created_at,retention_until)
			VALUES ($1,$2,$3::uuid,$4,NULLIF($5,''),NOW()-INTERVAL '31 days',
			CASE WHEN $6='' THEN NULL ELSE NOW()-$6::interval END)`,
			test.hash, test.kind, retentionTenant, test.state, test.origin, test.deadline)
	}
	job := NewRetentionJob(RetentionConfig{DB: db, Logger: logging.NewLogger()})
	job.scan()
	for _, test := range cases {
		t.Run(test.hash, func(t *testing.T) {
			var got string
			if err := db.QueryRow(`SELECT status FROM foghorn.artifacts WHERE tenant_id=$1::uuid AND artifact_hash=$2`, retentionTenant, test.hash).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("31-day-old %s = %s; want %s", test.hash, got, test.want)
			}
		})
	}
	t.Run("failed artifact recovered after selection", func(t *testing.T) {
		retentionExec(t, db, `INSERT INTO foghorn.artifacts
			(artifact_hash,artifact_type,tenant_id,status,created_at)
			VALUES ('recovered-clip','clip',$1::uuid,'failed',NOW()-INTERVAL '31 days')`, retentionTenant)
		q := foghorndb.New(db)
		selected, err := q.ListExpiredArtifacts(t.Context(), 30)
		if err != nil || len(selected) != 1 || selected[0].ArtifactHash != "recovered-clip" {
			t.Fatalf("failed candidate = %v, %v", selected, err)
		}
		retentionExec(t, db, `UPDATE foghorn.artifacts SET status='ready' WHERE tenant_id=$1::uuid AND artifact_hash='recovered-clip'`, retentionTenant)
		_, err = q.ExpireArtifactIfStillEligible(t.Context(), foghorndb.ExpireArtifactIfStillEligibleParams{
			ArtifactHash: "recovered-clip", RetentionDays: 30,
		})
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("recovered permanent clip expired: %v; want no eligible row", err)
		}
	})
}
