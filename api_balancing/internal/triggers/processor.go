package triggers

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/geo"
	"frameworks/api_balancing/internal/ingesterrors"
	"frameworks/api_balancing/internal/state"
	"frameworks/pkg/cache"
	"frameworks/pkg/clients/commodore"
	"frameworks/pkg/clients/decklog"
	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/geoip"
	"frameworks/pkg/logging"
	"frameworks/pkg/mist"
	pb "frameworks/pkg/proto"
)

// streamContext holds cached tenant and user information for a stream
type streamContext struct {
	TenantID          string
	UserID            string
	StreamID          string
	Source            string
	UpdatedAt         time.Time
	LastError         string
	BillingModel      string // "postpaid" or "prepaid" - affects cache TTL
	IsSuspended       bool   // true if tenant is suspended (balance < -$10)
	IsBalanceNegative bool   // true if prepaid balance <= 0 (should return 402)
}

// DVRStarter handles DVR recording orchestration.
// Implemented by FoghornGRPCServer to allow direct internal DVR start without Commodore hop.
type DVRStarter interface {
	StartDVR(ctx context.Context, req *pb.StartDVRRequest) (*pb.StartDVRResponse, error)
}

// Processor implements the MistTriggerProcessor interface for handling MistServer triggers
type Processor struct {
	logger              logging.Logger
	commodoreClient     *commodore.GRPCClient
	quartermasterClient *qmclient.GRPCClient
	decklogClient       *decklog.BatchedClient
	loadBalancer        *balancer.LoadBalancer
	geoipClient         *geoip.Reader
	geoipCache          *cache.Cache
	dvrService          DVRStarter // Internal DVR orchestration (FoghornGRPCServer)
	metrics             *ProcessorMetrics
	nodeID              string
	region              string
	clusterID           string
	ownerTenantID       string

	streamCache        *cache.Cache // Cache stream context (tenant + user)
	streamCacheMetaMu  sync.Mutex
	streamCacheHits    uint64
	streamCacheMisses  uint64
	streamCacheResInt  uint64
	streamCacheResPb   uint64
	streamCacheResErr  uint64
	streamCacheLastAt  time.Time
	streamCacheLastErr string

	nodeUUIDCache *cache.Cache // Cache node_id (logical) -> UUID
}

// NewProcessor creates a new MistServer trigger processor
func NewProcessor(logger logging.Logger, commodoreClient *commodore.GRPCClient, decklogClient *decklog.BatchedClient, loadBalancer *balancer.LoadBalancer, geoipClient *geoip.Reader) *Processor {
	p := &Processor{
		logger:          logger,
		commodoreClient: commodoreClient,
		decklogClient:   decklogClient,
		loadBalancer:    loadBalancer,
		geoipClient:     geoipClient,
		nodeID:          os.Getenv("NODE_ID"),
		region:          os.Getenv("REGION"),
		clusterID:       os.Getenv("CLUSTER_ID"),
	}

	p.streamCache = cache.New(cache.Options{
		TTL:                  10 * time.Minute,
		StaleWhileRevalidate: streamCacheSWR(),
		NegativeTTL:          0,
		MaxEntries:           50000,
	}, cache.MetricsHooks{
		OnHit:  func(_ map[string]string) { atomic.AddUint64(&p.streamCacheHits, 1) },
		OnMiss: func(_ map[string]string) { atomic.AddUint64(&p.streamCacheMisses, 1) },
	})

	p.nodeUUIDCache = cache.New(cache.Options{
		TTL:                  1 * time.Hour,
		StaleWhileRevalidate: 15 * time.Minute,
		NegativeTTL:          0,
		MaxEntries:           50000,
	}, cache.MetricsHooks{})

	return p
}

func streamCacheSWR() time.Duration {
	swr := 30 * time.Second
	if raw := os.Getenv("STREAM_CACHE_SWR"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil {
			return parsed
		}
	}
	return swr
}

// StreamContextCacheEntry is a single cached mapping used for tenant/user enrichment.
type StreamContextCacheEntry struct {
	Key       string    `json:"key"`
	TenantID  string    `json:"tenant_id"`
	UserID    string    `json:"user_id"`
	StreamID  string    `json:"stream_id"`
	Source    string    `json:"source"`
	UpdatedAt time.Time `json:"updated_at"`
	LastError string    `json:"last_error,omitempty"`
}

// StreamContextCacheSnapshot is a point-in-time view of the stream context cache + basic health stats.
type StreamContextCacheSnapshot struct {
	GeneratedAt time.Time                 `json:"generated_at"`
	Size        int                       `json:"size"`
	Hits        uint64                    `json:"hits"`
	Misses      uint64                    `json:"misses"`
	ResInternal uint64                    `json:"resolves_internal_name"`
	ResPlayback uint64                    `json:"resolves_playback_id"`
	ResErrors   uint64                    `json:"resolve_errors"`
	LastResolve time.Time                 `json:"last_resolve_at,omitempty"`
	LastError   string                    `json:"last_error,omitempty"`
	Entries     []StreamContextCacheEntry `json:"entries"`
}

func (p *Processor) StreamContextCacheSnapshot() StreamContextCacheSnapshot {
	var entries []StreamContextCacheEntry
	if p.streamCache != nil {
		for _, e := range p.streamCache.Snapshot() {
			info, ok := e.Value.(streamContext)
			if !ok {
				continue
			}
			lastErr := info.LastError
			if lastErr == "" && e.Err != nil {
				lastErr = e.Err.Error()
			}
			entries = append(entries, StreamContextCacheEntry{
				Key:       e.Key,
				TenantID:  info.TenantID,
				UserID:    info.UserID,
				StreamID:  info.StreamID,
				Source:    info.Source,
				UpdatedAt: info.UpdatedAt,
				LastError: lastErr,
			})
		}
	}

	p.streamCacheMetaMu.Lock()
	lastAt := p.streamCacheLastAt
	lastErr := p.streamCacheLastErr
	p.streamCacheMetaMu.Unlock()

	return StreamContextCacheSnapshot{
		GeneratedAt: time.Now(),
		Size:        len(entries),
		Hits:        atomic.LoadUint64(&p.streamCacheHits),
		Misses:      atomic.LoadUint64(&p.streamCacheMisses),
		ResInternal: atomic.LoadUint64(&p.streamCacheResInt),
		ResPlayback: atomic.LoadUint64(&p.streamCacheResPb),
		ResErrors:   atomic.LoadUint64(&p.streamCacheResErr),
		LastResolve: lastAt,
		LastError:   lastErr,
		Entries:     entries,
	}
}

// BillingStatus contains billing status for enforcement decisions
type BillingStatus struct {
	TenantID          string
	BillingModel      string // "postpaid" or "prepaid"
	IsSuspended       bool   // true if balance < -$10 (hard block)
	IsBalanceNegative bool   // true if balance <= 0 (402 warning)
	FromCache         bool   // true if status came from cache
}

// GetBillingStatus looks up billing status for a stream/artifact owner.
// First checks the stream cache (populated during ingest), then falls back to Quartermaster.
// Parameters:
//   - internalName: stream's internal name (e.g., "abc123-def456") - used for cache lookup
//   - tenantID: tenant ID - used for Quartermaster fallback if not in cache
//
// Returns nil if status cannot be determined (fail-open).
func (p *Processor) GetBillingStatus(ctx context.Context, internalName, tenantID string) *BillingStatus {
	// Try cache first (keyed by tenant + internal name)
	if p.streamCache != nil && internalName != "" && tenantID != "" {
		cacheKey := tenantID + ":" + internalName
		if cached, ok := p.streamCache.Peek(cacheKey); ok {
			info := cached.(streamContext)
			return &BillingStatus{
				TenantID:          info.TenantID,
				BillingModel:      info.BillingModel,
				IsSuspended:       info.IsSuspended,
				IsBalanceNegative: info.IsBalanceNegative,
				FromCache:         true,
			}
		}
	}

	// Fallback to Quartermaster if we have tenant ID
	if p.quartermasterClient != nil && tenantID != "" {
		qmCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		defer cancel()
		resp, err := p.quartermasterClient.ValidateTenant(qmCtx, tenantID, "")
		if err == nil && resp != nil && resp.Valid {
			return &BillingStatus{
				TenantID:          tenantID,
				BillingModel:      resp.BillingModel,
				IsSuspended:       resp.IsSuspended,
				IsBalanceNegative: resp.IsBalanceNegative,
				FromCache:         false,
			}
		}
		if err != nil {
			p.logger.WithFields(logging.Fields{
				"tenant_id":     tenantID,
				"internal_name": internalName,
				"error":         err,
			}).Warn("Quartermaster billing lookup failed")
		}
	}

	return nil // Fail-open
}

// InvalidateTenantCache evicts all cache entries for a specific tenant.
// Called when tenant suspension status changes (e.g., after payment).
// Returns the number of entries invalidated.
func (p *Processor) InvalidateTenantCache(tenantID string) int {
	if p.streamCache == nil || tenantID == "" {
		return 0
	}

	// Get all cache entries and find those belonging to this tenant
	var keysToEvict []string
	for _, e := range p.streamCache.Snapshot() {
		if strings.HasPrefix(e.Key, tenantID+":") {
			keysToEvict = append(keysToEvict, e.Key)
			continue
		}
		info, ok := e.Value.(streamContext)
		if ok && info.TenantID == tenantID {
			keysToEvict = append(keysToEvict, e.Key)
		}
	}

	// Evict each matching entry
	for _, key := range keysToEvict {
		p.streamCache.Delete(key)
	}

	if p.commodoreClient != nil {
		p.commodoreClient.InvalidateTenantCacheKeys(tenantID)
	}

	if len(keysToEvict) > 0 {
		p.logger.WithFields(logging.Fields{
			"tenant_id":           tenantID,
			"entries_invalidated": len(keysToEvict),
		}).Info("Invalidated tenant cache entries")
	}

	return len(keysToEvict)
}

// SetQuartermasterClient configures the Quartermaster client for node UUID lookups
func (p *Processor) SetQuartermasterClient(c *qmclient.GRPCClient) {
	p.quartermasterClient = c
}

// SetGeoIPCache configures a cache for GeoIP lookups
func (p *Processor) SetGeoIPCache(c *cache.Cache) {
	p.geoipCache = c
}

// SetDVRService configures the DVR orchestration service for auto-start recordings
func (p *Processor) SetDVRService(svc DVRStarter) {
	p.dvrService = svc
}

