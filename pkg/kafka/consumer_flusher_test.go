package kafka

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/twmb/franz-go/pkg/kgo"
)

type batchKey struct{}

type recordingFlusher struct {
	events  []string
	failed  []Message
	err     error
	begunAt int
}

func (f *recordingFlusher) BeginBatch(ctx context.Context) context.Context {
	f.events = append(f.events, "begin")
	f.begunAt++
	return context.WithValue(ctx, batchKey{}, f.begunAt)
}

func (f *recordingFlusher) FlushBatch(ctx context.Context) ([]Message, error) {
	f.events = append(f.events, "flush")
	if ctx.Value(batchKey{}) != f.begunAt {
		return nil, errors.New("flush ran without the batch context")
	}
	return f.failed, f.err
}

func flusherConsumer(flusher BatchFlusher, handled *[]string) *Consumer {
	c := &Consumer{logger: logrus.New(), handlers: make(map[string]Handler), flusher: flusher}
	c.handlers["events"] = func(ctx context.Context, msg Message) error {
		if ctx.Value(batchKey{}) == nil {
			return errors.New("handler ran without the batch context")
		}
		*handled = append(*handled, formatRecordKey(msg.Topic, msg.Partition, msg.Offset))
		return nil
	}
	return c
}

func committedKeys(records []*kgo.Record) []string {
	keys := make([]string, 0, len(records))
	for _, r := range records {
		keys = append(keys, formatRecordKey(r.Topic, r.Partition, r.Offset))
	}
	sort.Strings(keys)
	return keys
}

func TestConsumerProcessBatchFlushesOnceAfterEveryHandler(t *testing.T) {
	flusher := &recordingFlusher{}
	var handled []string
	c := flusherConsumer(flusher, &handled)
	records := []*kgo.Record{
		{Topic: "events", Partition: 0, Offset: 0},
		{Topic: "events", Partition: 0, Offset: 1},
		{Topic: "events", Partition: 1, Offset: 7},
	}

	commit, err := c.processBatch(context.Background(), records)
	if err != nil {
		t.Fatal(err)
	}
	if len(handled) != 3 {
		t.Fatalf("handled %v, want all three records", handled)
	}
	if got := flusher.events; len(got) != 2 || got[0] != "begin" || got[1] != "flush" {
		t.Fatalf("flusher calls = %v, want [begin flush]", got)
	}
	want := []string{formatRecordKey("events", 0, 1), formatRecordKey("events", 1, 7)}
	if got := committedKeys(commit); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("commit = %v, want %v", got, want)
	}

	if _, err := c.processBatch(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if len(flusher.events) != 2 {
		t.Fatalf("an empty poll batch ran the flusher: %v", flusher.events)
	}
}

func TestConsumerProcessBatchHoldsBackPartitionAtFailedFlushRecord(t *testing.T) {
	flusher := &recordingFlusher{failed: []Message{
		{Topic: "events", Partition: 0, Offset: 2},
		{Topic: "events", Partition: 0, Offset: 1},
		{Topic: "events", Partition: 2, Offset: 4},
	}}
	var handled []string
	c := flusherConsumer(flusher, &handled)
	records := []*kgo.Record{
		{Topic: "events", Partition: 0, Offset: 0},
		{Topic: "events", Partition: 0, Offset: 1},
		{Topic: "events", Partition: 0, Offset: 2},
		{Topic: "events", Partition: 1, Offset: 3},
		{Topic: "events", Partition: 2, Offset: 4},
		{Topic: "events", Partition: 2, Offset: 5},
	}

	commit, err := c.processBatch(context.Background(), records)
	if err != nil {
		t.Fatal(err)
	}
	// Partition 0 stops before its earliest failed offset, partition 1 is
	// untouched, and partition 2 has nothing before its failed first record.
	want := []string{formatRecordKey("events", 0, 0), formatRecordKey("events", 1, 3)}
	got := committedKeys(commit)
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("commit = %v, want %v", got, want)
	}
}

type ctxCheckingPoller struct {
	*fakeConsumerPoller
	commitCtxErr error
}

func (p *ctxCheckingPoller) CommitRecords(ctx context.Context, records ...*kgo.Record) error {
	p.commitCtxErr = ctx.Err()
	return p.fakeConsumerPoller.CommitRecords(ctx, records...)
}

func TestConsumerStartCommitsHandledRecordsWhenShutdownBeginsMidBatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	poller := &ctxCheckingPoller{fakeConsumerPoller: &fakeConsumerPoller{
		batch: kgo.Fetches{{Topics: []kgo.FetchTopic{{
			Topic:      "events",
			Partitions: []kgo.FetchPartition{{Partition: 0, Records: []*kgo.Record{{Topic: "events", Partition: 0, Offset: 9}}}},
		}}}},
		cancel: cancel,
	}}
	c := &Consumer{logger: logrus.New(), handlers: make(map[string]Handler), poller: poller}
	c.handlers["events"] = func(context.Context, Message) error {
		cancel()
		return nil
	}

	if err := c.Start(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start() = %v, want context.Canceled", err)
	}
	if len(poller.committed) != 1 || poller.committed[0].Offset != 9 {
		t.Fatalf("committed %v, want the handled record", poller.committed)
	}
	if poller.commitCtxErr != nil {
		t.Fatalf("commit ran with a cancelled context: %v", poller.commitCtxErr)
	}
}

func TestConsumerStartStopsWithoutCommitWhenFlushFails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	poller := &fakeConsumerPoller{
		batch: kgo.Fetches{{Topics: []kgo.FetchTopic{{
			Topic:      "events",
			Partitions: []kgo.FetchPartition{{Partition: 0, Records: []*kgo.Record{{Topic: "events", Partition: 0, Offset: 3}}}},
		}}}},
		cancel: cancel,
	}
	flushErr := errors.New("clickhouse unavailable")
	var handled []string
	c := flusherConsumer(&recordingFlusher{err: flushErr}, &handled)
	c.poller = poller

	err := c.Start(ctx)
	if !errors.Is(err, flushErr) {
		t.Fatalf("Start() = %v, want the flush error", err)
	}
	if len(poller.committed) != 0 {
		t.Fatalf("committed %d records after a failed flush", len(poller.committed))
	}
	if poller.rebalances == 0 {
		t.Fatal("Start returned without allowing rebalance")
	}
}
