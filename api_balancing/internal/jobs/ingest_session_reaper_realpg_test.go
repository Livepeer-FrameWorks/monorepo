//go:build schema_verify

package jobs

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

type noRelayPool struct{}

func (noRelayPool) GetOrCreate(string, string) (control.CommandRelayClient, error) {
	return nil, errors.New("relay not used by this test")
}

const reaperTenant = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"

// useReaperStores wires real PostgreSQL and the production conn_owner / lease store (miniredis) as
// replica inst-self. No node has a control connection until the test gives it one.
func useReaperStores(t *testing.T) (*sql.DB, *state.RedisStateStore) {
	t.Helper()
	conn := startRealPGForCleanup(t)
	prev := control.GetDB()
	control.SetDB(conn)
	t.Cleanup(func() { control.SetDB(prev) })

	mr := miniredis.RunT(t)
	store := state.NewRedisStateStore(goredis.NewClient(&goredis.Options{Addr: mr.Addr()}), "test-cluster")
	control.InitRelay(store, "inst-self", "10.0.0.1:9090", noRelayPool{}, logging.NewLogger())
	t.Cleanup(func() { control.InitRelay(nil, "", "", nil, logging.NewLogger()) })
	return conn, store
}

func seedProjectedSession(t *testing.T, conn *sql.DB, node, stream, trigger string) string {
	t.Helper()
	var sessionID string
	if err := conn.QueryRowContext(context.Background(), `
		INSERT INTO foghorn.ingest_sessions
			(tenant_id, node_id, stream_internal_name, connector_pid, start_trigger_uuid, started_at_unix_millis,
			 projection_state, started_at)
		VALUES ($1::uuid, $2, $3, 100, $4, 1000, 'active', NOW() - INTERVAL '30 minutes')
		RETURNING id::text
	`, reaperTenant, node, stream, trigger).Scan(&sessionID); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	return sessionID
}

func readSession(t *testing.T, conn *sql.DB, sessionID string) (bool, string) {
	t.Helper()
	var ended bool
	var reason string
	if err := conn.QueryRowContext(context.Background(), `SELECT ended_at IS NOT NULL, COALESCE(ended_reason,'') FROM foghorn.ingest_sessions WHERE id=$1::uuid`, sessionID).Scan(&ended, &reason); err != nil {
		t.Fatalf("read session: %v", err)
	}
	return ended, reason
}

func countOfflineEffects(t *testing.T, conn *sql.DB, stream string) int {
	t.Helper()
	var effects int
	if err := conn.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM foghorn.ingest_offline_effects WHERE tenant_id=$1::uuid AND stream_internal_name=$2`, reaperTenant, stream).Scan(&effects); err != nil {
		t.Fatalf("count offline effects: %v", err)
	}
	return effects
}

// Control loss within the lost-node window ends no ingest session. The session's node has no
// conn_owner on any replica (the production retirement guard reads it as absent), the session is 30
// minutes old, and the production reaper job runs several passes at the wall clock. The session stays
// open and no offline effect is queued.
func TestIngestSessionReaperJobKeepsAbsentNodeSession_RealPG(t *testing.T) {
	conn, _ := useReaperStores(t)
	const node, stream = "node-partitioned", "live+partitioned"
	sessionID := seedProjectedSession(t, conn, node, stream, "u-partitioned")

	ctx := context.Background()
	release, err := control.NodeRetireGuardLookup(ctx, node)
	if err != nil || release == nil {
		t.Fatalf("precondition: node must read as absent (guard=%v err=%v)", release != nil, err)
	}
	release()

	job := NewIngestSessionReaperJob(IngestSessionReaperConfig{Logger: logging.NewLogger()})
	for range 3 {
		job.reconcile()
	}

	if ended, reason := readSession(t, conn, sessionID); ended {
		t.Fatalf("session of a node that only lost its control connection ended (reason %q)", reason)
	}
	if effects := countOfflineEffects(t, conn, stream); effects != 0 {
		t.Fatalf("control loss queued %d offline effects, want 0", effects)
	}
}

// The production job, holding the lost-node lease, ends a node's session as node_lost once the node
// has been without a control connection and without evidence of life for the fixed window, measured
// on the job's clock from the first pass that saw it absent.
func TestIngestSessionReaperJobEndsLostNodeSession_RealPG(t *testing.T) {
	conn, _ := useReaperStores(t)
	const node, stream = "node-crashed", "live+crashed"
	sessionID := seedProjectedSession(t, conn, node, stream, "u-crashed")

	job := NewIngestSessionReaperJob(IngestSessionReaperConfig{Logger: logging.NewLogger()})
	t0 := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	clock := t0
	job.now = func() time.Time { return clock }

	for _, at := range []time.Duration{0, 2 * time.Minute, control.IngestNodeLostAfter - time.Second} {
		clock = t0.Add(at)
		job.reconcile()
		if ended, reason := readSession(t, conn, sessionID); ended {
			t.Fatalf("session ended %s after the node went away (reason %q), inside the window", at, reason)
		}
	}
	clock = t0.Add(control.IngestNodeLostAfter)
	job.reconcile()
	if ended, reason := readSession(t, conn, sessionID); !ended || reason != control.IngestEndedNodeLost {
		t.Fatalf("session at the window: ended=%v reason=%q, want %q", ended, reason, control.IngestEndedNodeLost)
	}
	if effects := countOfflineEffects(t, conn, stream); effects != 1 {
		t.Fatalf("offline effects = %d, want 1", effects)
	}
}

// Only the lease holder tracks lost nodes. While another replica holds the lease this replica ends
// nothing, and when it takes the lease over its clocks start from that pass.
func TestIngestSessionReaperJobLostNodeNeedsLease_RealPG(t *testing.T) {
	conn, store := useReaperStores(t)
	const node, stream = "node-crashed-elsewhere", "live+crashed-elsewhere"
	sessionID := seedProjectedSession(t, conn, node, stream, "u-elsewhere")
	ctx := context.Background()
	if held, err := store.TryAcquireLease(ctx, "ingest-node-lost", "inst-peer", time.Hour); err != nil || !held {
		t.Fatalf("peer lease: held=%v err=%v", held, err)
	}

	job := NewIngestSessionReaperJob(IngestSessionReaperConfig{Logger: logging.NewLogger()})
	t0 := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	clock := t0
	job.now = func() time.Time { return clock }
	for _, at := range []time.Duration{0, control.IngestNodeLostAfter, 2 * control.IngestNodeLostAfter} {
		clock = t0.Add(at)
		job.reconcile()
	}
	if ended, reason := readSession(t, conn, sessionID); ended {
		t.Fatalf("a replica without the lease ended a session (reason %q)", reason)
	}

	if err := store.ReleaseLease(ctx, "ingest-node-lost", "inst-peer"); err != nil {
		t.Fatalf("release peer lease: %v", err)
	}
	takeover := t0.Add(3 * control.IngestNodeLostAfter)
	clock = takeover
	job.reconcile()
	clock = takeover.Add(control.IngestNodeLostAfter - time.Second)
	job.reconcile()
	if ended, _ := readSession(t, conn, sessionID); ended {
		t.Fatal("the new lease holder ended the session before a full window of its own")
	}
	clock = takeover.Add(control.IngestNodeLostAfter)
	job.reconcile()
	if ended, reason := readSession(t, conn, sessionID); !ended || reason != control.IngestEndedNodeLost {
		t.Fatalf("session after a full window on the new lease holder: ended=%v reason=%q", ended, reason)
	}
}
