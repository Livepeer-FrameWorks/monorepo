//go:build schema_verify

package jobs

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func TestR9EveryJobTypeExpiresWithUnknownCapacity_RealPG(t *testing.T) {
	conn := startRealPGForCleanup(t)
	useRuntimeProcessingInventory(t)
	oldAccess := clusterAccessibleForTenant
	clusterAccessibleForTenant = func(context.Context, string, string) bool { return true }
	t.Cleanup(func() { clusterAccessibleForTenant = oldAccess })
	d := NewProcessingDispatcher(ProcessingDispatcherConfig{DB: conn, Logger: logging.NewLogger()})
	for index, kind := range []string{"transcode", "clip", "dvr", "chapter", "thumbnail", "extract_audio"} {
		t.Run(kind, func(t *testing.T) {
			job := fmt.Sprintf("7a900000-0000-4000-8000-%012d", 500+index)
			created := time.Now().UTC().Add(-25 * time.Hour)
			if _, err := conn.Exec(`INSERT INTO foghorn.processing_jobs(job_id,tenant_id,job_type,status,created_at) VALUES($1::uuid,$2::uuid,$3,'dispatched',$4)`, job, r9Tenant, kind, created); err != nil {
				t.Fatal(err)
			}
			d.dispatchJob(t.Context(), &processingJob{JobID: job, TenantID: r9Tenant, JobType: kind, CreatedAt: sql.NullTime{Time: created, Valid: true}})
			var status, reason string
			if err := conn.QueryRow(`SELECT status,error_message FROM foghorn.processing_jobs WHERE job_id=$1::uuid AND tenant_id=$2::uuid`, job, r9Tenant).Scan(&status, &reason); err != nil {
				t.Fatal(err)
			}
			if status != "failed" || reason != "processing timed out after 24 hours" {
				t.Fatalf("expired %s with unknown capacity: status=%s reason=%q", kind, status, reason)
			}
		})
	}
}

func TestR9FreshThumbnailOfOldVODRetainsJobDeadline_RealPG(t *testing.T) {
	for _, path := range []string{"dispatch", "recovery"} {
		t.Run(path, func(t *testing.T) {
			conn := startRealPGForCleanup(t)
			const job = "7a900000-0000-4000-8000-000000000550"
			const hash = "r9thumbnaildeadline0000000000001"
			oldUpload, created := time.Now().UTC().Add(-48*time.Hour), time.Now().UTC()
			if _, err := conn.Exec(`INSERT INTO foghorn.artifacts(artifact_hash,tenant_id,artifact_type,status,created_at) VALUES($1,$2::uuid,'vod','ready',$3)`, hash, r9Tenant, oldUpload); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.Exec(`INSERT INTO foghorn.processing_jobs(job_id,tenant_id,artifact_hash,job_type,status,created_at) VALUES($1::uuid,$2::uuid,$3,'thumbnail','queued',$4)`, job, r9Tenant, hash, created); err != nil {
				t.Fatal(err)
			}
			sm := state.ResetDefaultManagerForTests()
			t.Cleanup(sm.Shutdown)
			useRuntimeProcessingInventory(t)
			d := NewProcessingDispatcher(ProcessingDispatcherConfig{DB: conn, Logger: logging.NewLogger()})
			if path == "dispatch" {
				d.dispatchJob(t.Context(), &processingJob{JobID: job, TenantID: r9Tenant, JobType: "thumbnail", CreatedAt: sql.NullTime{Time: created, Valid: true}, UploadedAt: sql.NullTime{Time: oldUpload, Valid: true}, ArtifactType: sql.NullString{String: "vod", Valid: true}})
			} else {
				d.recoverStale()
			}
			var status string
			if err := conn.QueryRow(`SELECT status FROM foghorn.processing_jobs WHERE job_id=$1::uuid AND tenant_id=$2::uuid`, job, r9Tenant).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if status != "queued" {
				t.Fatalf("fresh thumbnail inherited its old VOD upload deadline in %s: status=%s", path, status)
			}
		})
	}
}