// SetMetrics configures optional Prometheus metrics for the trigger processor.
func (p *Processor) SetMetrics(m *ProcessorMetrics) {
	p.metrics = m
}

// SetClusterID sets the emitting cluster identifier for trigger enrichment.
func (p *Processor) SetClusterID(clusterID string) {
	if strings.TrimSpace(clusterID) == "" {
		return
	}
	p.clusterID = clusterID
}

// SetOwnerTenantID sets the cluster owner tenant for infra event attribution.
func (p *Processor) SetOwnerTenantID(tenantID string) {
	if strings.TrimSpace(tenantID) == "" {
		return
	}
	p.ownerTenantID = tenantID
}

func (p *Processor) ensureTriggerTenantID(trigger *pb.MistTrigger) string {
	if trigger == nil {
		return ""
	}
	if tid := trigger.GetTenantId(); strings.TrimSpace(tid) != "" {
		return tid
	}

	// Some trigger types carry tenant_id only inside the payload; accept/mirror it to the envelope
	// before enforcing the decklog send guard.
	switch tp := trigger.GetTriggerPayload().(type) {
	case *pb.MistTrigger_StreamLifecycleUpdate:
		if tid := strings.TrimSpace(tp.StreamLifecycleUpdate.GetTenantId()); tid != "" {
			trigger.TenantId = &tid
			return tid
		}
	case *pb.MistTrigger_ClientLifecycleUpdate:
		if tid := strings.TrimSpace(tp.ClientLifecycleUpdate.GetTenantId()); tid != "" {
			trigger.TenantId = &tid
			return tid
		}
	case *pb.MistTrigger_ClipLifecycleData:
		if tid := strings.TrimSpace(tp.ClipLifecycleData.GetTenantId()); tid != "" {
			trigger.TenantId = &tid
			return tid
		}
	case *pb.MistTrigger_DvrLifecycleData:
		if tid := strings.TrimSpace(tp.DvrLifecycleData.GetTenantId()); tid != "" {
			trigger.TenantId = &tid
			return tid
		}
	case *pb.MistTrigger_VodLifecycleData:
		if tid := strings.TrimSpace(tp.VodLifecycleData.GetTenantId()); tid != "" {
			trigger.TenantId = &tid
			return tid
		}
	case *pb.MistTrigger_StorageLifecycleData:
		if tid := strings.TrimSpace(tp.StorageLifecycleData.GetTenantId()); tid != "" {
			trigger.TenantId = &tid
			return tid
		}
	case *pb.MistTrigger_StorageSnapshot:
		if tid := strings.TrimSpace(tp.StorageSnapshot.GetTenantId()); tid != "" {
			trigger.TenantId = &tid
			return tid
		}
	case *pb.MistTrigger_ProcessBilling:
		if tid := strings.TrimSpace(tp.ProcessBilling.GetTenantId()); tid != "" {
			trigger.TenantId = &tid
			return tid
		}
	}

	return ""
}

func (p *Processor) sendTriggerToDecklog(trigger *pb.MistTrigger) error {
	if trigger == nil {
		return fmt.Errorf("nil trigger")
	}

	if trigger.ClusterId == nil && p.clusterID != "" {
		trigger.ClusterId = &p.clusterID
	}

	if p.ensureTriggerTenantID(trigger) == "" {
		if p.metrics != nil && p.metrics.DecklogTriggerSends != nil {
			p.metrics.DecklogTriggerSends.WithLabelValues(trigger.GetTriggerType(), "tenant_missing").Inc()
		}
		p.logger.WithFields(logging.Fields{
			"trigger_type": trigger.GetTriggerType(),
			"node_id":      trigger.GetNodeId(),
		}).Warn("Refusing to send trigger without tenant_id")
		return fmt.Errorf("tenant_id required for trigger type %s", trigger.GetTriggerType())
	}

	if p.metrics != nil && p.metrics.DecklogTriggerSends != nil {
		p.metrics.DecklogTriggerSends.WithLabelValues(trigger.GetTriggerType(), "attempt").Inc()
	}

	if p.decklogClient == nil {
		if p.metrics != nil && p.metrics.DecklogTriggerSends != nil {
			p.metrics.DecklogTriggerSends.WithLabelValues(trigger.GetTriggerType(), "client_nil").Inc()
		}
		return fmt.Errorf("decklog client not configured")
	}

	if err := p.decklogClient.SendTrigger(trigger); err != nil {
		if p.metrics != nil && p.metrics.DecklogTriggerSends != nil {
			p.metrics.DecklogTriggerSends.WithLabelValues(trigger.GetTriggerType(), "error").Inc()
		}
		return err
	}

	if p.metrics != nil && p.metrics.DecklogTriggerSends != nil {
		p.metrics.DecklogTriggerSends.WithLabelValues(trigger.GetTriggerType(), "success").Inc()
	}
	return nil
}

// ProcessTypedTrigger processes a typed protobuf MistTrigger directly
func (p *Processor) ProcessTypedTrigger(trigger *pb.MistTrigger) (string, bool, error) {
	if trigger == nil {
		return "", true, fmt.Errorf("nil trigger")
	}
	switch trigger.GetTriggerPayload().(type) {
	case *pb.MistTrigger_PushRewrite:
		return p.handlePushRewrite(trigger)
	case *pb.MistTrigger_PlayRewrite:
		return p.handlePlayRewrite(trigger)
	case *pb.MistTrigger_StreamSource:
		return p.handleStreamSource(trigger)
	case *pb.MistTrigger_PushOutStart:
		return p.handlePushOutStart(trigger)
	case *pb.MistTrigger_PushEnd:
		return p.handlePushEnd(trigger)
	case *pb.MistTrigger_ViewerConnect:
		return p.handleUserNew(trigger)
	case *pb.MistTrigger_ViewerDisconnect:
		return p.handleUserEnd(trigger)
	case *pb.MistTrigger_StreamBuffer:
		return p.handleStreamBuffer(trigger)
	case *pb.MistTrigger_StreamEnd:
		return p.handleStreamEnd(trigger)
	case *pb.MistTrigger_TrackList:
		return p.handleLiveTrackList(trigger)
	case *pb.MistTrigger_RecordingComplete:
		return p.handleRecordingEnd(trigger)
	case *pb.MistTrigger_RecordingSegment:
		return p.handleRecordingSegment(trigger)
	case *pb.MistTrigger_StreamLifecycleUpdate:
		return p.handleStreamLifecycleUpdate(trigger)
	case *pb.MistTrigger_ClientLifecycleUpdate:
		return p.handleClientLifecycleUpdate(trigger)
	case *pb.MistTrigger_NodeLifecycleUpdate:
		return p.handleNodeLifecycleUpdate(trigger)
	case *pb.MistTrigger_DvrLifecycleData:
		return p.handleDVRLifecycleData(trigger)
	case *pb.MistTrigger_StorageLifecycleData:
		return p.handleStorageLifecycleData(trigger)
	case *pb.MistTrigger_ProcessBilling:
		return p.handleProcessBilling(trigger)
	default:
		return "", trigger.GetBlocking(), fmt.Errorf("unsupported trigger payload type")
	}
}

// handleProcessBilling forwards ProcessBillingEvent to Decklog
func (p *Processor) handleProcessBilling(trigger *pb.MistTrigger) (string, bool, error) {
	pbill := trigger.GetTriggerPayload().(*pb.MistTrigger_ProcessBilling).ProcessBilling
	internalName := mist.ExtractInternalName(pbill.GetStreamName())

	// Enrich tenant context if not already present
	if pbill.TenantId == nil {
		info := p.applyStreamContext(trigger, internalName)
		if info.TenantID != "" {
			pbill.TenantId = &info.TenantID
		}
	} else if *pbill.TenantId != "" {
		trigger.TenantId = pbill.TenantId
	}
	if pbill.StreamId == nil || *pbill.StreamId == "" {
		if streamID := trigger.GetStreamId(); streamID != "" {
			pbill.StreamId = &streamID
		}
	}

	p.logger.WithFields(logging.Fields{
		"internal_name": internalName,
		"process_type":  pbill.GetProcessType(),
		"duration_ms":   pbill.GetDurationMs(),
		"node_id":       trigger.GetNodeId(),
	}).Debug("Processing process_billing trigger")

	// Forward to Decklog
	if err := p.sendTriggerToDecklog(trigger); err != nil {
		p.logger.WithFields(logging.Fields{
			"trigger_type": trigger.GetTriggerType(),
			"error":        err,
		}).Error("Failed to send process billing trigger to Decklog")
	}
	return "", false, nil
}

// ProcessTrigger satisfies the interface but is not used (control server uses ProcessTypedTrigger)
func (p *Processor) ProcessTrigger(triggerType string, rawPayload []byte, nodeID string) (string, bool, error) {
	return "", true, fmt.Errorf("ProcessTrigger not implemented - use ProcessTypedTrigger for fully typed flow")
}

// handleStorageLifecycleData forwards StorageLifecycleData to Decklog
func (p *Processor) handleStorageLifecycleData(trigger *pb.MistTrigger) (string, bool, error) {
	sld := trigger.GetTriggerPayload().(*pb.MistTrigger_StorageLifecycleData).StorageLifecycleData
	// Enrich tenant context if available in the payload
	if sld.TenantId != nil {
		trigger.TenantId = sld.TenantId
	}
	if sld.InternalName != nil && *sld.InternalName != "" {
		p.applyStreamContext(trigger, *sld.InternalName)
	} else if sld.StreamId != nil && *sld.StreamId != "" {
		// Fallback: resolve tenant/user context from stream_id (UUID)
		p.applyStreamContext(trigger, *sld.StreamId)
	}
	if sld.StreamId == nil || *sld.StreamId == "" {
		if streamID := trigger.GetStreamId(); streamID != "" {
			sld.StreamId = &streamID
		}
	}

	// Forward to Decklog
	if err := p.sendTriggerToDecklog(trigger); err != nil {
		p.logger.WithFields(logging.Fields{
			"trigger_type": trigger.GetTriggerType(),
			"error":        err,
		}).Error("Failed to send storage lifecycle trigger to Decklog")
	}
	return "", false, nil
}

