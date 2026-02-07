package metering

import (
	"encoding/json"
	"fmt"

	"frameworks/pkg/kafka"
	"frameworks/pkg/logging"
	"frameworks/pkg/models"
)

type PublisherConfig struct {
	Brokers   []string
	ClusterID string
	Topic     string
	Source    string
	Logger    logging.Logger
}

type Publisher struct {
	producer *kafka.KafkaProducer
	topic    string
	source   string
	logger   logging.Logger
}

func NewPublisher(cfg PublisherConfig) (*Publisher, error) {
	if len(cfg.Brokers) == 0 {
		return nil, fmt.Errorf("kafka brokers required for billing publisher")
	}
	topic := cfg.Topic
	if topic == "" {
		topic = "billing.usage_reports"
	}
	source := cfg.Source
	if source == "" {
		source = "skipper"
	}
	clusterID := cfg.ClusterID
	if clusterID == "" {
		clusterID = "local"
	}
	producer, err := kafka.NewKafkaProducer(cfg.Brokers, topic, clusterID, cfg.Logger)
	if err != nil {
		return nil, err
	}
	return &Publisher{
		producer: producer,
		topic:    topic,
		source:   source,
		logger:   cfg.Logger,
	}, nil
}

func (p *Publisher) Close() error {
	if p == nil || p.producer == nil {
		return nil
	}
	return p.producer.Close()
}

func (p *Publisher) PublishUsageSummary(summary models.UsageSummary) error {
	if p == nil || p.producer == nil {
		return nil
	}
	payload, err := json.Marshal(summary)
	if err != nil {
		return fmt.Errorf("marshal skipper usage summary: %w", err)
	}
	err = p.producer.ProduceMessage(
		p.topic,
		[]byte(summary.TenantID),
		payload,
		map[string]string{
			"source":    p.source,
			"type":      "usage_summary",
			"tenant_id": summary.TenantID,
		},
	)
	if err != nil {
		return err
	}
	if p.logger != nil {
		p.logger.WithFields(logging.Fields{
			"tenant_id": summary.TenantID,
			"topic":     p.topic,
		}).Info("Published Skipper usage summary to billing")
	}
	return nil
}
