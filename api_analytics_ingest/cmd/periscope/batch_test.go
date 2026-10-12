package main

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"frameworks/api_analytics_ingest/internal/database/periscopeingestdb"
	"frameworks/api_analytics_ingest/internal/handlers"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// rejectingConn accepts every native batch except a row carrying "poison",
// which the driver refuses to encode.
type rejectingConn struct {
	inserted []string
}

func (c *rejectingConn) PrepareBatch(context.Context, string) (periscopeingestdb.Batch, error) {
	return &rejectingBatch{conn: c}, nil
}

func (c *rejectingConn) Query(context.Context, string, ...interface{}) (periscopeingestdb.Rows, error) {
	return nil, errors.New("no reads expected")
}

type rejectingBatch struct {
	conn *rejectingConn
	rows []string
}

func (b *rejectingBatch) Append(v ...any) error {
	id, _ := v[1].(string)
	if id == "poison" {
		return errors.New("clickhouse: cannot encode value")
	}
	b.rows = append(b.rows, id)
	return nil
}

func (b *rejectingBatch) Close() error { return nil }

func (b *rejectingBatch) Send() error {
	b.conn.inserted = append(b.conn.inserted, b.rows...)
	return nil
}

type recordingDLQ struct{ keys []string }

func (d *recordingDLQ) ProduceMessage(_ string, key []byte, _ []byte, _ map[string]string) error {
	d.keys = append(d.keys, string(key))
	return nil
}

// writeIngestErrorRow stands in for a handler: it writes one row whose
// event_id is the record value, failing the first attempt when told to.
func writeIngestErrorRow(failFirst map[string]bool) messageHandler {
	return func(ctx context.Context, msg kafka.Message) error {
		w, err := periscopeingestdb.PrepareIngestError(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = w.Close() }()
		if err := w.Append(periscopeingestdb.IngestErrorRow{EventID: string(msg.Value)}); err != nil {
			return err
		}
		if err := w.Send(); err != nil {
			return err
		}
		if failFirst[string(msg.Value)] {
			delete(failFirst, string(msg.Value))
			return fmt.Errorf("lookup: %w", errors.New("connection refused"))
		}
		return nil
	}
}

func runIngestBatch(t *testing.T, useDLQ bool, values ...string) (*rejectingConn, *recordingDLQ, []kafka.Message) {
	t.Helper()
	conn := &rejectingConn{}
	dlq := &recordingDLQ{}
	logger := logging.NewLogger()
	metrics := &handlers.PeriscopeMetrics{}
	policy := consumerFailurePolicy{dlq: dlq, dlqTopic: "dlq", metrics: metrics, logger: logger}
	newBuffer := func(opts periscopeingestdb.WriteBufferOptions) *periscopeingestdb.WriteBuffer {
		return periscopeingestdb.NewWriteBuffer(conn, opts)
	}
	flusher := &ingestBatchFlusher{newBuffer: newBuffer, failures: policy, metrics: metrics, logger: logger}
	handler := policy.wrap("periscope-ingest-test", writeIngestErrorRow(map[string]bool{"retried": true}), useDLQ)

	ctx := flusher.BeginBatch(context.Background())
	for i, value := range values {
		if err := handler(ctx, kafka.Message{Topic: "analytics_events", Offset: int64(i), Value: []byte(value)}); err != nil {
			t.Fatalf("handler(%s): %v", value, err)
		}
	}
	if len(conn.inserted) != 0 {
		t.Fatal("rows were inserted before the batch flushed")
	}
	failed, err := flusher.FlushBatch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return conn, dlq, failed
}

func TestIngestBatchInsertsOnceAndDeadLettersRejectedRows(t *testing.T) {
	conn, dlq, failed := runIngestBatch(t, true, "a", "poison", "retried", "b")
	if fmt.Sprint(conn.inserted) != "[a retried b]" {
		t.Fatalf("inserted %v, want [a retried b]: the poison row dropped and the retried record's first attempt discarded", conn.inserted)
	}
	if len(dlq.keys) != 1 || dlq.keys[0] != "analytics_events:0:1" {
		t.Fatalf("dead-lettered %v, want the poison record", dlq.keys)
	}
	if len(failed) != 0 {
		t.Fatalf("failed records %v, want none once dead-lettered", failed)
	}
}

func TestIngestBatchReportsRejectedRowsOfRetryOnlyTopics(t *testing.T) {
	_, dlq, failed := runIngestBatch(t, false, "a", "poison", "b")
	if len(dlq.keys) != 0 {
		t.Fatalf("retry-only subscription dead-lettered %v", dlq.keys)
	}
	if len(failed) != 1 || failed[0].Offset != 1 {
		t.Fatalf("failed records %v, want the poison record so its partition does not commit past it", failed)
	}
}