// handleDVRLifecycleData forwards DVRLifecycleData to Decklog
func (p *Processor) handleDVRLifecycleData(trigger *pb.MistTrigger) (string, bool, error) {
	dld := trigger.GetTriggerPayload().(*pb.MistTrigger_DvrLifecycleData).DvrLifecycleData
	// Enrich tenant context if available in the payload
	if dld.TenantId != nil && *dld.TenantId != "" {
		trigger.TenantId = dld.TenantId
	}
	if dld.InternalName != nil && *dld.InternalName != "" {
		normalizedName := mist.ExtractInternalName(*dld.InternalName)
		p.applyStreamContext(trigger, normalizedName)
	} else if dld.StreamId != nil && *dld.StreamId != "" {
		// Fallback: resolve tenant/user context from stream_id (UUID)
		p.applyStreamContext(trigger, *dld.StreamId)
	}
	if dld.StreamId == nil || *dld.StreamId == "" {
		if streamID := trigger.GetStreamId(); streamID != "" {
			dld.StreamId = &streamID
		}
	}

	// Forward to Decklog
	if err := p.sendTriggerToDecklog(trigger); err != nil {
		p.logger.WithFields(logging.Fields{
			"trigger_type": trigger.GetTriggerType(),
			"dvr_hash":     dld.GetDvrHash(),
			"error":        err,
		}).Error("Failed to send DVR lifecycle trigger to Decklog")
	}
	return "", false, nil
}

// handlePushRewrite processes PUSH_REWRITE trigger (blocking)
func (p *Processor) handlePushRewrite(trigger *pb.MistTrigger) (string, bool, error) {
	pushRewrite := trigger.GetTriggerPayload().(*pb.MistTrigger_PushRewrite).PushRewrite
	p.logger.WithFields(logging.Fields{
		"stream_key": pushRewrite.GetStreamName(), // This is the stream key
		"node_id":    trigger.GetNodeId(),
		"push_url":   pushRewrite.GetPushUrl(),
		"hostname":   pushRewrite.GetHostname(),
	}).Debug("Processing PUSH_REWRITE trigger")

	// Call Commodore to validate stream key
	streamValidation, err := p.commodoreClient.ValidateStreamKey(context.Background(), pushRewrite.GetStreamName())
	if err != nil {
		p.logger.WithFields(logging.Fields{
			"stream_key": pushRewrite.GetStreamName(),
			"error":      err,
		}).Error("Failed to validate stream key with Commodore")
		return "", true, ingesterrors.New(pb.IngestErrorCode_INGEST_ERROR_INTERNAL, "failed to validate stream key")
	}

	if !streamValidation.Valid {
		message := streamValidation.Error
		if message == "" {
			message = "invalid stream key"
		}
		return "", true, ingesterrors.New(pb.IngestErrorCode_INGEST_ERROR_INVALID_STREAM_KEY, message)
	}

	// Check if tenant is suspended (prepaid balance < -$10)
	// Reject new ingests for suspended tenants
	if streamValidation.IsSuspended {
		p.logger.WithFields(logging.Fields{
			"stream_key": pushRewrite.GetStreamName(),
			"tenant_id":  streamValidation.TenantId,
		}).Warn("Rejecting ingest: tenant suspended due to negative balance")
		return "", true, ingesterrors.New(pb.IngestErrorCode_INGEST_ERROR_ACCOUNT_SUSPENDED, "account suspended - please top up your balance")
	}

	// Check if balance is negative (balance <= 0, but not yet suspended)
	// Return 402-style error for new ingests requiring payment
	if streamValidation.IsBalanceNegative {
		p.logger.WithFields(logging.Fields{
			"stream_key": pushRewrite.GetStreamName(),
			"tenant_id":  streamValidation.TenantId,
		}).Warn("Rejecting ingest: insufficient balance (402 Payment Required)")
		return "", true, ingesterrors.New(pb.IngestErrorCode_INGEST_ERROR_PAYMENT_REQUIRED, "payment required - please top up your balance")
	}

	// Cache stream context (tenant + user + billing info)
	if p.streamCache != nil {
		info := streamContext{
			TenantID:          streamValidation.TenantId,
			UserID:            streamValidation.UserId,
			StreamID:          streamValidation.StreamId,
			Source:            "validate_stream_key",
			UpdatedAt:         time.Now(),
			BillingModel:      streamValidation.BillingModel,
			IsSuspended:       streamValidation.IsSuspended,
			IsBalanceNegative: streamValidation.IsBalanceNegative,
		}
		// Use shorter cache TTL for prepaid tenants (1 min vs 10 min)
		// This ensures faster enforcement of balance changes
		cacheTTL := 10 * time.Minute
		if streamValidation.BillingModel == "prepaid" {
			cacheTTL = 1 * time.Minute
		}
		if streamValidation.TenantId != "" {
			cacheKey := streamValidation.TenantId + ":" + streamValidation.InternalName
			p.streamCache.Set(cacheKey, info, cacheTTL)
		}
		p.streamCacheMetaMu.Lock()
		p.streamCacheLastAt = info.UpdatedAt
		p.streamCacheLastErr = ""
		p.streamCacheMetaMu.Unlock()
	}
	if streamValidation.TenantId != "" {
		trigger.TenantId = &streamValidation.TenantId
	}
	if streamValidation.UserId != "" {
		trigger.UserId = &streamValidation.UserId
	}
	if streamValidation.StreamId != "" {
		trigger.StreamId = &streamValidation.StreamId
	}
	if streamValidation.StreamId != "" {
		streamID := streamValidation.StreamId
		pushRewrite.StreamId = &streamID
	}

	// Detect protocol from push URL
	protocol := p.detectProtocol(pushRewrite.GetPushUrl())

	// Get geographic data from node configuration
	var latitude, longitude *float64
	var location string
	var nodeBucket *pb.GeoBucket
	if nodeConfig := p.getNodeConfig(trigger.GetNodeId()); nodeConfig != nil {
		if nodeConfig.Latitude != 0 {
			latitude = &nodeConfig.Latitude
		}
		if nodeConfig.Longitude != 0 {
			longitude = &nodeConfig.Longitude
		}
		if nodeConfig.Location != "" {
			location = nodeConfig.Location
		}
		if b, centLat, centLon, ok := geo.Bucket(nodeConfig.Latitude, nodeConfig.Longitude); ok {
			nodeBucket = b
			latitude = &centLat
			longitude = &centLon
		}
	}

	// Enrich the existing MistTrigger with geo data and forward to Decklog
	// The PUSH_REWRITE trigger payload already contains all the stream ingest data
	pushRewrite.Protocol = &protocol
	pushRewrite.NodeId = &trigger.NodeId
	if latitude != nil {
		pushRewrite.Latitude = latitude
	}
	if longitude != nil {
		pushRewrite.Longitude = longitude
	}
	if nodeBucket != nil {
		pushRewrite.NodeBucket = nodeBucket
	}
	if location != "" {
		pushRewrite.Location = &location
	}

	// GeoIP enrich publisher location from hostname (encoder IP)
	if p.geoipClient != nil {
		if geoData := p.geoipClient.Lookup(pushRewrite.GetHostname()); geoData != nil {
			if geoData.CountryCode != "" {
				pushRewrite.PublisherCountryCode = &geoData.CountryCode
			}
			if geoData.City != "" {
				pushRewrite.PublisherCity = &geoData.City
			}
			if geoData.Latitude != 0 && geoData.Longitude != 0 {
				if b, centLat, centLon, ok := geo.Bucket(geoData.Latitude, geoData.Longitude); ok {
					pushRewrite.PublisherBucket = b
					pushRewrite.PublisherLatitude = &centLat
					pushRewrite.PublisherLongitude = &centLon
				}
			}
		}
	}

	// Forward the enriched MistTrigger directly to Decklog (Data Plane)
	// This flows to Periscope for operational state tracking
	if err := p.sendTriggerToDecklog(trigger); err != nil {
		p.logger.WithFields(logging.Fields{
			"stream_key": pushRewrite.GetStreamName(),
			"error":      err,
		}).Error("Failed to send stream ingest event to Decklog")
	}

	// NOTE: stream-start event no longer forwarded to Commodore (Control Plane separation)
	// Operational state (status, timing) now tracked in Periscope via Decklog events

	// Check if DVR recording is enabled for this stream and start it
	if streamValidation.IsRecordingEnabled {
		p.logger.WithFields(logging.Fields{
			"internal_name": streamValidation.InternalName,
		}).Info("DVR recording enabled for stream, starting DVR")

		// Start DVR recording via Foghorn's internal orchestration.
		// NOTE: We call Foghorn directly rather than proxying through Commodore.
		// Stream validation already happened (ValidateStreamKey), and Foghorn owns DVR state.
		// Future: If billing/quota checks are needed, add a pre-flight hook to Commodore/Purser here.
		go func() {
			if p.dvrService == nil {
				p.logger.WithField("internal_name", streamValidation.InternalName).
					Error("DVR service not configured, cannot start recording")
				return
			}
			userID := streamValidation.UserId
			dvrResponse, err := p.dvrService.StartDVR(context.Background(), &pb.StartDVRRequest{
				TenantId:     streamValidation.TenantId,
				InternalName: streamValidation.InternalName,
				UserId:       &userID,
			})
			if err != nil {
				p.logger.WithFields(logging.Fields{
					"internal_name": streamValidation.InternalName,
					"tenant_id":     streamValidation.TenantId,
					"error":         err,
				}).Error("Failed to start DVR recording")
			} else {
				p.logger.WithFields(logging.Fields{
					"internal_name": streamValidation.InternalName,
					"tenant_id":     streamValidation.TenantId,
					"dvr_hash":      dvrResponse.GetDvrHash(),
					"status":        dvrResponse.GetStatus(),
				}).Info("DVR recording started")
			}
		}()
	}

	// Return wildcard stream name for MistServer routing (live+ format)
	return fmt.Sprintf("live+%s", streamValidation.InternalName), false, nil
}

