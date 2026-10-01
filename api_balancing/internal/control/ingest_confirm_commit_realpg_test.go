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
)

// lostCommitAckConnector hands out connections whose transaction COMMIT reaches the server but
// reports failure to the caller when the transaction confirmed a source projection: the outcome a
// deadline expiring while COMMIT is in flight produces. onLostAck runs before the failure returns.
type lostCommitAckConnector struct {
	dsn       string
	driver    driver.Driver
	armed     atomic.Bool
	onLostAck func()
}

func (c *lostCommitAckConnector) Connect(context.Context) (driver.Conn, error) {
	inner, err := c.driver.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &lostCommitAckConn{inner: inner, connector: c}, nil
}

func (c *lostCommitAckConnector) Driver() driver.Driver { return c.driver }

type lostCommitAckConn struct {
	inner       driver.Conn
	connector   *lostCommitAckConnector
	sawConfirm  bool
	inLostAckTx bool
}

func (c *lostCommitAckConn) Prepare(query string) (driver.Stmt, error) { return c.inner.Prepare(query) }
func (c *lostCommitAckConn) Close() error                              { return c.inner.Close() }
func (c *lostCommitAckConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *lostCommitAckConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	beginner, ok := c.inner.(driver.ConnBeginTx)
	if !ok {
		return nil, errors.New("wrapped driver lacks BeginTx")
	}
	tx, err := beginner.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	c.sawConfirm = false
	return &lostCommitAckTx{inner: tx, conn: c}, nil
}

func (c *lostCommitAckConn) observe(query string) {
	if strings.Contains(query, "name: ConfirmSourceProjection") {
		c.sawConfirm = true
	}
}

func (c *lostCommitAckConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.observe(query)
	execer, ok := c.inner.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return execer.ExecContext(ctx, query, args)
}

func (c *lostCommitAckConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.observe(query)
	queryer, ok := c.inner.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return queryer.QueryContext(ctx, query, args)
}

type lostCommitAckTx struct {
	inner driver.Tx
	conn  *lostCommitAckConn
}

func (t *lostCommitAckTx) Commit() error {
	if err := t.inner.Commit(); err != nil {
		return err
	}
	if t.conn.sawConfirm && t.conn.connector.armed.CompareAndSwap(true, false) {
		if t.conn.connector.onLostAck != nil {
			t.conn.connector.onLostAck()
		}
		return errors.New("timeout: context deadline exceeded")
	}
	return nil
}

func (t *lostCommitAckTx) Rollback() error { return t.inner.Rollback() }

// A source-projection confirmation whose COMMIT lands while the admission's deadline expires is
// reported to the admission as failed, and the admission denies the push. The generation must not
// stay active: an active session with no publisher keeps its placement claim and refuses every later
// publisher of the stream as a duplicate. The cleanup also cannot use the admission's expired context.
func TestProjectSourceIfCurrentEndsConfirmationWhoseCommitAckWasLost_RealPG(t *testing.T) {
	plain, dsn := startRealPGWithDSN(t)
	connector := &lostCommitAckConnector{dsn: dsn, driver: plain.Driver()}
	conn := sql.OpenDB(connector)
	t.Cleanup(func() { _ = conn.Close() })
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })

	const node, stream, claimToken, cluster = "node-lost-ack", "lost-ack-stream", "lost-ack-trigger", "cluster-A"
	logger := logging.NewLogger()
	sessionID, outcome, err := CreateIngestSession(context.Background(), ingA, node, stream, 701, claimToken, time.Now().UnixMilli(), nil, cluster, logger)
	if err != nil || outcome != IngestSessionActive {
		t.Fatalf("mint: outcome=%v err=%v", outcome, err)
	}

	admissionCtx, expire := context.WithCancel(context.Background())
	defer expire()
	connector.onLostAck = expire
	connector.armed.Store(true)
	registry := NewStreamRegistry(nil, cluster, time.Minute)
	applied, resumed, err := ProjectSourceIfCurrent(admissionCtx, registry, ingA, node, stream, 701, claimToken, sessionID, AdmissionEffectIntent{BroadcastLive: true})
	if connector.armed.Load() {
		t.Fatal("the confirmation commit was never reached")
	}
	if err == nil || applied || resumed {
		t.Fatalf("lost confirmation ack: applied=%v resumed=%v err=%v, want a denial error", applied, resumed, err)
	}

	ctx := context.Background()
	if ended, reason := sessionEnded(t, sessionID); !ended || reason != "projection_failed" {
		t.Fatalf("session after a lost confirmation ack: ended=%v reason=%q, want ended projection_failed", ended, reason)
	}
	ownerEnded, err := IngestClaimOwnerEnded(ctx, ingA, stream, cluster, claimToken)
	if err != nil || !ownerEnded {
		t.Fatalf("claim owner ended=%v err=%v: a later publisher could not release the denied admission's claim", ownerEnded, err)
	}
	claims, err := activeIngestSessionClaims(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, claim := range claims {
		if claim.InternalName == stream {
			t.Fatalf("placement renewal still re-asserts the denied admission's claim: %+v", claim)
		}
	}
	var broadcastOffline bool
	if err := conn.QueryRowContext(ctx, `
		SELECT broadcast_offline FROM foghorn.ingest_offline_effects
		 WHERE tenant_id=$1::uuid AND stream_internal_name=$2 AND source_generation=$3::uuid AND state='pending'
	`, ingA, stream, sessionID).Scan(&broadcastOffline); err != nil {
		t.Fatalf("read the ended generation's offline effect: %v", err)
	}
	if !broadcastOffline {
		t.Fatal("ending a confirmed generation must broadcast it offline: its admission obligation may have announced it live")
	}
}
