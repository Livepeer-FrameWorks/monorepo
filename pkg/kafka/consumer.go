package kafka

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/twmb/franz-go/pkg/kgo"
)

// Consumer implements ConsumerInterface
type Consumer struct {
	client       *kgo.Client
	logger       *logrus.Logger
	clusterID    string
	groupID      string
	eventHandler EventHandler
}

// NewConsumer creates a new Kafka consumer
func NewConsumer(brokers []string, groupID string, clusterID string, clientID string, logger *logrus.Logger, handler EventHandler) (*Consumer, error) {
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
		client:       client,
		logger:       logger,
		clusterID:    clusterID,
		groupID:      groupID,
		eventHandler: handler,
	}, nil
}

func (c *Consumer) Close() error {
	c.client.Close()
	return nil
}

func (c *Consumer) Subscribe(topics []string) error {
	c.client.AddConsumeTopics(topics...)
	return nil
}

func (c *Consumer) Start(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			fetches := c.client.PollFetches(ctx)
			if errs := fetches.Errors(); len(errs) > 0 {
				c.logger.Errorf("errors while polling: %v", errs)
				continue
			}

			iter := fetches.RecordIter()
			var records []*kgo.Record

			for !iter.Done() {
				record := iter.Next()
				records = append(records, record)

				var event Event
				if err := json.Unmarshal(record.Value, &event); err != nil {
					c.logger.WithError(err).Error("failed to unmarshal event")
					continue
				}

				// Extract headers
				for _, header := range record.Headers {
					switch header.Key {
					case "source":
						event.Source = string(header.Value)
					case "tenant_id":
						event.TenantID = string(header.Value)
					case "channel":
						event.Channel = string(header.Value)
					}
				}

				if err := c.eventHandler.HandleEvent(event); err != nil {
					c.logger.WithError(err).Error("failed to handle event")
					continue
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