// handlePlayRewrite processes PLAY_REWRITE trigger (blocking)
func (p *Processor) handlePlayRewrite(trigger *pb.MistTrigger) (string, bool, error) {
	defaultStream := trigger.GetTriggerPayload().(*pb.MistTrigger_PlayRewrite).PlayRewrite
	playbackID := defaultStream.GetRequestedStream() // This is the stream name / playback ID

	p.logger.WithFields(logging.Fields{
		"requested_stream": defaultStream.GetRequestedStream(), // playback ID
		"viewer_host":      defaultStream.GetViewerHost(),
		"output_type":      defaultStream.GetOutputType(),
		"request_url":      defaultStream.GetRequestUrl(),
		"node_id":          trigger.GetNodeId(),
	}).Debug("Processing PLAY_REWRITE trigger")

	// Resolve the playback ID to its canonical internal name (e.g. "live+uuid" or "vod+hash").
	target, err := control.ResolveStream(context.Background(), playbackID)
	if err != nil {
		p.logger.WithFields(logging.Fields{
			"playback_id": playbackID,
			"error":       err,
		}).Warn("Failed to resolve playback ID")
	}

	// Check stream owner's billing status from cache (set during PUSH_REWRITE).
	// Falls back to Quartermaster when cache misses.
	billing := p.GetBillingStatus(context.Background(), target.InternalName, target.TenantID)
	if billing != nil {
		if billing.IsSuspended {
			p.logger.WithFields(logging.Fields{
				"playback_id": playbackID,
				"tenant_id":   billing.TenantID,
				"from_cache":  billing.FromCache,
			}).Warn("Rejecting viewer: stream owner suspended")
			return "", true, fmt.Errorf("stream unavailable - owner account suspended")
		}
		if billing.BillingModel == "prepaid" && billing.IsBalanceNegative {
			p.logger.WithFields(logging.Fields{
				"playback_id":   playbackID,
				"tenant_id":     billing.TenantID,
				"billing_model": billing.BillingModel,
				"from_cache":    billing.FromCache,
			}).Warn("Rejecting viewer: stream owner balance exhausted (402)")
			return "", true, fmt.Errorf("payment required - stream owner needs to top up balance")
		}
	} else if target.TenantID != "" {
		p.logger.WithFields(logging.Fields{
			"playback_id":   playbackID,
			"tenant_id":     target.TenantID,
			"internal_name": target.InternalName,
		}).Debug("Billing status unknown, failing open")
	}

	// Enrich with resolved internal name (UUID without prefix) for analytics correlation.
	// This ensures analytics can correlate viewer events with infrastructure events.
	resolvedName := mist.ExtractInternalName(target.InternalName)
	defaultStream.ResolvedInternalName = &resolvedName

	// Enrich the PlayRewriteTrigger (ViewerResolveTrigger) with viewer geographic data via GeoIP lookup.
	if p.geoipClient != nil && defaultStream.GetViewerHost() != "" {
		if geoData := geoip.LookupCached(context.Background(), p.geoipClient, p.geoipCache, defaultStream.GetViewerHost()); geoData != nil {
			defaultStream.CountryCode = &geoData.CountryCode
			defaultStream.City = &geoData.City
			defaultStream.Latitude = &geoData.Latitude
			defaultStream.Longitude = &geoData.Longitude

			p.logger.WithFields(logging.Fields{
				"viewer_ip":    defaultStream.GetViewerHost(),
				"country_code": geoData.CountryCode,
				"city":         geoData.City,
				"playback_id":  playbackID,
			}).Debug("Enriched PLAY_REWRITE with viewer geo data")
		}
	}

	// Enrich with node location name for analytics (e.g., "us-east-1", "Frankfurt")
	if nodeConfig := p.getNodeConfig(trigger.GetNodeId()); nodeConfig != nil {
		if nodeConfig.Location != "" {
			defaultStream.NodeLocation = &nodeConfig.Location
		}
	}

	// Apply the resolved TenantID if available.
	if target.TenantID != "" {
		trigger.TenantId = &target.TenantID
	}
	if target.StreamID != "" {
		trigger.StreamId = &target.StreamID
		defaultStream.StreamId = &target.StreamID
	}

	go func(tr *pb.MistTrigger, requested string) {
		if err := p.sendTriggerToDecklog(tr); err != nil {
			p.logger.WithFields(logging.Fields{
				"requested_stream": requested,
				"trigger_type":     tr.GetTriggerType(),
				"error":            err,
			}).Error("Failed to send play_rewrite trigger to Decklog")
		}
	}(trigger, playbackID)

	// Return the resolved fully-qualified stream name (e.g. "live+uuid") to MistServer.
	return target.InternalName, false, nil
}

// handleStreamSource processes STREAM_SOURCE trigger (blocking)
func (p *Processor) handleStreamSource(trigger *pb.MistTrigger) (string, bool, error) {
	streamSource := trigger.GetTriggerPayload().(*pb.MistTrigger_StreamSource).StreamSource
	streamName := streamSource.GetStreamName()

	p.logger.WithFields(logging.Fields{
		"stream_name": streamName,
		"node_id":     trigger.GetNodeId(),
	}).Debug("Processing STREAM_SOURCE trigger")

	// STREAM_SOURCE is for VOD artifacts only. Live streams have no static source -
	// they're push streams that MistServer receives from encoders.
	if strings.HasPrefix(streamName, "live+") {
		p.logger.WithFields(logging.Fields{
			"stream_name": streamName,
			"node_id":     trigger.GetNodeId(),
		}).Debug("STREAM_SOURCE not applicable for live streams; aborting")
		return "", true, nil
	}

	// Extract artifact internal name (strips vod+ or any other prefix)
	artifactInternal := mist.ExtractInternalName(streamName)

	artifactHash := ""
	if control.CommodoreClient != nil && artifactInternal != "" {
		if resp, err := control.CommodoreClient.ResolveArtifactInternalName(context.Background(), artifactInternal); err == nil && resp.Found {
			artifactHash = resp.ArtifactHash
		}
	}
	if artifactHash == "" {
		p.logger.WithFields(logging.Fields{
			"artifact_internal_name": artifactInternal,
			"stream_name":            streamName,
		}).Warn("Artifact internal name not found; cannot resolve stream source")
		return "", true, nil
	}

	target, err := control.ResolveStream(context.Background(), streamName)
	if err != nil {
		p.logger.WithFields(logging.Fields{
			"stream_name": streamName,
			"error":       err,
		}).Warn("Failed to resolve stream source")
	}
	if target != nil {
		if target.TenantID != "" {
			trigger.TenantId = &target.TenantID
		}
		if target.StreamID != "" {
			trigger.StreamId = &target.StreamID
			streamSource.StreamId = &target.StreamID
		}
	}

	// Resolve artifact from in-memory state (populated by Helmsman with correct paths)
	_, artifactInfo := state.DefaultManager().FindNodeByArtifactHash(artifactHash)
	if artifactInfo != nil {
		p.logger.WithFields(logging.Fields{
			"artifact_hash": artifactHash,
			"stream_name":   streamName,
			"file_path":     artifactInfo.GetFilePath(),
			"format":        artifactInfo.GetFormat(),
			"size_bytes":    artifactInfo.GetSizeBytes(),
		}).Info("VOD artifact resolved from in-memory state")

		go func(tr *pb.MistTrigger, name string) {
			if err := p.sendTriggerToDecklog(tr); err != nil {
				p.logger.WithFields(logging.Fields{
					"stream_name":  name,
					"trigger_type": tr.GetTriggerType(),
					"error":        err,
				}).Error("Failed to send stream_source trigger to Decklog")
			}
		}(trigger, streamName)

		// Return file path with shouldAbort=false to tell Helmsman to use this source
		return artifactInfo.GetFilePath(), false, nil
	}

	p.logger.WithFields(logging.Fields{
		"artifact_hash": artifactHash,
		"stream_name":   streamName,
	}).Warn("Artifact not found")

	go func(tr *pb.MistTrigger, name string) {
		if err := p.sendTriggerToDecklog(tr); err != nil {
			p.logger.WithFields(logging.Fields{
				"stream_name":  name,
				"trigger_type": tr.GetTriggerType(),
				"error":        err,
			}).Error("Failed to send stream_source trigger to Decklog")
		}
	}(trigger, streamName)

	// Return empty to let MistServer use default source (will fail for VOD)
	return "", true, nil
}

// handlePushEnd processes PUSH_END trigger (non-blocking)
func (p *Processor) handlePushEnd(trigger *pb.MistTrigger) (string, bool, error) {
	pushEnd := trigger.GetTriggerPayload().(*pb.MistTrigger_PushEnd).PushEnd
	internalName := mist.ExtractInternalName(pushEnd.GetStreamName())

	p.applyStreamContext(trigger, internalName)
	if streamID := trigger.GetStreamId(); streamID != "" {
		pushEnd.StreamId = &streamID
	}

	// Send enriched trigger to Decklog
	if err := p.sendTriggerToDecklog(trigger); err != nil {
		p.logger.WithFields(logging.Fields{
			"internal_name": internalName,
			"push_id":       pushEnd.GetPushId(),
			"trigger_type":  trigger.GetTriggerType(),
			"error":         err,
		}).Error("Failed to send push end trigger to Decklog")
	}

	return "", false, nil
}

// handlePushOutStart processes PUSH_OUT_START trigger (blocking)
func (p *Processor) handlePushOutStart(trigger *pb.MistTrigger) (string, bool, error) {
	pushOutStart := trigger.GetTriggerPayload().(*pb.MistTrigger_PushOutStart).PushOutStart
	// nodeID is available via trigger.GetNodeId() and flows to Decklog with the full trigger
	internalName := mist.ExtractInternalName(pushOutStart.GetStreamName())

	p.applyStreamContext(trigger, internalName)
	if streamID := trigger.GetStreamId(); streamID != "" {
		pushOutStart.StreamId = &streamID
	}

	// NOTE: push_out_start event no longer forwarded to Commodore (Control Plane separation)
	// Events flow through Decklog to Periscope for tracking

	// Send enriched trigger to Decklog (Data Plane)
	if err := p.sendTriggerToDecklog(trigger); err != nil {
		p.logger.WithFields(logging.Fields{
			"internal_name": internalName,
			"push_target":   pushOutStart.GetPushTarget(),
			"trigger_type":  trigger.GetTriggerType(),
			"error":         err,
		}).Error("Failed to send push out start trigger to Decklog")
	}
	return pushOutStart.GetPushTarget(), false, nil
}

