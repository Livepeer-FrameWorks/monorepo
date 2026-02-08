package kafka

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"testing"

	"github.com/sirupsen/logrus"
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
