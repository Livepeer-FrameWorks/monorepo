package periscopeingestdb

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
)

type testRow struct {
	TenantID  uuid.UUID
	Timestamp time.Time
	N         int
}

const (
	insertTestRows       = "INSERT INTO periscope.test_rows (n)"
	insertTestState      = "INSERT INTO test_state (n)"
	insertIdentifiedRows = "INSERT INTO periscope.identified_rows (n)"
)

func init() {
	identifiedStatements[insertIdentifiedRows] = byTenantAndTimestamp
}

// recordOrigin is a row origin naming its source record.
type recordOrigin struct{ identity string }

func (o *recordOrigin) IngestIdentity() string { return o.identity }

type fakeInsert struct {
	table string
	rows  []int
	ids   []uuid.UUID
}

type fakeBufferConn struct {
	inserts   []fakeInsert
	prepares  int
	appendErr func(n int) error
	sendErr   func(table string, rows []int) error
	stored    map[uuid.UUID]bool
	queries   int
	queryErr  error
}

func (c *fakeBufferConn) PrepareBatch(_ context.Context, query string) (Batch, error) {
	c.prepares++
	return &fakeBufferBatch{conn: c, table: insertTable(query)}, nil
}

func (c *fakeBufferConn) Query(_ context.Context, _ string, args ...interface{}) (Rows, error) {
	c.queries++
	if c.queryErr != nil {
		return nil, c.queryErr
	}
	var found []uuid.UUID
	for _, v := range args {
		if id, ok := v.(uuid.UUID); ok && c.stored[id] {
			found = append(found, id)
		}
	}
	return &fakeIDRows{ids: found, index: -1}, nil
}

type fakeIDRows struct {
	ids   []uuid.UUID
	index int
}

func (r *fakeIDRows) Next() bool { r.index++; return r.index < len(r.ids) }
func (r *fakeIDRows) Close() error {
	return nil
}
func (r *fakeIDRows) Err() error { return nil }
func (r *fakeIDRows) Scan(dest ...interface{}) error {
	*dest[0].(*uuid.UUID) = r.ids[r.index]
	return nil
}

type fakeBufferBatch struct {
	conn  *fakeBufferConn
	table string
	rows  []int
	ids   []uuid.UUID
}

func (b *fakeBufferBatch) Append(v ...interface{}) error {
	n := v[0].(int)
	if b.conn.appendErr != nil {
		if err := b.conn.appendErr(n); err != nil {
			return err
		}
	}
	b.rows = append(b.rows, n)
	if id, ok := v[len(v)-1].(uuid.UUID); ok {
		b.ids = append(b.ids, id)
	}
	return nil
}

func (b *fakeBufferBatch) Send() error {
	if b.conn.sendErr != nil {
		if err := b.conn.sendErr(b.table, b.rows); err != nil {
			return err
		}
	}
	b.conn.inserts = append(b.conn.inserts, fakeInsert{table: b.table, rows: b.rows, ids: b.ids})
	return nil
}

var errTransient = errors.New("connection refused")

func testBuffer(conn *fakeBufferConn) *WriteBuffer {
	return NewWriteBuffer(conn, WriteBufferOptions{IsRetryable: func(err error) bool { return errors.Is(err, errTransient) }})
}