// handleUserNew processes USER_NEW trigger (blocking)
func (p *Processor) handleUserNew(trigger *pb.MistTrigger) (string, bool, error) {
	userNew := trigger.GetTriggerPayload().(*pb.MistTrigger_ViewerConnect).ViewerConnect
	internalName := mist.ExtractInternalName(userNew.GetStreamName())
	p.logger.WithFields(logging.Fields{
		"session_id":      userNew.GetSessionId(),
		"internal_name":   internalName,
		"connection_addr": userNew.GetHost(),
		"node_id":         trigger.GetNodeId(),
	}).Debug("Processing USER_NEW trigger")

	info := p.applyStreamContext(trigger, userNew.GetStreamName())
	if streamID := trigger.GetStreamId(); streamID != "" {
		userNew.StreamId = &streamID
	}

	// Enrich ViewerConnect payload directly
	userNew.NodeId = func() *string { s := trigger.GetNodeId(); return &s }()

	// Add viewer geographic data from GeoIP if available (bucketized)
	if p.geoipClient != nil && userNew.GetHost() != "" {
		if geoData := geoip.LookupCached(context.Background(), p.geoipClient, p.geoipCache, userNew.GetHost()); geoData != nil {
			userNew.ClientCountry = &geoData.CountryCode
			userNew.ClientCity = &geoData.City
			if bucket, centLat, centLon, ok := geo.Bucket(geoData.Latitude, geoData.Longitude); ok {
				userNew.ClientLatitude = &centLat
				userNew.ClientLongitude = &centLon
				userNew.ClientBucket = bucket
			}
			// keep node bucket if available
			if nodeCfg := p.getNodeConfig(trigger.GetNodeId()); nodeCfg != nil {
				if bucket, _, _, ok := geo.Bucket(nodeCfg.Latitude, nodeCfg.Longitude); ok {
					userNew.NodeBucket = bucket
				}
			}

			p.logger.WithFields(logging.Fields{
				"connection_ip": userNew.GetHost(),
				"country_code":  geoData.CountryCode,
				"city":          geoData.City,
				"session_id":    userNew.GetSessionId(),
			}).Debug("Enriched USER_NEW with connection geo data (bucketized)")
		}
	}
	// Note: Client IP redaction now happens at API layer (GraphQL resolvers, Signalman)
	// Raw IP in 'host' field is preserved for ClickHouse storage and future analysis

	// Send enriched trigger to Decklog
	if err := p.sendTriggerToDecklog(trigger); err != nil {
		p.logger.WithFields(logging.Fields{
			"session_id":    userNew.GetSessionId(),
			"internal_name": internalName,
			"trigger_type":  trigger.GetTriggerType(),
			"error":         err,
		}).Error("Failed to send user connection trigger to Decklog")
	}

	// Update state (viewer +1) - reuse info from earlier lookup
	// CRITICAL: Extract internal name to avoid creating duplicate state entries
	state.DefaultManager().UpdateUserConnection(internalName, trigger.GetNodeId(), info.TenantID, 1)

	// Confirm virtual viewer: transition PENDING -> ACTIVE
	// This decrements PendingRedirects and recalculates AddBandwidth
	clientIP := userNew.GetHost()
	correlationID := extractCorrelationID(userNew.GetRequestUrl())
	if confirmed := state.DefaultManager().ConfirmVirtualViewerByID(
		correlationID,
		trigger.GetNodeId(),
		internalName,
		clientIP,
		userNew.GetSessionId(),
	); confirmed {
		p.logger.WithFields(logging.Fields{
			"node_id":       trigger.GetNodeId(),
			"internal_name": internalName,
			"client_ip":     clientIP,
		}).Debug("Virtual viewer confirmed (PENDING -> ACTIVE)")
	}

	// Allow user connection by returning "true"
	return "true", false, nil
}

// handleStreamBuffer processes STREAM_BUFFER trigger (non-blocking)
// Forwards the original StreamBufferTrigger to Decklog with full track data and health metrics.
func (p *Processor) handleStreamBuffer(trigger *pb.MistTrigger) (string, bool, error) {
	// Extract StreamBuffer payload from protobuf
	streamBuffer := trigger.GetTriggerPayload().(*pb.MistTrigger_StreamBuffer).StreamBuffer

	p.logger.WithFields(logging.Fields{
		"internal_name":    streamBuffer.GetStreamName(),
		"buffer_state":     streamBuffer.GetBufferState(),
		"track_count":      len(streamBuffer.GetTracks()),
		"stream_buffer_ms": streamBuffer.GetStreamBufferMs(),
		"stream_jitter_ms": streamBuffer.GetStreamJitterMs(),
		"mist_issues":      streamBuffer.GetMistIssues(),
		"node_id":          trigger.GetNodeId(),
	}).Debug("Processing STREAM_BUFFER trigger")

	// NOTE: stream-status event no longer forwarded to Commodore (Control Plane separation)
	// Events flow through Decklog to Periscope for tracking

	info := p.applyStreamContext(trigger, streamBuffer.GetStreamName())
	if streamID := trigger.GetStreamId(); streamID != "" {
		streamBuffer.StreamId = &streamID
	}

	// Update state from buffer first (this sets StartedAt on first buffer event)
	// CRITICAL: Extract internal name from stream name (e.g., "live+demo_stream" -> "demo_stream")
	// to avoid creating duplicate state entries for the same logical stream
	internalName := mist.ExtractInternalName(streamBuffer.GetStreamName())
	_ = state.DefaultManager().UpdateStreamFromBuffer(
		streamBuffer.GetStreamName(),
		internalName,
		trigger.GetNodeId(),
		info.TenantID,
		streamBuffer.GetBufferState(),
		"",
	)

	// Forward original StreamBufferTrigger to Decklog (preserves all track data and health metrics)
	// Helmsman already enriched it with has_issues, issues_description, quality_tier, etc.
	if err := p.sendTriggerToDecklog(trigger); err != nil {
		p.logger.WithFields(logging.Fields{
			"internal_name": streamBuffer.GetStreamName(),
			"trigger_type":  trigger.GetTriggerType(),
			"error":         err,
		}).Error("Failed to send stream buffer trigger to Decklog")
	}

	return "", false, nil
}

// handleStreamEnd processes STREAM_END trigger (non-blocking)
func (p *Processor) handleStreamEnd(trigger *pb.MistTrigger) (string, bool, error) {
	// Extract StreamEnd payload from protobuf
	streamEnd := trigger.GetTriggerPayload().(*pb.MistTrigger_StreamEnd).StreamEnd
	// CRITICAL: Extract internal name to match state keys
	internalName := mist.ExtractInternalName(streamEnd.GetStreamName())
	nodeID := trigger.GetNodeId()

	p.logger.WithFields(logging.Fields{
		"internal_name": internalName,
		"node_id":       nodeID,
	}).Debug("Processing STREAM_END trigger")

	// NOTE: stream-end event no longer forwarded to Commodore (Control Plane separation)
	// Events flow through Decklog to Periscope for tracking

	_ = p.applyStreamContext(trigger, internalName)
	streamEnd.NodeId = &nodeID
	if streamID := trigger.GetStreamId(); streamID != "" {
		streamEnd.StreamId = &streamID
	}

	// Send enriched trigger to Decklog
	if err := p.sendTriggerToDecklog(trigger); err != nil {
		p.logger.WithFields(logging.Fields{
			"internal_name": internalName,
			"trigger_type":  trigger.GetTriggerType(),
			"error":         err,
		}).Error("Failed to send stream end trigger to Decklog")
	}

	// Update state offline
	state.DefaultManager().SetOffline(internalName, nodeID)

	// Stop DVR on its storage node if active
	control.StopDVRByInternalName(internalName, p.logger)

	return "", false, nil
}

// handleUserEnd processes USER_END trigger (non-blocking)
func (p *Processor) handleUserEnd(trigger *pb.MistTrigger) (string, bool, error) {
	userEnd := trigger.GetTriggerPayload().(*pb.MistTrigger_ViewerDisconnect).ViewerDisconnect
	p.logger.WithFields(logging.Fields{
		"session_id":        userEnd.GetSessionId(),
		"internal_name":     userEnd.GetStreamName(),
		"connection_addr":   userEnd.GetHost(),
		"seconds_connected": userEnd.GetDuration(),
		"uploaded_bytes":    userEnd.GetUpBytes(),
		"downloaded_bytes":  userEnd.GetDownBytes(),
		"node_id":           trigger.GetNodeId(),
	}).Debug("Processing USER_END trigger")

	info := p.applyStreamContext(trigger, userEnd.GetStreamName())
	if streamID := trigger.GetStreamId(); streamID != "" {
		userEnd.StreamId = &streamID
	}

	userEnd.NodeId = func() *string { s := trigger.GetNodeId(); return &s }()

	// Add viewer geographic data from GeoIP if available (bucketized)
	if p.geoipClient != nil && userEnd.GetHost() != "" {
		if geoData := geoip.LookupCached(context.Background(), p.geoipClient, p.geoipCache, userEnd.GetHost()); geoData != nil {
			userEnd.CountryCode = &geoData.CountryCode
			userEnd.City = &geoData.City
			if bucket, centLat, centLon, ok := geo.Bucket(geoData.Latitude, geoData.Longitude); ok {
				userEnd.Latitude = &centLat
				userEnd.Longitude = &centLon
				userEnd.ClientBucket = bucket
			}
			if nodeCfg := p.getNodeConfig(trigger.GetNodeId()); nodeCfg != nil {
				if bucket, _, _, ok := geo.Bucket(nodeCfg.Latitude, nodeCfg.Longitude); ok {
					userEnd.NodeBucket = bucket
				}
			}

			p.logger.WithFields(logging.Fields{
				"connection_ip": userEnd.GetHost(),
				"country_code":  geoData.CountryCode,
				"city":          geoData.City,
				"session_id":    userEnd.GetSessionId(),
			}).Debug("Enriched USER_END with connection geo data (bucketized)")
		}
	}

	// Send enriched trigger to Decklog
	if err := p.sendTriggerToDecklog(trigger); err != nil {
		p.logger.WithFields(logging.Fields{
			"session_id":    userEnd.GetSessionId(),
			"internal_name": userEnd.GetStreamName(),
			"trigger_type":  trigger.GetTriggerType(),
			"error":         err,
		}).Error("Failed to send user disconnect trigger to Decklog")
	}

	// Update state (viewer -1) - reuse info from earlier lookup
	// CRITICAL: Extract internal name to match state keys
	internalStreamName := mist.ExtractInternalName(userEnd.GetStreamName())
	state.DefaultManager().UpdateUserConnection(internalStreamName, trigger.GetNodeId(), info.TenantID, -1)

	// Disconnect virtual viewer: transition ACTIVE -> DISCONNECTED
	clientIP := userEnd.GetHost()
	state.DefaultManager().DisconnectVirtualViewerBySessionID(userEnd.GetSessionId(), trigger.GetNodeId(), internalStreamName, clientIP)

	return "", false, nil
}

func extractCorrelationID(requestURL string) string {
	if requestURL == "" {
		return ""
	}
	parsedURL, err := url.Parse(requestURL)
	if err != nil {
		return ""
	}
	return parsedURL.Query().Get("fwcid")
}

