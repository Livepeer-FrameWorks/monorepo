package events

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	eventspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events"
	"google.golang.org/protobuf/proto"
)

// Kafka record headers of a domain event: CloudEvents 1.0 Kafka binary mode
// (attributes in ce_* headers, the protobuf payload as the value) plus the
// tenant and region headers every FrameWorks topic uses.
const (
	HeaderID          = "ce_id"
	HeaderSpecVersion = "ce_specversion"
	HeaderType        = "ce_type"
	HeaderSource      = "ce_source"
	HeaderTime        = "ce_time"
	HeaderSubject     = "ce_subject"
	HeaderDataSchema  = "ce_dataschema"
	HeaderContentType = "content-type"

	// CloudEvents extension attributes; names are lower-case alphanumeric
	// as the spec requires.
	HeaderVisibility       = "ce_visibility"
	HeaderActorAuthType    = "ce_actorauthtype"
	HeaderActorUserID      = "ce_actoruserid"
	HeaderActorTokenHash   = "ce_actortokenhash"
	HeaderAggregateVersion = "ce_aggregateversion"

	HeaderTenantID        = "tenant_id"
	HeaderSourceRegion    = "source_region"
	HeaderSourceClusterID = "source_cluster_id"

	SpecVersion = "1.0"
	ContentType = "application/protobuf"

	VisibilityPublic   = "public"
	VisibilityInternal = "internal"
)

// Header is one Kafka record header.
type Header struct {
	Key   string
	Value []byte
}

// RecordKey is the Kafka key and ce_subject of an event: the aggregate name
// and ID, so every event of one aggregate lands on one partition in order.
func RecordKey(spec Spec, aggregateID string) string {
	return spec.Aggregate + "/" + aggregateID
}

// EncodeRecord validates env (see Validate) and returns its Kafka key, headers,
// and value. Optional headers are omitted when empty.
func EncodeRecord(env *eventspb.DomainEvent) (key []byte, headers []Header, value []byte, err error) {
	spec, _, err := Validate(env)
	if err != nil {
		return nil, nil, nil, err
	}
	subject := RecordKey(spec, env.GetAggregateId())
	visibility := VisibilityInternal
	if spec.Public() {
		visibility = VisibilityPublic
	}
	add := func(k, v string) {
		if v != "" {
			headers = append(headers, Header{Key: k, Value: []byte(v)})
		}
	}
	add(HeaderSpecVersion, SpecVersion)
	add(HeaderID, env.GetId())
	add(HeaderType, spec.Type)
	add(HeaderSource, env.GetSource())
	add(HeaderTime, env.GetTime().AsTime().UTC().Format(time.RFC3339Nano))
	add(HeaderSubject, subject)
	add(HeaderDataSchema, string(spec.MessageName))
	add(HeaderContentType, ContentType)
	add(HeaderVisibility, visibility)
	add(HeaderTenantID, env.GetTenantId())
	add(HeaderSourceRegion, env.GetSourceRegion())
	add(HeaderSourceClusterID, env.GetSourceClusterId())
	if actor := env.GetActor(); actor != nil {
		add(HeaderActorAuthType, actor.GetAuthType())
		add(HeaderActorUserID, actor.GetUserId())
		if actor.GetTokenHash() != 0 {
			add(HeaderActorTokenHash, strconv.FormatUint(actor.GetTokenHash(), 10))
		}
	}
	if env.GetAggregateVersion() != 0 {
		add(HeaderAggregateVersion, strconv.FormatInt(env.GetAggregateVersion(), 10))
	}
	return []byte(subject), headers, env.GetData(), nil
}

// Record is a domain event read back from Kafka.
type Record struct {
	Event
	Spec          Spec
	Message       proto.Message
	Subject       string
	SourceRegion  string
	SourceCluster string
}

// ParseRecord decodes a domain.events record for a consumer. It fails with
// ErrUnknownType for a type this binary does not know, so the consumer can
// retry or alert instead of dropping it, and tolerates payload fields added by
// a newer producer.
func ParseRecord(key []byte, headers []Header, value []byte) (Record, error) {
	h := make(map[string]string, len(headers))
	for _, header := range headers {
		h[header.Key] = string(header.Value)
	}
	if h[HeaderSpecVersion] != SpecVersion {
		return Record{}, fmt.Errorf("%w: ce_specversion %q", ErrInvalidEnvelope, h[HeaderSpecVersion])
	}
	if h[HeaderID] == "" {
		return Record{}, ErrMissingID
	}
	spec, msg, err := Decode(h[HeaderType], value)
	if err != nil {
		return Record{}, err
	}
	occurred, err := time.Parse(time.RFC3339Nano, h[HeaderTime])
	if err != nil {
		return Record{}, fmt.Errorf("%w: ce_time: %w", ErrInvalidEnvelope, err)
	}
	subject := h[HeaderSubject]
	if subject == "" {
		subject = string(key)
	}
	aggregateID, ok := strings.CutPrefix(subject, spec.Aggregate+"/")
	if !ok || aggregateID == "" {
		return Record{}, fmt.Errorf("%w: subject %q is not %s/<id>", ErrInvalidEnvelope, subject, spec.Aggregate)
	}
	rec := Record{
		Event: Event{
			ID:          h[HeaderID],
			Type:        spec.Type,
			Source:      h[HeaderSource],
			Time:        occurred,
			TenantID:    h[HeaderTenantID],
			AggregateID: aggregateID,
			Actor: Actor{
				AuthType: h[HeaderActorAuthType],
				UserID:   h[HeaderActorUserID],
			},
			Data: value,
		},
		Spec:          spec,
		Message:       msg,
		Subject:       subject,
		SourceRegion:  h[HeaderSourceRegion],
		SourceCluster: h[HeaderSourceClusterID],
	}
	if v := h[HeaderActorTokenHash]; v != "" {
		if rec.Actor.TokenHash, err = strconv.ParseUint(v, 10, 64); err != nil {
			return Record{}, fmt.Errorf("%w: %s: %w", ErrInvalidEnvelope, HeaderActorTokenHash, err)
		}
	}
	if v := h[HeaderAggregateVersion]; v != "" {
		if rec.AggregateVersion, err = strconv.ParseInt(v, 10, 64); err != nil {
			return Record{}, fmt.Errorf("%w: %s: %w", ErrInvalidEnvelope, HeaderAggregateVersion, err)
		}
	}
	return rec, nil
}
