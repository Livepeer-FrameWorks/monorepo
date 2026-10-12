package kafka

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"
)

const shutdownTestTopic = "service_events"

// The services cancel the consumer's context and then Close it from the same
// goroutine (Lookout, Signalman) or Close it without cancelling. Start is then
// blocked in a poll of an idle topic. Close leaves the group, which waits for
// every poll that blocked rebalancing to be released; a poll Start makes after
// the cancel must not keep the group from being left.
func TestConsumerCloseReturnsWhileStartIsPolling(t *testing.T) {
	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(1, shutdownTestTopic))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cluster.Close)
	producer, err := kgo.NewClient(kgo.SeedBrokers(cluster.ListenAddrs()...), kgo.DefaultProduceTopic(shutdownTestTopic))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(producer.Close)

	for _, cancelFirst := range []bool{true, false} {
		for round := 0; round < 10; round++ {
			t.Run(fmt.Sprintf("cancelFirst=%t/round=%d", cancelFirst, round), func(t *testing.T) {
				closeConsumerWhilePolling(t, cluster, producer, fmt.Sprintf("shutdown-%t-%d", cancelFirst, round), cancelFirst)
			})
		}
	}
}

func closeConsumerWhilePolling(t *testing.T, cluster *kfake.Cluster, producer *kgo.Client, group string, cancelFirst bool) {
	t.Helper()
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	consumer, err := NewConsumer(cluster.ListenAddrs(), group, "test", group, logger)
	if err != nil {
		t.Fatal(err)
	}
	handled := make(chan struct{}, 1)
	consumer.AddHandler(shutdownTestTopic, func(context.Context, Message) error {
		select {
		case handled <- struct{}{}:
		default:
		}
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startDone := make(chan error, 1)
	go func() { startDone <- consumer.Start(ctx) }()

	if err := producer.ProduceSync(context.Background(), &kgo.Record{Value: []byte(group)}).FirstErr(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-handled:
	case <-time.After(20 * time.Second):
		t.Fatal("consumer never joined the group and handled a record")
	}
	// Give Start time to commit and block in its next poll of the idle topic.
	time.Sleep(50 * time.Millisecond)

	closed := make(chan struct{})
	go func() {
		if cancelFirst {
			cancel()
		}
		_ = consumer.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(15 * time.Second):
		t.Fatal("Close did not return while Start was polling")
	}
	select {
	case startErr := <-startDone:
		// Callers treat any other error from Start as a lost consumer.
		if cancelFirst && !errors.Is(startErr, context.Canceled) {
			t.Fatalf("Start returned %v after cancellation, want context.Canceled", startErr)
		}
		if !cancelFirst && startErr != nil {
			t.Fatalf("Start returned %v after Close, want nil", startErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after Close")
	}
}

// pollBlockingPoller mirrors kgo's BlockRebalanceOnPoll accounting: every
// poll, including one answered with the context's error, blocks rebalancing
// until AllowRebalance.
type pollBlockingPoller struct {
	noSeek
	mu       sync.Mutex
	blocking int
}

func (p *pollBlockingPoller) PollRecords(ctx context.Context, _ int) kgo.Fetches {
	p.mu.Lock()
	p.blocking++
	p.mu.Unlock()
	<-ctx.Done()
	return kgo.NewErrFetch(ctx.Err())
}

func (p *pollBlockingPoller) CommitRecords(context.Context, ...*kgo.Record) error { return nil }

func (p *pollBlockingPoller) AllowRebalance() {
	p.mu.Lock()
	p.blocking = 0
	p.mu.Unlock()
}

// Start must not return with a poll still blocking rebalances: leaving the
// group on Close would wait for it forever.
func TestConsumerStartReleasesRebalanceBlockOnReturn(t *testing.T) {
	poller := &pollBlockingPoller{}
	consumer := &Consumer{poller: poller, logger: logrus.New(), handlers: map[string]Handler{}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- consumer.Start(ctx) }()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Start error = %v, want context cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after cancellation")
	}
	poller.mu.Lock()
	defer poller.mu.Unlock()
	if poller.blocking != 0 {
		t.Fatalf("Start returned with %d poll(s) blocking rebalances", poller.blocking)
	}
}
