//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/lib/pq"
)

// lostResponseConnector opens connections whose ordinary delivery claim runs
// to completion on the server and then loses its response: the caller sees its
// context end, as Commodore does when the claim's timeout passes after the
// database has already done the work.
type lostResponseConnector struct {
	dsn     string
	leaseMs atomic.Int64
	lost    atomic.Int32
}

func (c *lostResponseConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := (&pq.Driver{}).Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &lostResponseConn{Conn: conn, connector: c}, nil
}

func (c *lostResponseConnector) Driver() driver.Driver { return &pq.Driver{} }

type lostResponseConn struct {
	driver.Conn
	connector *lostResponseConnector
}

func (c *lostResponseConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}

func (c *lostResponseConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	return c.Conn.(driver.ConnPrepareContext).PrepareContext(ctx, query)
}

func (c *lostResponseConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (c *lostResponseConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	queryer := c.Conn.(driver.QueryerContext)
	if !strings.Contains(query, "name: ClaimMediaAuthorityDeliveries ") {
		return queryer.QueryContext(ctx, query, args)
	}
	if len(args) > 0 {
		if leaseMs, ok := args[0].Value.(int64); ok {
			c.connector.leaseMs.Store(leaseMs)
		}
	}
	rows, err := queryer.QueryContext(context.WithoutCancel(ctx), query, args)
	if err != nil {
		return nil, err
	}
	dest := make([]driver.Value, len(rows.Columns()))
	for {
		if nextErr := rows.Next(dest); nextErr != nil {
			if !errors.Is(nextErr, io.EOF) {
				_ = rows.Close()
				return nil, nextErr
			}
			break
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	c.connector.lost.Add(1)
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestMediaAuthorityLostDeliveryClaim_RealPG drives the ordinary delivery
// worker through a claim whose response never arrives in time. The delivery
// must be claimable again at once, and a claim that did commit (its holder then
// stopped) must hold the delivery for no longer than about one attempt.
func TestMediaAuthorityLostDeliveryClaim_RealPG(t *testing.T) {
	db, dsn := startCommodoreRealPGWithDSN(t)
	testMediaAuthorityLostDeliveryClaim(t, db, dsn)
}

func TestMediaAuthorityLostDeliveryClaim_RealYugabyte(t *testing.T) {
	db, dsn, ok := dockerpg.OpenSharedYugabyteBaselineWithDSN(t, "lost_delivery_claim", "commodore")
	if !ok {
		t.Skip("requires make verify-placement-yugabyte-db")
	}
	testMediaAuthorityLostDeliveryClaim(t, db, dsn)
}

func testMediaAuthorityLostDeliveryClaim(t *testing.T, db *sql.DB, dsn string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	q := commodoredb.New(db)
	const tenantID, cellID = "lost-claim-tenant", "lost-claim-cell"
	now := time.Now().UTC()
	if err := q.InsertMediaAuthorityVersion(ctx, commodoredb.InsertMediaAuthorityVersionParams{
		AuthorityKind: "tenant", AuthorityID: tenantID, AuthorityVersion: 1, PayloadSchemaVersion: 1,
		Payload: []byte{}, PayloadSha256: make([]byte, 32), SourceRevisions: []byte("[]"),
		IssuedAt: now, RefreshAfter: now, ValidUntil: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if rows, err := q.UpsertCurrentMediaAuthority(ctx, commodoredb.UpsertCurrentMediaAuthorityParams{AuthorityKind: "tenant", AuthorityID: tenantID, AuthorityVersion: 1}); err != nil || rows != 1 {
		t.Fatalf("set current authority: rows=%d err=%v", rows, err)
	}
	if rows, err := q.EnqueueMediaAuthorityDelivery(ctx, commodoredb.EnqueueMediaAuthorityDeliveryParams{
		AuthorityKind: "tenant", AuthorityID: tenantID, AuthorityVersion: 1, CellID: cellID, SignedEnvelope: []byte{},
	}); err != nil || rows != 1 {
		t.Fatalf("enqueue delivery: rows=%d err=%v", rows, err)
	}

	connector := &lostResponseConnector{dsn: dsn}
	lossy := sql.OpenDB(connector)
	t.Cleanup(func() { _ = lossy.Close() })
	server := &CommodoreServer{db: lossy, logger: logging.NewLogger()}
	server.drainMediaAuthorityDeliveryCell(ctx, cellID)
	if connector.lost.Load() != 1 {
		t.Fatalf("lost claim responses = %d, want 1", connector.lost.Load())
	}

	if lease := time.Duration(connector.leaseMs.Load()) * time.Millisecond; lease <= 0 || lease > time.Minute {
		t.Errorf("delivery claim lease = %s; a claim whose holder stopped must free the delivery within a minute", lease)
	}
	var state string
	var leaseExpires sql.NullTime
	if err := db.QueryRowContext(ctx, `SELECT status, lease_expires_at FROM commodore.media_authority_deliveries
		WHERE authority_kind='tenant' AND authority_id=$1 AND authority_version=1 AND cell_id=$2`, tenantID, cellID).Scan(&state, &leaseExpires); err != nil {
		t.Fatal(err)
	}
	if state != "pending" || leaseExpires.Valid {
		t.Errorf("after a lost claim the delivery is status=%s lease_expires_at=%v, want pending with no lease", state, leaseExpires.Time)
	}
	claimed, err := q.ClaimMediaAuthorityDeliveries(ctx, commodoredb.ClaimMediaAuthorityDeliveriesParams{CellID: cellID, LeaseMs: 60000, BatchSize: 8})
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0].Attempts != 1 {
		t.Fatalf("delivery after a lost claim: claimed=%v, want it claimable at once as its first attempt", claimed)
	}
}
