//go:build schema_verify

package control

import (
	"context"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// seedOpenIngestSession inserts one active session (production CreateIngestSession is exercised
// elsewhere; here we only need rows for the reaper to evaluate).
func seedOpenIngestSession(t *testing.T, tenant, node, stream, uuid string, pid, millis int64) string {
	t.Helper()
	var id string
	if err := db.QueryRow(`
		INSERT INTO foghorn.ingest_sessions
			(tenant_id, node_id, stream_internal_name, connector_pid, start_trigger_uuid, started_at_unix_millis)
		VALUES ($1::uuid, $2, $3, $4, $5, $6)
		RETURNING id::text
	`, tenant, node, stream, pid, uuid, millis).Scan(&id); err != nil {
		t.Fatalf("seed ingest session: %v", err)
	}
	return id
}

func sessionEnded(t *testing.T, id string) (bool, string) {
	t.Helper()
	var ended bool
	var reason string
	if err := db.QueryRow(`
		SELECT ended_at IS NOT NULL, COALESCE(ended_reason,'')
		  FROM foghorn.ingest_sessions WHERE id = $1::uuid
	`, id).Scan(&ended, &reason); err != nil {
		t.Fatalf("read session state: %v", err)
	}
	return ended, reason
}

// fakeNodePresence is a mutable NodePresenceFunc: per node it reports present/absent, or an error.
type fakeNodePresence struct {
	present map[string]bool
	fail    map[string]bool
}

func (f *fakeNodePresence) lookup(_ context.Context, node string) (bool, error) {
	if f.fail[node] {
		return false, errConnOwnerUnavailable
	}
	return f.present[node], nil
}

func allowNodeRetire(_ context.Context, _ string) (func(), error) {
	return func() {}, nil
}

// TestIngestSessionReaper_RealPG proves the disconnect reaper against the real schema: a session is
// retired ONLY when its node's conn_owner is absent past the grace, and NEVER when the node is present
// (a control reconnect is not a session end) or when presence is unreadable (fail closed).
func TestIngestSessionReaper_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()
	lg := logging.NewLogger()
	grace := 90 * time.Second

	// A connected node survives because control reconnects do not end publisher sessions.
	present := seedOpenIngestSession(t, ingA, "node-present", "live+p", "u-p", 100, 1000)
	// disconnected: conn_owner absent → retired only after the grace.
	disconnected := seedOpenIngestSession(t, ingA, "node-gone", "live+g", "u-g", 101, 1000)
	// unreadable: presence lookup errors → never retired (fail closed).
	unreadable := seedOpenIngestSession(t, ingA, "node-err", "live+e", "u-e", 102, 1000)

	np := &fakeNodePresence{
		present: map[string]bool{"node-present": true, "node-gone": false, "node-err": false},
		fail:    map[string]bool{"node-err": true},
	}
	dwell := make(IngestReapDwell)
	t0 := time.Unix(1_700_000_000, 0)

	// Pass 1: nothing retired; the disconnected node only starts dwelling.
	if _, err := ReapIngestSessionsOnce(ctx, np.lookup, allowNodeRetire, dwell, t0, grace, lg); err != nil {
		t.Fatalf("pass 1: %v", err)
	}
	for _, id := range []string{present, disconnected, unreadable} {
		if ended, _ := sessionEnded(t, id); ended {
			t.Fatalf("session %s retired too early on pass 1", id)
		}
	}

	// Pass 2, still within the grace: the disconnected session must survive.
	if _, err := ReapIngestSessionsOnce(ctx, np.lookup, allowNodeRetire, dwell, t0.Add(grace-time.Second), grace, lg); err != nil {
		t.Fatalf("pass 2: %v", err)
	}
	if ended, _ := sessionEnded(t, disconnected); ended {
		t.Fatalf("disconnected session retired before the grace elapsed")
	}

	// Pass 3, past the grace: the disconnected session is retired; present and unreadable are not.
	if _, err := ReapIngestSessionsOnce(ctx, np.lookup, allowNodeRetire, dwell, t0.Add(grace+time.Second), grace, lg); err != nil {
		t.Fatalf("pass 3: %v", err)
	}
	if ended, reason := sessionEnded(t, disconnected); !ended || reason != "control_disconnect" {
		t.Fatalf("disconnected session not retired past the grace (ended=%v reason=%q)", ended, reason)
	}
	var offlineObligations int
	if err := conn.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM foghorn.ingest_offline_effects
		 WHERE tenant_id=$1::uuid AND stream_internal_name='live+g'
		   AND source_generation=$2::uuid AND state='pending'
	`, ingA, disconnected).Scan(&offlineObligations); err != nil {
		t.Fatalf("query disconnect offline obligation: %v", err)
	}
	if offlineObligations != 1 {
		t.Fatalf("disconnect retirement queued %d offline obligations, want 1", offlineObligations)
	}
	if ended, _ := sessionEnded(t, present); ended {
		t.Fatalf("a present node's session was retired (a control reconnect is not a session end)")
	}
	if ended, _ := sessionEnded(t, unreadable); ended {
		t.Fatalf("unreadable-presence session retired despite a fail-closed lookup")
	}
}

// TestIngestSessionReaper_BlipToleranceRealPG proves a node that reconnects within the grace resets the
// dwell clock, so a control-plane blip never retires its still-live session.
func TestIngestSessionReaper_BlipToleranceRealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()
	lg := logging.NewLogger()
	grace := 90 * time.Second

	blip := seedOpenIngestSession(t, ingA, "node-blip", "live+blip", "u-blip", 200, 1000)
	np := &fakeNodePresence{present: map[string]bool{"node-blip": false}} // absent at first
	dwell := make(IngestReapDwell)
	t0 := time.Unix(1_700_000_000, 0)

	// Pass 1: absent → start dwelling.
	if _, err := ReapIngestSessionsOnce(ctx, np.lookup, allowNodeRetire, dwell, t0, grace, lg); err != nil {
		t.Fatalf("pass 1: %v", err)
	}
	// Pass 2: the node reconnected (present) → dwell must clear.
	np.present["node-blip"] = true
	if _, err := ReapIngestSessionsOnce(ctx, np.lookup, allowNodeRetire, dwell, t0.Add(30*time.Second), grace, lg); err != nil {
		t.Fatalf("pass 2: %v", err)
	}
	if _, dwelling := dwell[blip]; dwelling {
		t.Fatal("dwell not cleared after the node reconnected")
	}
	// Pass 3: absent again, but the grace restarts from here — well past the original t0+grace, yet
	// only 1s into the NEW absence, so it must NOT retire.
	np.present["node-blip"] = false
	if _, err := ReapIngestSessionsOnce(ctx, np.lookup, allowNodeRetire, dwell, t0.Add(grace+time.Second), grace, lg); err != nil {
		t.Fatalf("pass 3: %v", err)
	}
	if ended, _ := sessionEnded(t, blip); ended {
		t.Fatal("a blipped-then-reconnected session was retired; the dwell clock did not reset")
	}
}

func TestIngestSessionReaper_RechecksAbsenceUnderRetireGuardRealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()
	grace := 90 * time.Second
	sessionID := seedOpenIngestSession(t, ingA, "node-raced-reconnect", "live+raced-reconnect", "u-raced-reconnect", 300, 1000)
	np := &fakeNodePresence{present: map[string]bool{"node-raced-reconnect": false}}
	dwell := make(IngestReapDwell)
	t0 := time.Unix(1_700_000_000, 0)
	if _, err := ReapIngestSessionsOnce(ctx, np.lookup, allowNodeRetire, dwell, t0, grace, logging.NewLogger()); err != nil {
		t.Fatalf("initial absence pass: %v", err)
	}
	guardObserved := false
	reconnectedBeforeGuard := func(context.Context, string) (func(), error) {
		guardObserved = true
		return nil, nil
	}
	if retired, err := ReapIngestSessionsOnce(ctx, np.lookup, reconnectedBeforeGuard, dwell, t0.Add(grace+time.Second), grace, logging.NewLogger()); err != nil || retired != 0 {
		t.Fatalf("guarded pass: retired=%d err=%v", retired, err)
	}
	if !guardObserved {
		t.Fatal("reaper did not recheck node absence through the retirement guard")
	}
	if ended, _ := sessionEnded(t, sessionID); ended {
		t.Fatal("session retired after the node reconnected between the pass snapshot and retirement")
	}
}

func TestNeverProjectedSessionReaperQueuesInactiveProjectionRealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()
	const node, stream = "node-never-projected", "live+never-projected"
	sessionID := seedOpenIngestSession(t, ingA, node, stream, "u-never-projected", 400, 1000)
	if _, err := db.Exec(`
		UPDATE foghorn.ingest_sessions
		   SET projection_state='pending', started_at=NOW()-INTERVAL '10 minutes'
		 WHERE id=$1::uuid
	`, sessionID); err != nil {
		t.Fatalf("age pending session: %v", err)
	}

	retired, err := ReapNeverProjectedIngestSessions(ctx, 2*time.Minute, logging.NewLogger())
	if err != nil || retired != 1 {
		t.Fatalf("reap pending projection: retired=%d err=%v", retired, err)
	}
	if ended, reason := sessionEnded(t, sessionID); !ended || reason != "projection_timeout" {
		t.Fatalf("pending session state: ended=%v reason=%q", ended, reason)
	}
	var effects int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM foghorn.ingest_offline_effects
		 WHERE tenant_id=$1::uuid AND stream_internal_name=$2 AND source_generation=$3::uuid AND state='pending'
	`, ingA, stream, sessionID).Scan(&effects); err != nil {
		t.Fatalf("count pending-session cleanup effects: %v", err)
	}
	if effects != 1 {
		t.Fatalf("pending-session cleanup effects = %d, want 1", effects)
	}
}
