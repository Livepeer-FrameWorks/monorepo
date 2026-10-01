//go:build schema_verify

package control

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/lib/pq"
)

// An admission whose source projection Foghorn confirmed after Helmsman stopped waiting leaves an
// active session that Mist refused: nothing closes it, and it refuses every reconnect on the node as
// a duplicate. Helmsman's abandonment report must end it and free the stream for the reconnect.
func TestAbandonIngestAdmission_EndsSessionConfirmedAfterHelmsmanGaveUp_RealPG(t *testing.T) {
	useRealPG(t)
	ctx := context.Background()
	logger := logging.NewLogger()
	const node, stream, execution, cluster = "node-abandon-active", "live+abandon-active", "abandon-active-trigger", "cluster-A"

	sessionID, outcome, err := CreateIngestSession(ctx, ingA, node, stream, 801, execution, time.Now().UnixMilli(), nil, cluster, logger)
	if err != nil || outcome != IngestSessionActive {
		t.Fatalf("mint: outcome=%v err=%v", outcome, err)
	}
	registry := NewStreamRegistry(nil, cluster, time.Minute)
	applied, resumed, err := ProjectSourceIfCurrent(ctx, registry, ingA, node, stream, 801, execution, sessionID, AdmissionEffectIntent{BroadcastLive: true})
	if err != nil || !applied || resumed {
		t.Fatalf("projection: applied=%v resumed=%v err=%v", applied, resumed, err)
	}

	// The defect: the confirmed session has no publisher and holds the stream against the reconnect.
	_, outcome, err = CreateIngestSession(ctx, ingA, node, stream, 802, "abandon-active-reconnect", time.Now().UnixMilli(), nil, cluster, logger)
	if err != nil || outcome != IngestSessionRejectedDuplicate {
		t.Fatalf("reconnect before the report: outcome=%v err=%v, want the duplicate refusal the report must clear", outcome, err)
	}

	ended, err := AbandonIngestAdmission(ctx, node, execution, 801, logger)
	if err != nil || ended != 1 {
		t.Fatalf("abandon: ended=%d err=%v, want 1", ended, err)
	}
	if isEnded, reason := sessionEnded(t, sessionID); !isEnded || reason != IngestEndedAdmissionAbandoned {
		t.Fatalf("abandoned session: ended=%v reason=%q, want %s", isEnded, reason, IngestEndedAdmissionAbandoned)
	}
	var broadcastOffline bool
	if err := db.QueryRowContext(ctx, `
		SELECT broadcast_offline FROM foghorn.ingest_offline_effects
		 WHERE tenant_id=$1::uuid AND stream_internal_name=$2 AND source_generation=$3::uuid AND state='pending'
	`, ingA, stream, sessionID).Scan(&broadcastOffline); err != nil {
		t.Fatalf("read the abandoned generation's offline effect: %v", err)
	}
	if !broadcastOffline {
		t.Fatal("the abandoned generation's admission obligation announced it live; its end must broadcast it offline")
	}
	claims, err := activeIngestSessionClaims(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, claim := range claims {
		if claim.ClaimToken == execution {
			t.Fatalf("placement renewal still re-asserts the abandoned admission's claim: %+v", claim)
		}
	}

	reconnect, outcome, err := CreateIngestSession(ctx, ingA, node, stream, 803, "abandon-active-reconnect-2", time.Now().UnixMilli(), nil, cluster, logger)
	if err != nil || outcome != IngestSessionActive || reconnect == "" {
		t.Fatalf("reconnect after the report: id=%q outcome=%v err=%v, want a new active session", reconnect, outcome, err)
	}

	again, err := AbandonIngestAdmission(ctx, node, execution, 801, logger)
	if err != nil || again != 0 {
		t.Fatalf("repeated report: ended=%d err=%v, want a no-op", again, err)
	}
	if isEnded, _ := sessionEnded(t, reconnect); isEnded {
		t.Fatal("a repeated report of the abandoned execution ended the reconnect's session")
	}
}

// The report can reach Foghorn before the admission it abandons has minted, when the admission's
// processing started late. The mint must then refuse the execution instead of opening a session no
// later report would end.
func TestAbandonIngestAdmission_RefusesLaterMintOfTheExecution_RealPG(t *testing.T) {
	useRealPG(t)
	ctx := context.Background()
	logger := logging.NewLogger()
	const node, stream, execution, cluster = "node-abandon-early", "live+abandon-early", "abandon-early-trigger", "cluster-A"

	ended, err := AbandonIngestAdmission(ctx, node, execution, 811, logger)
	if err != nil || ended != 0 {
		t.Fatalf("abandon before mint: ended=%d err=%v", ended, err)
	}
	id, outcome, err := CreateIngestSession(ctx, ingA, node, stream, 811, execution, time.Now().UnixMilli(), nil, cluster, logger)
	if err != nil || outcome != IngestSessionAlreadyEnded || id != "" {
		t.Fatalf("mint of the abandoned execution: id=%q outcome=%v err=%v, want already ended with no session", id, outcome, err)
	}
	var open int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM foghorn.ingest_sessions WHERE node_id=$1 AND ended_at IS NULL`, node).Scan(&open); err != nil {
		t.Fatal(err)
	}
	if open != 0 {
		t.Fatalf("%d open sessions after the abandoned execution's mint, want 0", open)
	}

	// The record is scoped to the node: another node's execution with the same UUID is admitted.
	if _, outcome, err := CreateIngestSession(ctx, ingA, "node-abandon-other", stream, 812, execution, time.Now().UnixMilli(), nil, cluster, logger); err != nil || outcome != IngestSessionActive {
		t.Fatalf("same UUID on another node: outcome=%v err=%v, want active", outcome, err)
	}

	// The reaper's tombstone purge retires the record with the close tombstones.
	if _, err := PurgeExpiredCloseTombstones(ctx, 0); err != nil {
		t.Fatalf("purge: %v", err)
	}
	var records int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM foghorn.ingest_admission_abandonments WHERE node_id=$1`, node).Scan(&records); err != nil {
		t.Fatal(err)
	}
	if records != 0 {
		t.Fatalf("%d abandonment records survived the purge", records)
	}
}

