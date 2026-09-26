//go:build schema_verify

package jobs

import (
	"context"
	"errors"
	"testing"

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

// Control loss alone never ends an ingest session. The session's node has no conn_owner on any
// replica (the production retirement guard reads it as absent), the session is 30 minutes old, and
// the production reaper job runs several passes. The session stays open and no offline effect is
// queued.
func TestIngestSessionReaperJobKeepsAbsentNodeSession_RealPG(t *testing.T) {
	conn := startRealPGForCleanup(t)
	prev := control.GetDB()
	control.SetDB(conn)
	t.Cleanup(func() { control.SetDB(prev) })

	mr := miniredis.RunT(t)
	store := state.NewRedisStateStore(goredis.NewClient(&goredis.Options{Addr: mr.Addr()}), "test-cluster")
	control.InitRelay(store, "inst-self", "10.0.0.1:9090", noRelayPool{}, logging.NewLogger())
	t.Cleanup(func() { control.InitRelay(nil, "", "", nil, logging.NewLogger()) })

	const (
		tenant = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		node   = "node-partitioned"
		stream = "live+partitioned"
	)
	ctx := context.Background()
	var sessionID string
	if err := conn.QueryRowContext(ctx, `
		INSERT INTO foghorn.ingest_sessions
			(tenant_id, node_id, stream_internal_name, connector_pid, start_trigger_uuid, started_at_unix_millis,
			 projection_state, started_at)
		VALUES ($1::uuid, $2, $3, 100, 'u-partitioned', 1000, 'active', NOW() - INTERVAL '30 minutes')
		RETURNING id::text
	`, tenant, node, stream).Scan(&sessionID); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	release, err := control.NodeRetireGuardLookup(ctx, node)
	if err != nil || release == nil {
		t.Fatalf("precondition: node must read as absent (guard=%v err=%v)", release != nil, err)
	}
	release()

	job := NewIngestSessionReaperJob(IngestSessionReaperConfig{Logger: logging.NewLogger()})
	for range 3 {
		job.reconcile()
	}

	var ended bool
	var reason string
	if err := conn.QueryRowContext(ctx, `SELECT ended_at IS NOT NULL, COALESCE(ended_reason,'') FROM foghorn.ingest_sessions WHERE id=$1::uuid`, sessionID).Scan(&ended, &reason); err != nil {
		t.Fatalf("read session: %v", err)
	}
	if ended {
		t.Fatalf("session of a node that only lost its control connection ended (reason %q)", reason)
	}
	var effects int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM foghorn.ingest_offline_effects WHERE tenant_id=$1::uuid AND stream_internal_name=$2`, tenant, stream).Scan(&effects); err != nil {
		t.Fatalf("count offline effects: %v", err)
	}
	if effects != 0 {
		t.Fatalf("control loss queued %d offline effects, want 0", effects)
	}
}
