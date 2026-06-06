package kafka

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
)

// Message represents a generic Kafka message
type Message struct {
	Key       []byte
	Value     []byte
	Headers   map[string]string
	Topic     string
	Partition int32
	Offset    int64
	Timestamp time.Time
}

// Handler is a function that processes a Kafka message
type Handler func(ctx context.Context, msg Message) error

// Consumer implements a generic Kafka consumer that routes messages to handlers
type Consumer struct {
	client    *kgo.Client
	logger    *logrus.Logger
	clusterID string
	groupID   string
	handlers  map[string]Handler
	mu        sync.RWMutex

	// lagTracker is non-nil when the caller passed WithLagTracker.
	// Start() launches a sibling goroutine that periodically samples
	// end-offset vs committed-offset per (topic, partition) and
	// publishes the difference to lagTracker.Gauge.
	lagTracker *LagTrackerConfig
}

// ConsumerOption configures a Consumer at construction time. Callers wanting
// non-default behaviour (e.g. broadcast/fanout consumer groups that must not
// replay history) pass these to NewConsumer.
type ConsumerOption func(*consumerOptions)

type consumerOptions struct {
	resetOffset kgo.Offset
	lagTracker  *LagTrackerConfig
}

// LagTrackerConfig configures the background lag tracker. Gauge labels
// must be ["topic", "partition"].
type LagTrackerConfig struct {
	Gauge    *prometheus.GaugeVec
	Interval time.Duration // defaults to 30s when zero
}

// WithLagTracker enables a background goroutine that samples consumer-group
// lag (end_offset - committed_offset) per (topic, partition) on a fixed
// interval and publishes the result to the provided GaugeVec. The tracker
// shares the consumer's existing kgo client via kadm; no second connection.
// The goroutine exits when the context passed to Start is cancelled.
func WithLagTracker(cfg LagTrackerConfig) ConsumerOption {
	return func(o *consumerOptions) {
		o.lagTracker = &cfg
	}
}

// WithResetOffsetLatest configures the consumer to start at the end of the
// log on first start (no committed offsets for the group). Use for per-instance
// broadcast/fanout groups where replaying retained history to live clients is
// not desired. Durable competing-consumer groups (analytics ingest, billing)
// must stay on the default (earliest).
func WithResetOffsetLatest() ConsumerOption {
	return func(o *consumerOptions) {
		o.resetOffset = kgo.NewOffset().AtEnd()
	}
}

// NewConsumer creates a new Kafka consumer. Default reset offset is AtStart
// (earliest), which is correct for durable consumers; broadcast groups should
// pass WithResetOffsetLatest.
func NewConsumer(brokers []string, groupID string, clusterID string, clientID string, logger *logrus.Logger, options ...ConsumerOption) (*Consumer, error) {
	cfg := consumerOptions{
		resetOffset: kgo.NewOffset().AtStart(),
	}
	for _, opt := range options {
		opt(&cfg)
	}

	opts := []kgo.Opt{
		kgo.SeedBrokers(brokers...),
		kgo.ConsumerGroup(groupID),
		kgo.ClientID(clientID),
		kgo.ConsumeResetOffset(cfg.resetOffset),
		kgo.DisableAutoCommit(),
		kgo.BlockRebalanceOnPoll(),
	}

	client, err := kgo.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create kafka client: %w", err)
	}

	return &Consumer{
		client:     client,
		logger:     logger,
		clusterID:  clusterID,
		groupID:    groupID,
		handlers:   make(map[string]Handler),
		lagTracker: cfg.lagTracker,
	}, nil
}

// AddHandler registers a handler for a specific topic and subscribes to it
func (c *Consumer) AddHandler(topic string, handler Handler) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.handlers[topic] = handler
	c.client.AddConsumeTopics(topic)
}

// Close closes the underlying client
func (c *Consumer) Close() error {
	c.client.Close()
	return nil
}

// lagFetcher is the minimal slice of kadm.Client used by the lag tracker.
// Defined as an interface so tests can inject a fake without a broker.
type lagFetcher interface {
	ListEndOffsets(ctx context.Context, topics ...string) (kadm.ListedOffsets, error)
	FetchOffsetsForTopics(ctx context.Context, group string, topics ...string) (kadm.OffsetResponses, error)
}

// topics returns the topics this consumer has handlers registered for.
func (c *Consumer) topics() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, 0, len(c.handlers))
	for topic := range c.handlers {
		out = append(out, topic)
	}
	return out
}

// publishLag samples end_offset - committed_offset for each (topic,
// partition) the consumer reads and updates the configured gauge. Returns
// an error only when both admin calls fail; partial data still gets
// published. Tests inject lagFetcher so no broker is required.
func (c *Consumer) publishLag(ctx context.Context, fetcher lagFetcher, topics []string) error {
	if c.lagTracker == nil || c.lagTracker.Gauge == nil || len(topics) == 0 {
		return nil
	}

	ends, err := fetcher.ListEndOffsets(ctx, topics...)
	if err != nil {
		return fmt.Errorf("lag fetch end offsets: %w", err)
	}
	commits, err := fetcher.FetchOffsetsForTopics(ctx, c.groupID, topics...)
	if err != nil {
		return fmt.Errorf("lag fetch committed offsets: %w", err)
	}

	committedByTP := make(map[string]map[int32]int64)
	commits.Each(func(r kadm.OffsetResponse) {
		if r.Err != nil {
			return
		}
		if _, ok := committedByTP[r.Topic]; !ok {
			committedByTP[r.Topic] = make(map[int32]int64)
		}
		committedByTP[r.Topic][r.Partition] = r.At
	})

	ends.Each(func(o kadm.ListedOffset) {
		if o.Err != nil {
			return
		}
		committed := int64(0)
		if perTopic, ok := committedByTP[o.Topic]; ok {
			if v, ok2 := perTopic[o.Partition]; ok2 {
				committed = v
			}
		}
		lag := o.Offset - committed
		if lag < 0 {
			lag = 0
		}
		c.lagTracker.Gauge.WithLabelValues(o.Topic, strconv.Itoa(int(o.Partition))).Set(float64(lag))
	})
	return nil
}

