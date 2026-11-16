package kafkasource

import (
	"context"
	"fmt"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/sessionrevocations"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"
)

type Transport struct {
	Brokers              []string
	MirrorRegionPrefixes []string
	ClientID             string
}

func Connect(ctx context.Context, cfg sessionrevocations.Config, transport Transport) (*sessionrevocations.Feed, func(), error) {
	if len(transport.Brokers) == 0 {
		return nil, nil, fmt.Errorf("revocation Kafka brokers are required")
	}
	if cfg.List == nil {
		return nil, nil, fmt.Errorf("authoritative revocation snapshot is required")
	}
	topics := []string{topology.TopicDomainEvents}
	for _, prefix := range transport.MirrorRegionPrefixes {
		if prefix != "" {
			topics = append(topics, topology.MirroredTopicName(prefix, topology.TopicDomainEvents))
		}
	}
	cfg.Open = func(ctx context.Context) (sessionrevocations.Source, error) {
		reader, err := kafka.OpenBroadcast(ctx, transport.Brokers, topics, transport.ClientID)
		if err != nil {
			return nil, err
		}
		return &kafkaSource{reader: reader}, nil
	}
	feed := sessionrevocations.Start(ctx, cfg)
	return feed, feed.Close, nil
}

type kafkaSource struct{ reader *kafka.Broadcast }

func (s *kafkaSource) Close()                            { s.reader.Close() }
func (s *kafkaSource) CatchUp(ctx context.Context) error { return s.reader.CatchUp(ctx) }
func (s *kafkaSource) Run(ctx context.Context, apply func(*commodorepb.SessionRevocation)) error {
	return s.reader.Run(ctx, func(_ context.Context, msg kafka.Message) error {
		rev, err := decodeRecord(msg)
		if err == nil && rev != nil {
			apply(rev)
		}
		return err
	})
}

func decodeRecord(msg kafka.Message) (*commodorepb.SessionRevocation, error) {
	kind := msg.Headers[events.HeaderType]
	if kind != "auth_session.revoked" && kind != "user.sessions_revoked" {
		return nil, nil
	}
	headers := make([]events.Header, 0, len(msg.Headers))
	for key, value := range msg.Headers {
		headers = append(headers, events.Header{Key: key, Value: []byte(value)})
	}
	record, err := events.ParseRecord(msg.Key, headers, msg.Value)
	if err != nil {
		return nil, fmt.Errorf("decode revocation: %w", err)
	}
	if record.Source != "commodore" || record.Spec.Public() || msg.Headers[events.HeaderVisibility] != events.VisibilityInternal {
		return nil, fmt.Errorf("revocation source or visibility refused")
	}
	rev := sessionrevocations.DecodePayload(record.TenantID, record.Type, record.Message)
	if rev == nil {
		return nil, fmt.Errorf("revocation identity missing")
	}
	if (rev.SessionId != "" && record.AggregateID != rev.SessionId) || (rev.SessionId == "" && record.AggregateID != rev.UserId) {
		return nil, fmt.Errorf("revocation aggregate does not match its identity")
	}
	return rev, nil
}
