package periscopeingestdb

import (
	"context"
	"strings"
	"sync/atomic"
	"time"

	pkgdatabase "github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/google/uuid"
)

// Batch is the subset of the ClickHouse batch contract needed by table writers.
type Batch interface {
	Append(v ...interface{}) error
	Send() error
}

// BatchPreparer prepares a native ClickHouse batch for an explicit INSERT.
type BatchPreparer interface {
	PrepareBatch(ctx context.Context, query string) (Batch, error)
}

// Writer binds one typed row shape to one fixed ClickHouse INSERT column list.
type Writer[Row any] struct {
	batch  Batch
	values func(Row) []interface{}
	table  string
	rows   int
}

var insertObserver atomic.Pointer[func(table string, rows int, err error)]

// SetInsertObserver registers f to observe every INSERT a writer or a
// WriteBuffer sends to ClickHouse: its table, its row count, and its error.
func SetInsertObserver(f func(table string, rows int, err error)) {
	insertObserver.Store(&f)
}

func observeInsert(table string, rows int, err error) {
	if f := insertObserver.Load(); f != nil {
		(*f)(table, rows, err)
	}
}

// rowScope is a row's lookup bounds: its tenant and, when the table has one,
// its time column value.
type rowScope struct {
	tenant uuid.UUID
	at     time.Time
}

// prepare binds a writer to a native batch, or to the WriteBuffer carried by
// ctx so its rows are inserted when the buffer flushes. A statement listed in
// identifiedStatements also writes ingest_id as its last column: under a
// WriteBuffer each row's id is derived from its source record, so a
// redelivered record's rows are recognized and dropped at flush; a direct
// write stores the zero id.
func prepare[Row any](ctx context.Context, db BatchPreparer, query string, values func(Row) []interface{}) (*Writer[Row], error) {
	identity := identifiedStatements[query]
	if identity != nil {
		query = withIngestIDColumn(query)
		rowValues := values
		values = func(row Row) []interface{} {
			return append(rowValues(row), uuid.Nil)
		}
	}
	if buffer := WriteBufferFrom(ctx); buffer != nil {
		return &Writer[Row]{batch: buffer.newBatch(query, rowOriginFrom(ctx), identity), values: values, table: insertTable(query)}, nil
	}
	batch, err := db.PrepareBatch(ctx, query)
	if err != nil {
		pkgdatabase.ObserveDatabaseError("periscope-ingest", pkgdatabase.EngineClickHouse, err, false)
		return nil, err
	}
	return &Writer[Row]{batch: batch, values: values, table: insertTable(query)}, nil
}

// withIngestIDColumn appends ingest_id to the column list of an INSERT whose
// column list closes the statement.
func withIngestIDColumn(query string) string {
	trimmed := strings.TrimRight(query, " \t\n")
	if !strings.HasSuffix(trimmed, ")") {
		return query
	}
	return strings.TrimSuffix(trimmed, ")") + ", ingest_id)"
}

// Append converts a typed table row into the driver's positional batch values.
func (w *Writer[Row]) Append(row Row) error {
	if buffered, ok := w.batch.(*bufferedBatch); ok {
		return buffered.appendRow(row, w.values(row))
	}
	err := w.batch.Append(w.values(row)...)
	pkgdatabase.ObserveDatabaseError("periscope-ingest", pkgdatabase.EngineClickHouse, err, false)
	if err == nil {
		w.rows++
	}
	return err
}

// Send flushes the native batch using the caller's existing commit boundary.
// Under a WriteBuffer it hands the rows to the buffer instead.
func (w *Writer[Row]) Send() error {
	err := w.batch.Send()
	if _, buffered := w.batch.(*bufferedBatch); buffered {
		return err
	}
	pkgdatabase.ObserveDatabaseError("periscope-ingest", pkgdatabase.EngineClickHouse, err, false)
	observeInsert(w.table, w.rows, err)
	return err
}

// Close releases a native batch when the driver supports explicit cleanup.
func (w *Writer[Row]) Close() error {
	if closer, ok := w.batch.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}
