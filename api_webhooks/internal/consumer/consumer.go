// Package consumer turns domain.events records into webhook deliveries.
package consumer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"frameworks/api_webhooks/internal/ledger"
	"frameworks/api_webhooks/internal/metrics"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"
)

// GroupID is Bosun's durable consumer group. Every replica joins it, so each
// record is handled by one replica and offsets survive restarts.
const GroupID = "bosun"

// Handling results counted in bosun_domain_events_total.
const (
	ResultStored           = "stored"
	ResultNoSubscriber     = "no_subscriber"
	ResultDuplicate        = "duplicate"
	ResultNotPublic        = "not_public"
	ResultRegistryInternal = "registry_internal"
	ResultNoTenant         = "no_tenant"
	ResultUnknownType      = "unknown_type"
	ResultUndecodable      = "undecodable"
)

// ErrUndecodable marks a record that can never be stored; the dead-letter
// wrapper sends it to the DLQ topic.
var ErrUndecodable = errors.New("domain event record does not decode")

// Recorder stores an event and its deliveries.
type Recorder interface {
	RecordEvent(ctx context.Context, ev ledger.IncomingEvent) (ledger.RecordResult, error)
}

// Handler handles domain.events records.
type Handler struct {
	Recorder Recorder
	Metrics  *metrics.Metrics
	Logger   logging.Logger
	// RetryDelay is the first delay of the in-place retry of a failed
	// database write; it doubles up to 30 seconds.
	RetryDelay time.Duration
}

// Handle stores one record and returns nil only after the transaction that
// writes the event and its deliveries committed, or when the record is not
// for delivery. The shared Kafka consumer commits the record's offset only
// after Handle returns nil.
//
//   - A record whose ce_visibility header is not "public" is dropped before
//     its payload is decoded.
//   - A type this binary does not register is dropped, counted, and logged
//     at error level, so Bosun must be upgraded before a producer emits a
//     new public type.
//   - A registered internal type under a public header, and a public event
//     without a tenant, are dropped and counted.
//   - An undecodable record returns ErrUndecodable for the dead-letter
//     wrapper.
//   - A database failure is retried in place until it succeeds or ctx ends,
//     because returning an error would let later offsets of the partition
//     commit past this record.
func (h *Handler) Handle(ctx context.Context, msg kafka.Message) error {
	if msg.Headers[events.HeaderVisibility] != events.VisibilityPublic {
		h.Metrics.DomainEvent(ResultNotPublic)
		return nil
	}
	headers := make([]events.Header, 0, len(msg.Headers))
	for key, value := range msg.Headers {
		headers = append(headers, events.Header{Key: key, Value: []byte(value)})
	}
	rec, err := events.ParseRecord(msg.Key, headers, msg.Value)
	if err != nil {
		if errors.Is(err, events.ErrUnknownType) {
			h.Metrics.DomainEvent(ResultUnknownType)
			if h.Logger != nil {
				h.Logger.WithError(err).WithFields(logging.Fields{
					"event_type": msg.Headers[events.HeaderType],
					"topic":      msg.Topic,
				}).Error("Dropping a public domain event of a type this Bosun does not register")
			}
			return nil
		}
		h.Metrics.DomainEvent(ResultUndecodable)
		return fmt.Errorf("%w: %w", ErrUndecodable, err)
	}
	if !rec.Spec.Public() {
		h.Metrics.DomainEvent(ResultRegistryInternal)
		if h.Logger != nil {
			h.Logger.WithFields(logging.Fields{"event_type": rec.Type, "event_id": rec.ID, "topic": msg.Topic}).
				Error("Dropping a domain event whose header says public but whose registered type is internal")
		}
		return nil
	}
	if rec.TenantID == "" {
		h.Metrics.DomainEvent(ResultNoTenant)
		return nil
	}
	incoming := ledger.IncomingEvent{
		ID:         rec.ID,
		TenantID:   rec.TenantID,
		Type:       rec.Type,
		SchemaName: string(rec.Spec.MessageName),
		Subject:    rec.Subject,
		Payload:    rec.Data,
		OccurredAt: rec.Time,
	}
	delay := h.RetryDelay
	if delay <= 0 {
		delay = 250 * time.Millisecond
	}
	for {
		result, err := h.Recorder.RecordEvent(ctx, incoming)
		if err == nil {
			switch {
			case result.NoSubscriber:
				h.Metrics.DomainEvent(ResultNoSubscriber)
			case result.Duplicate:
				h.Metrics.DomainEvent(ResultDuplicate)
			default:
				h.Metrics.DomainEvent(ResultStored)
				h.Metrics.Created(result.Deliveries)
			}
			return nil
		}
		if h.Logger != nil {
			h.Logger.WithError(err).WithFields(logging.Fields{
				"event_id":  rec.ID,
				"topic":     msg.Topic,
				"retry_in":  delay.String(),
				"retryable": database.IsRetryablePostgresError(err),
			}).Warn("Storing a webhook event failed; retrying before the offset commits")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay = min(delay*2, 30*time.Second)
	}
}

// Registrar subscribes a topic.
type Registrar interface {
	AddHandler(topic string, handler kafka.Handler)
}

// Register subscribes handler to the local domain.events topic and to the
// copy MirrorMaker2 writes for each region prefix, each wrapped by wrap with
// its consumer name, and returns the topics. The copies share one ledger, so
// an event read from several of them is stored once.
func Register(reg Registrar, prefixes []string, handler kafka.Handler, wrap func(name string, h kafka.Handler) kafka.Handler) []string {
	topics := []string{topology.TopicDomainEvents}
	reg.AddHandler(topology.TopicDomainEvents, wrap("bosun-domain", handler))
	for _, prefix := range prefixes {
		topic := topology.MirroredTopicName(prefix, topology.TopicDomainEvents)
		reg.AddHandler(topic, wrap("bosun-domain-mirror:"+prefix, handler))
		topics = append(topics, topic)
	}
	return topics
}
