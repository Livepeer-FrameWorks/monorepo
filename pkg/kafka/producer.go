package kafka

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/twmb/franz-go/pkg/kgo"
)

// KafkaProducer implements KafkaProducerInterface
type KafkaProducer struct {
	client    *kgo.Client
	logger    *logrus.Logger
	clusterID string
}

// NewKafkaProducer creates a new Kafka producer
func NewKafkaProducer(brokers []string, clusterID string, logger *logrus.Logger) (*KafkaProducer, error) {
	opts := []kgo.Opt{
		kgo.SeedBrokers(brokers...),
		kgo.ClientID("decklog"),
		kgo.ProducerBatchCompression(kgo.SnappyCompression()),
		kgo.ProducerLinger(10 * time.Millisecond),
		kgo.ProducerBatchMaxBytes(1000000),
	}

	client, err := kgo.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create kafka client: %w", err)
	}

	return &KafkaProducer{
		client:    client,
		logger:    logger,
		clusterID: clusterID,
	}, nil
}

func (p *KafkaProducer) Close() error {
	p.client.Close()
	return nil
}

func (p *KafkaProducer) ProduceMessage(topic string, key []byte, value []byte, headers map[string]string) error {
	record := &kgo.Record{
		Topic: topic,
		Key:   key,
		Value: value,
	}

	// Add headers if any
	if len(headers) > 0 {
		for k, v := range headers {
			record.Headers = append(record.Headers, kgo.RecordHeader{
				Key:   k,
				Value: []byte(v),
			})
		}
	}

	// Produce with context for timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := p.client.ProduceSync(ctx, record)
	if err := result.FirstErr(); err != nil {
		return fmt.Errorf("failed to produce message: %w", err)
	}

	return nil
}

func (p *KafkaProducer) HealthCheck() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := p.client.Ping(ctx); err != nil {
		return fmt.Errorf("kafka health check failed: %w", err)
	}
	return nil
}

func (p *KafkaProducer) GetMetrics() (map[string]interface{}, error) {
	metrics := map[string]interface{}{
		"cluster_id": p.clusterID,
	}
	return metrics, nil
}

// GetClient returns the underlying kgo.Client for health checks
func (p *KafkaProducer) GetClient() *kgo.Client {
	return p.client
}

// PublishTypedEvent publishes a single typed AnalyticsEvent
func (p *KafkaProducer) PublishTypedEvent(event *AnalyticsEvent) error {
	if event == nil {
		return fmt.Errorf("event cannot be nil")
	}

	// Marshal event to JSON
	value, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal event: %w", err)
	}

	// Create headers
	headers := map[string]string{
		"source":     event.Source,
		"event_type": event.EventType,
	}
	if event.TenantID != "" {
		headers["tenant_id"] = event.TenantID
	}

	// Publish to Kafka
	return p.ProduceMessage("analytics_events", []byte(event.EventID), value, headers)
}

// PublishTypedBatch publishes a batch of typed AnalyticsEvents
func (p *KafkaProducer) PublishTypedBatch(events []AnalyticsEvent) error {
	if len(events) == 0 {
		return nil // Nothing to publish
	}

	// Convert each event to a Kafka record
	var records []*kgo.Record
	for _, event := range events {
		// Marshal the event
		value, err := json.Marshal(event)
		if err != nil {
			return fmt.Errorf("failed to marshal event %s: %w", event.EventID, err)
		}

		// Create record with headers
		record := &kgo.Record{
			Topic: "analytics_events",
			Key:   []byte(event.EventID),
			Value: value,
			Headers: []kgo.RecordHeader{
				{Key: "source", Value: []byte(event.Source)},
				{Key: "event_type", Value: []byte(event.EventType)},
			},
		}

		// Add tenant_id header if present
		if event.TenantID != "" {
			record.Headers = append(record.Headers, kgo.RecordHeader{
				Key:   "tenant_id",
				Value: []byte(event.TenantID),
			})
		}

		records = append(records, record)
	}

	// Produce all records with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	results := p.client.ProduceSync(ctx, records...)
	if err := results.FirstErr(); err != nil {
		return fmt.Errorf("failed to produce batch: %w", err)
	}

	return nil
}