// An admission still between mint and confirmation when the report lands loses its projection: the
// session is ended, so the confirmation finds nothing current and the push is denied.
func TestAbandonIngestAdmission_EndsPendingAdmissionBeforeConfirmation_RealPG(t *testing.T) {
	useRealPG(t)
	ctx := context.Background()
	logger := logging.NewLogger()
	const node, stream, execution, cluster = "node-abandon-pending", "live+abandon-pending", "abandon-pending-trigger", "cluster-A"

	sessionID, outcome, err := CreateIngestSession(ctx, ingA, node, stream, 821, execution, time.Now().UnixMilli(), nil, cluster, logger)
	if err != nil || outcome != IngestSessionActive {
		t.Fatalf("mint: outcome=%v err=%v", outcome, err)
	}
	if ended, err := AbandonIngestAdmission(ctx, node, execution, 821, logger); err != nil || ended != 1 {
		t.Fatalf("abandon: ended=%d err=%v, want 1", ended, err)
	}
	registry := NewStreamRegistry(nil, cluster, time.Minute)
	applied, resumed, err := ProjectSourceIfCurrent(ctx, registry, ingA, node, stream, 821, execution, sessionID, AdmissionEffectIntent{BroadcastLive: true})
	if err != nil || applied || resumed {
		t.Fatalf("projection after the report: applied=%v resumed=%v err=%v, want a silent denial", applied, resumed, err)
	}
	if active, _, ok := registry.PushSourceSession(stream); ok && active {
		t.Fatal("the abandoned admission was projected as the stream's source")
	}
}

// serializationFailureConnector hands out connections whose first transaction that advanced a
// resumed generation's source revision is rolled back at COMMIT and reported as a YugabyteDB
// serialization failure, after the registry CAS of that attempt already succeeded.
type serializationFailureConnector struct {
	dsn      string
	driver   driver.Driver
	armed    atomic.Bool
	injected atomic.Int32
}

func (c *serializationFailureConnector) Connect(context.Context) (driver.Conn, error) {
	inner, err := c.driver.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &serializationFailureConn{inner: inner, connector: c}, nil
}

func (c *serializationFailureConnector) Driver() driver.Driver { return c.driver }

type serializationFailureConn struct {
	inner       driver.Conn
	connector   *serializationFailureConnector
	sawAdvance  bool
	activeInner driver.Tx
}

func (c *serializationFailureConn) Prepare(query string) (driver.Stmt, error) {
	return c.inner.Prepare(query)
}
func (c *serializationFailureConn) Close() error { return c.inner.Close() }
func (c *serializationFailureConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *serializationFailureConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	beginner, ok := c.inner.(driver.ConnBeginTx)
	if !ok {
		return nil, errors.New("wrapped driver lacks BeginTx")
	}
	tx, err := beginner.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	c.sawAdvance = false
	return &serializationFailureTx{inner: tx, conn: c}, nil
}

func (c *serializationFailureConn) observe(query string) {
	if strings.Contains(query, "name: AdvanceActiveSourceProjectionRevision") {
		c.sawAdvance = true
	}
}

