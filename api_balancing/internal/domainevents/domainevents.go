// Package domainevents writes Foghorn's domain events into
// foghorn.domain_event_outbox. Every caller passes the transaction that commits
// the state change the event describes; the shared relay started by the Foghorn
// binary delivers committed rows to Decklog.
//
// Foghorn's database is per cell, so each cell's outbox carries only the events
// of state that cell owns.
package domainevents

import (
	"context"
	"fmt"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events/outbox"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	"google.golang.org/protobuf/proto"
)

const (
	// Source is the CloudEvents source of every Foghorn domain event.
	Source = "foghorn"
	// Schema holds foghorn.domain_event_outbox.
	Schema = "foghorn"
)

// Enqueue builds the event for msg and inserts it through exec, which must be
// the transaction that commits the state change. It returns the event so a
// caller that also writes a legacy row can reuse its ID.
func Enqueue(ctx context.Context, exec outbox.Execer, tenantID, aggregateID string, msg proto.Message, opts ...events.Option) (events.Event, error) {
	if exec == nil {
		return events.Event{}, fmt.Errorf("domainevents: %T needs a transaction", msg)
	}
	ev, err := events.New(Source, tenantID, aggregateID, msg, opts...)
	if err != nil {
		return events.Event{}, fmt.Errorf("domainevents: build %T: %w", msg, err)
	}
	if err := outbox.Enqueue(ctx, exec, Schema, ev); err != nil {
		return events.Event{}, err
	}
	return ev, nil
}

// StreamConnected records a new ingest session for the stream.
func StreamConnected(ctx context.Context, exec outbox.Execer, tenantID, streamID, playbackID string, protocol publicv1.IngestProtocol) error {
	_, err := Enqueue(ctx, exec, tenantID, streamID, &publicv1.StreamConnected{StreamId: streamID, PlaybackId: playbackID, Protocol: protocol})
	return err
}

// StreamLive records that the stream's active session became playable.
func StreamLive(ctx context.Context, exec outbox.Execer, tenantID, streamID, playbackID string) error {
	_, err := Enqueue(ctx, exec, tenantID, streamID, &publicv1.StreamLive{StreamId: streamID, PlaybackId: playbackID})
	return err
}

// StreamIdle records that an ingest session of the stream ended. A session
// admitted without a public stream ID emits nothing, matching its absent
// stream.connected.
func StreamIdle(ctx context.Context, exec outbox.Execer, tenantID, streamID, playbackID string) error {
	if streamID == "" {
		return nil
	}
	_, err := Enqueue(ctx, exec, tenantID, streamID, &publicv1.StreamIdle{StreamId: streamID, PlaybackId: playbackID})
	return err
}

// IngestProtocol maps the protocol Foghorn detects from a push URL to the
// public enum.
func IngestProtocol(protocol string) publicv1.IngestProtocol {
	switch protocol {
	case "rtmp":
		return publicv1.IngestProtocol_INGEST_PROTOCOL_RTMP
	case "srt":
		return publicv1.IngestProtocol_INGEST_PROTOCOL_SRT
	case "whip":
		return publicv1.IngestProtocol_INGEST_PROTOCOL_WHIP
	default:
		return publicv1.IngestProtocol_INGEST_PROTOCOL_UNSPECIFIED
	}
}
