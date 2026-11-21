//go:build schema_verify

package jobs

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"frameworks/api_balancing/internal/state"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
)

func TestClusterCannotFitLadderFailsJobAndUpload_RealPG(t *testing.T) {
	useRuntimeProcessingInventory(t)
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	old := clusterAccessibleForTenant
	clusterAccessibleForTenant = func(context.Context, string, string) bool { return true }
	t.Cleanup(func() { clusterAccessibleForTenant = old })
	touchInCluster(sm, "small", routeTestCluster)
	setNodeMemory(sm, "small", 1536*1024*1024, map[string]state.ClassCapacity{mist.ProcessingClassVideoTranscode: {Total: 8}})
	conn := startRealPGForCleanup(t)
	const job, tenant, hash = "7a6e0000-0000-4000-8000-000000000001", "7a6e0000-0000-4000-8000-000000000002", "memoryimpossibleupload0000000001"
	if _, err := conn.Exec(`INSERT INTO foghorn.artifacts(artifact_hash,artifact_type,tenant_id,status) VALUES($1,'vod',$2::uuid,'queued')`, hash, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO foghorn.processing_jobs(job_id,tenant_id,artifact_hash,job_type,status,required_memory_bytes)VALUES($1::uuid,$2::uuid,$3,'transcode','dispatched',2147483648)`, job, tenant, hash); err != nil {
		t.Fatal(err)
	}
	d := NewProcessingDispatcher(ProcessingDispatcherConfig{DB: conn, Logger: logging.NewLogger()})
	d.dispatchJob(context.Background(), &processingJob{JobID: job, TenantID: tenant, ArtifactHash: sql.NullString{String: hash, Valid: true}, OriginCluster: routeTestCluster, RequiredMemoryBytes: 2147483648})
	var js, as, message string
	if err := conn.QueryRow(`SELECT j.status,a.status,j.error_message FROM foghorn.processing_jobs j JOIN foghorn.artifacts a ON a.artifact_hash=j.artifact_hash AND a.tenant_id=j.tenant_id WHERE j.job_id=$1::uuid AND j.tenant_id=$2::uuid`, job, tenant).Scan(&js, &as, &message); err != nil {
		t.Fatal(err)
	}
	if js != "failed" || as != "failed" || message != mist.ProcessingMemoryNoNodeReason {
		t.Fatalf("impossible ladder deferred forever: %s/%s %s", js, as, message)
	}
	var count int
	if err := conn.QueryRow(`SELECT count(*) FROM foghorn.domain_event_outbox WHERE tenant_id=$1::uuid AND event_type='upload.failed'`, tenant).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("failure event count=%d", count)
	}
}

func TestProcessingUploadDeadlineAndDeletedArtifactRecovery_RealPG(t *testing.T) {
	previous := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = previous })
	conn := startRealPGForCleanup(t)
	conn.SetMaxOpenConns(1)
	if _, err := conn.Exec("SET TIME ZONE 'UTC'"); err != nil {
		t.Fatal(err)
	}

	const tenant = "7a6e0000-0000-4000-8000-000000000003"
	for i, artifactStatus := range []string{"queued", "deleted"} {
		job := fmt.Sprintf("7a6e0000-0000-4000-8000-%012d", i+10)
		hash := fmt.Sprintf("memorywaitrecovery%014d", i)
		if _, err := conn.Exec(`INSERT INTO foghorn.artifacts(artifact_hash,artifact_type,tenant_id,status,created_at) VALUES($1,'vod',$2::uuid,$3,NOW()-INTERVAL '25 hours')`, hash, tenant, artifactStatus); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(`INSERT INTO foghorn.processing_jobs(job_id,tenant_id,artifact_hash,job_type,status,required_memory_bytes,wait_started_at)VALUES($1::uuid,$2::uuid,$3,'transcode','queued',1024,NOW()-INTERVAL '25 minutes')`, job, tenant, hash); err != nil {
			t.Fatal(err)
		}
	}
	newRecoveryDispatcher(t, conn).recoverStale()
	var jobs, failed, deleted, events int
	if err := conn.QueryRow(`SELECT count(*) FROM foghorn.processing_jobs WHERE tenant_id=$1::uuid AND status='failed'`, tenant).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(`SELECT count(*) FILTER(WHERE status='failed'),count(*) FILTER(WHERE status='deleted') FROM foghorn.artifacts WHERE tenant_id=$1::uuid`, tenant).Scan(&failed, &deleted); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(`SELECT count(*) FROM foghorn.domain_event_outbox WHERE tenant_id=$1::uuid AND event_type='upload.failed'`, tenant).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if jobs != 2 || failed != 1 || deleted != 1 || events != 1 {
		t.Fatalf("capacity recovery resurrected an artifact or stranded a job: jobs=%d failed=%d deleted=%d events=%d", jobs, failed, deleted, events)
	}
}

func TestProcessingMemoryMigrationReruns_RealPG(t *testing.T) {
	conn := startRealPGForCleanup(t)
	if _, err := conn.Exec(`ALTER TABLE foghorn.processing_jobs DROP COLUMN required_memory_bytes CASCADE;ALTER TABLE foghorn.processing_jobs DROP COLUMN wait_started_at;ALTER TABLE foghorn.processing_jobs DROP COLUMN wait_reason CASCADE;ALTER TABLE foghorn.processing_jobs DROP COLUMN source_probe;ALTER TABLE foghorn.processing_jobs DROP COLUMN memory_rejected_nodes`); err != nil {
		t.Fatal(err)
	}
	data, err := dbsql.Content.ReadFile("migrations/foghorn/v0.3.16/expand/014_processing_memory_placement.sql")
	if err != nil {
		t.Fatal(err)
	}
	refusals, err := dbsql.Content.ReadFile("migrations/foghorn/v0.3.16/expand/015_processing_memory_node_refusals.sql")
	if err != nil {
		t.Fatal(err)
	}
	first := strings.SplitN(string(data), ";", 2)[0]
	if _, err := conn.Exec(first); err != nil {
		t.Fatal(err)
	}
	// Replay a partially applied expand, preserving dollar-quoted function bodies.
	for replay := 0; replay < 2; replay++ {
		for _, migration := range [][]byte{data, refusals} {
			if _, err := conn.Exec(string(migration)); err != nil {
				t.Fatalf("replay %d: %v", replay, err)
			}
		}
	}

	var validated bool
	if err := conn.QueryRow(`SELECT convalidated FROM pg_constraint WHERE conrelid='foghorn.processing_jobs'::regclass AND conname='chk_processing_required_memory'`).Scan(&validated); err != nil {
		t.Fatal(err)
	}
	if validated {
		t.Fatal("expand scanned the existing table to validate its check")
	}
	check, err := dbsql.Content.ReadFile("migrations/foghorn/v0.3.16/postdeploy/008_validate_processing_memory.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(string(check)); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(`SELECT convalidated FROM pg_constraint WHERE conrelid='foghorn.processing_jobs'::regclass AND conname='chk_processing_required_memory'`).Scan(&validated); err != nil {
		t.Fatal(err)
	}
	if !validated {
		t.Fatal("postdeploy left the memory constraint unvalidated")
	}
	if _, err := conn.Exec(`INSERT INTO foghorn.processing_jobs(job_id,tenant_id,job_type,required_memory_bytes) VALUES('7a6e0000-0000-4000-8000-000000000020','7a6e0000-0000-4000-8000-000000000021','transcode',-1)`); err == nil {
		t.Fatal("negative memory requirement accepted")
	}
}

func TestUploadDeadlineUsesElapsedTimeAcrossTimeZones_RealPG(t *testing.T) {
	for _, offset := range []int{3600, -18000} {
		t.Run(fmt.Sprint(offset), func(t *testing.T) {
			previous := time.Local
			time.Local = time.FixedZone("scheduler", offset)
			t.Cleanup(func() { time.Local = previous })
			conn := startRealPGForCleanup(t)
			conn.SetMaxOpenConns(1)
			if _, err := conn.Exec("SET TIME ZONE 'UTC'"); err != nil {
				t.Fatal(err)
			}
			const tenant = "7a6e0000-0000-4000-8000-000000000004"
			for index, minutes := range []int{20, 25} {
				job := fmt.Sprintf("7a6e0000-0000-4000-8000-%012d", index+50)
				hash := fmt.Sprintf("memoryclock%020d", index)
				if _, err := conn.Exec(`INSERT INTO foghorn.artifacts(artifact_hash,artifact_type,tenant_id,status,created_at) VALUES($1,'vod',$2::uuid,'queued',NOW()-$3::int*INTERVAL '1 hour')`, hash, tenant, minutes); err != nil {
					t.Fatal(err)
				}
				if _, err := conn.Exec(`INSERT INTO foghorn.processing_jobs(job_id,tenant_id,artifact_hash,job_type,status,required_memory_bytes,wait_started_at) VALUES($1::uuid,$2::uuid,$3,'transcode','queued',1024,NOW()-$4::int*INTERVAL '1 hour')`, job, tenant, hash, minutes); err != nil {
					t.Fatal(err)
				}
			}
			const activeJob = "7a6e0000-0000-4000-8000-000000000052"
			if _, err := conn.Exec(`INSERT INTO foghorn.artifacts(artifact_hash,artifact_type,tenant_id,status) VALUES('memoryclockactive','vod',$1::uuid,'processing')`, tenant); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.Exec(`INSERT INTO foghorn.processing_jobs(job_id,tenant_id,artifact_hash,job_type,status,processing_node_id) VALUES($1::uuid,$2::uuid,'memoryclockactive','transcode','processing','edge-1')`, activeJob, tenant); err != nil {
				t.Fatal(err)
			}
			newRecoveryDispatcher(t, conn).recoverStale()
			var activeStatus string
			if err := conn.QueryRow(`SELECT status FROM foghorn.processing_jobs WHERE job_id=$1::uuid AND tenant_id=$2::uuid`, activeJob, tenant).Scan(&activeStatus); err != nil {
				t.Fatal(err)
			}
			if activeStatus != "processing" {
				t.Errorf("fresh processing lease was recovered early at offset %d: %s", offset, activeStatus)
			}
			for index, want := range []string{"queued", "failed"} {
				job := fmt.Sprintf("7a6e0000-0000-4000-8000-%012d", index+50)
				var got string
				if err := conn.QueryRow(`SELECT status FROM foghorn.processing_jobs WHERE job_id=$1::uuid AND tenant_id=$2::uuid`, job, tenant).Scan(&got); err != nil {
					t.Fatal(err)
				}
				if got != want {
					t.Errorf("scheduler offset %d changed whole-upload deadline: job %d=%s, want %s", offset, index, got, want)
				}
			}
		})
	}
}

func TestAvailabilityDeferralHasNoTimeout_RealPG(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	conn := startRealPGForCleanup(t)
	const job, tenant, hash = "7a7e0000-0000-4000-8000-000000000001", "7a7e0000-0000-4000-8000-000000000002", "availabilitywait0000000000000001"
	if _, err := conn.Exec(`INSERT INTO foghorn.artifacts(artifact_hash,artifact_type,tenant_id,status)VALUES($1,'vod',$2::uuid,'queued')`, hash, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO foghorn.processing_jobs(job_id,tenant_id,artifact_hash,job_type,status,required_memory_bytes,created_at)VALUES($1::uuid,$2::uuid,$3,'transcode','dispatched',2147483648,NOW()-INTERVAL '25 minutes')`, job, tenant, hash); err != nil {
		t.Fatal(err)
	}
	d := NewProcessingDispatcher(ProcessingDispatcherConfig{DB: conn, Logger: logging.NewLogger()})
	request := &processingJob{JobID: job, TenantID: tenant, ArtifactHash: sql.NullString{String: hash, Valid: true}, ArtifactType: sql.NullString{String: "vod", Valid: true}, OriginCluster: routeTestCluster, RequiredMemoryBytes: 2147483648}
	d.dispatchJob(context.Background(), request)
	var clock sql.NullTime
	if err := conn.QueryRow(`SELECT wait_started_at FROM foghorn.processing_jobs WHERE job_id=$1::uuid AND tenant_id=$2::uuid`, job, tenant).Scan(&clock); err != nil {
		t.Fatal(err)
	}
	if !clock.Valid {
		t.Fatal("queued wait was not timestamped for observability")
	}
	d.recoverStale()
	var status string
	if err := conn.QueryRow(`SELECT status FROM foghorn.processing_jobs WHERE job_id=$1::uuid AND tenant_id=$2::uuid`, job, tenant).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "queued" {
		t.Fatalf("availability wait became terminal: %s", status)
	}
	old := clusterAccessibleForTenant
	clusterAccessibleForTenant = func(context.Context, string, string) bool { return true }
	t.Cleanup(func() { clusterAccessibleForTenant = old })
	touchInCluster(sm, "recovered", routeTestCluster)
	setNodeMemory(sm, "recovered", 16*1024*1024*1024, map[string]state.ClassCapacity{mist.ProcessingClassVideoTranscode: {Total: 8}})
	if id, reason := routeProcessingJob(request); id != "recovered" {
		t.Fatalf("recovered node not selected: %s %s", id, reason)
	}
}