// handleLiveTrackList processes LIVE_TRACK_LIST trigger (non-blocking)
func (p *Processor) handleLiveTrackList(trigger *pb.MistTrigger) (string, bool, error) {
	// Extract LiveTrackList payload from protobuf
	liveTrackList := trigger.GetTriggerPayload().(*pb.MistTrigger_TrackList).TrackList
	// CRITICAL: Extract internal name to match state keys
	internalName := mist.ExtractInternalName(liveTrackList.GetStreamName())
	nodeID := trigger.GetNodeId()
	tracks := liveTrackList.GetTracks()

	p.logger.WithFields(logging.Fields{
		"internal_name": internalName,
		"node_id":       nodeID,
	}).Debug("Processing LIVE_TRACK_LIST trigger")

	// Track list is now structured data
	p.logger.WithFields(logging.Fields{
		"internal_name": internalName,
		"track_count":   len(tracks),
		"quality_tier":  liveTrackList.GetQualityTier(),
	}).Debug("Processing structured LIVE_TRACK_LIST")

	// Quality metrics are available but we send raw trackListJSON to protobuf

	info := p.applyStreamContext(trigger, internalName)
	if streamID := trigger.GetStreamId(); streamID != "" {
		liveTrackList.StreamId = &streamID
	}

	// Send enriched trigger to Decklog
	if err := p.sendTriggerToDecklog(trigger); err != nil {
		p.logger.WithFields(logging.Fields{
			"internal_name": internalName,
			"trigger_type":  trigger.GetTriggerType(),
			"error":         err,
		}).Error("Failed to send track list trigger to Decklog")
	}

	// Update state track list - using empty JSON string since we have structured data
	state.DefaultManager().UpdateTrackList(internalName, nodeID, info.TenantID, "")

	return "", false, nil
}

// handleRecordingEnd processes RECORDING_END trigger (non-blocking)
func (p *Processor) handleRecordingEnd(trigger *pb.MistTrigger) (string, bool, error) {
	// Extract RecordingEnd payload from protobuf
	recordingEnd := trigger.GetTriggerPayload().(*pb.MistTrigger_RecordingComplete).RecordingComplete
	internalName := mist.ExtractInternalName(recordingEnd.GetStreamName())
	nodeID := trigger.GetNodeId()

	p.logger.WithFields(logging.Fields{
		"internal_name":     internalName,
		"file_path":         recordingEnd.GetFilePath(),
		"output_protocol":   recordingEnd.GetOutputProtocol(),
		"bytes_written":     recordingEnd.GetBytesWritten(),
		"seconds_writing":   recordingEnd.GetSecondsWriting(),
		"time_started":      recordingEnd.GetTimeStarted(),
		"time_ended":        recordingEnd.GetTimeEnded(),
		"media_duration_ms": recordingEnd.GetMediaDurationMs(),
		"node_id":           nodeID,
	}).Debug("Processing RECORDING_END trigger")

	// NOTE: recording-status event no longer forwarded to Commodore (Control Plane separation)
	// Events flow through Decklog to Periscope for tracking

	_ = p.applyStreamContext(trigger, internalName)
	if streamID := trigger.GetStreamId(); streamID != "" {
		recordingEnd.StreamId = &streamID
	}

	// Send enriched trigger to Decklog
	if err := p.sendTriggerToDecklog(trigger); err != nil {
		p.logger.WithFields(logging.Fields{
			"internal_name": internalName,
			"trigger_type":  trigger.GetTriggerType(),
			"error":         err,
		}).Error("Failed to send recording trigger to Decklog")
	}

	return "", false, nil
}

// handleRecordingSegment processes RECORDING_SEGMENT trigger (non-blocking)
func (p *Processor) handleRecordingSegment(trigger *pb.MistTrigger) (string, bool, error) {
	// Extract RecordingSegment payload from protobuf
	seg := trigger.GetTriggerPayload().(*pb.MistTrigger_RecordingSegment).RecordingSegment
	internalName := mist.ExtractInternalName(seg.GetStreamName())

	// Enrich tenant context before forwarding
	info := p.applyStreamContext(trigger, internalName)
	if streamID := trigger.GetStreamId(); streamID != "" {
		seg.StreamId = &streamID
	}

	p.logger.WithFields(logging.Fields{
		"internal_name": internalName,
		"file_path":     seg.GetFilePath(),
		"duration_ms":   seg.GetDurationMs(),
		"node_id":       trigger.GetNodeId(),
		"tenant_id":     info.TenantID,
	}).Debug("Processing RECORDING_SEGMENT trigger")

	// Forward the enriched trigger to Decklog for analytics/billing
	if err := p.sendTriggerToDecklog(trigger); err != nil {
		p.logger.WithError(err).WithFields(logging.Fields{
			"internal_name": internalName,
			"node_id":       trigger.GetNodeId(),
		}).Error("Failed to send RECORDING_SEGMENT trigger to Decklog")
	}

	return "", false, nil
}

// handleStreamLifecycleUpdate forwards StreamLifecycleUpdate to Decklog and updates state
func (p *Processor) handleStreamLifecycleUpdate(trigger *pb.MistTrigger) (string, bool, error) {
	slu := trigger.GetTriggerPayload().(*pb.MistTrigger_StreamLifecycleUpdate).StreamLifecycleUpdate
	internal := mist.ExtractInternalName(slu.GetInternalName())
	nodeID := slu.GetNodeId()

	// Enrich tenant context before forwarding (same pattern as handleStreamEnd)
	info := p.applyStreamContext(trigger, internal)
	if info.TenantID != "" && slu.TenantId == nil {
		slu.TenantId = &info.TenantID
	}
	if slu.StreamId == nil || *slu.StreamId == "" {
		if streamID := trigger.GetStreamId(); streamID != "" {
			slu.StreamId = &streamID
		}
	}
	if slu.StreamId == nil || *slu.StreamId == "" {
		p.logger.WithFields(logging.Fields{
			"internal_name": internal,
			"trigger_type":  trigger.GetTriggerType(),
		}).Warn("StreamLifecycleUpdate missing stream_id")
	}

	// Enrich with StartedAt from state manager (for duration calculation)
	// State manager tracks when stream first went live
	if streamState := state.DefaultManager().GetStreamState(internal); streamState != nil {
		if streamState.StartedAt != nil && slu.StartedAt == nil {
			startedAtUnix := streamState.StartedAt.Unix()
			slu.StartedAt = &startedAtUnix
		}
	}

	// Forward the enriched StreamLifecycleUpdate to Decklog
	if err := p.sendTriggerToDecklog(trigger); err != nil {
		p.logger.WithFields(logging.Fields{
			"internal_name": internal,
			"trigger_type":  trigger.GetTriggerType(),
			"error":         err,
		}).Error("Failed to send stream lifecycle update to Decklog")
	}

	if slu.GetStatus() == "offline" {
		state.DefaultManager().SetOffline(internal, nodeID)
	} else {
		// Update stream stats in state manager for load balancing
		// This is critical: the balancer requires inputs > 0 to consider a node for playback
		total := int(slu.GetTotalViewers())
		inputs := int(slu.GetTotalInputs())
		up := int64(slu.GetUploadedBytes())
		down := int64(slu.GetDownloadedBytes())
		replicated := slu.GetReplicated()
		state.DefaultManager().UpdateNodeStats(internal, nodeID, total, inputs, up, down, replicated)
	}
	return "", false, nil
}

// handleClientLifecycleUpdate forwards ClientLifecycleUpdate to Decklog
func (p *Processor) handleClientLifecycleUpdate(trigger *pb.MistTrigger) (string, bool, error) {
	clu := trigger.GetTriggerPayload().(*pb.MistTrigger_ClientLifecycleUpdate).ClientLifecycleUpdate
	internal := clu.GetInternalName()

	// Enrich tenant context before forwarding (same pattern as handleUserNew/handleUserEnd)
	info := p.applyStreamContext(trigger, internal)
	if info.TenantID != "" && clu.TenantId == nil {
		clu.TenantId = &info.TenantID
	}
	if clu.StreamId == nil || *clu.StreamId == "" {
		if streamID := trigger.GetStreamId(); streamID != "" {
			clu.StreamId = &streamID
		}
	}
	if clu.StreamId == nil || *clu.StreamId == "" {
		p.logger.WithFields(logging.Fields{
			"internal_name": internal,
			"trigger_type":  trigger.GetTriggerType(),
		}).Warn("ClientLifecycleUpdate missing stream_id")
	}

	// Forward the enriched ClientLifecycleUpdate to Decklog
	if err := p.sendTriggerToDecklog(trigger); err != nil {
		p.logger.WithFields(logging.Fields{
			"internal_name": internal,
			"trigger_type":  trigger.GetTriggerType(),
			"error":         err,
		}).Error("Failed to send client lifecycle update to Decklog")
	}
	return "", false, nil
}

