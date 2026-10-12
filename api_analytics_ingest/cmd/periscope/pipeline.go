package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"frameworks/api_analytics_ingest/internal/database/periscopeingestdb"
	"frameworks/api_analytics_ingest/internal/handlers"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// dlqPublisher publishes a dead-lettered record. *kafka.KafkaProducer
// implements it; nil disables dead-lettering.
type dlqPublisher interface {
	ProduceMessage(topic string, key []byte, value []byte, headers map[string]string) error
}

type ingestConsumerConfig struct {
	brokers        []string
	groupID        string
	clusterID      string
	clientID       string
	topics         ingestTopics
	mirrorPrefixes []string
	dlqTopic       string
}

// newIngestConsumer builds the Kafka consumer that projects every subscribed
// topic into ClickHouse through analyticsHandler, and returns it with the
// topics it subscribed.
func newIngestConsumer(cfg ingestConsumerConfig, analyticsHandler *handlers.AnalyticsHandler, metrics *handlers.PeriscopeMetrics, dlq dlqPublisher, logger logging.Logger) (*kafka.Consumer, []string, error) {
	failures := consumerFailurePolicy{dlq: dlq, dlqTopic: cfg.dlqTopic, metrics: metrics, logger: logger}
	flusher := &ingestBatchFlusher{newBuffer: analyticsHandler.NewWriteBuffer, failures: failures, metrics: metrics, logger: logger}
	consumer, err := kafka.NewConsumer(cfg.brokers, cfg.groupID, cfg.clusterID, cfg.clientID, logger,
		kafka.WithLagTracker(kafka.LagTrackerConfig{Gauge: metrics.KafkaLag}),
		kafka.WithBatchFlusher(flusher),
	)
	if err != nil {
		return nil, nil, err
	}

	wrapWithDLQ := func(consumerName string, handler messageHandler) messageHandler {
		return failures.wrap(consumerName, handler, true)
	}
	wrapRetryOnly := func(consumerName string, handler messageHandler) messageHandler {
		return failures.wrap(consumerName, handler, false)
	}

	eventHandler := kafka.NewAnalyticsEventContextHandler(analyticsHandler.HandleAnalyticsEventContext, logger)
	serviceHandler := func(ctx context.Context, msg kafka.Message) error {
		var event kafka.ServiceEvent
		if err := json.Unmarshal(msg.Value, &event); err != nil {
			logger.WithError(err).Error("Failed to unmarshal service event")
			return fmt.Errorf("unmarshal service event: %w", err)
		}
		for k, v := range msg.Headers {
			if k == "source" && event.Source == "" {
				event.Source = v
			}
			if k == "tenant_id" && event.TenantID == "" {
				event.TenantID = v
			}
			if k == "event_type" && event.EventType == "" {
				event.EventType = v
			}
			// Envelope headers — propagate to the event when the producer
			// didn't stamp the body. Decklog backfills source on emit so
			// the headers normally agree; honor either source.
			if k == "source_region" && event.SourceRegion == "" {
				event.SourceRegion = v
			}
			if k == "source_cluster_id" && event.SourceClusterID == "" {
				event.SourceClusterID = v
			}
			if k == "stream_origin_region" && event.StreamOriginRegion == "" {
				event.StreamOriginRegion = v
			}
			if k == "stream_origin_cluster_id" && event.StreamOriginClusterID == "" {
				event.StreamOriginClusterID = v
			}
		}
		return analyticsHandler.HandleServiceEventContext(ctx, event)
	}
	// Decklog republishes the original MistTrigger envelope for final and
	// accounting triggers to the raw journal topic; HandleRawMistTriggerMessage
	// logs and drops poison protobuf payloads itself.
	subscriptions := ingestSubscriptions(
		cfg.topics,
		ingestHandlers{
			analytics:     eventHandler.HandleMessage,
			serviceEvents: serviceHandler,
			domainEvents:  analyticsHandler.HandleDomainEventMessage,
			rawTriggers:   analyticsHandler.HandleRawMistTriggerMessage,
		},
		wrapWithDLQ,
		wrapRetryOnly,
	)
	// MIRROR_REGION_PREFIXES lists the MirrorMaker2 source aliases whose
	// prefixed topic copies land in this aggregator Kafka cluster.
	subscribedTopics := registerIngestSubscriptions(consumer, subscriptions, cfg.mirrorPrefixes)
	return consumer, subscribedTopics, nil
}

