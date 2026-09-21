//go:build schema_verify

package control

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"frameworks/api_balancing/internal/domainevents"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	eventsoutbox "github.com/Livepeer-FrameWorks/monorepo/pkg/events/outbox"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	eventspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
)

const (
	domainTenant = "7a1e0000-0000-4000-8000-000000000001"
	domainStream = "7a1e0000-0000-4000-8000-0000000000aa"
)

type domainRow struct {
	eventID, eventType, aggregateType, aggregateID, scope, tenantID string
	payload                                                         []byte
}

func domainRows(t *testing.T, conn *sql.DB, eventType string) []domainRow {
	t.Helper()
	rows, err := conn.Query(`
		SELECT event_id::text, event_type, aggregate_type, aggregate_id, scope, COALESCE(tenant_id::text, ''), payload
		FROM foghorn.domain_event_outbox WHERE event_type = $1 ORDER BY enqueued_at, event_id`, eventType)
	if err != nil {
		t.Fatalf("read domain events: %v", err)
	}
	defer rows.Close()
	var out []domainRow
	for rows.Next() {
		var r domainRow
		if err := rows.Scan(&r.eventID, &r.eventType, &r.aggregateType, &r.aggregateID, &r.scope, &r.tenantID, &r.payload); err != nil {
			t.Fatalf("scan domain event: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func countDomainRows(t *testing.T, conn *sql.DB) int {
	t.Helper()
	var n int
	if err := conn.QueryRow(`SELECT count(*) FROM foghorn.domain_event_outbox`).Scan(&n); err != nil {
		t.Fatalf("count domain events: %v", err)
	}
	return n
}

func mintStreamSession(t *testing.T, node, stream string, pid int64, trigger string, startedMillis int64) (string, IngestSessionOutcome) {
	t.Helper()
	id, outcome, err := MintIngestSession(context.Background(), IngestSessionRequest{
		TenantID: domainTenant, NodeID: node, InternalName: stream, StreamID: domainStream,
		Protocol: publicv1.IngestProtocol_INGEST_PROTOCOL_RTMP, ConnectorPID: pid, TriggerUUID: trigger,
		StartedAtMillis: startedMillis, IngestClusterID: "cell-a",
	}, logging.NewLogger())
	if err != nil {
		t.Fatalf("mint %s: %v", trigger, err)
	}
	return id, outcome
}

// The stream lifecycle of ingest sessions on real PostgreSQL: connected is
// emitted once per generation, live only by the owner node's first playable
// buffer, and idle by every path that ends a session.
func TestStreamLifecycleDomainEvents_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()
	const stream = "live+domain"

	first, outcome := mintStreamSession(t, "node-owner", stream, 101, "trig-1", 1000)
	if outcome != IngestSessionActive {
		t.Fatalf("first mint outcome %v", outcome)
	}
	connected := domainRows(t, conn, "stream.connected")
	if len(connected) != 1 {
		t.Fatalf("a new generation emits one stream.connected, got %d", len(connected))
	}
	c := connected[0]
	if c.aggregateType != "streams" || c.aggregateID != domainStream || c.scope != "tenant" || c.tenantID != domainTenant {
		t.Fatalf("stream.connected row = %+v", c)
	}
	_, msg, err := events.Decode("stream.connected", c.payload)
	if err != nil {
		t.Fatal(err)
	}
	if got := msg.(*publicv1.StreamConnected); got.GetStreamId() != domainStream || got.GetProtocol() != publicv1.IngestProtocol_INGEST_PROTOCOL_RTMP {
		t.Fatalf("stream.connected payload = %+v", got)
	}

	// A resumed PUSH_REWRITE of the same trigger returns the same generation and emits nothing.
	again, outcome := mintStreamSession(t, "node-owner", stream, 101, "trig-1", 1500)
	if outcome != IngestSessionActive || again != first {
		t.Fatalf("resume returned %q/%v, want %q", again, outcome, first)
	}
	if n := len(domainRows(t, conn, "stream.connected")); n != 1 {
		t.Fatalf("a resume within the generation must not re-emit connected, got %d", n)
	}

	// A replica's playable buffer matches nothing.
	marked, err := MarkIngestSessionPlayable(ctx, domainTenant, "node-replica", stream, 2000)
	if err != nil || marked {
		t.Fatalf("replica buffer marked=%v err=%v", marked, err)
	}
	// A buffer older than the session start is a delayed trigger of an earlier session.
	marked, err = MarkIngestSessionPlayable(ctx, domainTenant, "node-owner", stream, 999)
	if err != nil || marked {
		t.Fatalf("pre-start buffer marked=%v err=%v", marked, err)
	}
	if n := len(domainRows(t, conn, "stream.live")); n != 0 {
		t.Fatalf("replica and stale buffers emit no stream.live, got %d", n)
	}
	// The owner's first FULL marks the session; a duplicate FULL does not.
	marked, err = MarkIngestSessionPlayable(ctx, domainTenant, "node-owner", stream, 2000)
	if err != nil || !marked {
		t.Fatalf("owner buffer marked=%v err=%v", marked, err)
	}
	marked, err = MarkIngestSessionPlayable(ctx, domainTenant, "node-owner", stream, 2100)
	if err != nil || marked {
		t.Fatalf("duplicate FULL marked=%v err=%v", marked, err)
	}
	if n := len(domainRows(t, conn, "stream.live")); n != 1 {
		t.Fatalf("a duplicate FULL emits stream.live once, got %d", n)
	}

	// The session-end transaction emits idle.
	closed, err := FinalizeIngestSessionClose(ctx, domainTenant, "node-owner", 101, 3000, stream, logging.NewLogger())
	if err != nil || closed.EndedSessionID != first {
		t.Fatalf("close ended %q err=%v", closed.EndedSessionID, err)
	}
	if n := len(domainRows(t, conn, "stream.idle")); n != 1 {
		t.Fatalf("session close emits one stream.idle, got %d", n)
	}

	// A new generation connects again, and the STREAM_END reaper ending it emits idle.
	second, outcome := mintStreamSession(t, "node-owner", stream, 102, "trig-2", 4000)
	if outcome != IngestSessionActive || second == first {
		t.Fatalf("second mint %q/%v", second, outcome)
	}
	if n := len(domainRows(t, conn, "stream.connected")); n != 2 {
		t.Fatalf("a new generation emits connected, got %d total", n)
	}
	reaped, err := EndIngestSessionsForStreamEnd(ctx, domainTenant, "node-owner", stream, 5000, logging.NewLogger())
	if err != nil || reaped != 1 {
		t.Fatalf("reaper ended %d err=%v", reaped, err)
	}
	if n := len(domainRows(t, conn, "stream.idle")); n != 2 {
		t.Fatalf("the reaper end emits stream.idle, got %d total", n)
	}

	// PID reuse on the same node supersedes the open generation: idle, then connected.
	third, _ := mintStreamSession(t, "node-owner", stream, 103, "trig-3", 6000)
	fourth, outcome := mintStreamSession(t, "node-owner", stream, 103, "trig-4", 7000)
	if outcome != IngestSessionActive || fourth == third {
		t.Fatalf("PID-reuse mint %q/%v", fourth, outcome)
	}
	if idle, conn3 := len(domainRows(t, conn, "stream.idle")), len(domainRows(t, conn, "stream.connected")); idle != 3 || conn3 != 4 {
		t.Fatalf("PID reuse emits idle+connected: idle=%d connected=%d", idle, conn3)
	}
	var order []string
	rows, err := conn.Query(`SELECT event_type FROM foghorn.domain_event_outbox ORDER BY enqueued_at, event_id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var ty string
		if err := rows.Scan(&ty); err != nil {
			t.Fatal(err)
		}
		order = append(order, ty)
	}
	_ = rows.Close()
	want := []string{"stream.connected", "stream.live", "stream.idle", "stream.connected", "stream.idle", "stream.connected", "stream.idle", "stream.connected"}
	if len(order) != len(want) {
		t.Fatalf("stream event order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("stream event order = %v, want %v", order, want)
		}
	}

	// A session admitted without a public stream ID emits nothing on mint or end.
	before := countDomainRows(t, conn)
	if _, _, err := CreateIngestSession(ctx, domainTenant, "node-x", "live+nostream", 201, "trig-ns", 1000, nil, "cell-a", logging.NewLogger()); err != nil {
		t.Fatal(err)
	}
	if _, err := EndIngestSessionsForStreamEnd(ctx, domainTenant, "node-x", "live+nostream", 2000, logging.NewLogger()); err != nil {
		t.Fatal(err)
	}
	if after := countDomainRows(t, conn); after != before {
		t.Fatalf("a session without a stream ID emitted %d events", after-before)
	}
}

func insertFinalizingDVR(t *testing.T, conn *sql.DB, hash string) {
	t.Helper()
	if _, err := conn.Exec(`
		INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, stream_id, status)
		VALUES ($1, 'dvr', $2::uuid, $3::uuid, 'finalizing')`, hash, domainTenant, domainStream); err != nil {
		t.Fatalf("insert dvr: %v", err)
	}
}

func artifactStatus(t *testing.T, conn *sql.DB, hash string) string {
	t.Helper()
	var s string
	if err := conn.QueryRow(`SELECT status FROM foghorn.artifacts WHERE artifact_hash = $1`, hash).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func legacyRowCount(t *testing.T, conn *sql.DB, artifactID string) int {
	t.Helper()
	var n int
	if err := conn.QueryRow(`SELECT count(*) FROM foghorn.artifact_event_outbox WHERE artifact_id = $1`, artifactID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Artifact transitions commit with their domain event: a failure of either
// outbox write rolls the status write back, and a committed transition's legacy
// row carries the domain event's ID.
func TestArtifactTransitionDomainEvents_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()
	ended := time.Unix(1_700_000_000, 0)

	// The domain outbox write fails: the terminal status write rolls back with it.
	insertFinalizingDVR(t, conn, "dvrdomainfail0000000000000000001")
	if _, err := conn.Exec(`ALTER TABLE foghorn.domain_event_outbox RENAME TO domain_event_outbox_offline`); err != nil {
		t.Fatal(err)
	}
	if _, err := setArtifactFailed(ctx, "dvrdomainfail0000000000000000001", "writer crashed", nil, ended, domainTenant, false); err == nil {
		t.Fatal("setArtifactFailed must fail when the domain event cannot be written")
	}
	if _, err := conn.Exec(`ALTER TABLE foghorn.domain_event_outbox_offline RENAME TO domain_event_outbox`); err != nil {
		t.Fatal(err)
	}
	if s := artifactStatus(t, conn, "dvrdomainfail0000000000000000001"); s != "finalizing" {
		t.Fatalf("a failed outbox insert must roll the status write back, status=%q", s)
	}
	if n := legacyRowCount(t, conn, "dvrdomainfail0000000000000000001"); n != 0 {
		t.Fatalf("no legacy row may survive the rollback, got %d", n)
	}

	// The legacy row write fails after the domain event was inserted: the status write and the
	// domain event both roll back.
	insertFinalizingDVR(t, conn, "dvrlegacyfail0000000000000000001")
	if _, err := conn.Exec(`ALTER TABLE foghorn.artifact_event_outbox RENAME TO artifact_event_outbox_offline`); err != nil {
		t.Fatal(err)
	}
	if _, err := setArtifactFailed(ctx, "dvrlegacyfail0000000000000000001", "writer crashed", nil, ended, domainTenant, false); err == nil {
		t.Fatal("setArtifactFailed must fail when the legacy row cannot be written")
	}
	if _, err := conn.Exec(`ALTER TABLE foghorn.artifact_event_outbox_offline RENAME TO artifact_event_outbox`); err != nil {
		t.Fatal(err)
	}
	if s := artifactStatus(t, conn, "dvrlegacyfail0000000000000000001"); s != "finalizing" {
		t.Fatalf("a rolled-back transition must leave the status unchanged, status=%q", s)
	}
	if n := countDomainRows(t, conn); n != 0 {
		t.Fatalf("a rolled-back status write must leave no artifact event, got %d", n)
	}

	// The committed transition: recording.failed plus its legacy row under one ID.
	const hash = "dvrdomainok000000000000000000001"
	insertFinalizingDVR(t, conn, hash)
	applied, err := setArtifactFailed(ctx, hash, "writer crashed", nil, ended, domainTenant, false)
	if err != nil || !applied {
		t.Fatalf("setArtifactFailed applied=%v err=%v", applied, err)
	}
	failed := domainRows(t, conn, "recording.failed")
	if len(failed) != 1 || failed[0].aggregateType != "artifacts" || failed[0].aggregateID != hash || failed[0].tenantID != domainTenant {
		t.Fatalf("recording.failed rows = %+v", failed)
	}
	_, msg, err := events.Decode("recording.failed", failed[0].payload)
	if err != nil {
		t.Fatal(err)
	}
	art := msg.(*publicv1.RecordingFailed).GetArtifact()
	if art.GetArtifactId() != hash || art.GetKind() != publicv1.ArtifactKind_ARTIFACT_KIND_RECORDING || art.GetStreamId() != domainStream {
		t.Fatalf("recording.failed artifact = %+v", art)
	}
	var legacyID, legacyKind string
	if err := conn.QueryRow(`SELECT id::text, event_kind FROM foghorn.artifact_event_outbox WHERE artifact_id = $1`, hash).Scan(&legacyID, &legacyKind); err != nil {
		t.Fatal(err)
	}
	if legacyKind != "dvr_lifecycle" || legacyID != failed[0].eventID {
		t.Fatalf("legacy row %s/%s must carry the domain event id %s", legacyKind, legacyID, failed[0].eventID)
	}
	// A repeated terminal write matches no 'finalizing' row and emits nothing.
	if applied, err := setArtifactFailed(ctx, hash, "writer crashed", nil, ended, domainTenant, false); err != nil || applied {
		t.Fatalf("repeat applied=%v err=%v", applied, err)
	}
	if n := len(domainRows(t, conn, "recording.failed")); n != 1 {
		t.Fatalf("a repeated terminal write emitted again: %d", n)
	}
}

// A requested deletion records exactly one artifact_deleted, in the deletion's
// transaction and attributed to the requester; a repeated delete records none.
// Commodore records no copy (TestArtifactDeletionNamesRequesterAndRecordsNoEvent),
// so this row is the only artifact_deleted for the deletion.
func TestArtifactDeletedRecordedOncePerDeletion_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()
	const hash = "dvrdeleteonce0000000000000000001"
	insertFinalizingDVR(t, conn, hash)

	deletedRows := func() []string {
		t.Helper()
		rows, err := conn.Query(`
			SELECT payload->>'userId' FROM foghorn.artifact_event_outbox
			WHERE event_kind = 'artifact_deleted' AND artifact_id = $1`, hash)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var users []string
		for rows.Next() {
			var user sql.NullString
			if err := rows.Scan(&user); err != nil {
				t.Fatal(err)
			}
			users = append(users, user.String)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return users
	}

	// The event write fails: the deletion rolls back with it.
	if _, err := conn.Exec(`ALTER TABLE foghorn.artifact_event_outbox RENAME TO artifact_event_outbox_offline`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := SoftDeleteDVRAndChapters(ctx, hash, domainTenant, "user-7"); err == nil {
		t.Fatal("the deletion must fail when artifact_deleted cannot be written")
	}
	if _, err := conn.Exec(`ALTER TABLE foghorn.artifact_event_outbox_offline RENAME TO artifact_event_outbox`); err != nil {
		t.Fatal(err)
	}
	if s := artifactStatus(t, conn, hash); s != "finalizing" {
		t.Fatalf("a failed event write must roll the deletion back, status=%q", s)
	}

	_, transitioned, err := SoftDeleteDVRAndChapters(ctx, hash, domainTenant, "user-7")
	if err != nil || !transitioned {
		t.Fatalf("delete transitioned=%v err=%v", transitioned, err)
	}
	if users := deletedRows(); len(users) != 1 || users[0] != "user-7" {
		t.Fatalf("one deletion must record exactly one artifact_deleted by user-7, got %q", users)
	}
	if _, transitioned, err = SoftDeleteDVRAndChapters(ctx, hash, domainTenant, "user-7"); err != nil || transitioned {
		t.Fatalf("repeat delete transitioned=%v err=%v", transitioned, err)
	}
	if users := deletedRows(); len(users) != 1 {
		t.Fatalf("a repeated delete recorded another artifact_deleted: %q", users)
	}
}

// relayPublisher records every published event ID. While loseAck is set it
// returns an error after recording, as when Decklog produced the batch but its
// acknowledgement was lost. rejectID makes a batch containing that event fail.
type relayPublisher struct {
	mu       sync.Mutex
	ids      []string
	types    []string
	loseAck  bool
	rejectID string
}

func (p *relayPublisher) PublishDomainEvents(_ context.Context, batch *eventspb.DomainEventBatch) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, ev := range batch.GetEvents() {
		if ev.GetId() == p.rejectID {
			return errors.New("decklog rejected the event")
		}
	}
	for _, ev := range batch.GetEvents() {
		if _, _, err := events.Validate(ev); err != nil {
			return err
		}
		p.ids = append(p.ids, ev.GetId())
		p.types = append(p.types, ev.GetType())
	}
	if p.loseAck {
		return errors.New("acknowledgement lost")
	}
	return nil
}

func makeDue(t *testing.T, conn *sql.DB) {
	t.Helper()
	if _, err := conn.Exec(`UPDATE foghorn.domain_event_outbox SET next_attempt_at = now() WHERE completed_at IS NULL`); err != nil {
		t.Fatal(err)
	}
}

// Foghorn's relay redelivers an event under the ID stored at enqueue after a
// lost acknowledgement, and holds a stream's later event until its earlier one
// is delivered.
func TestFoghornDomainEventRelay_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()
	const stream = "live+relay"

	mintStreamSession(t, "node-owner", stream, 301, "relay-1", 1000)
	if _, err := MarkIngestSessionPlayable(ctx, domainTenant, "node-owner", stream, 2000); err != nil {
		t.Fatal(err)
	}
	connected := domainRows(t, conn, "stream.connected")[0].eventID
	live := domainRows(t, conn, "stream.live")[0].eventID

	pub := &relayPublisher{loseAck: true, rejectID: connected}
	relay, err := eventsoutbox.NewRelay(conn, domainevents.Schema, pub, logging.NewLogger())
	if err != nil {
		t.Fatal(err)
	}
	// The stream's first event is rejected; its later event is not even claimed.
	if _, err := relay.DispatchOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(pub.ids) != 0 {
		t.Fatalf("stream.live must wait for stream.connected, published %v", pub.ids)
	}

	// Decklog produces the head but the acknowledgement is lost; the redispatch carries the same ID.
	pub.rejectID = ""
	makeDue(t, conn)
	if _, err := relay.DispatchOnce(ctx); err != nil {
		t.Fatal(err)
	}
	pub.loseAck = false
	makeDue(t, conn)
	if _, err := relay.DispatchOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(pub.ids) != 2 || pub.ids[0] != connected || pub.ids[1] != connected {
		t.Fatalf("redispatch after a lost ack must repeat the stored id %s, published %v", connected, pub.ids)
	}
	if _, err := relay.DispatchOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(pub.ids) != 3 || pub.ids[2] != live {
		t.Fatalf("stream.live must follow its stream's connected event, published %v", pub.ids)
	}
	var pending int
	if err := conn.QueryRow(`SELECT count(*) FROM foghorn.domain_event_outbox WHERE completed_at IS NULL`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("%d events left undelivered", pending)
	}
	// Foghorn emits only tenant-scoped types, and every row carries its tenant.
	var platform int
	if err := conn.QueryRow(`SELECT count(*) FROM foghorn.domain_event_outbox WHERE scope <> 'tenant' OR tenant_id IS NULL`).Scan(&platform); err != nil {
		t.Fatal(err)
	}
	if platform != 0 {
		t.Fatalf("%d Foghorn events stored without a tenant", platform)
	}
}
