package consumer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// DLQProducer writes one dead-letter record.
type DLQProducer interface {
	ProduceMessage(topic string, key []byte, value []byte, headers map[string]string) error
}

// DeadLetter returns the wrapper that sends records failing with
// ErrUndecodable to topic and reports them handled. Any other error is
// returned unchanged. A failed dead-letter write is retried in place until it
// succeeds or ctx ends, so the record's offset never commits before the record
// is kept somewhere.
func DeadLetter(producer DLQProducer, topic string, logger logging.Logger) func(name string, h kafka.Handler) kafka.Handler {
	return func(name string, h kafka.Handler) kafka.Handler {
		return func(ctx context.Context, msg kafka.Message) error {
			err := h(ctx, msg)
			if err == nil || !errors.Is(err, ErrUndecodable) {
				return err
			}
			payload, encodeErr := kafka.EncodeDLQMessage(msg, err, name)
			if encodeErr != nil {
				return fmt.Errorf("encode dead-letter record: %w", encodeErr)
			}
			key := msg.Key
			if len(key) == 0 {
				key = []byte(fmt.Sprintf("%s:%d:%d", msg.Topic, msg.Partition, msg.Offset))
			}
			headers := map[string]string{"source": name, "original_topic": msg.Topic}
			for _, h := range []string{events.HeaderTenantID, events.HeaderID} {
				if v, ok := msg.Headers[h]; ok {
					headers[h] = v
				}
			}
			if v, ok := msg.Headers[events.HeaderType]; ok {
				headers["event_type"] = v
			}
			delay := 250 * time.Millisecond
			for {
				produceErr := producer.ProduceMessage(topic, key, payload, headers)
				if produceErr == nil {
					if logger != nil {
						logger.WithError(err).WithFields(logging.Fields{
							"topic": msg.Topic, "partition": msg.Partition, "offset": msg.Offset, "dlq_topic": topic,
						}).Warn("Domain event record sent to the DLQ")
					}
					return nil
				}
				if logger != nil {
					logger.WithError(produceErr).WithField("dlq_topic", topic).Error("Writing a dead-letter record failed; retrying")
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(delay):
				}
				delay = min(delay*2, 30*time.Second)
			}
		}
	}
}
