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

// PublishBatch publishes a batch of events
func (p *KafkaProducer) PublishBatch(batch interface{}) error {
	// Convert batch to JSON to get the common fields
	batchJSON, err := json.Marshal(batch)
	if err != nil {
		return fmt.Errorf("failed to marshal batch: %w", err)
	}

	var batchMap map[string]interface{}
	if err := json.Unmarshal(batchJSON, &batchMap); err != nil {
		return fmt.Errorf("failed to unmarshal batch: %w", err)
	}

	// Extract common fields
	batchID, _ := batchMap["batch_id"].(string)
	source, _ := batchMap["source"].(string)
	tenantID, _ := batchMap["tenant_id"].(string)
	events, ok := batchMap["events"].([]interface{})
	if !ok {
		return fmt.Errorf("batch does not contain events array")
	}

	// Convert each event to a Kafka record
	var records []*kgo.Record
	for _, event := range events {
		// Re-marshal the individual event
		value, err := json.Marshal(event)
		if err != nil {
			return fmt.Errorf("failed to marshal event: %w", err)
		}

		// Extract event ID and type for headers
		eventMap := event.(map[string]interface{})
		eventID, _ := eventMap["event_id"].(string)
		eventType, _ := eventMap["event_type"].(string)

		// Prefer tenant_id from the event payload if available
		eventTenantID := ""
		if dm, ok := eventMap["data"].(map[string]interface{}); ok {
			if v, ok := dm["tenant_id"].(string); ok {
				eventTenantID = v
			}
		}

		// Create record with common headers
		record := &kgo.Record{
			Topic: "analytics_events",
			Key:   []byte(eventID),
			Value: value,
			Headers: []kgo.RecordHeader{
				{Key: "batch_id", Value: []byte(batchID)},
				{Key: "source", Value: []byte(source)},
				{Key: "event_type", Value: []byte(eventType)},
			},
		}
		if eventTenantID != "" {
			record.Headers = append(record.Headers, kgo.RecordHeader{Key: "tenant_id", Value: []byte(eventTenantID)})
		} else if tenantID != "" {
			record.Headers = append(record.Headers, kgo.RecordHeader{Key: "tenant_id", Value: []byte(tenantID)})
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