// handleNodeLifecycleUpdate processes NODE_LIFECYCLE_UPDATE triggers using protobuf directly
func (p *Processor) handleNodeLifecycleUpdate(trigger *pb.MistTrigger) (string, bool, error) {
	nu := trigger.GetTriggerPayload().(*pb.MistTrigger_NodeLifecycleUpdate).NodeLifecycleUpdate

	p.logger.WithFields(logging.Fields{
		"node_id":    nu.GetNodeId(),
		"is_healthy": nu.GetIsHealthy(),
		"bw_limit":   nu.GetBwLimit(),
		"ram_max":    nu.GetRamMax(),
		"location":   nu.GetLocation(),
	}).Info("Received NodeLifecycleUpdate from Helmsman")

	// Parse latitude/longitude for state manager
	var latitude, longitude *float64
	if nu.GetLatitude() != 0 {
		lat := nu.GetLatitude()
		latitude = &lat
	}
	if nu.GetLongitude() != 0 {
		lon := nu.GetLongitude()
		longitude = &lon
	}

	// Update node info in state manager
	state.DefaultManager().SetNodeInfo(nu.GetNodeId(), nu.GetBaseUrl(), nu.GetIsHealthy(), latitude, longitude, nu.GetLocation(), nu.GetOutputsJson(), nil)

	// Log mismatch between Helmsman-reported mode and Foghorn-authoritative mode.
	// Foghorn owns operational mode; Helmsman's heartbeat is confirmation only.
	if reportedMode, ok := mapOperationalMode(nu.GetOperationalMode()); ok {
		authoritativeMode := state.DefaultManager().GetNodeOperationalMode(nu.GetNodeId())
		if authoritativeMode != reportedMode {
			p.logger.WithFields(logging.Fields{
				"node_id":            nu.GetNodeId(),
				"reported_mode":      reportedMode,
				"authoritative_mode": authoritativeMode,
				"trigger_id":         trigger.GetRequestId(),
			}).Warn("Helmsman reported mode differs from Foghorn authoritative mode (may need ConfigSeed push)")
		}
	}

	// Update node metrics using protobuf data directly
	state.DefaultManager().UpdateNodeMetrics(nu.GetNodeId(), struct {
		CPU                  float64
		RAMMax               float64
		RAMCurrent           float64
		UpSpeed              float64
		DownSpeed            float64
		BWLimit              float64
		CapIngest            bool
		CapEdge              bool
		CapStorage           bool
		CapProcessing        bool
		Roles                []string
		StorageCapacityBytes uint64
		StorageUsedBytes     uint64
		MaxTranscodes        int
		CurrentTranscodes    int
	}{
		CPU:           float64(nu.GetCpuTenths()) / 10.0,
		RAMMax:        float64(nu.GetRamMax()),
		RAMCurrent:    float64(nu.GetRamCurrent()),
		UpSpeed:       float64(nu.GetUpSpeed()),
		DownSpeed:     float64(nu.GetDownSpeed()),
		BWLimit:       float64(nu.GetBwLimit()),
		CapIngest:     nu.GetCapabilities() != nil && nu.GetCapabilities().GetIngest(),
		CapEdge:       nu.GetCapabilities() != nil && nu.GetCapabilities().GetEdge(),
		CapStorage:    nu.GetCapabilities() != nil && nu.GetCapabilities().GetStorage(),
		CapProcessing: nu.GetCapabilities() != nil && nu.GetCapabilities().GetProcessing(),
		Roles: func() []string {
			if nu.GetCapabilities() == nil {
				return nil
			}
			return nu.GetCapabilities().GetRoles()
		}(),
		StorageCapacityBytes: func() uint64 {
			if nu.GetLimits() == nil {
				return 0
			}
			return nu.GetLimits().GetStorageCapacityBytes()
		}(),
		StorageUsedBytes: func() uint64 {
			if nu.GetLimits() == nil {
				return 0
			}
			return nu.GetLimits().GetStorageUsedBytes()
		}(),
		MaxTranscodes: func() int {
			if nu.GetLimits() == nil {
				return 0
			}
			return int(nu.GetLimits().GetMaxTranscodes())
		}(),
		CurrentTranscodes: 0,
	})

	// Update storage paths if present
	if storage := nu.GetStorage(); storage != nil {
		state.DefaultManager().SetNodeStoragePaths(nu.GetNodeId(), storage.GetLocalPath(), storage.GetS3Bucket(), storage.GetS3Prefix())
	}

	// Update GPU info if present (TODO: Add GPU field to NodeLifecycleUpdate protobuf)
	// if gpu := nu.GetGpu(); gpu != nil {
	//     state.DefaultManager().SetNodeGPUInfo(nu.GetNodeId(), gpu.GetVendor(), int(gpu.GetCount()), int(gpu.GetMemoryMb()), gpu.GetComputeCapability())
	// }

	// Update disk usage from OS-level stats reported by Helmsman
	state.DefaultManager().UpdateNodeDiskUsage(nu.GetNodeId(), nu.GetDiskTotalBytes(), nu.GetDiskUsedBytes())

	// Calculate total connections across all streams for virtual viewer reconciliation
	var totalConnections int
	for _, s := range nu.GetStreams() {
		totalConnections += int(s.GetTotal())
	}

	// Reconcile virtual viewers with real metrics from Helmsman
	// This replaces DecayAddBandwidth() - times out stale PENDING viewers and updates bandwidth estimates
	state.DefaultManager().ReconcileVirtualViewers(nu.GetNodeId(), totalConnections, nu.GetUpSpeed())

	// Update stream stats for each stream
	// CRITICAL: Extract internal name to match state keys (e.g., "live+demo_stream" -> "demo_stream")
	for streamName, s := range nu.GetStreams() {
		internalName := mist.ExtractInternalName(streamName)
		state.DefaultManager().UpdateNodeStats(internalName, nu.GetNodeId(), int(s.GetTotal()), int(s.GetInputs()), int64(s.GetBytesUp()), int64(s.GetBytesDown()), s.GetReplicated())
	}

	// Update artifacts directly from protobuf - this is critical for VOD playback
	if artifacts := nu.GetArtifacts(); len(artifacts) > 0 {
		state.DefaultManager().SetNodeArtifacts(nu.GetNodeId(), artifacts)
	}

	// Enrich with database UUID for subscription lookups (frontend uses UUID, not logical name)
	if uuid := p.resolveNodeUUID(nu.GetNodeId()); uuid != "" {
		nu.NodeUuid = &uuid
	}

	// Attribute infra events to the cluster owner tenant (not a stream tenant).
	if (trigger.TenantId == nil || *trigger.TenantId == "") && p.ownerTenantID != "" {
		trigger.TenantId = &p.ownerTenantID
	}
	if (nu.TenantId == nil || *nu.TenantId == "") && p.ownerTenantID != "" {
		nu.TenantId = &p.ownerTenantID
	}

	// Forward complete node lifecycle event to Decklog using protobuf directly
	// CRITICAL: Strip artifacts before sending to Decklog/Analytics to avoid excessive data
	nu.Artifacts = nil
	if err := p.sendTriggerToDecklog(trigger); err != nil {
		p.logger.WithFields(logging.Fields{
			"node_id":      nu.GetNodeId(),
			"trigger_type": trigger.GetTriggerType(),
			"error":        err,
		}).Error("Failed to send node lifecycle update to Decklog")
	}

	return "", false, nil
}

func mapOperationalMode(mode pb.NodeOperationalMode) (state.NodeOperationalMode, bool) {
	switch mode {
	case pb.NodeOperationalMode_NODE_OPERATIONAL_MODE_NORMAL:
		return state.NodeModeNormal, true
	case pb.NodeOperationalMode_NODE_OPERATIONAL_MODE_DRAINING:
		return state.NodeModeDraining, true
	case pb.NodeOperationalMode_NODE_OPERATIONAL_MODE_MAINTENANCE:
		return state.NodeModeMaintenance, true
	default:
		return "", false
	}
}

// resolveNodeUUID resolves a node's logical name (e.g., "edge-node-1") to its database UUID.
// Uses a local cache to avoid repeated Quartermaster lookups (node IDs rarely change).
// Returns empty string if lookup fails or Quartermaster is unavailable.
func (p *Processor) resolveNodeUUID(nodeID string) string {
	if nodeID == "" {
		return ""
	}

	if p.nodeUUIDCache == nil {
		return ""
	}

	// Lookup from Quartermaster if client available
	if p.quartermasterClient == nil {
		return ""
	}

	val, ok, _ := p.nodeUUIDCache.Get(context.Background(), nodeID, func(ctx context.Context, key string) (interface{}, bool, error) {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()

		node, err := p.quartermasterClient.GetNodeByLogicalName(ctx, key)
		if err != nil {
			p.logger.WithFields(logging.Fields{
				"node_id": key,
				"error":   err,
			}).Debug("Failed to resolve node UUID from Quartermaster")
			return nil, false, err
		}

		if node == nil || node.GetId() == "" {
			return nil, false, fmt.Errorf("node not found")
		}

		return node.GetId(), true, nil
	})
	if !ok {
		return ""
	}
	if uuid, ok := val.(string); ok {
		return uuid
	}
	return ""
}

