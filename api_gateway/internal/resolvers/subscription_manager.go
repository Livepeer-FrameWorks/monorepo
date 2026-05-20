package resolvers

import (
	"context"
	"fmt"
	"hash/fnv"
	"strings"
	"sync"
	"time"

	"frameworks/api_gateway/graph/model"
	signalmanclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/signalman"
	pkgconfig "github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/globalid"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

// SubscriptionManager manages gRPC streaming connections to Signalman for GraphQL subscriptions.
//
// signalmanAddr is the gateway's local-region Signalman (set by
// SIGNALMAN_GRPC_ADDR). signalmanAddrByRegion maps stream-origin region to a
// regional Signalman address (parsed from SIGNALMAN_GRPC_ADDR_BY_REGION); when
// a caller knows a stream's origin region, AddrForRegion picks that region's
// Signalman so live events stay in their origin cell. Empty region falls back
// to the local addr.
// StreamOriginResolver returns the region_id of the cluster currently sinking
// ingest for streamID. Empty result means "unknown" (treat as local). Used by
// stream-scoped subscribe paths so a viewer on EU bridge can attach to the
// US-origin stream's Signalman.
type StreamOriginResolver func(ctx context.Context, streamID string) (string, error)

const subscriptionClientKeySep = "\x00"

type SubscriptionManager struct {
	clients                 map[string]*signalmanclient.GRPCClient // Key: userID:tenantID:addr
	logger                  logging.Logger
	mutex                   sync.RWMutex
	signalmanAddr           string              // single-target fallback
	signalmanAddrsLocal     []string            // local-region replica list (preferred)
	signalmanAddrByRegion   map[string]string   // per-region single-target fallback
	signalmanAddrsByRegion  map[string][]string // multi-target replica list per region
	serviceToken            string              // Service token for service-to-service authentication
	cleanup                 chan string         // Channel for cleanup signals
	stopChan                chan struct{}
	metrics                 *GraphQLMetrics
	maxConnectionsPerTenant int
	tenantConnectionCounts  map[string]int
	streamOriginResolver    StreamOriginResolver
}

func subscriptionClientKey(userID, tenantID, addr string) string {
	return strings.Join([]string{userID, tenantID, addr}, subscriptionClientKeySep)
}

func tenantIDFromSubscriptionClientKey(key string) string {
	if parts := strings.SplitN(key, subscriptionClientKeySep, 3); len(parts) == 3 {
		return parts[1]
	}
	parts := strings.SplitN(key, ":", 3)
	if len(parts) >= 2 {
		return parts[1]
	}
	return ""
}

// SetStreamOriginResolver installs the resolver used by stream-scoped
// subscribe paths. Safe to call once at startup; not safe to swap at runtime.
func (sm *SubscriptionManager) SetStreamOriginResolver(r StreamOriginResolver) {
	sm.streamOriginResolver = r
}

// connectionAddrsForStream returns the Signalman replicas for a stream-scoped
// subscription. Empty streamID, no resolver, or a resolver lookup failure all
// fall back to the local-region replicas.
func (sm *SubscriptionManager) connectionAddrsForStream(ctx context.Context, streamID, tenantID string) []string {
	if streamID == "" || sm.streamOriginResolver == nil {
		return sm.addrsForRegion("", tenantID)
	}
	region, err := sm.streamOriginResolver(ctx, streamID)
	if err != nil {
		sm.logger.WithError(err).WithField("stream_id", streamID).Debug("stream origin lookup failed; using local Signalman")
		return sm.addrsForRegion("", tenantID)
	}
	return sm.addrsForRegion(region, tenantID)
}

// parseSignalmanAddrByRegion parses comma-separated `region=addr` pairs
// from SIGNALMAN_GRPC_ADDR_BY_REGION into a map. Empty input returns nil.
func parseSignalmanAddrByRegion(raw string) map[string]string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	out := map[string]string{}
	for entry := range strings.SplitSeq(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		eq := strings.IndexByte(entry, '=')
		if eq <= 0 || eq == len(entry)-1 {
			continue
		}
		region := strings.TrimSpace(entry[:eq])
		addr := strings.TrimSpace(entry[eq+1:])
		if region == "" || addr == "" {
			continue
		}
		out[region] = addr
	}
	return out
}

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