// runLagTracker is the background goroutine launched from Start when the
// consumer was constructed with WithLagTracker.
func (c *Consumer) runLagTracker(ctx context.Context) {
	interval := c.lagTracker.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	adm := kadm.NewClient(c.client)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			topics := c.topics()
			if len(topics) == 0 {
				continue
			}
			if err := c.publishLag(ctx, adm, topics); err != nil {
				// Admin errors are transient (broker rolling, network blip);
				// keep previous gauge values and try again next tick.
				if ctx.Err() == nil {
					c.logger.WithError(err).Debug("kafka lag sample failed")
				}
			}
		}
	}
}

// Start starts polling for messages
func (c *Consumer) Start(ctx context.Context) error {
	if c.lagTracker != nil && c.lagTracker.Gauge != nil {
		go c.runLagTracker(ctx)
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			fetches := c.client.PollFetches(ctx)
			if errs := fetches.Errors(); len(errs) > 0 {
				// Don't log context cancelled errors as errors
				if ctx.Err() != nil {
					return ctx.Err()
				}
				c.logger.Errorf("errors while polling: %v", errs)
				continue
			}

			iter := fetches.RecordIter()
			records := make([]*kgo.Record, 0)
			for !iter.Done() {
				records = append(records, iter.Next())
			}

			commitRecords := c.processRecords(ctx, records)
			if len(commitRecords) > 0 {
				if err := c.client.CommitRecords(ctx, commitRecords...); err != nil {
					if isRecoverableGroupCommitError(err) {
						return fmt.Errorf("kafka consumer group membership lost during commit: %w", err)
					}
					c.logger.WithError(err).Error("failed to commit records")
				}
			}
		}
	}
}

func isRecoverableGroupCommitError(err error) bool {
	return errors.Is(err, kerr.UnknownMemberID) ||
		errors.Is(err, kerr.IllegalGeneration) ||
		errors.Is(err, kerr.RebalanceInProgress) ||
		errors.Is(err, kerr.FencedInstanceID)
}

func (c *Consumer) processRecords(ctx context.Context, records []*kgo.Record) []*kgo.Record {
	type topicPartition struct {
		topic     string
		partition int32
	}
	blocked := make(map[topicPartition]bool)
	lastSuccess := make(map[topicPartition]*kgo.Record)

	for _, record := range records {
		tp := topicPartition{topic: record.Topic, partition: record.Partition}
		if blocked[tp] {
			// A prior message in this topic/partition failed. We must not
			// process or commit later offsets, otherwise we'd skip the failed
			// message on restart.
			continue
		}

		c.mu.RLock()
		handler, exists := c.handlers[record.Topic]
		c.mu.RUnlock()

		if !exists {
			// No handler registered - still commit to avoid reprocessing
			c.logger.WithField("topic", record.Topic).Warn("No handler registered for topic")
			lastSuccess[tp] = record
			continue
		}

		hdrs := make(map[string]string, len(record.Headers))
		for _, h := range record.Headers {
			hdrs[h.Key] = string(h.Value)
		}

		msg := Message{
			Key:       record.Key,
			Value:     record.Value,
			Headers:   hdrs,
			Topic:     record.Topic,
			Partition: record.Partition,
			Offset:    record.Offset,
			Timestamp: record.Timestamp,
		}

		if err := handler(ctx, msg); err != nil {
			c.logger.WithError(err).WithFields(logrus.Fields{
				"topic":     record.Topic,
				"partition": record.Partition,
				"offset":    record.Offset,
			}).Error("Failed to handle message - will retry on restart")
			// Block this partition to avoid committing offsets beyond the failed message.
			blocked[tp] = true
			continue
		}

		lastSuccess[tp] = record
	}

	if len(lastSuccess) == 0 {
		return nil
	}

	commitRecords := make([]*kgo.Record, 0, len(lastSuccess))
	for _, record := range lastSuccess {
		commitRecords = append(commitRecords, record)
	}
	return commitRecords
}

// HealthCheck pings the broker
func (c *Consumer) HealthCheck() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := c.client.Ping(ctx); err != nil {
		return fmt.Errorf("kafka health check failed: %w", err)
	}
	return nil
}

func (c *Consumer) GetClient() *kgo.Client {
	return c.client
}

func (c *Consumer) GetMetrics() (map[string]interface{}, error) {
	metrics := map[string]interface{}{
		"cluster_id": c.clusterID,
		"group_id":   c.groupID,
	}
	return metrics, nil
}
