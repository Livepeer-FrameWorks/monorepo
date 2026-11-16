package kafka_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"
)

func TestBroadcastReplicasEachApplyEveryPartitionBeforeCatchUp(t *testing.T) {
	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(3, "facts"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cluster.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	producer, err := kgo.NewClient(kgo.SeedBrokers(cluster.ListenAddrs()...), kgo.RecordPartitioner(kgo.ManualPartitioner()))
	if err != nil {
		t.Fatal(err)
	}
	defer producer.Close()
	var counts [2]atomic.Int32
	var readers []*kafka.Broadcast
	for i := range counts {
		reader, openErr := kafka.OpenBroadcast(ctx, cluster.ListenAddrs(), []string{"facts"}, "replica")
		if openErr != nil {
			t.Fatal(openErr)
		}
		defer reader.Close()
		readers = append(readers, reader)
		go func() {
			_ = reader.Run(ctx, func(context.Context, kafka.Message) error { counts[i].Add(1); return nil })
		}()
	}
	for partition := int32(0); partition < 3; partition++ {
		if err := producer.ProduceSync(ctx, &kgo.Record{Topic: "facts", Partition: partition, Value: []byte("fact")}).FirstErr(); err != nil {
			t.Fatal(err)
		}
	}
	for i, reader := range readers {
		if err := reader.CatchUp(ctx); err != nil {
			t.Fatal(err)
		}
		if got := counts[i].Load(); got != 3 {
			t.Fatalf("replica %d applied %d records, want all 3 partitions", i, got)
		}
	}
}

func TestBroadcastCatchUpWaitsForAppliedHandler(t *testing.T) {
	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(1, "facts"))
	if err != nil {
		t.Fatal(err)
	}
	defer cluster.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	reader, err := kafka.OpenBroadcast(ctx, cluster.ListenAddrs(), []string{"facts"}, "slow-projection")
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	producer, err := kgo.NewClient(kgo.SeedBrokers(cluster.ListenAddrs()...))
	if err != nil {
		t.Fatal(err)
	}
	defer producer.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	go func() {
		_ = reader.Run(ctx, func(ctx context.Context, _ kafka.Message) error {
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	if err := producer.ProduceSync(ctx, &kgo.Record{Topic: "facts", Value: []byte("fact")}).FirstErr(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	short, stop := context.WithTimeout(ctx, 30*time.Millisecond)
	if err := reader.CatchUp(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unapplied record reported caught up: %v", err)
	}
	stop()
	close(release)
	if err := reader.CatchUp(ctx); err != nil {
		t.Fatal(err)
	}
}