// GenerateAndSendStorageSnapshots generates and sends an hourly storage snapshot to Decklog
func (p *Processor) GenerateAndSendStorageSnapshots() error {
	p.logger.Info("Starting GenerateAndSendStorageSnapshots")
	ctx := context.Background()
	snapshot := state.DefaultManager().GetBalancerSnapshotAtomic()
	if snapshot == nil {
		p.logger.Warn("Balancer snapshot is empty, skipping storage snapshot generation")
		return nil
	}

	for _, nodeSnap := range snapshot.Nodes {
		// Skip non-storage nodes or unhealthy nodes
		if !nodeSnap.CapStorage || !nodeSnap.IsActive {
			continue
		}

		// Get full node state to access artifacts
		nodeState := state.DefaultManager().GetNodeState(nodeSnap.NodeID)
		if nodeState == nil {
			continue
		}

		// Node's tenant_id and location from its own state
		nodeOwnerTenantID := ""
		if t := nodeState.TenantID; t != "" {
			nodeOwnerTenantID = t
		}
		nodeLocation := nodeState.Location
		nodeCapabilities := &pb.NodeCapabilities{
			Ingest:     nodeState.CapIngest,
			Edge:       nodeState.CapEdge,
			Storage:    nodeState.CapStorage,
			Processing: nodeState.CapProcessing,
			Roles:      nodeState.Roles,
		}

		// Map to store aggregated usage per tenant for this node
		tenantUsageMap := make(map[string]*pb.TenantStorageUsage)

		// Iterate through artifacts to sum up usage per tenant
		for _, artifact := range nodeState.Artifacts {
			var tenantID string
			var contentType string

			// Resolve tenant and content type from artifact hash using unified resolver
			if target, err := control.ResolveArtifactByHash(ctx, artifact.GetClipHash()); err == nil {
				tenantID = target.TenantID
				contentType = target.ContentType
			} else {
				p.logger.WithError(err).WithField("clip_hash", artifact.GetClipHash()).Warn("Failed to resolve tenant for artifact, skipping")
				continue
			}

			if tenantID == "" {
				// Fallback: If artifact is on a dedicated node, use node's tenant ID
				if nodeOwnerTenantID != "" {
					tenantID = nodeOwnerTenantID
				} else {
					continue
				}
			}

			usage := tenantUsageMap[tenantID]
			if usage == nil {
				usage = &pb.TenantStorageUsage{TenantId: tenantID}
				tenantUsageMap[tenantID] = usage
			}

			usage.TotalBytes += artifact.GetSizeBytes()
			usage.FileCount++

			// Categorize by content type (resolved from DB)
			switch contentType {
			case "clip":
				usage.ClipBytes += artifact.GetSizeBytes()
			case "dvr":
				usage.DvrBytes += artifact.GetSizeBytes()
			default:
				// Unknown content type - count towards clips as fallback
				usage.ClipBytes += artifact.GetSizeBytes()
			}
			// VodBytes: Reserved for user-uploaded video artifacts (not yet implemented)
		}

		snapshotTenantID := nodeOwnerTenantID
		if snapshotTenantID == "" && p.ownerTenantID != "" {
			snapshotTenantID = p.ownerTenantID
		}

		// Construct the StorageSnapshot message
		var tenantUsages []*pb.TenantStorageUsage
		for _, tu := range tenantUsageMap {
			tenantUsages = append(tenantUsages, tu)
		}

		storageSnapshot := &pb.StorageSnapshot{
			NodeId:       nodeSnap.NodeID,
			Timestamp:    time.Now().Unix(),
			TenantId:     func() *string { s := snapshotTenantID; return &s }(),
			Location:     func() *string { s := nodeLocation; return &s }(),
			Capabilities: nodeCapabilities,
			Usage:        tenantUsages,
			StorageScope: stringPtr("hot"),
		}

		// Send to Decklog
		trigger := &pb.MistTrigger{
			TriggerType: "STORAGE_SNAPSHOT",
			NodeId:      nodeSnap.NodeID,
			Timestamp:   time.Now().Unix(),
			TenantId:    func() *string { s := snapshotTenantID; return &s }(),
			TriggerPayload: &pb.MistTrigger_StorageSnapshot{
				StorageSnapshot: storageSnapshot,
			},
		}

		if err := p.sendTriggerToDecklog(trigger); err != nil {
			p.logger.WithError(err).WithField("node_id", nodeSnap.NodeID).Error("Failed to send StorageSnapshot to Decklog")
		} else {
			p.logger.WithField("node_id", nodeSnap.NodeID).Info("Successfully sent StorageSnapshot to Decklog")
		}
	}

	// Emit a cold-storage snapshot (S3 authoritative) aggregated across artifacts table.
	coldUsageMap, err := control.GetColdStorageUsage(context.Background())
	if err != nil {
		p.logger.WithError(err).Warn("Failed to compute cold storage usage")
		return nil
	}
	if len(coldUsageMap) == 0 {
		return nil
	}

	var coldUsages []*pb.TenantStorageUsage
	for _, usage := range coldUsageMap {
		totalBytes := usage.DvrBytes + usage.ClipBytes + usage.VodBytes
		coldUsages = append(coldUsages, &pb.TenantStorageUsage{
			TenantId:        usage.TenantID,
			TotalBytes:      totalBytes,
			FileCount:       usage.FileCount,
			DvrBytes:        usage.DvrBytes,
			ClipBytes:       usage.ClipBytes,
			VodBytes:        usage.VodBytes,
			FrozenDvrBytes:  usage.DvrBytes,
			FrozenClipBytes: usage.ClipBytes,
			FrozenVodBytes:  usage.VodBytes,
		})
	}

	coldTenantID := p.ownerTenantID
	coldSnapshot := &pb.StorageSnapshot{
		NodeId:       "s3",
		Timestamp:    time.Now().Unix(),
		TenantId:     func() *string { s := coldTenantID; return &s }(),
		Usage:        coldUsages,
		StorageScope: stringPtr("cold"),
	}

	coldTrigger := &pb.MistTrigger{
		TriggerType: "STORAGE_SNAPSHOT",
		NodeId:      "s3",
		Timestamp:   time.Now().Unix(),
		TenantId:    func() *string { s := coldTenantID; return &s }(),
		TriggerPayload: &pb.MistTrigger_StorageSnapshot{
			StorageSnapshot: coldSnapshot,
		},
	}

	if err := p.sendTriggerToDecklog(coldTrigger); err != nil {
		p.logger.WithError(err).Warn("Failed to send cold storage snapshot to Decklog")
	} else {
		p.logger.Info("Successfully sent cold storage snapshot to Decklog")
	}
	return nil
}

func stringPtr(s string) *string {
	return &s
}

func (p *Processor) resolveStreamContext(ctx context.Context, key, tenantIDHint string, allowCache bool) (streamContext, bool, error) {
	// For artifact hashes (VOD playback), check in-memory state first.
	// This avoids Commodore calls for artifacts we already know about.
	if tenantIDHint != "" && p.streamCache != nil {
		_, artifactInfo := state.DefaultManager().FindNodeByArtifactHash(key)
		if artifactInfo != nil && artifactInfo.GetStreamName() != "" {
			parentInternal := mist.ExtractInternalName(artifactInfo.GetStreamName())
			cacheKey := tenantIDHint + ":" + parentInternal
			if v, ok := p.streamCache.Peek(cacheKey); ok {
				if parentInfo, ok := v.(streamContext); ok && parentInfo.TenantID != "" {
					info := streamContext{
						TenantID:  parentInfo.TenantID,
						UserID:    parentInfo.UserID,
						StreamID:  parentInfo.StreamID,
						Source:    "artifact_parent_cache",
						UpdatedAt: time.Now(),
					}
					p.streamCacheMetaMu.Lock()
					p.streamCacheLastAt = info.UpdatedAt
					p.streamCacheLastErr = ""
					p.streamCacheMetaMu.Unlock()
					return info, true, nil
				}
			}
		}
	}

	// Fallback: call Commodore's unified resolver (single call checks all registries)
	if p.commodoreClient == nil {
		err := fmt.Errorf("commodore client not configured")
		atomic.AddUint64(&p.streamCacheResErr, 1)
		p.streamCacheMetaMu.Lock()
		p.streamCacheLastAt = time.Now()
		p.streamCacheLastErr = err.Error()
		p.streamCacheMetaMu.Unlock()
		return streamContext{}, false, err
	}

	resp, err := p.commodoreClient.ResolveIdentifier(ctx, key)
	if err != nil {
		atomic.AddUint64(&p.streamCacheResErr, 1)
		p.streamCacheMetaMu.Lock()
		p.streamCacheLastAt = time.Now()
		p.streamCacheLastErr = err.Error()
		p.streamCacheMetaMu.Unlock()
		p.logger.WithFields(logging.Fields{
			"identifier": key,
			"error":      err,
		}).Warn("Failed to resolve identifier from Commodore")
		return streamContext{}, false, err
	}

	if !resp.GetFound() {
		atomic.AddUint64(&p.streamCacheResErr, 1)
		p.streamCacheMetaMu.Lock()
		p.streamCacheLastAt = time.Now()
		p.streamCacheLastErr = "not found"
		p.streamCacheMetaMu.Unlock()
		p.logger.WithFields(logging.Fields{
			"identifier": key,
		}).Warn("Identifier not found in any Commodore registry")
		return streamContext{}, false, fmt.Errorf("identifier not found")
	}

	// Cache the result
	now := time.Now()
	info := streamContext{
		TenantID:  resp.GetTenantId(),
		UserID:    resp.GetUserId(),
		StreamID:  resp.GetStreamId(),
		Source:    "resolve_" + resp.GetIdentifierType(),
		UpdatedAt: now,
	}

	if resp.GetIdentifierType() == "playback_id" {
		atomic.AddUint64(&p.streamCacheResPb, 1)
	} else {
		atomic.AddUint64(&p.streamCacheResInt, 1)
	}

	p.streamCacheMetaMu.Lock()
	p.streamCacheLastAt = now
	p.streamCacheLastErr = ""
	p.streamCacheMetaMu.Unlock()

	// If this was a playback_id, also cache by the canonical internal_name
	if allowCache && resp.GetIdentifierType() == "playback_id" && resp.GetInternalName() != "" && p.streamCache != nil && resp.GetTenantId() != "" {
		cacheKey := resp.GetTenantId() + ":" + resp.GetInternalName()
		p.streamCache.Set(cacheKey, info, 10*time.Minute)
	}

	p.logger.WithFields(logging.Fields{
		"identifier":      key,
		"identifier_type": resp.GetIdentifierType(),
		"tenant_id":       info.TenantID,
	}).Debug("Resolved identifier from Commodore")

	return info, true, nil
}

// getStreamContext gets tenant and user IDs from cache, with fallback to Commodore
func (p *Processor) getStreamContext(ctx context.Context, streamName, tenantIDHint string) streamContext {
	if streamName == "" {
		return streamContext{}
	}

	internalName := mist.ExtractInternalName(streamName)
	if p.streamCache == nil || tenantIDHint == "" {
		info, ok, _ := p.resolveStreamContext(ctx, internalName, tenantIDHint, false)
		if !ok {
			return streamContext{}
		}
		return info
	}

	cacheKey := tenantIDHint + ":" + internalName
	val, ok, _ := p.streamCache.Get(ctx, cacheKey, func(ctx context.Context, _ string) (interface{}, bool, error) {
		return p.resolveStreamContext(ctx, internalName, tenantIDHint, true)
	})

	if !ok {
		return streamContext{}
	}
	if info, ok := val.(streamContext); ok {
		return info
	}
	return streamContext{}
}

// applyStreamContext enriches trigger with tenant/user/stream IDs if available.
func (p *Processor) applyStreamContext(trigger *pb.MistTrigger, streamName string) streamContext {
	tenantHint := ""
	if trigger != nil && trigger.TenantId != nil {
		tenantHint = *trigger.TenantId
	}
	info := p.getStreamContext(context.Background(), streamName, tenantHint)
	if trigger == nil {
		return info
	}
	if info.TenantID != "" && (trigger.TenantId == nil || *trigger.TenantId == "") {
		trigger.TenantId = &info.TenantID
	}
	if info.UserID != "" && (trigger.UserId == nil || *trigger.UserId == "") {
		trigger.UserId = &info.UserID
	}
	if info.StreamID != "" && (trigger.StreamId == nil || *trigger.StreamId == "") {
		trigger.StreamId = &info.StreamID
	}
	return info
}

// detectProtocol extracts protocol from push URL
func (p *Processor) detectProtocol(pushURL string) string {
	if pushURL == "" {
		return ""
	}

	if strings.HasPrefix(pushURL, "rtmp://") {
		return "rtmp"
	} else if strings.HasPrefix(pushURL, "srt://") {
		return "srt"
	} else if strings.HasPrefix(pushURL, "whip://") {
		return "whip"
	} else if strings.HasPrefix(pushURL, "http://") || strings.HasPrefix(pushURL, "https://") {
		return "http"
	}

	return ""
}

// NodeConfig represents node configuration including geographic data
type NodeConfig struct {
	Latitude  float64
	Longitude float64
	Location  string
}

// getNodeConfig returns node configuration including geographic data
func (p *Processor) getNodeConfig(nodeID string) *NodeConfig {
	// Get node state directly from unified state manager
	nodeState := state.DefaultManager().GetNodeState(nodeID)
	if nodeState == nil {
		return nil
	}

	config := &NodeConfig{
		Location: nodeState.Location,
	}

	// Handle pointer types for latitude/longitude
	if nodeState.Latitude != nil {
		config.Latitude = *nodeState.Latitude
	}
	if nodeState.Longitude != nil {
		config.Longitude = *nodeState.Longitude
	}

	return config
}
