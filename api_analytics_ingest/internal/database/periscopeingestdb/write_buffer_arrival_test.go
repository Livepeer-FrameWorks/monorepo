package periscopeingestdb

import (
	"context"
	"testing"
	"time"
)

// valuesConn records every inserted row's values by table.
type valuesConn struct {
	queries map[string]string
	rows    map[string][][]interface{}
	fail    int
}

func newValuesConn() *valuesConn {
	return &valuesConn{queries: map[string]string{}, rows: map[string][][]interface{}{}}
}

func (c *valuesConn) PrepareBatch(_ context.Context, query string) (Batch, error) {
	return &valuesBatch{conn: c, table: insertTable(query), query: query}, nil
}

func (c *valuesConn) Query(context.Context, string, ...interface{}) (Rows, error) {
	return &fakeIDRows{index: -1}, nil
}

type valuesBatch struct {
	conn  *valuesConn
	table string
	query string
	rows  [][]interface{}
}

func (b *valuesBatch) Append(v ...interface{}) error {
	b.rows = append(b.rows, append([]interface{}(nil), v...))
	return nil
}

func (b *valuesBatch) Send() error {
	if b.conn.fail > 0 {
		b.conn.fail--
		return errTransient
	}
	b.conn.queries[b.table] = b.query
	b.conn.rows[b.table] = append(b.conn.rows[b.table], b.rows...)
	return nil
}

func (c *valuesConn) arrivals(t *testing.T, table string) []int64 {
	t.Helper()
	index := insertColumnIndex(c.queries[table], arrivalColumns[table])
	if index < 0 {
		t.Fatalf("%s has no %s column in %q", table, arrivalColumns[table], c.queries[table])
	}
	var out []int64
	for _, values := range c.rows[table] {
		out = append(out, values[index].(int64))
	}
	return out
}

func bufferArrivalRows(t *testing.T, ctx context.Context, handledAtMS int64) {
	t.Helper()
	send := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	viewer, err := PrepareViewerSessionFinal(ctx, nil)
	send(err)
	// A pure replay and its correction, handled 5 ms apart.
	send(viewer.Append(ViewerSessionFinalRow{SessionID: "s", ProjectionVersionMS: handledAtMS}))
	send(viewer.Append(ViewerSessionFinalRow{SessionID: "s", ProjectionVersionMS: handledAtMS + 5}))
	send(viewer.Send())
	restream, err := PrepareRestreamSessionFinal(ctx, nil)
	send(err)
	send(restream.Append(RestreamSessionFinalRow{ProjectionVersionMS: handledAtMS}))
	send(restream.Send())
	stream, err := PrepareStreamSessionFinal(ctx, nil)
	send(err)
	send(stream.Append(StreamSessionFinalRow{ProjectionVersionMS: handledAtMS}))
	send(stream.Send())
	segment, err := PrepareProcessingSegmentFinal(ctx, nil)
	send(err)
	send(segment.Append(ProcessingSegmentFinalRow{ProjectionVersionMS: handledAtMS}))
	send(segment.Send())
	divergence, err := PrepareProjectionDivergence(ctx, nil)
	send(err)
	send(divergence.Append(ProjectionDivergenceRow{ObservedAtMS: handledAtMS + 3}))
	send(divergence.Send())
	snapshot, err := PrepareStorageSnapshot(ctx, nil)
	send(err)
	send(snapshot.Append(StorageSnapshotRow{IngestedAtMS: handledAtMS + 1}))
	send(snapshot.Send())
	request, err := PrepareAPIRequest(ctx, nil)
	send(err)
	send(request.Append(APIRequestRow{IngestedAtMS: handledAtMS + 2}))
	send(request.Send())
}

// Ledger and billing cursors walk each arrival column up to a settlement lag
// behind now. A row stamped when its record was handled and inserted after a
// cursor passed that stamp would never be read, so every arrival column is
// stamped when the flush that inserts the row starts, keeping the order the
// handlers stamped and rising across flushes.
func TestWriteBufferStampsArrivalColumnsAtFlush(t *testing.T) {
	conn := newValuesConn()
	// The poll batch was handled an hour ago; ClickHouse rejected its first
	// insert attempt.
	handledAtMS := time.Now().Add(-time.Hour).UnixMilli()
	conn.fail = 1
	buffer := NewWriteBuffer(conn, WriteBufferOptions{IsRetryable: func(error) bool { return true }})
	bufferArrivalRows(t, WithWriteBuffer(context.Background(), buffer), handledAtMS)
	if err := buffer.Flush(context.Background()); err == nil {
		t.Fatal("first flush attempt succeeded, want the transient failure")
	}
	retriedAt := time.Now().UnixMilli()
	if err := buffer.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}

	var firstFlushMax int64
	for table := range arrivalColumns {
		got := conn.arrivals(t, table)
		if len(got) == 0 {
			t.Fatalf("%s: no rows inserted", table)
		}
		for _, stamp := range got {
			if stamp < retriedAt {
				t.Errorf("%s arrival %d precedes the flush attempt that inserted it at %d", table, stamp, retriedAt)
			}
			firstFlushMax = max(firstFlushMax, stamp)
		}
	}
	viewer := conn.arrivals(t, "viewer_sessions_final")
	if len(viewer) != 2 || viewer[1]-viewer[0] != 5 {
		t.Errorf("viewer_sessions_final arrivals %v, want the correction still 5 ms after the replay", viewer)
	}

	next := newValuesConn()
	buffer = NewWriteBuffer(next, WriteBufferOptions{})
	bufferArrivalRows(t, WithWriteBuffer(context.Background(), buffer), handledAtMS)
	if err := buffer.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	for table := range arrivalColumns {
		for _, stamp := range next.arrivals(t, table) {
			if stamp <= firstFlushMax {
				t.Errorf("%s arrival %d of a later flush does not follow the earlier flush's %d", table, stamp, firstFlushMax)
			}
		}
	}
}
