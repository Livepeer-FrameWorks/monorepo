package kafka

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/sirupsen/logrus"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
)

func TestConsumerProcessRecordsBlocksPartitionOnFailure(t *testing.T) {
	logger := logrus.New()
	consumer := &Consumer{
		logger:   logger,
		handlers: make(map[string]Handler),
	}

	var handled []string
	consumer.handlers["events"] = func(_ context.Context, msg Message) error {
		handled = append(handled, formatRecordKey(msg.Topic, msg.Partition, msg.Offset))
		if msg.Partition == 0 && msg.Offset == 1 {
			return errors.New("handler failure")
		}
		return nil
	}

	records := []*kgo.Record{
		{Topic: "events", Partition: 0, Offset: 0},
		{Topic: "events", Partition: 0, Offset: 1},
		{Topic: "events", Partition: 0, Offset: 2},
		{Topic: "events", Partition: 1, Offset: 0},
		{Topic: "events", Partition: 1, Offset: 1},
	}

	commitRecords := consumer.processRecords(context.Background(), records)

	sort.Strings(handled)
	expectedHandled := []string{
		formatRecordKey("events", 0, 0),
		formatRecordKey("events", 0, 1),
		formatRecordKey("events", 1, 0),
		formatRecordKey("events", 1, 1),
	}
	sort.Strings(expectedHandled)

	if len(handled) != len(expectedHandled) {
		t.Fatalf("handled records = %v, want %v", handled, expectedHandled)
	}
	for i, value := range handled {
		if value != expectedHandled[i] {
			t.Fatalf("handled records = %v, want %v", handled, expectedHandled)
		}
	}

	commitKeys := make([]string, 0, len(commitRecords))
	for _, record := range commitRecords {
		commitKeys = append(commitKeys, formatRecordKey(record.Topic, record.Partition, record.Offset))
	}
	sort.Strings(commitKeys)

	expectedCommitKeys := []string{
		formatRecordKey("events", 0, 0),
		formatRecordKey("events", 1, 1),
	}
	sort.Strings(expectedCommitKeys)

	if len(commitKeys) != len(expectedCommitKeys) {
		t.Fatalf("commit records = %v, want %v", commitKeys, expectedCommitKeys)
	}
	for i, value := range commitKeys {
		if value != expectedCommitKeys[i] {
			t.Fatalf("commit records = %v, want %v", commitKeys, expectedCommitKeys)
		}
	}
}

func formatRecordKey(topic string, partition int32, offset int64) string {
	return topic + ":" + formatInt32(partition) + ":" + formatInt64(offset)
}

func formatInt32(value int32) string {
	return formatInt64(int64(value))
}

func formatInt64(value int64) string {
	return strconv.FormatInt(value, 10)
}

type fakeLagFetcher struct {
	ends    kadm.ListedOffsets
	commits kadm.OffsetResponses
	endErr  error
	commErr error
}

func (f *fakeLagFetcher) ListEndOffsets(_ context.Context, _ ...string) (kadm.ListedOffsets, error) {
	return f.ends, f.endErr
}

func (f *fakeLagFetcher) FetchOffsetsForTopics(_ context.Context, _ string, _ ...string) (kadm.OffsetResponses, error) {
	return f.commits, f.commErr
}

func buildEnds(entries map[string]map[int32]int64) kadm.ListedOffsets {
	out := make(kadm.ListedOffsets)
	for topic, parts := range entries {
		out[topic] = make(map[int32]kadm.ListedOffset)
		for p, off := range parts {
			out[topic][p] = kadm.ListedOffset{Topic: topic, Partition: p, Offset: off}
		}
	}
	return out
}

func buildCommits(entries map[string]map[int32]int64) kadm.OffsetResponses {
	out := make(kadm.OffsetResponses)
	for topic, parts := range entries {
		out[topic] = make(map[int32]kadm.OffsetResponse)
		for p, off := range parts {
			out[topic][p] = kadm.OffsetResponse{Offset: kadm.Offset{Topic: topic, Partition: p, At: off}}
		}
	}
	return out
}

func gaugeValue(t *testing.T, vec *prometheus.GaugeVec, labels ...string) float64 {
	t.Helper()
	g, err := vec.GetMetricWithLabelValues(labels...)
	if err != nil {
		t.Fatalf("get gauge: %v", err)
	}
	var m dto.Metric
	if err := g.Write(&m); err != nil {
		t.Fatalf("write gauge: %v", err)
	}
	return m.GetGauge().GetValue()
}