// parseSignalmanAddrsByRegion parses `region=a,b,c;region=d,e` from
// SIGNALMAN_GRPC_ADDRS_BY_REGION into a region→[]addr map. Semicolons separate
// regions; commas separate replicas within a region. Empty input returns nil.
func parseSignalmanAddrsByRegion(raw string) map[string][]string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	out := map[string][]string{}
	for _, entry := range strings.Split(raw, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		eq := strings.IndexByte(entry, '=')
		if eq <= 0 || eq == len(entry)-1 {
			continue
		}
		region := strings.TrimSpace(entry[:eq])
		addrs := parseSignalmanAddrs(entry[eq+1:])
		if region == "" || len(addrs) == 0 {
			continue
		}
		out[region] = addrs
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// AddrForRegion returns the Signalman address that should serve subscriptions
// for a stream whose origin region is `region`. Empty region or unknown region
// falls back to the local Signalman.
func (sm *SubscriptionManager) AddrForRegion(region string) string {
	addrs := sm.addrsForRegion(region, "")
	if len(addrs) == 0 {
		return sm.signalmanAddr
	}
	return addrs[0]
}

// addrsForRegion returns the ordered list of Signalman replicas to attempt for
// the given region. The first entry is the preferred dial target; subsequent
// entries are failover candidates. Order is rotated by tenantID hash so
// distinct tenants prefer different replicas (load spread). Empty/unknown
// region falls back to the local-region list, then the single-addr value.
func (sm *SubscriptionManager) addrsForRegion(region, tenantID string) []string {
	var addrs []string
	if region != "" {
		if multi := sm.signalmanAddrsByRegion[region]; len(multi) > 0 {
			addrs = multi
		} else if single, ok := sm.signalmanAddrByRegion[region]; ok && single != "" {
			addrs = []string{single}
		}
	}
	if len(addrs) == 0 {
		if len(sm.signalmanAddrsLocal) > 0 {
			addrs = sm.signalmanAddrsLocal
		} else if sm.signalmanAddr != "" {
			addrs = []string{sm.signalmanAddr}
		}
	}
	return rotateAddrs(addrs, tenantID)
}

// rotateAddrs returns a copy of addrs rotated by hash(tenantID) so each tenant
// prefers a stable but tenant-dependent entry. With one address the result is
// a one-element slice; the failover loop in tryConnect uses the rest as
// fallbacks in order.
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

func (sm *SubscriptionManager) incrementTenantConnection(tenantID string) {
	if sm.maxConnectionsPerTenant <= 0 || tenantID == "" {
		return
	}
	sm.tenantConnectionCounts[tenantID]++
}

func (sm *SubscriptionManager) decrementTenantConnection(tenantID string) {
	if sm.maxConnectionsPerTenant <= 0 || tenantID == "" {
		return
	}
	if current, ok := sm.tenantConnectionCounts[tenantID]; ok {
		if current <= 1 {
			delete(sm.tenantConnectionCounts, tenantID)
		} else {
			sm.tenantConnectionCounts[tenantID] = current - 1
		}
	}
}

func (sm *SubscriptionManager) removeClientLocked(key string, client *signalmanclient.GRPCClient, tenantID string) {
	if err := client.Close(); err != nil {
		sm.logger.WithError(err).WithFields(logging.Fields{
			"tenant_id": tenantID,
		}).Warn("Failed to close Signalman gRPC client")
	}
	delete(sm.clients, key)
	sm.decrementTenantConnection(tenantID)
}

func (sm *SubscriptionManager) removeClient(config ConnectionConfig, client *signalmanclient.GRPCClient) {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()
	for key, existing := range sm.clients {
		if existing == client {
			sm.removeClientLocked(key, client, config.TenantID)
			return
		}
	}
	if err := client.Close(); err != nil {
		sm.logger.WithError(err).WithField("tenant_id", config.TenantID).Warn("Failed to close uncached Signalman client")
	}
}

func waitSignalmanRetry(ctx context.Context) bool {
	timer := time.NewTimer(500 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (sm *SubscriptionManager) runSignalmanSubscription(ctx context.Context, config ConnectionConfig, addrs []string, initial *signalmanclient.GRPCClient, closeOutput func(), subscribe func(*signalmanclient.GRPCClient) error, handle func(*pb.SignalmanEvent) bool) {
	defer closeOutput()
	client := initial
	initialSubscribed := initial != nil

	for {
		if client == nil {
			var err error
			client, err = sm.getOrCreateConnectionFromList(ctx, config, addrs)
			if err != nil {
				sm.logger.WithError(err).WithField("tenant_id", config.TenantID).Warn("Failed to connect to Signalman replica")
				if !waitSignalmanRetry(ctx) {
					return
				}
				continue
			}
		}

		if !initialSubscribed {
			if err := subscribe(client); err != nil {
				sm.logger.WithError(err).WithField("tenant_id", config.TenantID).Warn("Failed to subscribe to Signalman replica")
				sm.removeClient(config, client)
				client = nil
				if !waitSignalmanRetry(ctx) {
					return
				}
				continue
			}
		}
		initialSubscribed = false

		retry := sm.drainSignalmanEvents(ctx, client, handle)
		if !retry {
			return
		}
		sm.removeClient(config, client)
		client = nil
		if !waitSignalmanRetry(ctx) {
			return
		}
	}
}

func (sm *SubscriptionManager) drainSignalmanEvents(ctx context.Context, client *signalmanclient.GRPCClient, handle func(*pb.SignalmanEvent) bool) bool {
	events := client.Events()
	errors := client.Errors()
	for {
		select {
		case <-ctx.Done():
			return false
		case err := <-errors:
			if err != nil {
				sm.logger.WithError(err).Warn("Signalman stream failed; reconnecting subscription")
				return true
			}
		case event := <-events:
			if event == nil {
				continue
			}
			if !handle(event) {
				return false
			}
		}
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

// ConnectionConfig represents configuration for a gRPC connection
type ConnectionConfig struct {
	UserID   string
	TenantID string
	JWT      string // JWT is kept for compatibility but not used in gRPC (auth via metadata if needed)
}

// SubscriptionManagerConfig keeps the preferred replica lists and the
// single-address fallbacks in one startup config.
type SubscriptionManagerConfig struct {
	SignalmanAddr           string              // single addr fallback
	SignalmanAddrsLocal     []string            // local-region replica list (preferred)
	SignalmanAddrByRegion   map[string]string   // per-region single addr fallback
	SignalmanAddrsByRegion  map[string][]string // per-region replica lists (preferred)
	ServiceToken            string
	MaxConnectionsPerTenant int
	Metrics                 *GraphQLMetrics
}

// NewSubscriptionManager creates a new gRPC subscription connection manager
// from a SubscriptionManagerConfig. Entries with empty values are ignored.
func NewSubscriptionManager(logger logging.Logger, cfg SubscriptionManagerConfig) *SubscriptionManager {
	regionalSingle := make(map[string]string, len(cfg.SignalmanAddrByRegion))
	for region, addr := range cfg.SignalmanAddrByRegion {
		region = strings.TrimSpace(region)
		addr = strings.TrimSpace(addr)
		if region == "" || addr == "" {
			continue
		}
		regionalSingle[region] = addr
	}
	regionalMulti := make(map[string][]string, len(cfg.SignalmanAddrsByRegion))
	for region, addrs := range cfg.SignalmanAddrsByRegion {
		region = strings.TrimSpace(region)
		clean := make([]string, 0, len(addrs))
		for _, a := range addrs {
			a = strings.TrimSpace(a)
			if a != "" {
				clean = append(clean, a)
			}
		}
		if region == "" || len(clean) == 0 {
			continue
		}
		regionalMulti[region] = clean
	}
	local := make([]string, 0, len(cfg.SignalmanAddrsLocal))
	for _, a := range cfg.SignalmanAddrsLocal {
		a = strings.TrimSpace(a)
		if a != "" {
			local = append(local, a)
		}
	}

	sm := &SubscriptionManager{
		clients:                 make(map[string]*signalmanclient.GRPCClient),
		logger:                  logger,
		signalmanAddr:           cfg.SignalmanAddr,
		signalmanAddrsLocal:     local,
		signalmanAddrByRegion:   regionalSingle,
		signalmanAddrsByRegion:  regionalMulti,
		serviceToken:            cfg.ServiceToken,
		cleanup:                 make(chan string, 10),
		stopChan:                make(chan struct{}),
		metrics:                 cfg.Metrics,
		maxConnectionsPerTenant: cfg.MaxConnectionsPerTenant,
		tenantConnectionCounts:  make(map[string]int),
	}

	// Start cleanup goroutine
	go sm.cleanupWorker()

	return sm
}

// GetOrCreateConnection gets an existing connection or creates a new one for a
// user/tenant pair targeting the local-region Signalman. With multiple local
// replicas, the picker rotates by tenant hash and fails over to the next
// replica if the chosen one cannot be reached.
func (sm *SubscriptionManager) GetOrCreateConnection(ctx context.Context, config ConnectionConfig) (*signalmanclient.GRPCClient, error) {
	return sm.getOrCreateConnectionFromList(ctx, config, sm.addrsForRegion("", config.TenantID))
}

// GetOrCreateConnectionForRegion picks the regional Signalman addr for the
// stream-origin region and returns a connection to it. Empty/unknown region
// falls back to the local-region list. The connection-cache key includes the
// addr so EU and US viewers of a US-origin stream don't share a connection.
func (sm *SubscriptionManager) GetOrCreateConnectionForRegion(ctx context.Context, config ConnectionConfig, region string) (*signalmanclient.GRPCClient, error) {
	return sm.getOrCreateConnectionFromList(ctx, config, sm.addrsForRegion(region, config.TenantID))
}

// getOrCreateConnectionFromList tries each addr in order. Returns the first
// connection that opens; on transient connect failures (network or grpc), it
// tries the next addr. The returned error wraps the last failure. A successful
// connection is cached keyed by addr so subsequent requests for the same user/
// tenant/addr reuse it; when the stream dies the periodic cleanup evicts it
// and the next request can land on any replica.
func (sm *SubscriptionManager) getOrCreateConnectionFromList(ctx context.Context, config ConnectionConfig, addrs []string) (*signalmanclient.GRPCClient, error) {
	if len(addrs) == 0 {
		if sm.signalmanAddr == "" {
			return nil, fmt.Errorf("no Signalman addresses configured")
		}
		addrs = []string{sm.signalmanAddr}
	}

	// Reuse any already-connected client across the candidate set first.
	sm.mutex.RLock()
	for _, addr := range addrs {
		key := subscriptionClientKey(config.UserID, config.TenantID, addr)
		if client, exists := sm.clients[key]; exists && client.IsConnected() {
			sm.mutex.RUnlock()
			return client, nil
		}
	}
	sm.mutex.RUnlock()

	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	// Double-check after acquiring write lock.
	for _, addr := range addrs {
		key := subscriptionClientKey(config.UserID, config.TenantID, addr)
		if client, exists := sm.clients[key]; exists && client.IsConnected() {
			return client, nil
		}
		if client, exists := sm.clients[key]; exists {
			sm.removeClientLocked(key, client, config.TenantID)
		}
	}

	if sm.maxConnectionsPerTenant > 0 && sm.tenantConnectionCounts[config.TenantID] >= sm.maxConnectionsPerTenant {
		sm.logger.WithFields(logging.Fields{
			"tenant_id": config.TenantID,
			"limit":     sm.maxConnectionsPerTenant,
		}).Warn("Reached max Signalman connections for tenant")
		return nil, fmt.Errorf("tenant %s has reached the max number of active subscriptions", config.TenantID)
	}

	var lastErr error
	connectTimeout := time.Duration(pkgconfig.GetEnvInt("SIGNALMAN_CONNECT_TIMEOUT_SECONDS", 5)) * time.Second
	for _, addr := range addrs {
		client, err := signalmanclient.NewGRPCClient(signalmanclient.GRPCConfig{
			GRPCAddr:      addr,
			Timeout:       connectTimeout,
			Logger:        sm.logger,
			UserID:        config.UserID,
			TenantID:      config.TenantID,
			ServiceToken:  sm.serviceToken,
			AllowInsecure: pkgconfig.GetEnvBool("GRPC_ALLOW_INSECURE", false),
			CACertFile:    pkgconfig.GetEnv("GRPC_TLS_CA_PATH", ""),
			ServerName:    pkgconfig.GetServiceGRPCTLSServerName("signalman"),
		})
		if err != nil {
			lastErr = err
			sm.logger.WithError(err).WithFields(logging.Fields{
				"signalman_addr": addr,
				"tenant_id":      config.TenantID,
			}).Warn("Failed to create Signalman gRPC client; trying next replica")
			continue
		}

		if err := client.Connect(ctx); err != nil {
			lastErr = err
			if closeErr := client.Close(); closeErr != nil {
				sm.logger.WithError(closeErr).WithField("signalman_addr", addr).Warn("Failed to close Signalman client after connect failure")
			}
			sm.logger.WithError(err).WithFields(logging.Fields{
				"signalman_addr": addr,
				"tenant_id":      config.TenantID,
			}).Warn("Failed to connect to Signalman replica; trying next")
			continue
		}

		key := subscriptionClientKey(config.UserID, config.TenantID, addr)
		sm.clients[key] = client
		sm.incrementTenantConnection(config.TenantID)
		if sm.metrics != nil {
			sm.metrics.WebSocketConnections.WithLabelValues(config.TenantID).Inc()
			sm.metrics.WebSocketMessages.WithLabelValues("outbound", "connection_success").Inc()
		}
		sm.logger.WithFields(logging.Fields{
			"user_id":        config.UserID,
			"tenant_id":      config.TenantID,
			"signalman_addr": addr,
		}).Info("Created new gRPC connection to Signalman")
		return client, nil
	}

	if sm.metrics != nil {
		sm.metrics.WebSocketMessages.WithLabelValues("outbound", "connection_error").Inc()
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no Signalman addresses available")
	}
	return nil, fmt.Errorf("failed to connect to any Signalman replica (%d tried): %w", len(addrs), lastErr)
}

// streamScopedAddrs returns candidate Signalman replicas for a `*string`
// streamID. Nil means tenant-global, so it falls back to local replicas.
func (sm *SubscriptionManager) streamScopedAddrs(ctx context.Context, streamID *string, tenantID string) []string {
	if streamID == nil {
		return sm.addrsForRegion("", tenantID)
	}
	return sm.connectionAddrsForStream(ctx, *streamID, tenantID)
}

// SubscribeToStreams subscribes to stream events and returns a channel of updates
// Returns model.StreamEvent (canonical live stream event shape)
func (sm *SubscriptionManager) SubscribeToStreams(ctx context.Context, config ConnectionConfig, streamID *string) (<-chan *model.StreamEvent, error) {
	addrs := sm.streamScopedAddrs(ctx, streamID, config.TenantID)
	client, err := sm.getOrCreateConnectionFromList(ctx, config, addrs)
	if err != nil {
		return nil, err
	}

	if err := client.SubscribeToStreams(); err != nil {
		return nil, fmt.Errorf("failed to subscribe to streams: %w", err)
	}

	updates := make(chan *model.StreamEvent, 10)
	go sm.runSignalmanSubscription(ctx, config, addrs, client, func() { close(updates) }, func(c *signalmanclient.GRPCClient) error {
		return c.SubscribeToStreams()
	}, func(event *pb.SignalmanEvent) bool {
		if event.EventType != pb.EventType_EVENT_TYPE_STREAM_LIFECYCLE_UPDATE &&
			event.EventType != pb.EventType_EVENT_TYPE_STREAM_END &&
			event.EventType != pb.EventType_EVENT_TYPE_STREAM_BUFFER &&
			event.EventType != pb.EventType_EVENT_TYPE_STREAM_TRACK_LIST &&
			event.EventType != pb.EventType_EVENT_TYPE_PUSH_REWRITE &&
			event.EventType != pb.EventType_EVENT_TYPE_STREAM_SOURCE &&
			event.EventType != pb.EventType_EVENT_TYPE_PLAY_REWRITE {
			return true
		}
		if tenantMismatch(config.TenantID, event) {
			return true
		}
		if streamID != nil {
			msgStreamID := getStreamIDFromProtoEvent(event)
			if msgStreamID == "" || msgStreamID != *streamID {
				return true
			}
		}
		update := mapSignalmanStreamEvent(event)
		if update == nil {
			return true
		}
		return sendSubscriptionUpdate(ctx, updates, update)
	})
	return updates, nil
}

// SubscribeToAnalytics subscribes to analytics events and returns a channel of updates
// Returns proto.ClientLifecycleUpdate directly (bound to GraphQL ViewerMetrics)
func (sm *SubscriptionManager) SubscribeToAnalytics(ctx context.Context, config ConnectionConfig, streamID *string) (<-chan *pb.ClientLifecycleUpdate, error) {
	addrs := sm.streamScopedAddrs(ctx, streamID, config.TenantID)
	client, err := sm.getOrCreateConnectionFromList(ctx, config, addrs)
	if err != nil {
		return nil, err
	}

	if err := client.SubscribeToAnalytics(); err != nil {
		return nil, fmt.Errorf("failed to subscribe to analytics: %w", err)
	}

	updates := make(chan *pb.ClientLifecycleUpdate, 10)
	go sm.runSignalmanSubscription(ctx, config, addrs, client, func() { close(updates) }, func(c *signalmanclient.GRPCClient) error {
		return c.SubscribeToAnalytics()
	}, func(event *pb.SignalmanEvent) bool {
		if event.EventType != pb.EventType_EVENT_TYPE_CLIENT_LIFECYCLE_UPDATE || tenantMismatch(config.TenantID, event) {
			return true
		}
		if streamID != nil {
			msgStreamID := getStreamIDFromProtoEvent(event)
			if msgStreamID == "" || msgStreamID != *streamID {
				return true
			}
		}
		if event.Data == nil {
			return true
		}
		cl := event.Data.GetClientLifecycle()
		if cl == nil {
			return true
		}
		return sendSubscriptionUpdate(ctx, updates, cl)
	})
	return updates, nil
}

// SubscribeToConnections subscribes to viewer connection events and returns a channel of updates
// Returns proto.ConnectionEvent directly (bound to GraphQL ConnectionEvent)
func (sm *SubscriptionManager) SubscribeToConnections(ctx context.Context, config ConnectionConfig, streamID *string) (<-chan *pb.ConnectionEvent, error) {
	addrs := sm.streamScopedAddrs(ctx, streamID, config.TenantID)
	client, err := sm.getOrCreateConnectionFromList(ctx, config, addrs)
	if err != nil {
		return nil, err
	}

	if err := client.SubscribeToAnalytics(); err != nil {
		return nil, fmt.Errorf("failed to subscribe to analytics: %w", err)
	}

	updates := make(chan *pb.ConnectionEvent, 10)
	go sm.runSignalmanSubscription(ctx, config, addrs, client, func() { close(updates) }, func(c *signalmanclient.GRPCClient) error {
		return c.SubscribeToAnalytics()
	}, func(event *pb.SignalmanEvent) bool {
		if event.EventType != pb.EventType_EVENT_TYPE_VIEWER_CONNECT &&
			event.EventType != pb.EventType_EVENT_TYPE_VIEWER_DISCONNECT {
			return true
		}
		if tenantMismatch(config.TenantID, event) {
			return true
		}
		if streamID != nil {
			msgStreamID := getStreamIDFromProtoEvent(event)
			if msgStreamID == "" || msgStreamID != *streamID {
				return true
			}
		}
		ce := mapSignalmanConnectionEvent(event)
		if ce == nil {
			return true
		}
		return sendSubscriptionUpdate(ctx, updates, ce)
	})
	return updates, nil
}

// SubscribeToStorageEvents subscribes to storage lifecycle events and returns a channel of updates
// Returns proto.StorageEvent (mapped from StorageLifecycleData)
func (sm *SubscriptionManager) SubscribeToStorageEvents(ctx context.Context, config ConnectionConfig, streamID *string) (<-chan *pb.StorageEvent, error) {
	addrs := sm.streamScopedAddrs(ctx, streamID, config.TenantID)
	client, err := sm.getOrCreateConnectionFromList(ctx, config, addrs)
	if err != nil {
		return nil, err
	}

	if err := client.SubscribeToAnalytics(); err != nil {
		return nil, fmt.Errorf("failed to subscribe to analytics: %w", err)
	}

	updates := make(chan *pb.StorageEvent, 10)
	go sm.runSignalmanSubscription(ctx, config, addrs, client, func() { close(updates) }, func(c *signalmanclient.GRPCClient) error {
		return c.SubscribeToAnalytics()
	}, func(event *pb.SignalmanEvent) bool {
		if event.EventType != pb.EventType_EVENT_TYPE_STORAGE_LIFECYCLE || tenantMismatch(config.TenantID, event) {
			return true
		}
		if streamID != nil {
			msgStreamID := getStreamIDFromProtoEvent(event)
			if msgStreamID == "" || msgStreamID != *streamID {
				return true
			}
		}
		update := mapSignalmanStorageEvent(event)
		if update == nil {
			return true
		}
		return sendSubscriptionUpdate(ctx, updates, update)
	})
	return updates, nil
}

// SubscribeToProcessingEvents subscribes to processing/transcoding events and returns a channel of updates
// Returns proto.ProcessingUsageRecord (mapped from ProcessBillingEvent)
func (sm *SubscriptionManager) SubscribeToProcessingEvents(ctx context.Context, config ConnectionConfig, streamID *string) (<-chan *pb.ProcessingUsageRecord, error) {
	addrs := sm.streamScopedAddrs(ctx, streamID, config.TenantID)
	client, err := sm.getOrCreateConnectionFromList(ctx, config, addrs)
	if err != nil {
		return nil, err
	}

	if err := client.SubscribeToAnalytics(); err != nil {
		return nil, fmt.Errorf("failed to subscribe to analytics: %w", err)
	}

	updates := make(chan *pb.ProcessingUsageRecord, 10)
	go sm.runSignalmanSubscription(ctx, config, addrs, client, func() { close(updates) }, func(c *signalmanclient.GRPCClient) error {
		return c.SubscribeToAnalytics()
	}, func(event *pb.SignalmanEvent) bool {
		if event.EventType != pb.EventType_EVENT_TYPE_PROCESS_BILLING || tenantMismatch(config.TenantID, event) {
			return true
		}
		if streamID != nil {
			msgStreamID := getStreamIDFromProtoEvent(event)
			if msgStreamID == "" || msgStreamID != *streamID {
				return true
			}
		}
		update := mapSignalmanProcessingEvent(event)
		if update == nil {
			return true
		}
		return sendSubscriptionUpdate(ctx, updates, update)
	})
	return updates, nil
}

// SubscribeToSystem subscribes to system events and returns a channel of updates
// Returns proto.NodeLifecycleUpdate directly (bound to GraphQL SystemHealthEvent)
func (sm *SubscriptionManager) SubscribeToSystem(ctx context.Context, config ConnectionConfig) (<-chan *pb.NodeLifecycleUpdate, error) {
	addrs := sm.addrsForRegion("", config.TenantID)
	client, err := sm.getOrCreateConnectionFromList(ctx, config, addrs)
	if err != nil {
		return nil, err
	}

	if err := client.SubscribeToSystem(); err != nil {
		return nil, fmt.Errorf("failed to subscribe to system: %w", err)
	}

	updates := make(chan *pb.NodeLifecycleUpdate, 10)
	go sm.runSignalmanSubscription(ctx, config, addrs, client, func() { close(updates) }, func(c *signalmanclient.GRPCClient) error {
		return c.SubscribeToSystem()
	}, func(event *pb.SignalmanEvent) bool {
		if event.EventType != pb.EventType_EVENT_TYPE_NODE_LIFECYCLE_UPDATE || tenantMismatch(config.TenantID, event) {
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
	return updates, nil
}

// SubscribeToTrackList subscribes to track list events and returns a channel of updates
// Returns proto.StreamTrackListTrigger directly (bound to GraphQL TrackListEvent)
func (sm *SubscriptionManager) SubscribeToTrackList(ctx context.Context, config ConnectionConfig, streamID string) (<-chan *pb.StreamTrackListTrigger, error) {
	addrs := sm.connectionAddrsForStream(ctx, streamID, config.TenantID)
	client, err := sm.getOrCreateConnectionFromList(ctx, config, addrs)
	if err != nil {
		return nil, err
	}

	if err := client.SubscribeToStreams(); err != nil {
		return nil, fmt.Errorf("failed to subscribe to track list updates: %w", err)
	}

	updates := make(chan *pb.StreamTrackListTrigger, 10)
	go sm.runSignalmanSubscription(ctx, config, addrs, client, func() { close(updates) }, func(c *signalmanclient.GRPCClient) error {
		return c.SubscribeToStreams()
	}, func(event *pb.SignalmanEvent) bool {
		if event.EventType != pb.EventType_EVENT_TYPE_STREAM_TRACK_LIST || tenantMismatch(config.TenantID, event) {
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
	return updates, nil
}

// SubscribeToLifecycle subscribes to lifecycle events (clip) and returns a channel
// Returns proto.ClipLifecycleData directly (bound to GraphQL ClipLifecycle)
func (sm *SubscriptionManager) SubscribeToLifecycle(ctx context.Context, config ConnectionConfig, streamID string) (<-chan *pb.ClipLifecycleData, error) {
	addrs := sm.connectionAddrsForStream(ctx, streamID, config.TenantID)
	client, err := sm.getOrCreateConnectionFromList(ctx, config, addrs)
	if err != nil {
		return nil, err
	}
	if err := client.SubscribeToAnalytics(); err != nil {
		return nil, fmt.Errorf("failed to subscribe to lifecycle: %w", err)
	}
	updates := make(chan *pb.ClipLifecycleData, 10)
	go sm.runSignalmanSubscription(ctx, config, addrs, client, func() { close(updates) }, func(c *signalmanclient.GRPCClient) error {
		return c.SubscribeToAnalytics()
	}, func(event *pb.SignalmanEvent) bool {
		if event.EventType != pb.EventType_EVENT_TYPE_CLIP_LIFECYCLE || tenantMismatch(config.TenantID, event) {
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
	return updates, nil
}

// SubscribeToDVRLifecycle subscribes to DVR lifecycle events and returns a channel
// Returns proto.DVRLifecycleData directly (bound to GraphQL DVREvent)
func (sm *SubscriptionManager) SubscribeToDVRLifecycle(ctx context.Context, config ConnectionConfig, streamID string) (<-chan *pb.DVRLifecycleData, error) {
	addrs := sm.connectionAddrsForStream(ctx, streamID, config.TenantID)
	client, err := sm.getOrCreateConnectionFromList(ctx, config, addrs)
	if err != nil {
		return nil, err
	}
	if err := client.SubscribeToAnalytics(); err != nil {
		return nil, fmt.Errorf("failed to subscribe to DVR lifecycle: %w", err)
	}
	updates := make(chan *pb.DVRLifecycleData, 10)
	go sm.runSignalmanSubscription(ctx, config, addrs, client, func() { close(updates) }, func(c *signalmanclient.GRPCClient) error {
		return c.SubscribeToAnalytics()
	}, func(event *pb.SignalmanEvent) bool {
		if event.EventType != pb.EventType_EVENT_TYPE_DVR_LIFECYCLE || tenantMismatch(config.TenantID, event) {
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
	return updates, nil
}

// SubscribeToVodLifecycle subscribes to VOD lifecycle events and returns a channel
// Returns proto.VodLifecycleData directly (bound via gqlgen.yml)
func (sm *SubscriptionManager) SubscribeToVodLifecycle(ctx context.Context, config ConnectionConfig) (<-chan *pb.VodLifecycleData, error) {
	addrs := sm.addrsForRegion("", config.TenantID)
	client, err := sm.getOrCreateConnectionFromList(ctx, config, addrs)
	if err != nil {
		return nil, err
	}
	// VOD lifecycle events are delivered on the analytics channel.
	if err := client.SubscribeToAnalytics(); err != nil {
		return nil, fmt.Errorf("failed to subscribe to VOD lifecycle: %w", err)
	}
	updates := make(chan *pb.VodLifecycleData, 10)
	go sm.runSignalmanSubscription(ctx, config, addrs, client, func() { close(updates) }, func(c *signalmanclient.GRPCClient) error {
		return c.SubscribeToAnalytics()
	}, func(event *pb.SignalmanEvent) bool {
		if event.EventType != pb.EventType_EVENT_TYPE_VOD_LIFECYCLE || tenantMismatch(config.TenantID, event) {
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
	return updates, nil
}

// SubscribeToMessages subscribes to messaging events and returns a channel
// Returns model.Message (mapped from MessageLifecycleData)
func (sm *SubscriptionManager) SubscribeToMessages(ctx context.Context, config ConnectionConfig, conversationID string) (<-chan *model.Message, error) {
	client, err := sm.GetOrCreateConnection(ctx, config)
	if err != nil {
		return nil, err
	}
	if err := client.SubscribeToMessaging(); err != nil {
		return nil, fmt.Errorf("failed to subscribe to messaging: %w", err)
	}
	updates := make(chan *model.Message, 10)
	go sm.processMessageMessages(ctx, client, updates, conversationID, config.TenantID)
	return updates, nil
}

// SubscribeToConversations subscribes to messaging events and returns conversation updates
func (sm *SubscriptionManager) SubscribeToConversations(ctx context.Context, config ConnectionConfig, conversationID string) (<-chan *model.Conversation, error) {
	client, err := sm.GetOrCreateConnection(ctx, config)
	if err != nil {
		return nil, err
	}
	if err := client.SubscribeToMessaging(); err != nil {
		return nil, fmt.Errorf("failed to subscribe to messaging: %w", err)
	}
	updates := make(chan *model.Conversation, 10)
	go sm.processConversationMessages(ctx, client, updates, conversationID, config.TenantID)
	return updates, nil
}

// SubscribeToFirehose subscribes to ALL events (streams, analytics, system) and returns a unified channel
func (sm *SubscriptionManager) SubscribeToFirehose(ctx context.Context, config ConnectionConfig) (<-chan *model.TenantEvent, error) {
	client, err := sm.GetOrCreateConnection(ctx, config)
	if err != nil {
		return nil, err
	}

	// Subscribe to all channels
	if err := client.SubscribeToStreams(); err != nil {
		return nil, fmt.Errorf("failed to subscribe to streams for firehose: %w", err)
	}
	if err := client.SubscribeToAnalytics(); err != nil {
		sm.logger.Warn("Failed to subscribe to analytics for firehose", "error", err)
		// Continue - analytics subscription is optional
	}
	if err := client.SubscribeToSystem(); err != nil {
		sm.logger.Warn("Failed to subscribe to system for firehose", "error", err)
		// Continue - system subscription is optional
	}
	if err := client.SubscribeToAI(); err != nil {
		sm.logger.Warn("Failed to subscribe to AI for firehose", "error", err)
	}

	updates := make(chan *model.TenantEvent, 50) // Larger buffer for firehose
	go sm.processFirehoseMessages(ctx, client, updates, config.TenantID)
	return updates, nil
}

// processFirehoseMessages processes ALL events from Signalman and converts them to TenantEvent
func (sm *SubscriptionManager) processFirehoseMessages(ctx context.Context, client *signalmanclient.GRPCClient, output chan<- *model.TenantEvent, tenantID string) {
	defer close(output)

	events := client.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}

			if tenantMismatch(tenantID, event) {
				continue
			}

			tenantEvent := sm.convertProtoToTenantEvent(event)
			if tenantEvent != nil {
				select {
				case output <- tenantEvent:
				case <-ctx.Done():
					return
				}
			}
		}
	}
}

// convertProtoToTenantEvent converts any Signalman proto event to a unified TenantEvent
// Uses proto enum strings for event type (e.g., EVENT_TYPE_STREAM_LIFECYCLE_UPDATE)
// Passes proto types directly where possible via gqlgen.yml bindings
func (sm *SubscriptionManager) convertProtoToTenantEvent(event *pb.SignalmanEvent) *model.TenantEvent {
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
	if event.Channel != pb.Channel_CHANNEL_UNSPECIFIED {
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
	case pb.EventType_EVENT_TYPE_STREAM_LIFECYCLE_UPDATE,
		pb.EventType_EVENT_TYPE_STREAM_END,
		pb.EventType_EVENT_TYPE_STREAM_BUFFER,
		pb.EventType_EVENT_TYPE_PUSH_REWRITE,
		pb.EventType_EVENT_TYPE_STREAM_SOURCE,
		pb.EventType_EVENT_TYPE_PLAY_REWRITE:
		tenantEvent.StreamEvent = mapSignalmanStreamEvent(event)

	case pb.EventType_EVENT_TYPE_VIEWER_CONNECT,
		pb.EventType_EVENT_TYPE_VIEWER_DISCONNECT:
		tenantEvent.ConnectionEvent = mapSignalmanConnectionEvent(event)

	case pb.EventType_EVENT_TYPE_CLIENT_LIFECYCLE_UPDATE:
		// Pass proto ClientLifecycleUpdate directly (bound to ViewerMetrics)
		tenantEvent.ViewerMetrics = event.Data.GetClientLifecycle()

	case pb.EventType_EVENT_TYPE_STREAM_TRACK_LIST:
		// Pass proto StreamTrackListTrigger directly (bound to TrackListUpdate)
		tenantEvent.TrackListUpdate = event.Data.GetTrackList()

	case pb.EventType_EVENT_TYPE_CLIP_LIFECYCLE:
		// Pass proto ClipLifecycleData directly (bound to ClipLifecycle)
		tenantEvent.ClipLifecycle = event.Data.GetClipLifecycle()

	case pb.EventType_EVENT_TYPE_DVR_LIFECYCLE:
		// Pass proto DVRLifecycleData directly (bound to DVREvent)
		tenantEvent.DvrEvent = event.Data.GetDvrLifecycle()

	case pb.EventType_EVENT_TYPE_NODE_LIFECYCLE_UPDATE:
		// Pass proto NodeLifecycleUpdate directly (bound to SystemHealthEvent)
		tenantEvent.SystemHealthEvent = event.Data.GetNodeLifecycle()

	case pb.EventType_EVENT_TYPE_LOAD_BALANCING:
		tenantEvent.RoutingEvent = mapSignalmanRoutingEvent(event)

	case pb.EventType_EVENT_TYPE_VOD_LIFECYCLE:
		// Pass proto VodLifecycleData directly (bound via gqlgen.yml)
		tenantEvent.VodLifecycle = event.Data.GetVodLifecycle()

	case pb.EventType_EVENT_TYPE_STORAGE_LIFECYCLE:
		tenantEvent.StorageEvent = mapSignalmanStorageEvent(event)

	case pb.EventType_EVENT_TYPE_PROCESS_BILLING:
		tenantEvent.ProcessingEvent = mapSignalmanProcessingEvent(event)
	case pb.EventType_EVENT_TYPE_STORAGE_SNAPSHOT:
		tenantEvent.StorageSnapshot = event.Data.GetStorageSnapshot()

	case pb.EventType_EVENT_TYPE_SKIPPER_INVESTIGATION:
		tenantEvent.SkipperInvestigation = &model.SkipperInvestigationEvent{
			ReportID:     "",
			ResourceType: "skipper_investigation",
		}
	}

	return tenantEvent
}

// getChannelForEventType returns the channel name for a given event type
func (sm *SubscriptionManager) getChannelForEventType(eventType pb.EventType) string {
	switch eventType {
	case pb.EventType_EVENT_TYPE_STREAM_LIFECYCLE_UPDATE,
		pb.EventType_EVENT_TYPE_STREAM_TRACK_LIST,
		pb.EventType_EVENT_TYPE_STREAM_BUFFER,
		pb.EventType_EVENT_TYPE_STREAM_END,
		pb.EventType_EVENT_TYPE_PUSH_REWRITE,
		pb.EventType_EVENT_TYPE_STREAM_SOURCE,
		pb.EventType_EVENT_TYPE_PLAY_REWRITE,
		pb.EventType_EVENT_TYPE_VOD_LIFECYCLE:
		return "STREAMS"

	case pb.EventType_EVENT_TYPE_NODE_LIFECYCLE_UPDATE,
		pb.EventType_EVENT_TYPE_LOAD_BALANCING:
		return "SYSTEM"
	case pb.EventType_EVENT_TYPE_STORAGE_LIFECYCLE,
		pb.EventType_EVENT_TYPE_PROCESS_BILLING,
		pb.EventType_EVENT_TYPE_STORAGE_SNAPSHOT:
		return "ANALYTICS"
	case pb.EventType_EVENT_TYPE_MESSAGE_LIFECYCLE:
		return "MESSAGING"
	case pb.EventType_EVENT_TYPE_SKIPPER_INVESTIGATION:
		return "AI"

	default:
		return "ANALYTICS"
	}
}

func channelToTenantChannel(channel pb.Channel) string {
	switch channel {
	case pb.Channel_CHANNEL_STREAMS:
		return "STREAMS"
	case pb.Channel_CHANNEL_ANALYTICS:
		return "ANALYTICS"
	case pb.Channel_CHANNEL_SYSTEM:
		return "SYSTEM"
	case pb.Channel_CHANNEL_ALL:
		return "ALL"
	case pb.Channel_CHANNEL_MESSAGING:
		return "MESSAGING"
	case pb.Channel_CHANNEL_AI:
		return "AI"
	default:
		return "ANALYTICS"
	}
}

func tenantMismatch(tenantID string, event *pb.SignalmanEvent) bool {
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

// processStreamMessages processes stream messages from Signalman gRPC
// Maps proto payloads into canonical StreamEvent for live subscriptions
func (sm *SubscriptionManager) processStreamMessages(ctx context.Context, client *signalmanclient.GRPCClient, output chan<- *model.StreamEvent, streamID *string, tenantID string) {
	defer close(output)

	events := client.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}

			// Filter by event type - handle lifecycle, start, end, buffer, track list
			if event.EventType != pb.EventType_EVENT_TYPE_STREAM_LIFECYCLE_UPDATE &&
				event.EventType != pb.EventType_EVENT_TYPE_STREAM_END &&
				event.EventType != pb.EventType_EVENT_TYPE_STREAM_BUFFER &&
				event.EventType != pb.EventType_EVENT_TYPE_STREAM_TRACK_LIST &&
				event.EventType != pb.EventType_EVENT_TYPE_PUSH_REWRITE &&
				event.EventType != pb.EventType_EVENT_TYPE_STREAM_SOURCE &&
				event.EventType != pb.EventType_EVENT_TYPE_PLAY_REWRITE {
				continue
			}

			if tenantMismatch(tenantID, event) {
				continue
			}

			// Filter by stream ID if specified
			if streamID != nil {
				msgStreamID := getStreamIDFromProtoEvent(event)
				if msgStreamID == "" || msgStreamID != *streamID {
					continue
				}
			}

			update := mapSignalmanStreamEvent(event)
			if update == nil {
				continue
			}

			select {
			case output <- update:
			case <-ctx.Done():
				return
			}
		}
	}
}

// processAnalyticsMessages processes analytics messages from Signalman gRPC
// Passes proto.ClientLifecycleUpdate directly without conversion
func (sm *SubscriptionManager) processAnalyticsMessages(ctx context.Context, client *signalmanclient.GRPCClient, output chan<- *pb.ClientLifecycleUpdate, streamID *string, tenantID string) {
	defer close(output)

	events := client.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}

			// Filter for client lifecycle events
			if event.EventType != pb.EventType_EVENT_TYPE_CLIENT_LIFECYCLE_UPDATE {
				continue
			}

			if tenantMismatch(tenantID, event) {
				continue
			}

			// Extract ClientLifecycleUpdate directly from proto
			if event.Data != nil {
				if cl := event.Data.GetClientLifecycle(); cl != nil {
					if streamID != nil {
						msgStreamID := getStreamIDFromProtoEvent(event)
						if msgStreamID == "" || msgStreamID != *streamID {
							continue
						}
					}
					select {
					case output <- cl:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}
}

// processConnectionMessages processes viewer connect/disconnect messages from Signalman gRPC
// Maps proto viewer events into ConnectionEvent for GraphQL consumption
func (sm *SubscriptionManager) processConnectionMessages(ctx context.Context, client *signalmanclient.GRPCClient, output chan<- *pb.ConnectionEvent, streamID *string, tenantID string) {
	defer close(output)

	events := client.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}

			if event.EventType != pb.EventType_EVENT_TYPE_VIEWER_CONNECT &&
				event.EventType != pb.EventType_EVENT_TYPE_VIEWER_DISCONNECT {
				continue
			}

			if tenantMismatch(tenantID, event) {
				continue
			}

			if streamID != nil {
				msgStreamID := getStreamIDFromProtoEvent(event)
				if msgStreamID == "" || msgStreamID != *streamID {
					continue
				}
			}

			ce := mapSignalmanConnectionEvent(event)
			if ce == nil {
				continue
			}

			select {
			case output <- ce:
			case <-ctx.Done():
				return
			}
		}
	}
}

// processStorageMessages processes storage lifecycle messages from Signalman gRPC
// Maps proto storage lifecycle data into StorageEvent for GraphQL consumption
func (sm *SubscriptionManager) processStorageMessages(ctx context.Context, client *signalmanclient.GRPCClient, output chan<- *pb.StorageEvent, streamID *string, tenantID string) {
	defer close(output)

	events := client.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}

			if event.EventType != pb.EventType_EVENT_TYPE_STORAGE_LIFECYCLE {
				continue
			}

			if tenantMismatch(tenantID, event) {
				continue
			}

			if streamID != nil {
				msgStreamID := getStreamIDFromProtoEvent(event)
				if msgStreamID == "" || msgStreamID != *streamID {
					continue
				}
			}

			update := mapSignalmanStorageEvent(event)
			if update == nil {
				continue
			}

			select {
			case output <- update:
			case <-ctx.Done():
				return
			}
		}
	}
}

// processProcessingMessages processes process billing messages from Signalman gRPC
// Maps proto process billing data into ProcessingUsageRecord for GraphQL consumption
func (sm *SubscriptionManager) processProcessingMessages(ctx context.Context, client *signalmanclient.GRPCClient, output chan<- *pb.ProcessingUsageRecord, streamID *string, tenantID string) {
	defer close(output)

	events := client.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}

			if event.EventType != pb.EventType_EVENT_TYPE_PROCESS_BILLING {
				continue
			}

			if tenantMismatch(tenantID, event) {
				continue
			}

			if streamID != nil {
				msgStreamID := getStreamIDFromProtoEvent(event)
				if msgStreamID == "" || msgStreamID != *streamID {
					continue
				}
			}

			update := mapSignalmanProcessingEvent(event)
			if update == nil {
				continue
			}

			select {
			case output <- update:
			case <-ctx.Done():
				return
			}
		}
	}
}

// processSystemMessages processes system messages from Signalman gRPC
// Passes proto.NodeLifecycleUpdate directly without conversion
func (sm *SubscriptionManager) processSystemMessages(ctx context.Context, client *signalmanclient.GRPCClient, output chan<- *pb.NodeLifecycleUpdate, tenantID string) {
	defer close(output)

	events := client.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}

			// Filter for node lifecycle events
			if event.EventType != pb.EventType_EVENT_TYPE_NODE_LIFECYCLE_UPDATE {
				continue
			}

			if tenantMismatch(tenantID, event) {
				continue
			}

			// Extract NodeLifecycleUpdate directly from proto
			if event.Data != nil {
				if nl := event.Data.GetNodeLifecycle(); nl != nil {
					select {
					case output <- nl:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}
}

// processTrackListMessages processes track list messages from Signalman gRPC
// Passes proto.StreamTrackListTrigger directly without conversion
func (sm *SubscriptionManager) processTrackListMessages(ctx context.Context, client *signalmanclient.GRPCClient, output chan<- *pb.StreamTrackListTrigger, streamID string, tenantID string) {
	defer close(output)

	events := client.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}

			if event.EventType != pb.EventType_EVENT_TYPE_STREAM_TRACK_LIST {
				continue
			}

			if tenantMismatch(tenantID, event) {
				continue
			}

			// Filter by stream ID if specified
			if streamID != "" {
				msgStreamID := getStreamIDFromProtoEvent(event)
				if msgStreamID == "" || msgStreamID != streamID {
					continue
				}
			}

			// Extract StreamTrackListTrigger directly from proto
			if event.Data != nil {
				if tl := event.Data.GetTrackList(); tl != nil {
					select {
					case output <- tl:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}
}

// processLifecycleMessages processes clip/dvr lifecycle messages from Signalman gRPC
// Passes proto.ClipLifecycleData directly without conversion
func (sm *SubscriptionManager) processLifecycleMessages(ctx context.Context, client *signalmanclient.GRPCClient, output chan<- *pb.ClipLifecycleData, streamID string, tenantID string) {
	defer close(output)

	events := client.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}

			// Filter for clip lifecycle events
			if event.EventType != pb.EventType_EVENT_TYPE_CLIP_LIFECYCLE {
				continue
			}

			if tenantMismatch(tenantID, event) {
				continue
			}

			// Extract ClipLifecycleData directly from proto
			if event.Data != nil {
				if cl := event.Data.GetClipLifecycle(); cl != nil {
					// Filter by stream ID if specified
					if streamID != "" && cl.GetStreamId() != streamID {
						continue
					}
					select {
					case output <- cl:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}
}

func (sm *SubscriptionManager) processDVRLifecycleMessages(ctx context.Context, client *signalmanclient.GRPCClient, output chan<- *pb.DVRLifecycleData, streamID string, tenantID string) {
	defer close(output)

	events := client.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}

			// Filter for DVR lifecycle events
			if event.EventType != pb.EventType_EVENT_TYPE_DVR_LIFECYCLE {
				continue
			}

			if tenantMismatch(tenantID, event) {
				continue
			}

			// Extract DVRLifecycleData directly from proto
			if event.Data != nil {
				if dvr := event.Data.GetDvrLifecycle(); dvr != nil {
					// Filter by stream ID if specified
					if streamID != "" && dvr.GetStreamId() != streamID {
						continue
					}
					select {
					case output <- dvr:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}
}

// processVodLifecycleMessages processes VOD lifecycle messages from Signalman gRPC
// Passes proto.VodLifecycleData directly (bound via gqlgen.yml, no conversion needed)
func (sm *SubscriptionManager) processVodLifecycleMessages(ctx context.Context, client *signalmanclient.GRPCClient, output chan<- *pb.VodLifecycleData, tenantID string) {
	defer close(output)

	events := client.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}

			// Filter for VOD lifecycle events
			if event.EventType != pb.EventType_EVENT_TYPE_VOD_LIFECYCLE {
				continue
			}

			if tenantMismatch(tenantID, event) {
				continue
			}

			// Pass proto directly - no conversion needed (bound via gqlgen.yml)
			if event.Data != nil {
				if vod := event.Data.GetVodLifecycle(); vod != nil {
					select {
					case output <- vod:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}
}

// processMessageMessages processes messaging events from Signalman gRPC
// Maps proto.MessageLifecycleData to model.Message for GraphQL consumption
func (sm *SubscriptionManager) processMessageMessages(ctx context.Context, client *signalmanclient.GRPCClient, output chan<- *model.Message, conversationID string, tenantID string) {
	defer close(output)

	events := client.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}

			// Filter for message lifecycle events
			if event.EventType != pb.EventType_EVENT_TYPE_MESSAGE_LIFECYCLE {
				continue
			}

			if event.Data == nil {
				continue
			}

			ml := event.Data.GetMessageLifecycle()
			if ml == nil {
				continue
			}

			// Enforce tenant isolation when tenant_id is present
			if tenantID != "" {
				if ml.TenantId == nil || *ml.TenantId != tenantID {
					continue
				}
			}

			// Filter by conversation ID
			if conversationID != "" && ml.GetConversationId() != conversationID {
				continue
			}

			// Only forward message_created events (new messages)
			if ml.EventType != pb.MessageLifecycleData_EVENT_TYPE_MESSAGE_CREATED {
				continue
			}

			// Map to GraphQL Message type
			msg := mapMessageLifecycleToMessage(ml)
			if msg == nil {
				continue
			}

			select {
			case output <- msg:
			case <-ctx.Done():
				return
			}
		}
	}
}

// mapMessageLifecycleToMessage converts proto MessageLifecycleData to GraphQL Message
func mapMessageLifecycleToMessage(ml *pb.MessageLifecycleData) *model.Message {
	if ml == nil {
		return nil
	}

	rawConversationID := ml.GetConversationId()
	if rawConversationID == "" {
		return nil
	}

	// Parse sender - default to AGENT if unknown
	sender := pb.MessageSender_MESSAGE_SENDER_AGENT
	if ml.Sender != nil {
		switch *ml.Sender {
		case "USER":
			sender = pb.MessageSender_MESSAGE_SENDER_USER
		case "AGENT":
			sender = pb.MessageSender_MESSAGE_SENDER_AGENT
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

// processConversationMessages processes conversation lifecycle events from Signalman gRPC
func (sm *SubscriptionManager) processConversationMessages(ctx context.Context, client *signalmanclient.GRPCClient, output chan<- *model.Conversation, conversationID string, tenantID string) {
	defer close(output)

	events := client.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}

			// Filter for message lifecycle events
			if event.EventType != pb.EventType_EVENT_TYPE_MESSAGE_LIFECYCLE {
				continue
			}

			if event.Data == nil {
				continue
			}

			ml := event.Data.GetMessageLifecycle()
			if ml == nil {
				continue
			}

			// Enforce tenant isolation when tenant_id is present
			if tenantID != "" {
				if ml.TenantId == nil || *ml.TenantId != tenantID {
					continue
				}
			}

			// Filter by conversation ID
			if conversationID != "" && ml.GetConversationId() != conversationID {
				continue
			}

			// Conversation lifecycle events and message updates that affect conversation summary
			switch ml.EventType {
			case pb.MessageLifecycleData_EVENT_TYPE_CONVERSATION_CREATED,
				pb.MessageLifecycleData_EVENT_TYPE_CONVERSATION_UPDATED,
				pb.MessageLifecycleData_EVENT_TYPE_MESSAGE_CREATED,
				pb.MessageLifecycleData_EVENT_TYPE_MESSAGE_UPDATED:
			default:
				continue
			}

			conv := mapMessageLifecycleToConversation(ml)
			if conv == nil {
				continue
			}

			select {
			case output <- conv:
			case <-ctx.Done():
				return
			}
		}
	}
}

func mapMessageLifecycleToConversation(ml *pb.MessageLifecycleData) *model.Conversation {
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
	case pb.MessageLifecycleData_EVENT_TYPE_MESSAGE_CREATED,
		pb.MessageLifecycleData_EVENT_TYPE_MESSAGE_UPDATED:
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

func parseConversationStatus(status *string) pb.ConversationStatus {
	if status == nil || *status == "" {
		return pb.ConversationStatus_CONVERSATION_STATUS_OPEN
	}

	switch strings.ToUpper(*status) {
	case "OPEN":
		return pb.ConversationStatus_CONVERSATION_STATUS_OPEN
	case "RESOLVED":
		return pb.ConversationStatus_CONVERSATION_STATUS_RESOLVED
	case "PENDING":
		return pb.ConversationStatus_CONVERSATION_STATUS_PENDING
	default:
		return pb.ConversationStatus_CONVERSATION_STATUS_OPEN
	}
}

// CleanupConnection removes a connection from the pool
func (sm *SubscriptionManager) CleanupConnection(userID, tenantID string) {
	prefix := strings.Join([]string{userID, tenantID, ""}, subscriptionClientKeySep)

	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	for key, client := range sm.clients {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		sm.removeClientLocked(key, client, tenantID)

		sm.logger.WithFields(logging.Fields{
			"user_id":   userID,
			"tenant_id": tenantID,
			"key":       key,
		}).Info("Cleaned up gRPC connection")
	}
}

// cleanupWorker handles cleanup requests
func (sm *SubscriptionManager) cleanupWorker() {
	ticker := time.NewTicker(5 * time.Minute) // Periodic cleanup
	defer ticker.Stop()

	for {
		select {
		case <-sm.stopChan:
			return
		case key := <-sm.cleanup:
			sm.mutex.Lock()
			if client, exists := sm.clients[key]; exists {
				sm.removeClientLocked(key, client, tenantIDFromSubscriptionClientKey(key))
			}
			sm.mutex.Unlock()
		case <-ticker.C:
			// Periodic cleanup of disconnected clients
			sm.periodicCleanup()
		}
	}
}

// periodicCleanup removes disconnected clients
func (sm *SubscriptionManager) periodicCleanup() {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	type clientInfo struct {
		key      string
		tenantID string
	}
	var toRemove []clientInfo

	for key, client := range sm.clients {
		if !client.IsConnected() {
			toRemove = append(toRemove, clientInfo{key: key, tenantID: tenantIDFromSubscriptionClientKey(key)})
		}
	}

	for _, info := range toRemove {
		if client, exists := sm.clients[info.key]; exists {
			sm.removeClientLocked(info.key, client, info.tenantID)
		}
	}

	if len(toRemove) > 0 {
		sm.logger.WithFields(logging.Fields{
			"cleaned_connections": len(toRemove),
		}).Info("Periodic cleanup removed disconnected gRPC connections")
	}
}

// Shutdown gracefully shuts down the subscription manager
func (sm *SubscriptionManager) Shutdown() error {
	close(sm.stopChan)

	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	// Close all connections
	for key, client := range sm.clients {
		sm.removeClientLocked(key, client, tenantIDFromSubscriptionClientKey(key))
	}

	sm.logger.Info("Subscription manager shutdown completed")
	return nil
}

// getStreamIDFromProtoEvent extracts stream ID from a proto SignalmanEvent
func getStreamIDFromProtoEvent(event *pb.SignalmanEvent) string {
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
