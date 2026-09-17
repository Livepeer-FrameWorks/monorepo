package resolvers

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"frameworks/api_gateway/graph/model"
	signalmanclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/signalman"
	pkgconfig "github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/globalid"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	deckhandpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/deckhand"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	signalmanpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/signalman"
)

const (
	// upstreamLinger keeps an unused upstream stream open briefly so a quick
	// resubscribe, such as a page reload, reuses it.
	upstreamLinger       = 5 * time.Second
	upstreamRetryInitial = 200 * time.Millisecond
	upstreamRetryMax     = 5 * time.Second
)

var errSubscriptionManagerClosed = errors.New("subscription manager is shut down")

// streamOpener opens upstream Signalman streams; *signalmanclient.Dialer
// implements it.
type streamOpener interface {
	Open(ctx context.Context, addr string, key signalmanclient.StreamKey) (signalmanclient.EventStream, error)
	Close() error
}

// SubscriptionManager serves GraphQL subscriptions from this region's Signalman
// replicas. Every GraphQL subscription for the same tenant and channel shares
// one upstream stream, whose events are fanned out to independently buffered
// subscribers. One reconnect loop per stream restores delivery after upstream
// failures without the subscribers resubscribing.
type SubscriptionManager struct {
	logger                    logging.Logger
	metrics                   *GraphQLMetrics
	addrs                     []string
	opener                    streamOpener
	maxSubscriptionsPerTenant int
	linger                    time.Duration

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu                  sync.Mutex
	closed              bool
	upstreams           map[signalmanclient.StreamKey]*upstreamEntry
	tenantSubscriptions map[string]int
}

type upstreamEntry struct {
	key    signalmanclient.StreamKey
	fanout *signalmanclient.Fanout
	cancel context.CancelFunc
	linger *time.Timer
}

// ConnectionConfig identifies the authenticated caller of a subscription.
type ConnectionConfig struct {
	UserID   string
	TenantID string
	// JWT is not forwarded: upstream streams authenticate with the service
	// token and carry the tenant as metadata.
	JWT string
}

// SubscriptionManagerConfig configures a SubscriptionManager.
type SubscriptionManagerConfig struct {
	// SignalmanAddrs are this region's Signalman targets, tried in a
	// tenant-rotated order when an upstream stream opens.
	SignalmanAddrs []string
	ServiceToken   string
	// MaxSubscriptionsPerTenant caps concurrent GraphQL subscriptions per
	// tenant on this Bridge replica; zero disables the cap.
	MaxSubscriptionsPerTenant int
	Metrics                   *GraphQLMetrics
}

// NewSubscriptionManager creates a subscription manager that opens upstream
// streams through a pooled Signalman dialer. A dialer configuration error is
// reported by every subscription attempt.
func NewSubscriptionManager(logger logging.Logger, cfg SubscriptionManagerConfig) *SubscriptionManager {
	dialer, err := signalmanclient.NewDialer(signalmanclient.DialerConfig{
		ServiceToken:  cfg.ServiceToken,
		AllowInsecure: pkgconfig.GetEnvBool("GRPC_ALLOW_INSECURE", false),
		CACertFile:    pkgconfig.GetEnv("GRPC_TLS_CA_PATH", ""),
		ServerName:    pkgconfig.GetServiceGRPCTLSServerName("signalman"),
		Logger:        logger,
		OpenTimeout:   time.Duration(pkgconfig.GetEnvInt("SIGNALMAN_CONNECT_TIMEOUT_SECONDS", 5)) * time.Second,
	})
	var opener streamOpener = dialer
	if err != nil {
		logger.WithError(err).Error("Failed to configure Signalman dialer; subscriptions will fail")
		opener = failingOpener{err: err}
	}
	return newSubscriptionManager(logger, cfg, opener)
}

