package kafka_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/sirupsen/logrus"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"
)

// A record whose handler fails, and that nothing settles, must be handled
// again by the running consumer: the partition is fetched from that record and
// its commit never passes it, even when later records keep arriving.
func TestConsumerRefetchesAFailedRecordInsteadOfSkippingIt(t *testing.T) {
	const topic, group = "events", "retry-group"
	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(1, topic))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cluster.Close)
	producer, err := kgo.NewClient(kgo.SeedBrokers(cluster.ListenAddrs()...))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(producer.Close)
	admin := kadm.NewClient(producer)
	produce := func(values ...string) {
		t.Helper()
		var records []*kgo.Record
		for _, v := range values {
			records = append(records, &kgo.Record{Topic: topic, Value: []byte(v)})
		}
		if produceErr := producer.ProduceSync(context.Background(), records...).FirstErr(); produceErr != nil {
			t.Fatal(produceErr)
		}
	}
	committed := func() int64 {
		offsets, fetchErr := admin.FetchOffsets(context.Background(), group)
		if fetchErr != nil {
			return -1
		}
		o, ok := offsets.Lookup(topic, 0)
		if !ok || o.Err != nil {
			return -1
		}
		return o.At
	}

	var mu sync.Mutex
	attempts := map[string]int{}
	var handled []string
	consumer, err := kafka.NewConsumer(cluster.ListenAddrs(), group, "test", "retry-test", logrus.New())
	if err != nil {
		t.Fatal(err)
	}
	consumer.AddHandler(topic, func(_ context.Context, msg kafka.Message) error {
		mu.Lock()
		defer mu.Unlock()
		value := string(msg.Value)
		attempts[value]++
		if value == "2" && attempts[value] <= 2 {
			return errors.New("permanent-looking failure")
		}
		handled = append(handled, value)
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = consumer.Start(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		_ = consumer.Close()
	})

	produce("0", "1", "2", "3", "4")
	// A later record must not let the commit pass the failed one.
	time.Sleep(200 * time.Millisecond)
	produce("5")

	deadline := time.Now().Add(30 * time.Second)
	for committed() < 6 {
		if time.Now().After(deadline) {
			mu.Lock()
			defer mu.Unlock()
			t.Fatalf("committed offset %d, want 6; handled %v, attempts %v", committed(), handled, attempts)
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if attempts["2"] != 3 {
		t.Fatalf("record 2 attempted %d times, want 3", attempts["2"])
	}
	if got := fmt.Sprint(handled); got != "[0 1 2 3 4 5]" {
		t.Fatalf("handled %s, want every record once in order", got)
	}
}
