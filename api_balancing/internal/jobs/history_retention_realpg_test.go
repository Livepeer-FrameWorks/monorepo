//go:build schema_verify

package jobs

import (
	"context"
	"database/sql"
	"testing"

	"frameworks/api_balancing/internal/database/foghorndb"
)

const retentionTenant = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"

func retentionExec(t *testing.T, conn *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := conn.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("seed: %v\n%s", err, query)
	}
}

func retentionSession(t *testing.T, conn *sql.DB, stream, trigger, endedAgo string) string {
	t.Helper()
	var id string
	query := `
		INSERT INTO foghorn.ingest_sessions
			(tenant_id, node_id, stream_internal_name, connector_pid, start_trigger_uuid, started_at_unix_millis,
			 projection_state, started_at, ended_at)
		VALUES ($1::uuid, 'node-r', $2, 100, $3, 1000, 'active', NOW() - INTERVAL '90 days',
		        CASE WHEN $4 = '' THEN NULL ELSE NOW() - $4::interval END)
		RETURNING id::text`
	if err := conn.QueryRowContext(context.Background(), query, retentionTenant, stream, trigger, endedAgo).Scan(&id); err != nil {
		t.Fatalf("seed session %s: %v", trigger, err)
	}
	return id
}

func retentionRowExists(t *testing.T, conn *sql.DB, query string, args ...any) bool {
	t.Helper()
	var exists bool
	if err := conn.QueryRowContext(context.Background(), "SELECT EXISTS ("+query+")", args...).Scan(&exists); err != nil {
		t.Fatalf("probe: %v", err)
	}
	return exists
}