func (c *serializationFailureConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.observe(query)
	execer, ok := c.inner.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return execer.ExecContext(ctx, query, args)
}

func (c *serializationFailureConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.observe(query)
	queryer, ok := c.inner.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return queryer.QueryContext(ctx, query, args)
}

type serializationFailureTx struct {
	inner driver.Tx
	conn  *serializationFailureConn
}

func (t *serializationFailureTx) Commit() error {
	if t.conn.sawAdvance && t.conn.connector.armed.CompareAndSwap(true, false) {
		if err := t.inner.Rollback(); err != nil {
			return err
		}
		t.conn.connector.injected.Add(1)
		return &pq.Error{Code: "40001", Message: "restart read required"}
	}
	return t.inner.Commit()
}

func (t *serializationFailureTx) Rollback() error { return t.inner.Rollback() }

// A resumed admission whose shared projection fell behind repairs it in a transaction that keeps the
// stream lock across the registry CAS. A serialization failure on that transaction, here at COMMIT
// after the CAS accepted the new revision, must replay the attempt rather than deny a publisher
// whose session is already active.
func TestRepairResumedSourceProjectionReplaysSerializationFailure_RealPG(t *testing.T) {
	plain, dsn := startRealPGWithDSN(t)
	connector := &serializationFailureConnector{dsn: dsn, driver: plain.Driver()}
	conn := sql.OpenDB(connector)
	t.Cleanup(func() { _ = conn.Close() })
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })

	ctx := context.Background()
	logger := logging.NewLogger()
	const node, stream, execution, cluster = "node-repair-retry", "live+repair-retry", "repair-retry-trigger", "cluster-A"
	sessionID, outcome, err := CreateIngestSession(ctx, ingA, node, stream, 831, execution, time.Now().UnixMilli(), nil, cluster, logger)
	if err != nil || outcome != IngestSessionActive {
		t.Fatalf("mint: outcome=%v err=%v", outcome, err)
	}
	registry := NewStreamRegistry(nil, cluster, time.Minute)
	if applied, _, err := ProjectSourceIfCurrent(ctx, registry, ingA, node, stream, 831, execution, sessionID, AdmissionEffectIntent{BroadcastLive: true}); err != nil || !applied {
		t.Fatalf("first projection: applied=%v err=%v", applied, err)
	}
	var confirmed int64
	if err := conn.QueryRowContext(ctx, `SELECT source_revision FROM foghorn.ingest_sessions WHERE id=$1::uuid`, sessionID).Scan(&confirmed); err != nil {
		t.Fatal(err)
	}
	// The shared watermark moves past the confirmed revision while the database generation stays
	// current: the cache divergence the repair resolves.
	if _, projected, err := registry.ProjectSource(stream, "node-divergent", 999, "divergent-trigger", "00000000-0000-0000-0000-0000000000aa", confirmed+1); err != nil || !projected {
		t.Fatalf("diverge shared watermark: projected=%v err=%v", projected, err)
	}

	connector.armed.Store(true)
	applied, resumed, err := ProjectSourceIfCurrent(ctx, registry, ingA, node, stream, 831, execution, sessionID, AdmissionEffectIntent{BroadcastLive: true})
	if connector.injected.Load() != 1 {
		t.Fatalf("serialization failures injected=%d, want 1: the repair transaction never reached COMMIT", connector.injected.Load())
	}
	if err != nil || !applied || !resumed {
		t.Fatalf("resumed repair after a serialization failure: applied=%v resumed=%v err=%v, want applied", applied, resumed, err)
	}
	var repaired int64
	if err := conn.QueryRowContext(ctx, `SELECT source_revision FROM foghorn.ingest_sessions WHERE id=$1::uuid`, sessionID).Scan(&repaired); err != nil {
		t.Fatal(err)
	}
	if repaired <= confirmed+1 {
		t.Fatalf("repaired revision %d does not exceed the divergent watermark %d", repaired, confirmed+1)
	}
	generation, active, ok := registry.SourceGenerationSnapshot(stream, node)
	if !ok || !active || generation != sessionID {
		t.Fatalf("shared projection after repair: generation=%q active=%v ok=%v, want %s active", generation, active, ok, sessionID)
	}
	if got := sourceRevisionForCluster(registryEntry(t, registry, stream), cluster); got != repaired {
		t.Fatalf("shared projection revision %d, want the committed %d", got, repaired)
	}
}

func registryEntry(t *testing.T, registry *StreamRegistry, stream string) StreamEntry {
	t.Helper()
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	entry, ok := registry.byInt[stream]
	if !ok {
		t.Fatalf("registry has no entry for %s", stream)
	}
	return entry.entry
}
