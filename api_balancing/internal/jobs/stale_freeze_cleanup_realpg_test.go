//go:build schema_verify

package jobs

import (
	"database/sql"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	_ "github.com/lib/pq"
)

func startRealPGForCleanup(t *testing.T) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-cleanup-realpg-%d", time.Now().UnixNano())
	run := func(args ...string) (string, error) {
		out, err := exec.Command("docker", args...).CombinedOutput()
		return string(out), err
	}
	if out, err := run("run", "-d", "--name", name, "-P", "-e", "POSTGRES_PASSWORD=harness", "pgvector/pgvector:pg15"); err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}
	t.Cleanup(func() { _, _ = run("rm", "-f", name) })
	portOut, err := run("port", name, "5432/tcp")
	if err != nil {
		t.Fatalf("docker port: %v\n%s", err, portOut)
	}
	port := ""
	for _, line := range strings.Split(strings.TrimSpace(portOut), "\n") {
		if i := strings.LastIndex(line, ":"); i >= 0 {
			port = strings.TrimSpace(line[i+1:])
			break
		}
	}
	conn, err := sql.Open("postgres", fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/postgres?sslmode=disable", port))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	deadline := time.Now().Add(90 * time.Second)
	for {
		if err := conn.Ping(); err == nil {
			break
		}
		if time.Now().After(deadline) {
			logs, _ := run("logs", "--tail", "40", name)
			t.Fatalf("postgres did not become ready:\n%s", logs)
		}
		time.Sleep(time.Second)
	}
	schema, rerr := dbsql.Content.ReadFile("schema/foghorn.sql")
	if rerr != nil {
		t.Fatalf("read foghorn.sql: %v", rerr)
	}
	if _, err := conn.Exec(string(schema)); err != nil {
		t.Fatalf("apply foghorn.sql: %v", err)
	}
	return conn
}

// TestStaleFreezeCleanup_RealPG drives the ACTUAL production StaleFreezeCleanupJob.cleanup() against the
// real foghorn.sql schema and proves the multipart-vs-cleanup separation: a live multipart VOD ingest
// (in_progress, storage_location='pending', NO freeze identity) is NEVER touched, while a timed-out real
// freeze attempt (freezing + identity) is recovered to retryable 'failed' with its identity cleared.
func TestStaleFreezeCleanup_RealPG(t *testing.T) {
	conn := startRealPGForCleanup(t)
	const tid = "11111111-1111-1111-1111-111111111111"

	// A multipart VOD upload: exactly the state CreateVodUpload writes, aged well past the stale window.
	if _, err := conn.Exec(`INSERT INTO foghorn.artifacts
		(artifact_hash, artifact_type, tenant_id, status, sync_status, storage_location, last_sync_attempt)
		VALUES ('mp', 'vod', $1::uuid, 'uploading', 'in_progress', 'pending', NOW() - INTERVAL '2 hours')`, tid); err != nil {
		t.Fatalf("seed multipart: %v", err)
	}
	// A real, timed-out freeze attempt (ready + freezing + identity + bound descriptor).
	if _, err := conn.Exec(`INSERT INTO foghorn.artifacts
		(artifact_hash, artifact_type, tenant_id, status, sync_status, storage_location, sync_request_id, sync_node_id, sync_object_key, last_sync_attempt)
		VALUES ('fz', 'vod', $1::uuid, 'ready', 'in_progress', 'freezing', 'reqF', 'nodeF', 'vod/tenant/fz/fz.mp4', NOW() - INTERVAL '2 hours')`, tid); err != nil {
		t.Fatalf("seed freeze: %v", err)
	}

	// Run the PRODUCTION cleanup with a 30-minute stale window.
	j := NewStaleFreezeCleanupJob(StaleFreezeCleanupConfig{DB: conn, Logger: logging.NewLogger(), StaleAfter: 30 * time.Minute})
	j.cleanup()

	scalar := func(q string) string {
		var s sql.NullString
		if err := conn.QueryRow(q).Scan(&s); err != nil {
			t.Fatalf("scalar %q: %v", q, err)
		}
		return s.String
	}
	if got := scalar(`SELECT sync_status||'/'||storage_location FROM foghorn.artifacts WHERE artifact_hash='mp'`); got != "in_progress/pending" {
		t.Fatalf("multipart upload must be untouched, got %q", got)
	}
	if got := scalar(`SELECT sync_status||'/'||storage_location||'/'||COALESCE(sync_request_id,'<null>') FROM foghorn.artifacts WHERE artifact_hash='fz'`); got != "failed/local/<null>" {
		t.Fatalf("timed-out freeze must be recovered to failed/local with cleared identity, got %q", got)
	}
	if got := scalar(`SELECT sync_object_key FROM foghorn.artifacts WHERE artifact_hash='fz'`); got != "vod/tenant/fz/fz.mp4" {
		t.Fatalf("recovered freeze must retain its descriptor for the next attempt, got %q", got)
	}
	// The abandoned attempt's staging object was DURABLY enqueued for deletion in the SAME transaction as
	// the reset (the StagingCleanupJob drains it) — recovery no longer leaks staging on a best-effort delete.
	wantKey := control.FreezeStagingKey("vod/tenant/fz/fz.mp4", "reqF")
	if got := scalar(`SELECT object_key FROM foghorn.staging_cleanup_queue`); got != wantKey {
		t.Fatalf("recovery must enqueue the abandoned attempt's staging object %q, got %q", wantKey, got)
	}
}