func newSubscriptionManager(logger logging.Logger, cfg SubscriptionManagerConfig, opener streamOpener) *SubscriptionManager {
	addrs := make([]string, 0, len(cfg.SignalmanAddrs))
	for _, addr := range cfg.SignalmanAddrs {
		if addr = strings.TrimSpace(addr); addr != "" {
			addrs = append(addrs, addr)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &SubscriptionManager{
		logger:                    logger,
		metrics:                   cfg.Metrics,
		addrs:                     addrs,
		opener:                    opener,
		maxSubscriptionsPerTenant: cfg.MaxSubscriptionsPerTenant,
		linger:                    upstreamLinger,
		ctx:                       ctx,
		cancel:                    cancel,
		upstreams:                 make(map[signalmanclient.StreamKey]*upstreamEntry),
		tenantSubscriptions:       make(map[string]int),
	}
}

type failingOpener struct{ err error }

func (o failingOpener) Open(context.Context, string, signalmanclient.StreamKey) (signalmanclient.EventStream, error) {
	return nil, o.err
}

func (o failingOpener) Close() error { return nil }

// parseSignalmanAddrs parses a comma-separated list of "host:port" entries
// from SIGNALMAN_GRPC_ADDRS. Empty input returns nil.
func parseSignalmanAddrs(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	out := []string{}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry != "" {
			out = append(out, entry)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// rotateAddrs returns a copy of addrs rotated by hash(tenantID) so each tenant
// prefers a stable but tenant-dependent entry; later entries are failover
// candidates in order.
func rotateAddrs(addrs []string, tenantID string) []string {
	if len(addrs) <= 1 {
		out := make([]string, len(addrs))
		copy(out, addrs)
		return out
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(tenantID))
	off := int(h.Sum32()) % len(addrs)
	if off < 0 {
		off += len(addrs)
	}
	out := make([]string, 0, len(addrs))
	out = append(out, addrs[off:]...)
	out = append(out, addrs[:off]...)
	return out
}

// trackSubscriptionStart/End paired calls update the SubscriptionsActive gauge
// for one served GraphQL subscription.
func (sm *SubscriptionManager) trackSubscriptionStart(operation string) {
	if sm.metrics != nil && sm.metrics.SubscriptionsActive != nil {
		sm.metrics.SubscriptionsActive.WithLabelValues(operation).Inc()
	}
}

func (sm *SubscriptionManager) trackSubscriptionEnd(operation string) {
	if sm.metrics != nil && sm.metrics.SubscriptionsActive != nil {
		sm.metrics.SubscriptionsActive.WithLabelValues(operation).Dec()
	}
}

func (sm *SubscriptionManager) trackUpstream(tenantID string, delta float64) {
	if sm.metrics != nil && sm.metrics.SignalmanStreams != nil {
		sm.metrics.SignalmanStreams.WithLabelValues(tenantID).Add(delta)
	}
}

func (sm *SubscriptionManager) recordUpstreamOpen(outcome string) {
	if sm.metrics != nil && sm.metrics.WebSocketMessages != nil {
		sm.metrics.WebSocketMessages.WithLabelValues("outbound", outcome).Inc()
	}
}

func sendSubscriptionUpdate[T any](ctx context.Context, output chan<- T, update T) bool {
	select {
	case output <- update:
		return true
	case <-ctx.Done():
		return false
	}
}

// startSubscription attaches one GraphQL subscription to the upstream stream of
// every channel it needs and serves events to handle until ctx ends, handle
// returns false, or a subscriber is ended. closeOutput runs when serving stops.
func (sm *SubscriptionManager) startSubscription(ctx context.Context, operation string, config ConnectionConfig, channels []signalmanpb.Channel, closeOutput func(), handle func(*signalmanpb.SignalmanEvent) bool) error {
	subs, release, err := sm.attach(config, channels)
	if err != nil {
		return err
	}
	sm.trackSubscriptionStart(operation)
	go func() {
		defer sm.trackSubscriptionEnd(operation)
		defer closeOutput()
		defer release()
		sm.serve(ctx, operation, subs, handle)
	}()
	return nil
}

func (sm *SubscriptionManager) attach(config ConnectionConfig, channels []signalmanpb.Channel) ([]*signalmanclient.Subscriber, func(), error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.closed {
		return nil, nil, errSubscriptionManagerClosed
	}
	if len(sm.addrs) == 0 {
		return nil, nil, fmt.Errorf("no Signalman addresses configured")
	}
	tenantID := config.TenantID
	if sm.maxSubscriptionsPerTenant > 0 && tenantID != "" && sm.tenantSubscriptions[tenantID] >= sm.maxSubscriptionsPerTenant {
		sm.logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"limit":     sm.maxSubscriptionsPerTenant,
		}).Warn("Reached max GraphQL subscriptions for tenant")
		return nil, nil, fmt.Errorf("tenant %s has reached the max number of active subscriptions", tenantID)
	}

	entries := make([]*upstreamEntry, 0, len(channels))
	subs := make([]*signalmanclient.Subscriber, 0, len(channels))
	for _, channel := range channels {
		key := signalmanclient.StreamKey{TenantID: tenantID, Channel: channel}
		// Signalman admits only tenantless service streams to the platform
		// channel, so every operator on this replica shares one such stream;
		// the operator's tenant still counts toward the subscription cap.
		if channel == signalmanpb.Channel_CHANNEL_PLATFORM {
			key.TenantID = ""
		}
		entry := sm.upstreamLocked(key)
		sub, err := entry.fanout.Attach()
		if err != nil {
			for i, attached := range subs {
				sm.detachLocked(entries[i], attached)
			}
			return nil, nil, err
		}
		entries = append(entries, entry)
		subs = append(subs, sub)
	}
	if tenantID != "" {
		sm.tenantSubscriptions[tenantID]++
	}

	var once sync.Once
	release := func() {
		once.Do(func() {
			sm.mu.Lock()
			defer sm.mu.Unlock()
			for i, sub := range subs {
				sm.detachLocked(entries[i], sub)
			}
			if tenantID == "" {
				return
			}
			if sm.tenantSubscriptions[tenantID] <= 1 {
				delete(sm.tenantSubscriptions, tenantID)
			} else {
				sm.tenantSubscriptions[tenantID]--
			}
		})
	}
	return subs, release, nil
}

// upstreamLocked returns the live entry for key, starting its stream loop when
// none exists.
func (sm *SubscriptionManager) upstreamLocked(key signalmanclient.StreamKey) *upstreamEntry {
	if entry, ok := sm.upstreams[key]; ok {
		if entry.linger != nil {
			entry.linger.Stop()
			entry.linger = nil
		}
		return entry
	}
	ctx, cancel := context.WithCancel(sm.ctx)
	entry := &upstreamEntry{
		key:    key,
		fanout: signalmanclient.NewFanout(signalmanclient.SubscriberQueueDepth),
		cancel: cancel,
	}
	sm.upstreams[key] = entry
	sm.wg.Add(1)
	go sm.runUpstream(ctx, entry)
	return entry
}

// detachLocked removes sub from entry. When it was the last subscriber the
// upstream stream closes after the linger period unless a new subscriber
// attaches first.
func (sm *SubscriptionManager) detachLocked(entry *upstreamEntry, sub *signalmanclient.Subscriber) {
	if entry.fanout.Detach(sub) > 0 || sm.upstreams[entry.key] != entry || entry.linger != nil {
		return
	}
	entry.linger = time.AfterFunc(sm.linger, func() {
		sm.mu.Lock()
		defer sm.mu.Unlock()
		if sm.upstreams[entry.key] != entry || entry.fanout.Len() > 0 {
			return
		}
		delete(sm.upstreams, entry.key)
		entry.cancel()
		entry.fanout.Close(nil)
	})
}

// runUpstream keeps one upstream stream open for entry until its context ends,
// reopening with jittered exponential backoff after failures. Subscribers stay
// attached to the fanout across reconnects.
func (sm *SubscriptionManager) runUpstream(ctx context.Context, entry *upstreamEntry) {
	defer sm.wg.Done()
	sm.trackUpstream(entry.key.TenantID, 1)
	defer sm.trackUpstream(entry.key.TenantID, -1)

	retry := upstreamRetryInitial
	for {
		stream, addr, err := sm.openUpstream(ctx, entry.key)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			sm.recordUpstreamOpen("connection_error")
			sm.logger.WithError(err).WithFields(logging.Fields{
				"tenant_id": entry.key.TenantID,
				"channel":   entry.key.Channel.String(),
				"retry_in":  retry.String(),
			}).Warn("Failed to open Signalman stream")
			if !sleepContext(ctx, jitter(retry)) {
				return
			}
			retry = min(retry*2, upstreamRetryMax)
			continue
		}
		sm.recordUpstreamOpen("connection_success")
		retry = upstreamRetryInitial

		for {
			event, recvErr := stream.Recv()
			if recvErr != nil {
				if ctx.Err() == nil {
					sm.logger.WithError(recvErr).WithFields(logging.Fields{
						"tenant_id":      entry.key.TenantID,
						"channel":        entry.key.Channel.String(),
						"signalman_addr": addr,
					}).Warn("Signalman stream ended; reconnecting")
				}
				break
			}
			if slow := entry.fanout.Publish(event); slow > 0 {
				sm.logger.WithFields(logging.Fields{
					"tenant_id": entry.key.TenantID,
					"channel":   entry.key.Channel.String(),
					"ended":     slow,
				}).Warn("Ended GraphQL subscriptions that fell behind")
			}
		}
		stream.Close()
		if !sleepContext(ctx, jitter(retry)) {
			return
		}
	}
}

func (sm *SubscriptionManager) openUpstream(ctx context.Context, key signalmanclient.StreamKey) (signalmanclient.EventStream, string, error) {
	var lastErr error
	for _, addr := range rotateAddrs(sm.addrs, key.TenantID) {
		stream, err := sm.opener.Open(ctx, addr, key)
		if err == nil {
			return stream, addr, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	return nil, "", fmt.Errorf("open Signalman %s stream on %d replica(s): %w", key.Channel, len(sm.addrs), lastErr)
}

func jitter(d time.Duration) time.Duration {
	if d <= 1 {
		return d
	}
	return d/2 + rand.N(d/2)
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// serve delivers events from subs to handle, one at a time, until ctx ends,
// handle returns false, or any subscriber ends.
func (sm *SubscriptionManager) serve(ctx context.Context, operation string, subs []*signalmanclient.Subscriber, handle func(*signalmanpb.SignalmanEvent) bool) {
	if len(subs) == 1 {
		sub := subs[0]
		for {
			select {
			case <-ctx.Done():
				return
			case <-sub.Done():
				sm.logSubscriberEnded(operation, sub.Err())
				return
			case event := <-sub.Events():
				if !handle(event) {
					return
				}
			}
		}
	}

	merged := make(chan *signalmanpb.SignalmanEvent)
	ended := make(chan error, len(subs))
	stop := make(chan struct{})
	defer close(stop)
	for _, sub := range subs {
		go func() {
			for {
				select {
				case <-stop:
					return
				case <-sub.Done():
					ended <- sub.Err()
					return
				case event := <-sub.Events():
					select {
					case merged <- event:
					case <-stop:
						return
					}
				}
			}
		}()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case err := <-ended:
			sm.logSubscriberEnded(operation, err)
			return
		case event := <-merged:
			if !handle(event) {
				return
			}
		}
	}
}

func (sm *SubscriptionManager) logSubscriberEnded(operation string, err error) {
	if err == nil {
		return
	}
	fields := logging.Fields{"operation": operation}
	if errors.Is(err, signalmanclient.ErrSlowSubscriber) {
		sm.logger.WithError(err).WithFields(fields).Warn("GraphQL subscription ended because it fell behind")
		return
	}
	sm.logger.WithError(err).WithFields(fields).Info("GraphQL subscription ended")
}

func streamScoped(streamID *string, event *signalmanpb.SignalmanEvent) bool {
	if streamID == nil {
		return true
	}
	msgStreamID := getStreamIDFromProtoEvent(event)
	return msgStreamID != "" && msgStreamID == *streamID
}

// SubscribeToStreams subscribes to stream events and returns a channel of updates
// Returns model.StreamEvent (canonical live stream event shape)
func (sm *SubscriptionManager) SubscribeToStreams(ctx context.Context, config ConnectionConfig, streamID *string) (<-chan *model.StreamEvent, error) {
	updates := make(chan *model.StreamEvent, 10)
	err := sm.startSubscription(ctx, "streams", config, []signalmanpb.Channel{signalmanpb.Channel_CHANNEL_STREAMS}, func() { close(updates) }, func(event *signalmanpb.SignalmanEvent) bool {
		if event.EventType != signalmanpb.EventType_EVENT_TYPE_STREAM_LIFECYCLE_UPDATE &&
			event.EventType != signalmanpb.EventType_EVENT_TYPE_STREAM_END &&
			event.EventType != signalmanpb.EventType_EVENT_TYPE_STREAM_BUFFER &&
			event.EventType != signalmanpb.EventType_EVENT_TYPE_STREAM_TRACK_LIST &&
			event.EventType != signalmanpb.EventType_EVENT_TYPE_PUSH_REWRITE &&
			event.EventType != signalmanpb.EventType_EVENT_TYPE_STREAM_SOURCE &&
			event.EventType != signalmanpb.EventType_EVENT_TYPE_PLAY_REWRITE {
			return true
		}
		if tenantMismatch(config.TenantID, event) || !streamScoped(streamID, event) {
			return true
		}
		update := mapSignalmanStreamEvent(event)
		if update == nil {
			return true
		}
		return sendSubscriptionUpdate(ctx, updates, update)
	})
	if err != nil {
		return nil, err
	}
	return updates, nil
}

// SubscribeToAnalytics subscribes to analytics events and returns a channel of updates
func (sm *SubscriptionManager) SubscribeToAnalytics(ctx context.Context, config ConnectionConfig, streamID *string) (<-chan *ipcpb.ClientLifecycleUpdate, error) {
	updates := make(chan *ipcpb.ClientLifecycleUpdate, 10)
	err := sm.startSubscription(ctx, "analytics", config, []signalmanpb.Channel{signalmanpb.Channel_CHANNEL_ANALYTICS}, func() { close(updates) }, func(event *signalmanpb.SignalmanEvent) bool {
		if event.EventType != signalmanpb.EventType_EVENT_TYPE_CLIENT_LIFECYCLE_UPDATE || tenantMismatch(config.TenantID, event) {
			return true
		}
		if !streamScoped(streamID, event) || event.Data == nil {
			return true
		}
		cl := event.Data.GetClientLifecycle()
		if cl == nil {
			return true
		}
		return sendSubscriptionUpdate(ctx, updates, cl)
	})
	if err != nil {
		return nil, err
	}
	return updates, nil
}

// SubscribeToConnections subscribes to viewer connection events and returns a channel of updates
func (sm *SubscriptionManager) SubscribeToConnections(ctx context.Context, config ConnectionConfig, streamID *string) (<-chan *periscopepb.ConnectionEvent, error) {
	updates := make(chan *periscopepb.ConnectionEvent, 10)
	err := sm.startSubscription(ctx, "connections", config, []signalmanpb.Channel{signalmanpb.Channel_CHANNEL_ANALYTICS}, func() { close(updates) }, func(event *signalmanpb.SignalmanEvent) bool {
		if event.EventType != signalmanpb.EventType_EVENT_TYPE_VIEWER_CONNECT &&
			event.EventType != signalmanpb.EventType_EVENT_TYPE_VIEWER_DISCONNECT {
			return true
		}
		if tenantMismatch(config.TenantID, event) || !streamScoped(streamID, event) {
			return true
		}
		ce := mapSignalmanConnectionEvent(event)
		if ce == nil {
			return true
		}
		return sendSubscriptionUpdate(ctx, updates, ce)
	})
	if err != nil {
		return nil, err
	}
	return updates, nil
}

// SubscribeToStorageEvents subscribes to storage lifecycle events and returns a channel of updates
func (sm *SubscriptionManager) SubscribeToStorageEvents(ctx context.Context, config ConnectionConfig, streamID *string) (<-chan *periscopepb.StorageEvent, error) {
	updates := make(chan *periscopepb.StorageEvent, 10)
	err := sm.startSubscription(ctx, "storage_events", config, []signalmanpb.Channel{signalmanpb.Channel_CHANNEL_ANALYTICS}, func() { close(updates) }, func(event *signalmanpb.SignalmanEvent) bool {
		if event.EventType != signalmanpb.EventType_EVENT_TYPE_STORAGE_LIFECYCLE || tenantMismatch(config.TenantID, event) {
			return true
		}
		if !streamScoped(streamID, event) {
			return true
		}
		update := mapSignalmanStorageEvent(event)
		if update == nil {
			return true
		}
		return sendSubscriptionUpdate(ctx, updates, update)
	})
	if err != nil {
		return nil, err
	}
	return updates, nil
}

// SubscribeToProcessingEvents subscribes to processing/transcoding events and returns a channel of updates
func (sm *SubscriptionManager) SubscribeToProcessingEvents(ctx context.Context, config ConnectionConfig, streamID *string) (<-chan *periscopepb.ProcessingUsageRecord, error) {
	updates := make(chan *periscopepb.ProcessingUsageRecord, 10)
	err := sm.startSubscription(ctx, "processing_events", config, []signalmanpb.Channel{signalmanpb.Channel_CHANNEL_ANALYTICS}, func() { close(updates) }, func(event *signalmanpb.SignalmanEvent) bool {
		if event.EventType != signalmanpb.EventType_EVENT_TYPE_PROCESS_BILLING || tenantMismatch(config.TenantID, event) {
			return true
		}
		if !streamScoped(streamID, event) {
			return true
		}
		update := mapSignalmanProcessingEvent(event)
		if update == nil {
			return true
		}
		return sendSubscriptionUpdate(ctx, updates, update)
	})
	if err != nil {
		return nil, err
	}
	return updates, nil
}

// SubscribeToSystem subscribes to system events and returns a channel of updates
func (sm *SubscriptionManager) SubscribeToSystem(ctx context.Context, config ConnectionConfig) (<-chan *ipcpb.NodeLifecycleUpdate, error) {
	updates := make(chan *ipcpb.NodeLifecycleUpdate, 10)
	err := sm.startSubscription(ctx, "system", config, []signalmanpb.Channel{signalmanpb.Channel_CHANNEL_SYSTEM}, func() { close(updates) }, func(event *signalmanpb.SignalmanEvent) bool {
		if event.EventType != signalmanpb.EventType_EVENT_TYPE_NODE_LIFECYCLE_UPDATE || tenantMismatch(config.TenantID, event) {
			return true
		}
		if event.Data == nil {
			return true
		}
		nl := event.Data.GetNodeLifecycle()
		if nl == nil {
			return true
		}
		return sendSubscriptionUpdate(ctx, updates, nl)
	})
	if err != nil {
		return nil, err
	}
	return updates, nil
}

// SubscribeToIncidents delivers incident updates for the subscriber's tenant.
// Incident events always carry a tenant, so an event without one is dropped
// instead of being treated as a broadcast.
func (sm *SubscriptionManager) SubscribeToIncidents(ctx context.Context, config ConnectionConfig) (<-chan *model.IncidentUpdatedEvent, error) {
	updates := make(chan *model.IncidentUpdatedEvent, 10)
	err := sm.startSubscription(ctx, "incidents", config, []signalmanpb.Channel{signalmanpb.Channel_CHANNEL_SYSTEM}, func() { close(updates) }, func(event *signalmanpb.SignalmanEvent) bool {
		if event.EventType != signalmanpb.EventType_EVENT_TYPE_INCIDENT_UPDATED || !incidentEventForTenant(config.TenantID, event) {
			return true
		}
		update := incidentUpdatedFromIPC(event.GetData().GetIncidentUpdated())
		if update == nil {
			return true
		}
		return sendSubscriptionUpdate(ctx, updates, update)
	})
	if err != nil {
		return nil, err
	}
	return updates, nil
}

// SubscribeToPlatformIncidents delivers every incident change, of any scope and
// tenant, from Signalman's platform operator channel. Callers must have
// verified the platform operator grant.
func (sm *SubscriptionManager) SubscribeToPlatformIncidents(ctx context.Context, config ConnectionConfig) (<-chan *model.IncidentUpdatedEvent, error) {
	updates := make(chan *model.IncidentUpdatedEvent, 10)
	err := sm.startSubscription(ctx, "platform_incidents", config, []signalmanpb.Channel{signalmanpb.Channel_CHANNEL_PLATFORM}, func() { close(updates) }, func(event *signalmanpb.SignalmanEvent) bool {
		if event.Channel != signalmanpb.Channel_CHANNEL_PLATFORM || event.EventType != signalmanpb.EventType_EVENT_TYPE_INCIDENT_UPDATED {
			return true
		}
		update := incidentUpdatedFromIPC(event.GetData().GetIncidentUpdated())
		if update == nil {
			return true
		}
		return sendSubscriptionUpdate(ctx, updates, update)
	})
	if err != nil {
		return nil, err
	}
	return updates, nil
}

func incidentEventForTenant(tenantID string, event *signalmanpb.SignalmanEvent) bool {
	if tenantID == "" || event.TenantId == nil || *event.TenantId != tenantID {
		return false
	}
	return event.GetData().GetIncidentUpdated().GetTenantId() == tenantID
}

// SubscribeToTrackList subscribes to track list events and returns a channel of updates
func (sm *SubscriptionManager) SubscribeToTrackList(ctx context.Context, config ConnectionConfig, streamID string) (<-chan *ipcpb.StreamTrackListTrigger, error) {
	updates := make(chan *ipcpb.StreamTrackListTrigger, 10)
	err := sm.startSubscription(ctx, "track_list", config, []signalmanpb.Channel{signalmanpb.Channel_CHANNEL_STREAMS}, func() { close(updates) }, func(event *signalmanpb.SignalmanEvent) bool {
		if event.EventType != signalmanpb.EventType_EVENT_TYPE_STREAM_TRACK_LIST || tenantMismatch(config.TenantID, event) {
			return true
		}
		if msgStreamID := getStreamIDFromProtoEvent(event); msgStreamID == "" || msgStreamID != streamID {
			return true
		}
		if event.Data == nil {
			return true
		}
		tl := event.Data.GetTrackList()
		if tl == nil {
			return true
		}
		return sendSubscriptionUpdate(ctx, updates, tl)
	})
	if err != nil {
		return nil, err
	}
	return updates, nil
}

// SubscribeToLifecycle subscribes to lifecycle events (clip) and returns a channel
func (sm *SubscriptionManager) SubscribeToLifecycle(ctx context.Context, config ConnectionConfig, streamID string) (<-chan *ipcpb.ClipLifecycleData, error) {
	updates := make(chan *ipcpb.ClipLifecycleData, 10)
	err := sm.startSubscription(ctx, "lifecycle", config, []signalmanpb.Channel{signalmanpb.Channel_CHANNEL_ANALYTICS}, func() { close(updates) }, func(event *signalmanpb.SignalmanEvent) bool {
		if event.EventType != signalmanpb.EventType_EVENT_TYPE_CLIP_LIFECYCLE || tenantMismatch(config.TenantID, event) {
			return true
		}
		if event.Data == nil {
			return true
		}
		cl := event.Data.GetClipLifecycle()
		if cl == nil || cl.GetStreamId() != streamID {
			return true
		}
		return sendSubscriptionUpdate(ctx, updates, cl)
	})
	if err != nil {
		return nil, err
	}
	return updates, nil
}

// SubscribeToDVRLifecycle subscribes to DVR lifecycle events and returns a channel
func (sm *SubscriptionManager) SubscribeToDVRLifecycle(ctx context.Context, config ConnectionConfig, streamID string) (<-chan *ipcpb.DVRLifecycleData, error) {
	updates := make(chan *ipcpb.DVRLifecycleData, 10)
	err := sm.startSubscription(ctx, "dvr_lifecycle", config, []signalmanpb.Channel{signalmanpb.Channel_CHANNEL_ANALYTICS}, func() { close(updates) }, func(event *signalmanpb.SignalmanEvent) bool {
		if event.EventType != signalmanpb.EventType_EVENT_TYPE_DVR_LIFECYCLE || tenantMismatch(config.TenantID, event) {
			return true
		}
		if event.Data == nil {
			return true
		}
		dvr := event.Data.GetDvrLifecycle()
		if dvr == nil || dvr.GetStreamId() != streamID {
			return true
		}
		return sendSubscriptionUpdate(ctx, updates, dvr)
	})
	if err != nil {
		return nil, err
	}
	return updates, nil
}

// SubscribeToVodLifecycle subscribes to VOD lifecycle events, which Signalman
// delivers on the analytics channel.
func (sm *SubscriptionManager) SubscribeToVodLifecycle(ctx context.Context, config ConnectionConfig) (<-chan *ipcpb.VodLifecycleData, error) {
	updates := make(chan *ipcpb.VodLifecycleData, 10)
	err := sm.startSubscription(ctx, "vod_lifecycle", config, []signalmanpb.Channel{signalmanpb.Channel_CHANNEL_ANALYTICS}, func() { close(updates) }, func(event *signalmanpb.SignalmanEvent) bool {
		if event.EventType != signalmanpb.EventType_EVENT_TYPE_VOD_LIFECYCLE || tenantMismatch(config.TenantID, event) {
			return true
		}
		if event.Data == nil {
			return true
		}
		vod := event.Data.GetVodLifecycle()
		if vod == nil {
			return true
		}
		return sendSubscriptionUpdate(ctx, updates, vod)
	})
	if err != nil {
		return nil, err
	}
	return updates, nil
}

// SubscribeToMessages subscribes to messaging events and returns a channel
// Returns model.Message (mapped from MessageLifecycleData)
func (sm *SubscriptionManager) SubscribeToMessages(ctx context.Context, config ConnectionConfig, conversationID string) (<-chan *model.Message, error) {
	updates := make(chan *model.Message, 10)
	err := sm.startSubscription(ctx, "messages", config, []signalmanpb.Channel{signalmanpb.Channel_CHANNEL_MESSAGING}, func() { close(updates) }, func(event *signalmanpb.SignalmanEvent) bool {
		ml := messageLifecycleFor(event, config.TenantID, conversationID)
		if ml == nil || ml.EventType != ipcpb.MessageLifecycleData_EVENT_TYPE_MESSAGE_CREATED {
			return true
		}
		msg := mapMessageLifecycleToMessage(ml)
		if msg == nil {
			return true
		}
		return sendSubscriptionUpdate(ctx, updates, msg)
	})
	if err != nil {
		return nil, err
	}
	return updates, nil
}

// SubscribeToConversations subscribes to messaging events and returns conversation updates
func (sm *SubscriptionManager) SubscribeToConversations(ctx context.Context, config ConnectionConfig, conversationID string) (<-chan *model.Conversation, error) {
	updates := make(chan *model.Conversation, 10)
	err := sm.startSubscription(ctx, "conversations", config, []signalmanpb.Channel{signalmanpb.Channel_CHANNEL_MESSAGING}, func() { close(updates) }, func(event *signalmanpb.SignalmanEvent) bool {
		ml := messageLifecycleFor(event, config.TenantID, conversationID)
		if ml == nil {
			return true
		}
		switch ml.EventType {
		case ipcpb.MessageLifecycleData_EVENT_TYPE_CONVERSATION_CREATED,
			ipcpb.MessageLifecycleData_EVENT_TYPE_CONVERSATION_UPDATED,
			ipcpb.MessageLifecycleData_EVENT_TYPE_MESSAGE_CREATED,
			ipcpb.MessageLifecycleData_EVENT_TYPE_MESSAGE_UPDATED:
		default:
			return true
		}
		conv := mapMessageLifecycleToConversation(ml)
		if conv == nil {
			return true
		}
		return sendSubscriptionUpdate(ctx, updates, conv)
	})
	if err != nil {
		return nil, err
	}
	return updates, nil
}

// messageLifecycleFor returns the message lifecycle payload of event when it
// belongs to tenantID (when set) and conversationID (when set).
func messageLifecycleFor(event *signalmanpb.SignalmanEvent, tenantID, conversationID string) *ipcpb.MessageLifecycleData {
	if event.EventType != signalmanpb.EventType_EVENT_TYPE_MESSAGE_LIFECYCLE || event.Data == nil {
		return nil
	}
	ml := event.Data.GetMessageLifecycle()
	if ml == nil {
		return nil
	}
	if tenantID != "" && (ml.TenantId == nil || *ml.TenantId != tenantID) {
		return nil
	}
	if conversationID != "" && ml.GetConversationId() != conversationID {
		return nil
	}
	return ml
}

// SubscribeToFirehose subscribes to the streams, analytics, system, and AI
// channels and returns every tenant event on one channel.
func (sm *SubscriptionManager) SubscribeToFirehose(ctx context.Context, config ConnectionConfig) (<-chan *model.TenantEvent, error) {
	updates := make(chan *model.TenantEvent, 50)
	channels := []signalmanpb.Channel{
		signalmanpb.Channel_CHANNEL_STREAMS,
		signalmanpb.Channel_CHANNEL_ANALYTICS,
		signalmanpb.Channel_CHANNEL_SYSTEM,
		signalmanpb.Channel_CHANNEL_AI,
	}
	err := sm.startSubscription(ctx, "firehose", config, channels, func() { close(updates) }, func(event *signalmanpb.SignalmanEvent) bool {
		if tenantMismatch(config.TenantID, event) {
			return true
		}
		tenantEvent := sm.convertProtoToTenantEvent(event)
		if tenantEvent == nil {
			return true
		}
		return sendSubscriptionUpdate(ctx, updates, tenantEvent)
	})
	if err != nil {
		return nil, err
	}
	return updates, nil
}

// convertProtoToTenantEvent converts any Signalman proto event to a unified TenantEvent
// Uses proto enum strings for event type (e.g., EVENT_TYPE_STREAM_LIFECYCLE_UPDATE)
// Passes proto types directly where possible via gqlgen.yml bindings
func (sm *SubscriptionManager) convertProtoToTenantEvent(event *signalmanpb.SignalmanEvent) *model.TenantEvent {
	if event == nil {
		return nil
	}

	timestamp := time.Now()
	if event.Timestamp != nil {
		timestamp = event.Timestamp.AsTime()
	}

	// Use proto enum string directly (EVENT_TYPE_STREAM_LIFECYCLE_UPDATE, etc.)
	eventType := event.EventType.String()
	channel := sm.getChannelForEventType(event.EventType)
	if event.Channel != signalmanpb.Channel_CHANNEL_UNSPECIFIED {
		channel = channelToTenantChannel(event.Channel)
	}

	tenantEvent := &model.TenantEvent{
		Type:      eventType,
		Channel:   channel,
		Timestamp: timestamp,
	}

	if event.Data == nil {
		return tenantEvent
	}

	// Populate the appropriate event type based on the channel/event type
	// Pass proto types directly where possible via gqlgen.yml bindings
	switch event.EventType {
	case signalmanpb.EventType_EVENT_TYPE_STREAM_LIFECYCLE_UPDATE,
		signalmanpb.EventType_EVENT_TYPE_STREAM_END,
		signalmanpb.EventType_EVENT_TYPE_STREAM_BUFFER,
		signalmanpb.EventType_EVENT_TYPE_PUSH_REWRITE,
		signalmanpb.EventType_EVENT_TYPE_STREAM_SOURCE,
		signalmanpb.EventType_EVENT_TYPE_PLAY_REWRITE:
		tenantEvent.StreamEvent = mapSignalmanStreamEvent(event)

	case signalmanpb.EventType_EVENT_TYPE_VIEWER_CONNECT,
		signalmanpb.EventType_EVENT_TYPE_VIEWER_DISCONNECT:
		tenantEvent.ConnectionEvent = mapSignalmanConnectionEvent(event)

	case signalmanpb.EventType_EVENT_TYPE_CLIENT_LIFECYCLE_UPDATE:
		// Pass proto ClientLifecycleUpdate directly (bound to ViewerMetrics)
		tenantEvent.ViewerMetrics = event.Data.GetClientLifecycle()

	case signalmanpb.EventType_EVENT_TYPE_STREAM_TRACK_LIST:
		// Pass proto StreamTrackListTrigger directly (bound to TrackListUpdate)
		tenantEvent.TrackListUpdate = event.Data.GetTrackList()

	case signalmanpb.EventType_EVENT_TYPE_CLIP_LIFECYCLE:
		// Pass proto ClipLifecycleData directly (bound to ClipLifecycle)
		tenantEvent.ClipLifecycle = event.Data.GetClipLifecycle()

	case signalmanpb.EventType_EVENT_TYPE_DVR_LIFECYCLE:
		// Pass proto DVRLifecycleData directly (bound to DVREvent)
		tenantEvent.DvrEvent = event.Data.GetDvrLifecycle()

	case signalmanpb.EventType_EVENT_TYPE_NODE_LIFECYCLE_UPDATE:
		// Pass proto NodeLifecycleUpdate directly (bound to SystemHealthEvent)
		tenantEvent.SystemHealthEvent = event.Data.GetNodeLifecycle()

	case signalmanpb.EventType_EVENT_TYPE_LOAD_BALANCING:
		tenantEvent.RoutingEvent = mapSignalmanRoutingEvent(event)

	case signalmanpb.EventType_EVENT_TYPE_VOD_LIFECYCLE:
		// Pass proto VodLifecycleData directly (bound via gqlgen.yml)
		tenantEvent.VodLifecycle = event.Data.GetVodLifecycle()

	case signalmanpb.EventType_EVENT_TYPE_STORAGE_LIFECYCLE:
		tenantEvent.StorageEvent = mapSignalmanStorageEvent(event)

	case signalmanpb.EventType_EVENT_TYPE_PROCESS_BILLING:
		tenantEvent.ProcessingEvent = mapSignalmanProcessingEvent(event)
	case signalmanpb.EventType_EVENT_TYPE_STORAGE_SNAPSHOT:
		tenantEvent.StorageSnapshot = event.Data.GetStorageSnapshot()

	case signalmanpb.EventType_EVENT_TYPE_SKIPPER_INVESTIGATION:
		tenantEvent.SkipperInvestigation = &model.SkipperInvestigationEvent{
			ReportID:     "",
			ResourceType: "skipper_investigation",
		}

	case signalmanpb.EventType_EVENT_TYPE_INCIDENT_UPDATED:
		if event.TenantId != nil && incidentEventForTenant(*event.TenantId, event) {
			tenantEvent.IncidentUpdated = incidentUpdatedFromIPC(event.Data.GetIncidentUpdated())
		}
	}

	return tenantEvent
}

// getChannelForEventType returns the channel name for a given event type
func (sm *SubscriptionManager) getChannelForEventType(eventType signalmanpb.EventType) string {
	switch eventType {
	case signalmanpb.EventType_EVENT_TYPE_STREAM_LIFECYCLE_UPDATE,
		signalmanpb.EventType_EVENT_TYPE_STREAM_TRACK_LIST,
		signalmanpb.EventType_EVENT_TYPE_STREAM_BUFFER,
		signalmanpb.EventType_EVENT_TYPE_STREAM_END,
		signalmanpb.EventType_EVENT_TYPE_PUSH_REWRITE,
		signalmanpb.EventType_EVENT_TYPE_STREAM_SOURCE,
		signalmanpb.EventType_EVENT_TYPE_PLAY_REWRITE,
		signalmanpb.EventType_EVENT_TYPE_VOD_LIFECYCLE:
		return "STREAMS"

	case signalmanpb.EventType_EVENT_TYPE_NODE_LIFECYCLE_UPDATE,
		signalmanpb.EventType_EVENT_TYPE_LOAD_BALANCING,
		signalmanpb.EventType_EVENT_TYPE_INCIDENT_UPDATED:
		return "SYSTEM"
	case signalmanpb.EventType_EVENT_TYPE_STORAGE_LIFECYCLE,
		signalmanpb.EventType_EVENT_TYPE_PROCESS_BILLING,
		signalmanpb.EventType_EVENT_TYPE_STORAGE_SNAPSHOT:
		return "ANALYTICS"
	case signalmanpb.EventType_EVENT_TYPE_MESSAGE_LIFECYCLE:
		return "MESSAGING"
	case signalmanpb.EventType_EVENT_TYPE_SKIPPER_INVESTIGATION:
		return "AI"

	default:
		return "ANALYTICS"
	}
}

func channelToTenantChannel(channel signalmanpb.Channel) string {
	switch channel {
	case signalmanpb.Channel_CHANNEL_STREAMS:
		return "STREAMS"
	case signalmanpb.Channel_CHANNEL_ANALYTICS:
		return "ANALYTICS"
	case signalmanpb.Channel_CHANNEL_SYSTEM:
		return "SYSTEM"
	case signalmanpb.Channel_CHANNEL_ALL:
		return "ALL"
	case signalmanpb.Channel_CHANNEL_MESSAGING:
		return "MESSAGING"
	case signalmanpb.Channel_CHANNEL_AI:
		return "AI"
	default:
		return "ANALYTICS"
	}
}

func tenantMismatch(tenantID string, event *signalmanpb.SignalmanEvent) bool {
	if tenantID == "" || event == nil {
		return false
	}
	// Some system/infrastructure broadcasts are emitted without a tenant id.
	// Keep delivering those to tenant-scoped subscribers.
	if event.TenantId == nil {
		return false
	}
	return *event.TenantId != tenantID
}

// mapMessageLifecycleToMessage converts proto MessageLifecycleData to GraphQL Message
func mapMessageLifecycleToMessage(ml *ipcpb.MessageLifecycleData) *model.Message {
	if ml == nil {
		return nil
	}

	rawConversationID := ml.GetConversationId()
	if rawConversationID == "" {
		return nil
	}

	// Parse sender - default to AGENT if unknown
	sender := deckhandpb.MessageSender_MESSAGE_SENDER_AGENT
	if ml.Sender != nil {
		switch *ml.Sender {
		case "USER":
			sender = deckhandpb.MessageSender_MESSAGE_SENDER_USER
		case "AGENT":
			sender = deckhandpb.MessageSender_MESSAGE_SENDER_AGENT
		}
	}

	// Get message ID (use conversation ID as fallback)
	msgID := rawConversationID
	if ml.MessageId != nil && *ml.MessageId != "" {
		msgID = *ml.MessageId
	}

	// Get content
	content := ""
	if ml.Content != nil {
		content = *ml.Content
	}

	return &model.Message{
		ID:             globalid.EncodeComposite(globalid.TypeMessage, rawConversationID, msgID),
		ConversationID: globalid.Encode(globalid.TypeConversation, rawConversationID),
		Content:        content,
		Sender:         sender,
		CreatedAt:      time.Unix(ml.Timestamp, 0),
	}
}

func mapMessageLifecycleToConversation(ml *ipcpb.MessageLifecycleData) *model.Conversation {
	if ml == nil {
		return nil
	}

	rawConversationID := ml.GetConversationId()
	if rawConversationID == "" {
		return nil
	}

	status := parseConversationStatus(ml.Status)
	subject := (*string)(nil)
	if ml.Subject != nil && *ml.Subject != "" {
		subject = ml.Subject
	}

	timestamp := ml.Timestamp
	var updatedAt time.Time
	if timestamp > 0 {
		updatedAt = time.Unix(timestamp, 0)
	} else {
		updatedAt = time.Now()
	}

	var lastMessage *model.Message
	switch ml.EventType {
	case ipcpb.MessageLifecycleData_EVENT_TYPE_MESSAGE_CREATED,
		ipcpb.MessageLifecycleData_EVENT_TYPE_MESSAGE_UPDATED:
		lastMessage = mapMessageLifecycleToMessage(ml)
	}

	return &model.Conversation{
		ID:          globalid.Encode(globalid.TypeConversation, rawConversationID),
		Subject:     subject,
		Status:      status,
		LastMessage: lastMessage,
		UnreadCount: 0,
		CreatedAt:   updatedAt,
		UpdatedAt:   updatedAt,
	}
}

func parseConversationStatus(status *string) deckhandpb.ConversationStatus {
	if status == nil || *status == "" {
		return deckhandpb.ConversationStatus_CONVERSATION_STATUS_OPEN
	}

	switch strings.ToUpper(*status) {
	case "OPEN":
		return deckhandpb.ConversationStatus_CONVERSATION_STATUS_OPEN
	case "RESOLVED":
		return deckhandpb.ConversationStatus_CONVERSATION_STATUS_RESOLVED
	case "PENDING":
		return deckhandpb.ConversationStatus_CONVERSATION_STATUS_PENDING
	default:
		return deckhandpb.ConversationStatus_CONVERSATION_STATUS_OPEN
	}
}

// Shutdown ends every served subscription, closes the upstream streams, and
// waits for their loops to exit.
func (sm *SubscriptionManager) Shutdown() error {
	sm.mu.Lock()
	if sm.closed {
		sm.mu.Unlock()
		return nil
	}
	sm.closed = true
	for key, entry := range sm.upstreams {
		if entry.linger != nil {
			entry.linger.Stop()
		}
		entry.cancel()
		entry.fanout.Close(errSubscriptionManagerClosed)
		delete(sm.upstreams, key)
	}
	sm.mu.Unlock()

	sm.cancel()
	sm.wg.Wait()
	err := sm.opener.Close()
	sm.logger.Info("Subscription manager shutdown completed")
	return err
}

// getStreamIDFromProtoEvent extracts stream ID from a proto SignalmanEvent
func getStreamIDFromProtoEvent(event *signalmanpb.SignalmanEvent) string {
	if event.Data == nil {
		return ""
	}

	// Check each possible payload type for stream identification
	raw := ""
	if cl := event.Data.GetClientLifecycle(); cl != nil {
		raw = cl.GetStreamId()
	} else if tl := event.Data.GetTrackList(); tl != nil {
		raw = tl.GetStreamId()
	} else if cl := event.Data.GetClipLifecycle(); cl != nil {
		raw = cl.GetStreamId()
	} else if dl := event.Data.GetDvrLifecycle(); dl != nil {
		raw = dl.GetStreamId()
	} else if lb := event.Data.GetLoadBalancing(); lb != nil {
		raw = lb.GetStreamId()
	} else if pr := event.Data.GetPushRewrite(); pr != nil {
		raw = pr.GetStreamId()
	} else if pr := event.Data.GetPlayRewrite(); pr != nil {
		raw = pr.GetStreamId()
	} else if ss := event.Data.GetStreamSource(); ss != nil {
		raw = ss.GetStreamId()
	} else if pos := event.Data.GetPushOutStart(); pos != nil {
		raw = pos.GetStreamId()
	} else if pe := event.Data.GetPushEnd(); pe != nil {
		raw = pe.GetStreamId()
	} else if vc := event.Data.GetViewerConnect(); vc != nil {
		raw = vc.GetStreamId()
	} else if vd := event.Data.GetViewerDisconnect(); vd != nil {
		raw = vd.GetStreamId()
	} else if se := event.Data.GetStreamEnd(); se != nil {
		raw = se.GetStreamId()
	} else if event.Data.GetRecording() != nil {
		raw = ""
	} else if buf := event.Data.GetStreamBuffer(); buf != nil {
		raw = buf.GetStreamId()
	} else if sl := event.Data.GetStreamLifecycle(); sl != nil {
		raw = sl.GetStreamId()
	} else if change := event.Data.GetStreamChange(); change != nil {
		raw = change.GetStreamId()
	} else if st := event.Data.GetStorageLifecycle(); st != nil {
		raw = st.GetStreamId()
	} else if pbill := event.Data.GetProcessBilling(); pbill != nil {
		raw = pbill.GetStreamId()
	}

	if raw == "" {
		return ""
	}
	return raw
}
