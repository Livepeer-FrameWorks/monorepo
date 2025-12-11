package kafka

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
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
}

// NewConsumer creates a new Kafka consumer
func NewConsumer(brokers []string, groupID string, clusterID string, clientID string, logger *logrus.Logger) (*Consumer, error) {
	opts := []kgo.Opt{
		kgo.SeedBrokers(brokers...),
		kgo.ConsumerGroup(groupID),
		kgo.ClientID(clientID),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.DisableAutoCommit(),
		kgo.BlockRebalanceOnPoll(),
	}

	client, err := kgo.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create kafka client: %w", err)
	}

	return &Consumer{
		client:    client,
		logger:    logger,
		clusterID: clusterID,
		groupID:   groupID,
		handlers:  make(map[string]Handler),
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

// Start starts polling for messages
func (c *Consumer) Start(ctx context.Context) error {
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
			var records []*kgo.Record

			for !iter.Done() {
				record := iter.Next()
				records = append(records, record)

				c.mu.RLock()
				handler, exists := c.handlers[record.Topic]
				c.mu.RUnlock()

				if !exists {
					// Try to find a default/wildcard handler or just log
					// For now, we just skip
					c.logger.WithField("topic", record.Topic).Warn("No handler registered for topic")
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
					}).Error("Failed to handle message")
					// Continue processing other messages, don't break the loop
				}
			}

			if len(records) > 0 {
				if err := c.client.CommitRecords(ctx, records...); err != nil {
					c.logger.WithError(err).Error("failed to commit records")
				}
			}
		}
	}
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