func TestR9ReadyThumbnailRetainsMemoryReservationEligibility_RealPG(t *testing.T) {
	conn := startRealPGForCleanup(t)
	const job = "7a900000-0000-4000-8000-000000000551"
	const hash = "r9thumbnailfairness0000000000001"
	if _, err := conn.Exec(`INSERT INTO foghorn.artifacts(artifact_hash,tenant_id,artifact_type,status,created_at) VALUES($1,$2::uuid,'vod','ready',NOW()-INTERVAL '48 hours')`, hash, r9Tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO foghorn.processing_jobs(job_id,tenant_id,artifact_hash,job_type,status,created_at,required_memory_bytes,wait_reason,wait_started_at) VALUES($1::uuid,$2::uuid,$3,'thumbnail','queued',NOW()-INTERVAL '1 hour',1024,'memory',NOW()-INTERVAL '2 minutes')`, job, r9Tenant, hash); err != nil {
		t.Fatal(err)
	}
	waiters, err := foghorndb.New(conn).ListProcessingMemoryWaiters(t.Context(), foghorndb.ListProcessingMemoryWaitersParams{WaitThreshold: time.Now().UTC().Add(-time.Minute), UploadDeadline: time.Now().UTC().Add(-24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if len(waiters) != 1 || waiters[0].JobID != job {
		t.Fatalf("ready thumbnail lost memory reservation eligibility: %+v", waiters)
	}
}

func TestR9OlderReadyThumbnailBlocksYoungerReservation_RealPG(t *testing.T) {
	conn := startRealPGForCleanup(t)
	const older = "7a900000-0000-4000-8000-000000000552"
	const younger = "7a900000-0000-4000-8000-000000000553"
	for index, job := range []string{older, younger} {
		hash := fmt.Sprintf("r9thumborder%020d", index)
		status, kind, age := "ready", "thumbnail", 48
		if index == 1 {
			status, kind, age = "processing", "transcode", 1
		}
		if _, err := conn.Exec(`INSERT INTO foghorn.artifacts(artifact_hash,tenant_id,artifact_type,status,origin_cluster_id,created_at) VALUES($1,$2::uuid,'vod',$3,'origin',NOW()-$4::int*INTERVAL '1 hour')`, hash, r9Tenant, status, age); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(`INSERT INTO foghorn.processing_jobs(job_id,tenant_id,artifact_hash,job_type,status,created_at,required_memory_bytes,wait_reason,wait_started_at) VALUES($1::uuid,$2::uuid,$3,$4,'queued',NOW()-$5::int*INTERVAL '1 hour',1024,'memory',NOW()-INTERVAL '2 minutes')`, job, r9Tenant, hash, kind, 2-index); err != nil {
			t.Fatal(err)
		}
	}
	q := foghorndb.New(conn)
	changed, err := q.ReserveProcessingNode(t.Context(), foghorndb.ReserveProcessingNodeParams{NodeID: "edge", RequiredBytes: 1024, JobID: younger, TenantID: r9Tenant, WaitThreshold: time.Now().UTC().Add(-time.Minute), NodeBudget: 8192})
	if err != nil {
		t.Fatal(err)
	}
	if changed != 0 {
		t.Fatal("younger job reserved ahead of an older ready-source thumbnail")
	}
	changed, err = q.ReserveProcessingNode(t.Context(), foghorndb.ReserveProcessingNodeParams{NodeID: "edge", RequiredBytes: 1024, JobID: older, TenantID: r9Tenant, WaitThreshold: time.Now().UTC().Add(-time.Minute), NodeBudget: 8192})
	if err != nil || changed != 1 {
		t.Fatalf("oldest ready-source thumbnail could not reserve: changed=%d err=%v", changed, err)
	}
}
