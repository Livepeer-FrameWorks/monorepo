package periscopeingestdb

import (
	"context"

	pkgdatabase "github.com/Livepeer-FrameWorks/monorepo/pkg/database"
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
}

func prepare[Row any](ctx context.Context, db BatchPreparer, query string, values func(Row) []interface{}) (*Writer[Row], error) {
	batch, err := db.PrepareBatch(ctx, query)
	if err != nil {
		pkgdatabase.ObserveDatabaseError("periscope-ingest", pkgdatabase.EngineClickHouse, err, false)
		return nil, err
	}
	return &Writer[Row]{batch: batch, values: values}, nil
}

// Append converts a typed table row into the driver's positional batch values.
func (w *Writer[Row]) Append(row Row) error {
	err := w.batch.Append(w.values(row)...)
	pkgdatabase.ObserveDatabaseError("periscope-ingest", pkgdatabase.EngineClickHouse, err, false)
	return err
}

// Send flushes the native batch using the caller's existing commit boundary.
func (w *Writer[Row]) Send() error {
	err := w.batch.Send()
	pkgdatabase.ObserveDatabaseError("periscope-ingest", pkgdatabase.EngineClickHouse, err, false)
	return err
}

// Close releases a native batch when the driver supports explicit cleanup.
func (w *Writer[Row]) Close() error {
	if closer, ok := w.batch.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}
