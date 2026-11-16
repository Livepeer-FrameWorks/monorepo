package kafka

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
)

// Broadcast reads every partition for one in-memory replica. It never joins a
// consumer group or resumes another process's committed offsets. Open captures
// the live boundary before the caller hydrates its authoritative snapshot.
type Broadcast struct {
	client     *kgo.Client
	admin      *kadm.Client
	topics     []string
	mu         sync.Mutex
	positions  map[string]map[int32]int64
	changed    chan struct{}
	done       chan struct{}
	err        error
	finishOnce sync.Once
}

func OpenBroadcast(ctx context.Context, brokers, topics []string, clientID string) (*Broadcast, error) {
	if len(brokers) == 0 || len(topics) == 0 {
		return nil, errors.New("broadcast brokers and topics are required")
	}
	client, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.ClientID(clientID), kgo.KeepControlRecords(), kgo.FetchMaxWait(250*time.Millisecond))
	if err != nil {
		return nil, err
	}
	b := &Broadcast{client: client, admin: kadm.NewClient(client), topics: append([]string(nil), topics...), positions: map[string]map[int32]int64{}, changed: make(chan struct{}), done: make(chan struct{})}
	ends, err := b.ends(ctx)
	if err != nil {
		client.Close()
		return nil, err
	}
	ends.Each(func(o kadm.ListedOffset) {
		if b.positions[o.Topic] == nil {
			b.positions[o.Topic] = map[int32]int64{}
		}
		b.positions[o.Topic][o.Partition] = o.Offset
	})
	client.AddConsumePartitions(ends.KOffsets())
	return b, nil
}

func (b *Broadcast) ends(ctx context.Context) (kadm.ListedOffsets, error) {
	ends, err := b.admin.ListEndOffsets(ctx, b.topics...)
	if err == nil {
		err = ends.Error()
	}
	if err != nil {
		return nil, fmt.Errorf("broadcast partition boundary: %w", err)
	}
	for _, topic := range b.topics {
		if len(ends[topic]) == 0 {
			return nil, fmt.Errorf("broadcast topic %q has no partitions", topic)
		}
	}
	return ends, nil
}

// Run advances a partition only after its handler applied the record. Any
// delivery/decoding failure ends this reader so its owner can rehydrate safely.
func (b *Broadcast) Run(ctx context.Context, handle Handler) (err error) {
	defer func() { b.finish(err) }()
	for ctx.Err() == nil {
		fetches := b.client.PollRecords(ctx, defaultMaxPollRecords)
		if err := fetches.Err(); err != nil {
			return err
		}
		for _, record := range fetches.Records() {
			headers := make(map[string]string, len(record.Headers))
			for _, header := range record.Headers {
				headers[header.Key] = string(header.Value)
			}
			if err := handle(ctx, Message{Topic: record.Topic, Partition: record.Partition, Offset: record.Offset, Key: record.Key, Value: record.Value, Headers: headers, Timestamp: record.Timestamp}); err != nil {
				return err
			}
			b.mu.Lock()
			b.positions[record.Topic][record.Partition] = record.Offset + 1
			close(b.changed)
			b.changed = make(chan struct{})
			b.mu.Unlock()
		}
	}
	return ctx.Err()
}

// CatchUp waits until all records through a newly observed boundary have been
// applied. New partitions require a new reader and a recovery snapshot.
func (b *Broadcast) CatchUp(ctx context.Context) error {
	ends, err := b.ends(ctx)
	if err != nil {
		return err
	}
	for {
		b.mu.Lock()
		caught := true
		var partitionErr error
		ends.Each(func(o kadm.ListedOffset) {
			position, ok := b.positions[o.Topic][o.Partition]
			if !ok {
				partitionErr = fmt.Errorf("broadcast partition set changed: %s/%d", o.Topic, o.Partition)
			}
			if position < o.Offset {
				caught = false
			}
		})
		changed, doneErr := b.changed, b.err
		b.mu.Unlock()
		if partitionErr != nil {
			b.Close()
			return partitionErr
		}
		select {
		case <-b.done:
			if doneErr != nil {
				return doneErr
			}
			return errors.New("broadcast closed")
		default:
		}
		if caught {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-b.done:
			return errors.New("broadcast stopped during catch-up")
		case <-changed:
		}
	}
}

func (b *Broadcast) finish(err error) {
	b.finishOnce.Do(func() { b.mu.Lock(); b.err = err; close(b.done); b.mu.Unlock() })
}

func (b *Broadcast) Close() { b.finish(context.Canceled); b.client.Close() }
