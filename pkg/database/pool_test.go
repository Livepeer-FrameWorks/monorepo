package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
)

// burstDriver counts opened connections and holds every query until the
// whole burst is in flight, so a burst occupies one connection per caller.
type burstDriver struct {
	opens   atomic.Int64
	barrier *sync.WaitGroup
}

func (d *burstDriver) Open(string) (driver.Conn, error) {
	d.opens.Add(1)
	return &burstConn{driver: d}, nil
}

type burstConn struct{ driver *burstDriver }

func (c *burstConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not supported") }
func (c *burstConn) Close() error                        { return nil }
func (c *burstConn) Begin() (driver.Tx, error)           { return nil, errors.New("not supported") }

func (c *burstConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	c.driver.barrier.Done()
	c.driver.barrier.Wait()
	return &burstRows{}, nil
}

type burstRows struct{ done bool }

func (r *burstRows) Columns() []string { return []string{"v"} }
func (r *burstRows) Close() error      { return nil }
func (r *burstRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	dest[0] = int64(1)
	return nil
}

type burstConnector struct{ driver *burstDriver }

func (c burstConnector) Connect(context.Context) (driver.Conn, error) { return c.driver.Open("") }
func (c burstConnector) Driver() driver.Driver                        { return c.driver }

// A new YSQL connection pays for a backend start and a cold catalog cache: its
// first query on a table costs far more than the same query on a warm
// connection. A pool that closes the connections a burst opened makes the next
// burst of the same size pay that again for every caller.
func TestDefaultPoolKeepsConnectionsABurstOpened(t *testing.T) {
	cfg := DefaultConfig()
	burst := cfg.MaxOpenConns
	drv := &burstDriver{}
	db := sql.OpenDB(burstConnector{driver: drv})
	t.Cleanup(func() { _ = db.Close() })
	configurePool(db, cfg)

	runBurst := func() {
		var barrier sync.WaitGroup
		barrier.Add(burst)
		drv.barrier = &barrier
		var callers sync.WaitGroup
		for range burst {
			callers.Add(1)
			go func() {
				defer callers.Done()
				var v int
				if err := db.QueryRowContext(context.Background(), "SELECT 1").Scan(&v); err != nil {
					t.Errorf("query: %v", err)
				}
			}()
		}
		callers.Wait()
	}

	runBurst()
	opened := drv.opens.Load()
	if opened != int64(burst) {
		t.Fatalf("first burst of %d opened %d connections", burst, opened)
	}
	runBurst()
	if reopened := drv.opens.Load() - opened; reopened != 0 {
		t.Fatalf("second burst of %d opened %d new connections; the pool closed connections the first burst left idle (MaxIdleClosed=%d)",
			burst, reopened, db.Stats().MaxIdleClosed)
	}
}