func writeRows(t *testing.T, ctx context.Context, query string, origin any, rows ...testRow) {
	t.Helper()
	w, err := prepare(WithRowOrigin(ctx, origin), nil, query, func(r testRow) []interface{} { return []interface{}{r.N} })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	for _, r := range rows {
		if err := w.Append(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Send(); err != nil {
		t.Fatal(err)
	}
}

func row(n int) testRow {
	return testRow{TenantID: uuid.MustParse("00000000-0000-0000-0000-000000000001"), Timestamp: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), N: n}
}

func TestWriteBufferInsertsEachStatementOnceInSendOrder(t *testing.T) {
	conn := &fakeBufferConn{}
	buffer := testBuffer(conn)
	ctx := WithWriteBuffer(context.Background(), buffer)
	for i := 0; i < 50; i++ {
		writeRows(t, ctx, insertTestRows, i, row(i))
		if i%10 == 0 {
			writeRows(t, ctx, insertTestState, i, row(1000+i))
		}
	}
	// A writer closed before Send leaves nothing behind.
	unsent, err := prepare(ctx, nil, insertTestRows, func(r testRow) []interface{} { return []interface{}{r.N} })
	if err != nil {
		t.Fatal(err)
	}
	_ = unsent.Append(row(-1))
	_ = unsent.Close()
	if len(conn.inserts) != 0 {
		t.Fatal("rows were inserted before Flush")
	}

	if err := buffer.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(conn.inserts) != 2 || conn.inserts[0].table != "test_rows" || conn.inserts[1].table != "test_state" {
		t.Fatalf("inserts = %+v, want one per statement in first-written order", conn.inserts)
	}
	for i, n := range conn.inserts[0].rows {
		if n != i {
			t.Fatalf("test_rows order = %v, want 0..49", conn.inserts[0].rows)
		}
	}
	if len(conn.inserts[1].rows) != 5 || buffer.Pending() != 0 {
		t.Fatalf("test_state rows = %v, pending = %d", conn.inserts[1].rows, buffer.Pending())
	}
}

func TestWriteBufferFailsOnlyTheRowsClickHouseRejects(t *testing.T) {
	conn := &fakeBufferConn{
		appendErr: func(n int) error {
			if n == 3 {
				return errors.New("clickhouse: input value with length 3 exceeds FixedString(2) capacity")
			}
			return nil
		},
		sendErr: func(_ string, rows []int) error {
			for _, n := range rows {
				if n == 7 {
					return errors.New("code: 53, message: Type mismatch")
				}
			}
			return nil
		},
	}
	buffer := testBuffer(conn)
	ctx := WithWriteBuffer(context.Background(), buffer)
	for i := 0; i < 10; i++ {
		writeRows(t, ctx, insertTestRows, i, row(i))
	}
	if err := buffer.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	failures := buffer.TakeFailures()
	if len(failures) != 2 || failures[0].Origin != 3 || failures[1].Origin != 7 || failures[0].Table != "test_rows" {
		t.Fatalf("failures = %+v, want origins 3 and 7", failures)
	}
	inserted := map[int]bool{}
	for _, ins := range conn.inserts {
		for _, n := range ins.rows {
			if inserted[n] {
				t.Fatalf("row %d inserted twice", n)
			}
			inserted[n] = true
		}
	}
	for i := 0; i < 10; i++ {
		if want := i != 3 && i != 7; inserted[i] != want {
			t.Fatalf("row %d inserted = %v, want %v (inserts %+v)", i, inserted[i], want, conn.inserts)
		}
	}
}

func TestWriteBufferKeepsRowsAcrossTransientFailures(t *testing.T) {
	down := true
	conn := &fakeBufferConn{sendErr: func(table string, _ []int) error {
		if down && table == "test_state" {
			return fmt.Errorf("send: %w", errTransient)
		}
		return nil
	}}
	buffer := testBuffer(conn)
	ctx := WithWriteBuffer(context.Background(), buffer)
	writeRows(t, ctx, insertTestRows, 1, row(1))
	writeRows(t, ctx, insertTestState, 2, row(2))

	if err := buffer.Flush(context.Background()); !errors.Is(err, errTransient) {
		t.Fatalf("Flush() = %v, want the transient error", err)
	}
	if buffer.Pending() != 1 || len(buffer.TakeFailures()) != 0 {
		t.Fatalf("pending = %d after a transient failure, want only the failed statement", buffer.Pending())
	}
	down = false
	if err := buffer.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(conn.inserts) != 2 || conn.inserts[0].table != "test_rows" || conn.inserts[1].table != "test_state" {
		t.Fatalf("inserts = %+v, want test_rows once and test_state once", conn.inserts)
	}
}

func TestWriteBufferSplitsInsertsAtClickHousePartitionLimit(t *testing.T) {
	conn := &fakeBufferConn{}
	buffer := testBuffer(conn)
	ctx := WithWriteBuffer(context.Background(), buffer)
	const tenants = 250
	for i := 0; i < tenants*2; i++ {
		r := row(i)
		r.TenantID = uuid.NewSHA1(uuid.Nil, []byte(fmt.Sprint(i%tenants)))
		writeRows(t, ctx, insertTestRows, i, r)
	}
	if err := buffer.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(conn.inserts) != 3 {
		t.Fatalf("inserts = %d, want 3 for %d tenant partitions", len(conn.inserts), tenants)
	}
	for _, ins := range conn.inserts {
		seen := map[int]bool{}
		last := -1
		for _, n := range ins.rows {
			seen[n%tenants] = true
			if n%tenants == ins.rows[0]%tenants && n <= last {
				t.Fatalf("a tenant's rows lost their order: %v", ins.rows)
			}
			if n%tenants == ins.rows[0]%tenants {
				last = n
			}
		}
		if len(seen) > maxPartitionsPerInsert {
			t.Fatalf("one INSERT spans %d partitions", len(seen))
		}
	}
}

func insertedIDs(conn *fakeBufferConn) map[uuid.UUID]bool {
	ids := map[uuid.UUID]bool{}
	for _, ins := range conn.inserts {
		for _, id := range ins.ids {
			ids[id] = true
		}
	}
	return ids
}

// A record delivered again, in a batch cut differently from the first and
// next to a mirrored copy of another record, adds no row: every identified row
// carries an ingest_id derived from its record and position, and the flush
// drops ids the table holds or the batch repeats.
func TestWriteBufferDropsRowsOfRedeliveredRecords(t *testing.T) {
	conn := &fakeBufferConn{stored: map[uuid.UUID]bool{}}
	first := testBuffer(conn)
	ctx := WithWriteBuffer(context.Background(), first)
	writeRows(t, ctx, insertIdentifiedRows, &recordOrigin{"rec-1"}, row(1), row(2))
	writeRows(t, ctx, insertTestState, &recordOrigin{"rec-1"}, row(3))
	if err := first.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored := insertedIDs(conn)
	if len(stored) != 2 {
		t.Fatalf("stored ids = %d, want one per identified row", len(stored))
	}
	conn.stored = stored
	conn.inserts = nil

	second := testBuffer(conn)
	ctx = WithWriteBuffer(context.Background(), second)
	writeRows(t, ctx, insertIdentifiedRows, &recordOrigin{"rec-1"}, row(1), row(2))
	writeRows(t, ctx, insertTestState, &recordOrigin{"rec-1"}, row(3))
	writeRows(t, ctx, insertIdentifiedRows, &recordOrigin{"rec-2"}, row(4))
	writeRows(t, ctx, insertIdentifiedRows, &recordOrigin{"rec-2"}, row(4))
	if err := second.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := map[string][]int{}
	for _, ins := range conn.inserts {
		got[ins.table] = append(got[ins.table], ins.rows...)
	}
	// The state table replaces by key and stores no ingest_id, so its row is
	// written again; the identified table gains only rec-2's row.
	if fmt.Sprint(got["identified_rows"]) != "[4]" || fmt.Sprint(got["test_state"]) != "[3]" {
		t.Fatalf("inserted %v, want identified [4] and state [3]", got)
	}
	if conn.queries != 2 {
		t.Fatalf("duplicate lookups = %d, want one per flush of the identified table", conn.queries)
	}
}

func TestWriteBufferDiscardOriginRestartsItsIngestIDs(t *testing.T) {
	conn := &fakeBufferConn{stored: map[uuid.UUID]bool{}}
	buffer := testBuffer(conn)
	ctx := WithWriteBuffer(context.Background(), buffer)
	retried := &recordOrigin{"retried"}
	writeRows(t, ctx, insertIdentifiedRows, retried, row(1))
	writeRows(t, ctx, insertIdentifiedRows, &recordOrigin{"other"}, row(2))
	buffer.DiscardOrigin(retried)
	if !buffer.HasPending("identified_rows", func(r any) bool { return r.(testRow).N == 2 }) ||
		buffer.HasPending("identified_rows", func(r any) bool { return r.(testRow).N == 1 }) {
		t.Fatal("DiscardOrigin removed the wrong rows")
	}
	writeRows(t, ctx, insertIdentifiedRows, retried, row(1))
	if err := buffer.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !insertedIDs(conn)[IngestID("retried", "identified_rows", 0)] {
		t.Fatal("the retried record's first row did not get the first ingest_id again")
	}
}
