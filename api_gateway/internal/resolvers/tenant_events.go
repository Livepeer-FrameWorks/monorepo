package resolvers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"frameworks/api_gateway/internal/demo"
	"frameworks/api_gateway/internal/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	signalmanpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/signalman"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ErrNotPublicEvent is returned for an event whose payload is not a registered
// public event of its type.
var ErrNotPublicEvent = errors.New("event is not a public event")

// TenantEventFilter selects the public events one tenantEvents subscription
// receives. An empty filter passes every public event.
type TenantEventFilter struct {
	// Types holds the exact event type strings to pass; empty passes all.
	Types map[string]struct{}
	// StreamID is the raw stream UUID the payload must name; empty passes all.
	StreamID string
}

// NewTenantEventFilter builds a filter from the subscription arguments.
// streamID may be a relay global ID or a raw stream UUID.
func NewTenantEventFilter(types []string, streamID *string) (TenantEventFilter, error) {
	filter := TenantEventFilter{}
	if len(types) > 0 {
		filter.Types = make(map[string]struct{}, len(types))
		for _, t := range types {
			filter.Types[t] = struct{}{}
		}
	}
	if streamID != nil && *streamID != "" {
		raw, err := normalizeStreamID(*streamID)
		if err != nil {
			return TenantEventFilter{}, err
		}
		filter.StreamID = raw
	}
	return filter, nil
}

// Matches reports whether event passes the filter. It assumes event already
// passed PublicEventPayload's registry check.
func (f TenantEventFilter) Matches(event *signalmanpb.TenantEvent) bool {
	if len(f.Types) > 0 {
		if _, ok := f.Types[event.GetType()]; !ok {
			return false
		}
	}
	if f.StreamID == "" {
		return true
	}
	msg, err := event.GetData().UnmarshalNew()
	if err != nil {
		return false
	}
	streamID := PublicEventStreamID(msg)
	return streamID != "" && streamID == f.StreamID
}

// authorizeTenantEvents narrows an unfiltered subscription to readable categories.
// Explicitly requesting a forbidden type fails instead of silently omitting it.
func authorizeTenantEvents(ctx context.Context, filter TenantEventFilter) (TenantEventFilter, error) {
	allowed := make(map[string]struct{})
	for _, spec := range events.Specs() {
		if !spec.Public() {
			continue
		}
		if len(filter.Types) > 0 {
			if _, requested := filter.Types[spec.Type]; !requested {
				continue
			}
		}
		scope := tenantEventScope(spec.Type)
		if scope == "" {
			continue
		}
		if err := middleware.RequirePermission(ctx, scope); err != nil {
			if len(filter.Types) > 0 {
				return TenantEventFilter{}, err
			}
			continue
		}
		allowed[spec.Type] = struct{}{}
	}
	if len(allowed) == 0 {
		return TenantEventFilter{}, fmt.Errorf("%w: no readable tenant event types", middleware.ErrForbidden)
	}
	filter.Types = allowed
	return filter, nil
}

func tenantEventScope(eventType string) string {
	category, _, _ := strings.Cut(eventType, ".")
	switch category {
	case "stream", "clip", "recording", "upload", "multistream":
		return "streams:read"
	case "billing", "account":
		return "billing:read"
	case "api_token", "custom_domain":
		return "developer:read"
	default:
		return ""
	}
}

// PublicEventStreamID returns the stream a public payload names: stream_id on
// stream events and MultistreamStatusChanged, artifact.stream_id on media
// events. Payloads without a stream return "".
func PublicEventStreamID(msg proto.Message) string {
	switch m := msg.(type) {
	case interface{ GetArtifact() *publicv1.Artifact }:
		return m.GetArtifact().GetStreamId()
	case interface{ GetStreamId() string }:
		return m.GetStreamId()
	default:
		return ""
	}
}

// isRegisteredPublic reports whether event names a registered public type
// whose payload is that type's message, without decoding the payload.
func isRegisteredPublic(event *signalmanpb.TenantEvent) bool {
	spec, ok := events.Lookup(event.GetType())
	if !ok || !spec.Public() || event.GetData() == nil {
		return false
	}
	return event.GetData().MessageName() == spec.MessageName
}

// PublicEventPayload decodes the payload of event and returns it only when the
// registry marks its message public and registers it under event's type.
func PublicEventPayload(event *signalmanpb.TenantEvent) (proto.Message, error) {
	if event.GetData() == nil {
		return nil, ErrNotPublicEvent
	}
	msg, err := event.GetData().UnmarshalNew()
	if err != nil {
		return nil, fmt.Errorf("decode event payload: %w", err)
	}
	spec, ok := events.SpecFor(msg)
	if !ok || !spec.Public() || spec.Type != event.GetType() {
		return nil, ErrNotPublicEvent
	}
	return msg, nil
}

// EventTimestamp converts an optional payload timestamp; unset is nil.
func EventTimestamp(ts *timestamppb.Timestamp) *time.Time {
	if ts == nil {
		return nil
	}
	t := ts.AsTime()
	return &t
}

// DoTenantEvents serves the tenantEvents subscription: the caller's tenant's
// public events, narrowed by types and streamID.
func (r *Resolver) DoTenantEvents(ctx context.Context, types []string, streamID *string) (<-chan *signalmanpb.TenantEvent, error) {
	filter, err := NewTenantEventFilter(types, streamID)
	if err != nil {
		return nil, err
	}

	if middleware.IsDemoMode(ctx) {
		ch := make(chan *signalmanpb.TenantEvent, 10)
		go func() {
			defer close(ch)
			for _, event := range demo.GenerateTenantEvents() {
				if !filter.Matches(event) {
					continue
				}
				select {
				case ch <- event:
				case <-ctx.Done():
					return
				}
				if !sleepContext(ctx, 2*time.Second) {
					return
				}
			}
		}()
		return ch, nil
	}

	user, err := middleware.RequireAuth(ctx)
	if err != nil {
		return nil, fmt.Errorf("authentication required for tenant events: %w", err)
	}
	if user.TenantID == "" {
		return nil, fmt.Errorf("tenant events require a tenant")
	}
	filter, err = authorizeTenantEvents(ctx, filter)
	if err != nil {
		return nil, err
	}
	config := ConnectionConfig{UserID: user.UserID, TenantID: user.TenantID, JWT: ctxkeys.GetJWTToken(ctx)}
	ch, err := r.SubManager.SubscribeToTenantEvents(ctx, config, filter)
	if err != nil {
		r.Logger.WithError(err).WithFields(logging.Fields{
			"user_id":   user.UserID,
			"tenant_id": user.TenantID,
		}).Error("Failed to set up tenant events subscription")
		return nil, fmt.Errorf("failed to set up tenant events subscription: %w", err)
	}
	return ch, nil
}
