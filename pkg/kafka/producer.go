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
	client       *kgo.Client
	logger       *logrus.Logger
	clusterID    string
	defaultTopic string
}

// NewKafkaProducer creates a new Kafka producer
func NewKafkaProducer(brokers []string, defaultTopic string, clusterID string, logger *logrus.Logger) (*KafkaProducer, error) {
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
		client:       client,
		logger:       logger,
		clusterID:    clusterID,
		defaultTopic: defaultTopic,
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

// analyticsEventHeaders builds the Kafka header set for an AnalyticsEvent.
// `source` and `event_type` are always emitted (treated as required envelope);
// the rest are conditional so empty values don't pollute headers that
// downstream backfill keys off "header present".
func analyticsEventHeaders(event *AnalyticsEvent) map[string]string {
	headers := map[string]string{
		"source":     event.Source,
		"event_type": event.EventType,
	}
	conditional := [...]struct {
		key string
		val string
	}{
		{"event_id", event.EventID},
		{"tenant_id", event.TenantID},
		{"source_region", event.SourceRegion},
		{"source_cluster_id", event.SourceClusterID},
		{"stream_origin_region", event.StreamOriginRegion},
		{"stream_origin_cluster_id", event.StreamOriginClusterID},
	}
	for _, c := range conditional {
		if c.val != "" {
			headers[c.key] = c.val
		}
	}
	return headers
}

// PublishTypedEvent publishes a single typed AnalyticsEvent
func (p *KafkaProducer) PublishTypedEvent(event *AnalyticsEvent) error {
	if event == nil {
		return fmt.Errorf("event cannot be nil")
	}

	value, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal event: %w", err)
	}

	return p.ProduceMessage(p.defaultTopic, []byte(event.EventID), value, analyticsEventHeaders(event))
}

// PublishTypedBatch publishes a batch of typed AnalyticsEvents
func (p *KafkaProducer) PublishTypedBatch(events []AnalyticsEvent) error {
	if len(events) == 0 {
		return nil // Nothing to publish
	}

	records := make([]*kgo.Record, 0, len(events))
	for i := range events {
		event := &events[i]
		value, err := json.Marshal(event)
		if err != nil {
			return fmt.Errorf("failed to marshal event %s: %w", event.EventID, err)
		}

		headers := analyticsEventHeaders(event)
		recordHeaders := make([]kgo.RecordHeader, 0, len(headers))
		for k, v := range headers {
			recordHeaders = append(recordHeaders, kgo.RecordHeader{Key: k, Value: []byte(v)})
		}

		records = append(records, &kgo.Record{
			Topic:   p.defaultTopic,
			Key:     []byte(event.EventID),
			Value:   value,
			Headers: recordHeaders,
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	results := p.client.ProduceSync(ctx, records...)
	if err := results.FirstErr(); err != nil {
		return fmt.Errorf("failed to produce batch: %w", err)
	}

	return nil
}
