package events

import (
	"errors"
	"fmt"
	"time"

	eventspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Validation failures. Decklog maps ErrUnknownType to FAILED_PRECONDITION and
// every other one to INVALID_ARGUMENT.
var (
	ErrUnknownType     = errors.New("event type is not registered")
	ErrMissingID       = errors.New("event id is missing")
	ErrInvalidID       = errors.New("event id is not a UUIDv7")
	ErrScopeMismatch   = errors.New("tenant does not match the event scope")
	ErrInvalidPayload  = errors.New("payload does not decode as the registered message")
	ErrInvalidEnvelope = errors.New("invalid event envelope")
)

// Event is a domain event as a producer stores it in its outbox. Every field
// is fixed at creation, so a redelivery carries the same ID.
type Event struct {
	ID               string
	Type             string
	Source           string
	Time             time.Time
	TenantID         string
	AggregateID      string
	AggregateVersion int64
	Actor            Actor
	// Data is the deterministic binary protobuf encoding of the message.
	Data []byte
}

// Option adjusts an event built by New.
type Option func(*Event)

// WithActor attributes the event to the caller, see ActorFromContext.
func WithActor(actor Actor) Option { return func(e *Event) { e.Actor = actor } }

// WithAggregateVersion records the aggregate revision the event produced.
func WithAggregateVersion(version int64) Option {
	return func(e *Event) { e.AggregateVersion = version }
}

// WithTime sets when the state change happened; New uses the current time
// otherwise.
func WithTime(t time.Time) Option { return func(e *Event) { e.Time = t } }

// New builds an event for msg, whose type must be registered. tenantID must be
// a UUID for tenant-scoped types and empty for platform-scoped ones. The event
// gets a fresh UUIDv7 ID, which the producer stores with the outbox row.
func New(source, tenantID, aggregateID string, msg proto.Message, opts ...Option) (Event, error) {
	spec, ok := SpecFor(msg)
	if !ok {
		name := "<nil>"
		if msg != nil {
			name = string(msg.ProtoReflect().Descriptor().FullName())
		}
		return Event{}, fmt.Errorf("%w: %s", ErrUnknownType, name)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Event{}, fmt.Errorf("generate event id: %w", err)
	}
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(msg)
	if err != nil {
		return Event{}, fmt.Errorf("marshal %s: %w", spec.Type, err)
	}
	ev := Event{
		ID:          id.String(),
		Type:        spec.Type,
		Source:      source,
		Time:        time.Now().UTC(),
		TenantID:    tenantID,
		AggregateID: aggregateID,
		Data:        data,
	}
	for _, opt := range opts {
		opt(&ev)
	}
	// Outbox tables store microseconds; truncating here keeps the time a
	// producer holds identical to the one every delivery carries.
	ev.Time = ev.Time.UTC().Truncate(time.Microsecond)
	if err := validateFields(spec, ev.ID, ev.Source, ev.TenantID, ev.AggregateID, !ev.Time.IsZero()); err != nil {
		return Event{}, err
	}
	return ev, nil
}

// Envelope converts the event to the Decklog wire form.
func (e Event) Envelope() *eventspb.DomainEvent {
	return &eventspb.DomainEvent{
		Id:               e.ID,
		Type:             e.Type,
		Source:           e.Source,
		Time:             timestamppb.New(e.Time),
		TenantId:         e.TenantID,
		AggregateId:      e.AggregateID,
		AggregateVersion: e.AggregateVersion,
		Actor: &eventspb.Actor{
			AuthType:  e.Actor.AuthType,
			UserId:    e.Actor.UserID,
			TokenHash: e.Actor.TokenHash,
		},
		Data: e.Data,
	}
}

// Validate checks an envelope the way Decklog does before producing it: the
// type is registered, the ID is a UUIDv7, the tenant matches the scope, and
// the payload decodes as the registered message with no fields that message
// does not declare.
func Validate(env *eventspb.DomainEvent) (Spec, proto.Message, error) {
	if env == nil {
		return Spec{}, nil, fmt.Errorf("%w: nil event", ErrInvalidEnvelope)
	}
	spec, ok := Lookup(env.GetType())
	if !ok {
		return Spec{}, nil, fmt.Errorf("%w: %q", ErrUnknownType, env.GetType())
	}
	if err := validateFields(spec, env.GetId(), env.GetSource(), env.GetTenantId(), env.GetAggregateId(), env.GetTime().IsValid() && env.GetTime().AsTime().Unix() > 0); err != nil {
		return Spec{}, nil, err
	}
	msg := spec.NewMessage()
	if err := proto.Unmarshal(env.GetData(), msg); err != nil {
		return Spec{}, nil, fmt.Errorf("%w: %s: %w", ErrInvalidPayload, spec.Type, err)
	}
	if path := unknownFieldPath(msg.ProtoReflect(), string(spec.MessageName)); path != "" {
		return Spec{}, nil, fmt.Errorf("%w: %s carries fields %s does not declare", ErrInvalidPayload, spec.Type, path)
	}
	return spec, msg, nil
}

// Decode returns the registered message for eventType decoded from data.
// Consumers use it: fields added by a newer producer are kept as unknown
// fields instead of failing the decode.
func Decode(eventType string, data []byte) (Spec, proto.Message, error) {
	spec, ok := Lookup(eventType)
	if !ok {
		return Spec{}, nil, fmt.Errorf("%w: %q", ErrUnknownType, eventType)
	}
	msg := spec.NewMessage()
	if err := proto.Unmarshal(data, msg); err != nil {
		return Spec{}, nil, fmt.Errorf("%w: %s: %w", ErrInvalidPayload, spec.Type, err)
	}
	return spec, msg, nil
}

func validateFields(spec Spec, id, source, tenantID, aggregateID string, hasTime bool) error {
	if id == "" {
		return ErrMissingID
	}
	if parsed, err := uuid.Parse(id); err != nil || parsed.Version() != 7 {
		return fmt.Errorf("%w: %q", ErrInvalidID, id)
	}
	switch spec.Scope {
	case eventspb.Scope_SCOPE_TENANT:
		if _, err := uuid.Parse(tenantID); err != nil {
			return fmt.Errorf("%w: %s requires a tenant UUID, got %q", ErrScopeMismatch, spec.Type, tenantID)
		}
	case eventspb.Scope_SCOPE_PLATFORM:
		if tenantID != "" {
			return fmt.Errorf("%w: %s is platform-scoped and carries tenant %q", ErrScopeMismatch, spec.Type, tenantID)
		}
	default:
		return fmt.Errorf("%w: %s has no scope", ErrScopeMismatch, spec.Type)
	}
	if source == "" {
		return fmt.Errorf("%w: source is missing", ErrInvalidEnvelope)
	}
	if aggregateID == "" {
		return fmt.Errorf("%w: aggregate id is missing", ErrInvalidEnvelope)
	}
	if !hasTime {
		return fmt.Errorf("%w: time is missing", ErrInvalidEnvelope)
	}
	return nil
}

// unknownFieldPath returns the path of the first message in m's tree that
// carries unknown fields, or "".
func unknownFieldPath(m protoreflect.Message, path string) string {
	if len(m.GetUnknown()) > 0 {
		return path
	}
	var found string
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		child := path + "." + string(fd.Name())
		switch {
		case fd.IsList() && fd.Message() != nil:
			list := v.List()
			for i := 0; i < list.Len() && found == ""; i++ {
				found = unknownFieldPath(list.Get(i).Message(), fmt.Sprintf("%s[%d]", child, i))
			}
		case fd.IsMap() && fd.MapValue().Message() != nil:
			v.Map().Range(func(k protoreflect.MapKey, mv protoreflect.Value) bool {
				found = unknownFieldPath(mv.Message(), fmt.Sprintf("%s[%v]", child, k.Interface()))
				return found == ""
			})
		case fd.Message() != nil && !fd.IsList() && !fd.IsMap():
			found = unknownFieldPath(v.Message(), child)
		}
		return found == ""
	})
	return found
}