// One retention pass deletes the history past its horizon that nothing references, across more than one batch,
// and keeps every row a reader still needs: ended generations bound to a recording or owed an admission effect,
// terminal jobs that parent another job or whose clip outcome Commodore has not consumed, recent history, live rows
// and undelivered events.
func TestRetentionPurgesOnlyUnreferencedHistory_RealPG(t *testing.T) { //nolint:funlen // One fixture covers every kept and deleted class.
	conn := startRealPGForCleanup(t)
	ctx := context.Background()

	// Sessions. More old, unreferenced ended sessions than one batch holds.
	retentionExec(t, conn, `
		INSERT INTO foghorn.ingest_sessions
			(tenant_id, node_id, stream_internal_name, connector_pid, start_trigger_uuid, started_at_unix_millis,
			 projection_state, started_at, ended_at)
		SELECT $1::uuid, 'node-bulk', 'bulk-' || g, 100, 'bulk-trigger-' || g, 1000, 'active',
		       NOW() - INTERVAL '90 days', NOW() - INTERVAL '40 days'
		FROM generate_series(1, $2::int) g`, retentionTenant, historyPurgeBatch+200)
	oldEnded := retentionSession(t, conn, "s-old", "t-old", "31 days")
	recentEnded := retentionSession(t, conn, "s-recent", "t-recent", "29 days")
	live := retentionSession(t, conn, "s-live", "t-live", "")
	dvrBound := retentionSession(t, conn, "s-dvr", "t-dvr", "31 days")
	retentionExec(t, conn, `
		INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, stream_internal_name, status, ingest_generation)
		VALUES ('dvrretained000000000000000000001', 'dvr', $1::uuid, 's-dvr', 'completed', $2::uuid)`, retentionTenant, dvrBound)
	admissionOwed := retentionSession(t, conn, "s-adm", "t-adm", "31 days")
	retentionExec(t, conn, `
		INSERT INTO foghorn.ingest_admission_effects (tenant_id, stream_internal_name, node_id, source_generation, source_revision, state)
		VALUES ($1::uuid, 's-adm', 'node-r', $2::uuid, 1, 'pending')`, retentionTenant, admissionOwed)
	admissionSettled := retentionSession(t, conn, "s-adm-done", "t-adm-done", "31 days")
	retentionExec(t, conn, `
		INSERT INTO foghorn.ingest_admission_effects (tenant_id, stream_internal_name, node_id, source_generation, source_revision, state)
		VALUES ($1::uuid, 's-adm-done', 'node-r', $2::uuid, 1, 'applied')`, retentionTenant, admissionSettled)

	// Processing jobs.
	job := func(status, age string, artifact any, parent any) string {
		var id string
		if err := conn.QueryRowContext(ctx, `
			INSERT INTO foghorn.processing_jobs (tenant_id, job_type, status, artifact_hash, parent_job_id, created_at, updated_at)
			VALUES ($1::uuid, 'process', $2, $3, $4::uuid, NOW() - $5::interval, NOW() - $5::interval)
			RETURNING job_id::text`, retentionTenant, status, artifact, parent, age).Scan(&id); err != nil {
			t.Fatalf("seed job: %v", err)
		}
		return id
	}
	oldCompleted := job("completed", "31 days", nil, nil)
	oldFailed := job("failed", "31 days", nil, nil)
	recentCompleted := job("completed", "29 days", nil, nil)
	oldQueued := job("queued", "31 days", nil, nil)
	parent := job("completed", "31 days", nil, nil)
	child := job("completed", "1 day", nil, parent)
	retentionExec(t, conn, `
		INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, stream_internal_name, status)
		VALUES ('clipunconsumed00000000000000001', 'clip', $1::uuid, 's-clip', 'ready')`, retentionTenant)
	retentionExec(t, conn, `
		INSERT INTO foghorn.artifact_creation_commands (request_id, tenant_id, kind, artifact_hash, status)
		VALUES (gen_random_uuid(), $1::uuid, 'clip', 'clipunconsumed00000000000000001', 'committed')`, retentionTenant)
	unconsumedClip := job("completed", "31 days", "clipunconsumed00000000000000001", nil)

	// Artifact events.
	event := func(kind, completedAgo string) string {
		var id string
		if err := conn.QueryRowContext(ctx, `
			INSERT INTO foghorn.artifact_event_outbox (event_kind, tenant_id, created_at, completed_at)
			VALUES ($1, $2::uuid, NOW() - INTERVAL '30 days', CASE WHEN $3 = '' THEN NULL ELSE NOW() - $3::interval END)
			RETURNING id::text`, kind, retentionTenant, completedAgo).Scan(&id); err != nil {
			t.Fatalf("seed event: %v", err)
		}
		return id
	}
	oldDelivered := event("vod_lifecycle", "8 days")
	recentDelivered := event("vod_lifecycle", "6 days")
	undelivered := event("vod_lifecycle", "")

	purged, err := purgeRetainedHistory(ctx, foghorndb.New(conn))
	if err != nil {
		t.Fatalf("retention pass: %v", err)
	}
	wantSessions := int64(historyPurgeBatch + 200 + 2)
	if purged.sessions != wantSessions || purged.jobs != 2 || purged.events != 1 {
		t.Fatalf("purged %+v, want sessions=%d jobs=2 events=1", purged, wantSessions)
	}

	session := func(id string) bool {
		return retentionRowExists(t, conn, `SELECT 1 FROM foghorn.ingest_sessions WHERE id = $1::uuid`, id)
	}
	if retentionRowExists(t, conn, `SELECT 1 FROM foghorn.ingest_sessions WHERE node_id = 'node-bulk'`) {
		t.Fatal("old unreferenced ended sessions beyond one batch survived the pass")
	}
	for name, id := range map[string]string{"old ended": oldEnded, "settled admission": admissionSettled} {
		if session(id) {
			t.Fatalf("%s session survived the pass", name)
		}
	}
	for name, id := range map[string]string{"recent ended": recentEnded, "live": live, "dvr bound": dvrBound, "admission owed": admissionOwed} {
		if !session(id) {
			t.Fatalf("%s session was deleted", name)
		}
	}

	jobRow := func(id string) bool {
		return retentionRowExists(t, conn, `SELECT 1 FROM foghorn.processing_jobs WHERE job_id = $1::uuid`, id)
	}
	for name, id := range map[string]string{"old completed": oldCompleted, "old failed": oldFailed} {
		if jobRow(id) {
			t.Fatalf("%s job survived the pass", name)
		}
	}
	for name, id := range map[string]string{"recent completed": recentCompleted, "old queued": oldQueued, "parent": parent, "child": child, "unconsumed clip": unconsumedClip} {
		if !jobRow(id) {
			t.Fatalf("%s job was deleted", name)
		}
	}

	eventRow := func(id string) bool {
		return retentionRowExists(t, conn, `SELECT 1 FROM foghorn.artifact_event_outbox WHERE id = $1::uuid`, id)
	}
	if eventRow(oldDelivered) {
		t.Fatal("event delivered 8 days ago survived the pass")
	}
	if !eventRow(recentDelivered) || !eventRow(undelivered) {
		t.Fatal("a recently delivered or undelivered event was deleted")
	}

	again, err := purgeRetainedHistory(ctx, foghorndb.New(conn))
	if err != nil || again != (retentionPurge{}) {
		t.Fatalf("second pass purged %+v (err %v), want nothing left to delete", again, err)
	}
}