// consumerFailurePolicy decides what happens to a record whose handler
// failed: a transient dependency failure is retried in place, and any other
// failure is dead-lettered when the subscription allows it.
type consumerFailurePolicy struct {
	dlq      dlqPublisher
	dlqTopic string
	metrics  *handlers.PeriscopeMetrics
	logger   logging.Logger
}

func (p consumerFailurePolicy) wrap(consumerName string, handler messageHandler, useDLQ bool) messageHandler {
	logger, metrics := p.logger, p.metrics
	return func(ctx context.Context, msg kafka.Message) error {
		start := time.Now()
		var buffer *periscopeingestdb.WriteBuffer
		origin := &recordOrigin{msg: msg, consumerName: consumerName, useDLQ: useDLQ}
		if batch := ingestBatchFrom(ctx); batch != nil {
			batch.published = append(batch.published, msg.Timestamp)
			buffer = batch.buffer
			ctx = periscopeingestdb.WithRowOrigin(ctx, origin)
		}
		err := handler(ctx, msg)
		for attempt := 1; err != nil && isRetryableConsumerError(err); attempt++ {
			delay := retryableConsumerBackoff(attempt)
			logger.WithError(err).WithFields(logging.Fields{
				"topic":       msg.Topic,
				"partition":   msg.Partition,
				"offset":      msg.Offset,
				"attempt":     attempt,
				"retry_in":    delay.String(),
				"consumer":    consumerName,
				"dlq_enabled": useDLQ,
			}).Warn("Handler dependency failed; retrying message")

			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
			case <-timer.C:
			}
			if ctx.Err() != nil {
				err = ctx.Err()
				break
			}

			// The failed attempt's rows that are still buffered would
			// otherwise be inserted next to the retry's copy.
			if buffer != nil {
				buffer.DiscardOrigin(origin)
			}
			err = handler(ctx, msg)
		}
		if metrics.KafkaDuration != nil {
			metrics.KafkaDuration.WithLabelValues("consume").Observe(time.Since(start).Seconds())
		}
		if metrics.KafkaMessages != nil {
			status := "ok"
			if err != nil {
				status = "error"
			}
			metrics.KafkaMessages.WithLabelValues(msg.Topic, "consume", status).Inc()
		}
		if err == nil {
			return nil
		}
		return p.settle(consumerName, msg, err, useDLQ)
	}
}

// settle returns nil once a failed record is dead-lettered, and the failure
// itself when the record must stay uncommitted.
func (p consumerFailurePolicy) settle(consumerName string, msg kafka.Message, err error, useDLQ bool) error {
	logger := p.logger
	if isRetryableConsumerError(err) {
		logger.WithError(err).WithFields(logging.Fields{
			"topic":     msg.Topic,
			"partition": msg.Partition,
			"offset":    msg.Offset,
		}).Warn("Handler dependency failed; leaving message uncommitted for retry")
		return err
	}
	if !useDLQ || p.dlq == nil {
		return err
	}

	payload, encodeErr := kafka.EncodeDLQMessage(msg, err, consumerName)
	if encodeErr != nil {
		logger.WithError(encodeErr).WithFields(logging.Fields{
			"topic":     msg.Topic,
			"partition": msg.Partition,
			"offset":    msg.Offset,
		}).Error("Failed to encode DLQ payload")
		return encodeErr
	}

	key := msg.Key
	if len(key) == 0 {
		key = []byte(fmt.Sprintf("%s:%d:%d", msg.Topic, msg.Partition, msg.Offset))
	}

	headers := dlqHeaders(consumerName, msg)
	if produceErr := p.dlq.ProduceMessage(p.dlqTopic, key, payload, headers); produceErr != nil {
		logger.WithError(produceErr).WithFields(logging.Fields{
			"topic":     msg.Topic,
			"partition": msg.Partition,
			"offset":    msg.Offset,
		}).Error("Failed to publish message to DLQ")
		return produceErr
	}

	if p.metrics.DLQMessages != nil {
		p.metrics.DLQMessages.WithLabelValues(msg.Topic, fmt.Sprintf("%T", err)).Inc()
	}

	logger.WithError(err).WithFields(logging.Fields{
		"topic":     msg.Topic,
		"partition": msg.Partition,
		"offset":    msg.Offset,
		"dlq_topic": p.dlqTopic,
	}).Warn("Message sent to DLQ after handler error")
	return nil
}
