package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"frameworks/api_analytics_ingest/internal/database/periscopeingestdb"
	"frameworks/api_analytics_ingest/internal/handlers"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// ingestBatchFlusher buffers the ClickHouse rows every handler writes while a
// poll batch is handled and inserts them, one INSERT per statement, before
// the batch's offsets commit. Handlers keep their per-event logic; only the
// INSERT round trips move to the end of the batch.
type ingestBatchFlusher struct {
	newBuffer func(periscopeingestdb.WriteBufferOptions) *periscopeingestdb.WriteBuffer
	failures  consumerFailurePolicy
	metrics   *handlers.PeriscopeMetrics
	logger    logging.Logger
}

type ingestBatchKey struct{}

type ingestBatch struct {
	buffer    *periscopeingestdb.WriteBuffer
	published []time.Time
}

// recordOrigin is the WriteBuffer origin of every row a record's handler
// writes: a row ClickHouse rejects permanently settles its record through the
// subscription's failure policy.
type recordOrigin struct {
	msg          kafka.Message
	consumerName string
	useDLQ       bool
}

// IngestIdentity names the record by its key and value. A redelivery of the
// record, or the copy MirrorMaker2 writes to a mirrored topic, carries the same
// bytes, so its rows get the ingest_id they had.
func (o *recordOrigin) IngestIdentity() string {
	sum := sha256.New()
	_, _ = sum.Write(o.msg.Key)
	_, _ = sum.Write([]byte{0})
	_, _ = sum.Write(o.msg.Value)
	return hex.EncodeToString(sum.Sum(nil))
}

// insertObserver counts every INSERT the writers send, buffered or direct, by
// table and outcome, and records its size.
func insertObserver(metrics *handlers.PeriscopeMetrics) func(table string, rows int, err error) {
	return func(table string, rows int, err error) {
		status := "success"
		if err != nil {
			status = "error"
		}
		if metrics.ClickHouseInserts != nil {
			metrics.ClickHouseInserts.WithLabelValues(table, status).Inc()
		}
		if err == nil && metrics.WriteInsertRows != nil {
			metrics.WriteInsertRows.WithLabelValues(table).Observe(float64(rows))
		}
	}
}

func ingestBatchFrom(ctx context.Context) *ingestBatch {
	if batch, ok := ctx.Value(ingestBatchKey{}).(*ingestBatch); ok {
		return batch
	}
	return nil
}

func (f *ingestBatchFlusher) BeginBatch(ctx context.Context) context.Context {
	metrics := f.metrics
	buffer := f.newBuffer(periscopeingestdb.WriteBufferOptions{
		IsRetryable: isRetryableConsumerError,
		OnDuplicate: func(table string, rows int) {
			if metrics.DuplicateRows != nil {
				metrics.DuplicateRows.WithLabelValues(table).Add(float64(rows))
			}
		},
	})
	batch := &ingestBatch{buffer: buffer}
	ctx = periscopeingestdb.WithWriteBuffer(ctx, buffer)
	return context.WithValue(ctx, ingestBatchKey{}, batch)
}

// FlushBatch inserts the batch's rows, retrying a transient ClickHouse failure
// until it succeeds or ctx ends. It returns the records whose rows ClickHouse
// rejected and the failure policy did not dead-letter.
func (f *ingestBatchFlusher) FlushBatch(ctx context.Context) ([]kafka.Message, error) {
	batch := ingestBatchFrom(ctx)
	if batch == nil {
		return nil, nil
	}
	start := time.Now()
	for attempt := 1; ; attempt++ {
		err := batch.buffer.Flush(ctx)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		delay := retryableConsumerBackoff(attempt)
		f.logger.WithError(err).WithFields(logging.Fields{
			"attempt":      attempt,
			"retry_in":     delay.String(),
			"pending_rows": batch.buffer.Pending(),
		}).Warn("ClickHouse insert of the poll batch failed; retrying")
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}

	var failed []kafka.Message
	settled := make(map[*recordOrigin]struct{})
	for _, failure := range batch.buffer.TakeFailures() {
		origin, ok := failure.Origin.(*recordOrigin)
		if !ok {
			continue
		}
		if _, done := settled[origin]; done {
			continue
		}
		settled[origin] = struct{}{}
		f.logger.WithError(failure.Err).WithFields(logging.Fields{
			"table":     failure.Table,
			"topic":     origin.msg.Topic,
			"partition": origin.msg.Partition,
			"offset":    origin.msg.Offset,
		}).Error("ClickHouse rejected a row of the poll batch")
		if err := f.failures.settle(origin.consumerName, origin.msg, failure.Err, origin.useDLQ); err != nil {
			failed = append(failed, origin.msg)
		}
	}

	done := time.Now()
	if f.metrics.WriteFlushDuration != nil {
		f.metrics.WriteFlushDuration.WithLabelValues().Observe(done.Sub(start).Seconds())
	}
	if f.metrics.WriteBatchRecords != nil {
		f.metrics.WriteBatchRecords.WithLabelValues().Observe(float64(len(batch.published)))
	}
	if f.metrics.IngestDelay != nil {
		delay := f.metrics.IngestDelay.WithLabelValues()
		for _, published := range batch.published {
			if !published.IsZero() {
				delay.Observe(done.Sub(published).Seconds())
			}
		}
	}
	return failed, nil
}