func newLagTrackerConsumer(t *testing.T) (*Consumer, *prometheus.GaugeVec) {
	t.Helper()
	gauge := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "test_lag"}, []string{"topic", "partition"})
	c := &Consumer{
		logger:     logrus.New(),
		groupID:    "test-group",
		handlers:   map[string]Handler{"events": nil, "billing": nil},
		lagTracker: &LagTrackerConfig{Gauge: gauge},
	}
	return c, gauge
}

func TestPublishLag_SetsLagPerTopicPartition(t *testing.T) {
	c, gauge := newLagTrackerConsumer(t)
	fetcher := &fakeLagFetcher{
		ends: buildEnds(map[string]map[int32]int64{
			"events":  {0: 100, 1: 50},
			"billing": {0: 200},
		}),
		commits: buildCommits(map[string]map[int32]int64{
			"events":  {0: 80, 1: 50},
			"billing": {0: 150},
		}),
	}

	if err := c.publishLag(context.Background(), fetcher, []string{"events", "billing"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := gaugeValue(t, gauge, "events", "0"); got != 20 {
		t.Fatalf("events/0 lag = %v, want 20", got)
	}
	if got := gaugeValue(t, gauge, "events", "1"); got != 0 {
		t.Fatalf("events/1 lag = %v, want 0", got)
	}
	if got := gaugeValue(t, gauge, "billing", "0"); got != 50 {
		t.Fatalf("billing/0 lag = %v, want 50", got)
	}
}

func TestPublishLag_MissingCommitDefaultsToZero(t *testing.T) {
	c, gauge := newLagTrackerConsumer(t)
	fetcher := &fakeLagFetcher{
		ends: buildEnds(map[string]map[int32]int64{"events": {0: 42}}),
		// No commits returned: treat as committed=0, so lag = end.
		commits: kadm.OffsetResponses{},
	}

	if err := c.publishLag(context.Background(), fetcher, []string{"events"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := gaugeValue(t, gauge, "events", "0"); got != 42 {
		t.Fatalf("events/0 lag = %v, want 42", got)
	}
}

func TestPublishLag_EndOffsetErrorReturnsError(t *testing.T) {
	c, gauge := newLagTrackerConsumer(t)
	gauge.WithLabelValues("events", "0").Set(12)
	fetcher := &fakeLagFetcher{endErr: errors.New("broker unavailable")}

	if err := c.publishLag(context.Background(), fetcher, []string{"events"}); err == nil {
		t.Fatalf("expected error when end-offset fetch fails")
	}
	if got := gaugeValue(t, gauge, "events", "0"); got != 12 {
		t.Fatalf("lag gauge changed on end-offset error: got %v, want 12", got)
	}
}

func TestPublishLag_CommitErrorLeavesPreviousValue(t *testing.T) {
	c, gauge := newLagTrackerConsumer(t)
	gauge.WithLabelValues("events", "0").Set(99)
	fetcher := &fakeLagFetcher{
		ends:    buildEnds(map[string]map[int32]int64{"events": {0: 7}}),
		commErr: errors.New("offset api transient"),
	}

	if err := c.publishLag(context.Background(), fetcher, []string{"events"}); err == nil {
		t.Fatalf("expected error when committed-offset fetch fails")
	}
	if got := gaugeValue(t, gauge, "events", "0"); got != 99 {
		t.Fatalf("lag gauge changed on commit error: got %v, want 99", got)
	}
}

func TestPublishLag_NoLagTrackerIsNoop(t *testing.T) {
	c := &Consumer{
		logger:   logrus.New(),
		groupID:  "test-group",
		handlers: map[string]Handler{"events": nil},
	}
	fetcher := &fakeLagFetcher{
		ends: buildEnds(map[string]map[int32]int64{"events": {0: 100}}),
	}
	if err := c.publishLag(context.Background(), fetcher, []string{"events"}); err != nil {
		t.Fatalf("expected nil error when lag tracker not configured, got %v", err)
	}
}

func TestRunLagTrackerStopsWhenContextIsCancelled(t *testing.T) {
	gauge := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "test_lag_shutdown"}, []string{"topic", "partition"})
	c := &Consumer{
		client:     nil,
		logger:     logrus.New(),
		groupID:    "test-group",
		handlers:   map[string]Handler{"events": nil},
		lagTracker: &LagTrackerConfig{Gauge: gauge, Interval: time.Hour},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.runLagTracker(ctx)
		close(done)
	}()
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("lag tracker did not stop after context cancellation")
	}
}
