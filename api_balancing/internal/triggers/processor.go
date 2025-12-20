package triggers

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/geo"
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
	TenantID  string
	UserID    string
	Source    string
	UpdatedAt time.Time
	LastError string
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

	streamCache        map[string]streamContext // Cache stream context (tenant + user)
	streamCacheMu      sync.RWMutex
	streamCacheHits    uint64
	streamCacheMisses  uint64
	streamCacheResInt  uint64
	streamCacheResPb   uint64
	streamCacheResErr  uint64
	streamCacheLastAt  time.Time
	streamCacheLastErr string

	nodeUUIDCache   map[string]string // Cache node_id (logical) -> UUID
	nodeUUIDCacheMu sync.RWMutex
}

// hash generates a simple hash for string input
func hash(s string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(s))
	return h.Sum32()
}

// NewProcessor creates a new MistServer trigger processor
func NewProcessor(logger logging.Logger, commodoreClient *commodore.GRPCClient, decklogClient *decklog.BatchedClient, loadBalancer *balancer.LoadBalancer, geoipClient *geoip.Reader) *Processor {
	return &Processor{
		logger:          logger,
		commodoreClient: commodoreClient,
		decklogClient:   decklogClient,
		loadBalancer:    loadBalancer,
		geoipClient:     geoipClient,
		nodeID:          os.Getenv("NODE_ID"),
		region:          os.Getenv("REGION"),
		streamCache:     make(map[string]streamContext),
		nodeUUIDCache:   make(map[string]string),
	}
}

// StreamContextCacheEntry is a single cached mapping used for tenant/user enrichment.
type StreamContextCacheEntry struct {
	Key       string    `json:"key"`
	TenantID  string    `json:"tenant_id"`
	UserID    string    `json:"user_id"`
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
	p.streamCacheMu.RLock()
	entries := make([]StreamContextCacheEntry, 0, len(p.streamCache))
	for k, v := range p.streamCache {
		entries = append(entries, StreamContextCacheEntry{
			Key:       k,
			TenantID:  v.TenantID,
			UserID:    v.UserID,
			Source:    v.Source,
			UpdatedAt: v.UpdatedAt,
			LastError: v.LastError,
		})
	}
	lastAt := p.streamCacheLastAt
	lastErr := p.streamCacheLastErr
	p.streamCacheMu.RUnlock()

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

func (p *Processor) sendTriggerToDecklog(trigger *pb.MistTrigger) error {
	if trigger == nil {
		return fmt.Errorf("nil trigger")
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
		ctx := context.Background()
		info := p.getStreamContext(ctx, internalName)
		if info.TenantID != "" {
			trigger.TenantId = &info.TenantID
			pbill.TenantId = &info.TenantID
		}
		if info.UserID != "" {
			trigger.UserId = &info.UserID
		}
	} else if *pbill.TenantId != "" {
		trigger.TenantId = pbill.TenantId
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
		return "", true, err
	}

	// Cache stream context (tenant + user)
	p.streamCacheMu.Lock()
	p.streamCache[streamValidation.InternalName] = streamContext{
		TenantID:  streamValidation.TenantId,
		UserID:    streamValidation.UserId,
		Source:    "validate_stream_key",
		UpdatedAt: time.Now(),
	}
	p.streamCacheLastAt = time.Now()
	p.streamCacheLastErr = ""
	p.streamCacheMu.Unlock()
	if streamValidation.TenantId != "" {
		trigger.TenantId = &streamValidation.TenantId
	}
	if streamValidation.UserId != "" {
		trigger.UserId = &streamValidation.UserId
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
	target, _ := control.ResolveStream(context.Background(), playbackID)

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

	// NOTE: play_rewrite events are NOT forwarded to Decklog/ClickHouse.
	// Viewer analytics are already captured via:
	// - connection_events (USER_NEW/USER_END) - actual session start/stop with duration, bytes
	// - routing_events - load balancer decisions with node selection
	// play_rewrite is only needed for its blocking purpose (resolving playback ID to stream name).

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

	// Extract artifact hash - either from vod+ prefix or raw hash (viewer requests)
	var artifactHash string
	if strings.HasPrefix(streamName, "vod+") {
		artifactHash = strings.TrimPrefix(streamName, "vod+")
	} else {
		// Viewers request raw hash, not "vod+hash"
		artifactHash = streamName
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

		// Return file path with shouldAbort=false to tell Helmsman to use this source
		return artifactInfo.GetFilePath(), false, nil
	}

	p.logger.WithFields(logging.Fields{
		"artifact_hash": artifactHash,
		"stream_name":   streamName,
	}).Warn("Artifact not found")

	// Return empty to let MistServer use default source (will fail for VOD)
	return "", true, nil
}

// handlePushEnd processes PUSH_END trigger (non-blocking)
func (p *Processor) handlePushEnd(trigger *pb.MistTrigger) (string, bool, error) {
	pushEnd := trigger.GetTriggerPayload().(*pb.MistTrigger_PushEnd).PushEnd
	internalName := mist.ExtractInternalName(pushEnd.GetStreamName())

	ctx := context.Background()
	info := p.getStreamContext(ctx, internalName)
	if info.TenantID != "" {
		trigger.TenantId = &info.TenantID
	}
	if info.UserID != "" {
		trigger.UserId = &info.UserID
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

	ctx := context.Background()
	info := p.getStreamContext(ctx, internalName)
	if info.TenantID != "" {
		trigger.TenantId = &info.TenantID
	}
	if info.UserID != "" {
		trigger.UserId = &info.UserID
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
	p.logger.WithFields(logging.Fields{
		"session_id":      userNew.GetSessionId(),
		"internal_name":   userNew.GetStreamName(),
		"connection_addr": userNew.GetHost(),
		"node_id":         trigger.GetNodeId(),
	}).Debug("Processing USER_NEW trigger")

	ctx := context.Background()
	info := p.getStreamContext(ctx, userNew.GetStreamName())
	if info.TenantID != "" {
		trigger.TenantId = &info.TenantID
	}
	if info.UserID != "" {
		trigger.UserId = &info.UserID
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
			"internal_name": userNew.GetStreamName(),
			"trigger_type":  trigger.GetTriggerType(),
			"error":         err,
		}).Error("Failed to send user connection trigger to Decklog")
	}

	// Update state (viewer +1) - reuse info from earlier lookup
	// CRITICAL: Extract internal name to avoid creating duplicate state entries
	internalName := mist.ExtractInternalName(userNew.GetStreamName())
	state.DefaultManager().UpdateUserConnection(internalName, trigger.GetNodeId(), info.TenantID, 1)

	// Confirm virtual viewer: transition PENDING -> ACTIVE
	// This decrements PendingRedirects and recalculates AddBandwidth
	clientIP := userNew.GetHost()
	if confirmed := state.DefaultManager().ConfirmVirtualViewer(trigger.GetNodeId(), internalName, clientIP); confirmed {
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

	ctx := context.Background()
	info := p.getStreamContext(ctx, streamBuffer.GetStreamName())
	if info.TenantID != "" {
		trigger.TenantId = &info.TenantID
	}
	if info.UserID != "" {
		trigger.UserId = &info.UserID
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

	ctx := context.Background()
	info := p.getStreamContext(ctx, internalName)
	if info.TenantID != "" {
		trigger.TenantId = &info.TenantID
	}
	if info.UserID != "" {
		trigger.UserId = &info.UserID
	}
	streamEnd.NodeId = &nodeID

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

	ctx := context.Background()
	info := p.getStreamContext(ctx, userEnd.GetStreamName())
	if info.TenantID != "" {
		trigger.TenantId = &info.TenantID
	}
	if info.UserID != "" {
		trigger.UserId = &info.UserID
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
	state.DefaultManager().DisconnectVirtualViewer(trigger.GetNodeId(), internalStreamName, clientIP)

	return "", false, nil
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

	ctx := context.Background()
	info := p.getStreamContext(ctx, internalName)
	if info.TenantID != "" {
		trigger.TenantId = &info.TenantID
	}
	if info.UserID != "" {
		trigger.UserId = &info.UserID
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
	internalName := recordingEnd.GetStreamName()
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

	ctx := context.Background()
	info := p.getStreamContext(ctx, internalName)
	if info.TenantID != "" {
		trigger.TenantId = &info.TenantID
	}
	if info.UserID != "" {
		trigger.UserId = &info.UserID
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
	ctx := context.Background()
	info := p.getStreamContext(ctx, internalName)
	if info.TenantID != "" {
		trigger.TenantId = &info.TenantID
	}
	if info.UserID != "" {
		trigger.UserId = &info.UserID
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
	internal := slu.GetInternalName()
	nodeID := slu.GetNodeId()

	// Enrich tenant context before forwarding (same pattern as handleStreamEnd)
	ctx := context.Background()
	info := p.getStreamContext(ctx, internal)
	if info.TenantID != "" {
		trigger.TenantId = &info.TenantID
		slu.TenantId = &info.TenantID
	}
	if info.UserID != "" {
		trigger.UserId = &info.UserID
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
	_ = p.sendTriggerToDecklog(trigger)

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
	ctx := context.Background()
	info := p.getStreamContext(ctx, internal)
	if info.TenantID != "" {
		trigger.TenantId = &info.TenantID
		clu.TenantId = &info.TenantID
	}
	if info.UserID != "" {
		trigger.UserId = &info.UserID
	}

	// Forward the enriched ClientLifecycleUpdate to Decklog
	_ = p.sendTriggerToDecklog(trigger)
	return "", false, nil
}

// handleNodeLifecycleUpdate processes NODE_LIFECYCLE_UPDATE triggers using protobuf directly
func (p *Processor) handleNodeLifecycleUpdate(trigger *pb.MistTrigger) (string, bool, error) {
	nu := trigger.GetTriggerPayload().(*pb.MistTrigger_NodeLifecycleUpdate).NodeLifecycleUpdate

	p.logger.WithFields(logging.Fields{
		"node_id":  nu.GetNodeId(),
		"bw_limit": nu.GetBwLimit(),
		"ram_max":  nu.GetRamMax(),
		"location": nu.GetLocation(),
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

	// Forward complete node lifecycle event to Decklog using protobuf directly
	// CRITICAL: Strip artifacts before sending to Decklog/Analytics to avoid excessive data
	nu.Artifacts = nil
	_ = p.sendTriggerToDecklog(trigger)

	return "", false, nil
}

// Helper methods

// extractField extracts a field from the raw payload (form data or JSON)
func (p *Processor) extractField(payload []byte, field string) string {
	payloadStr := string(payload)

	// Try URL-encoded format first (form data)
	values, err := url.ParseQuery(payloadStr)
	if err == nil {
		if val := values.Get(field); val != "" {
			return val
		}
	}

	// Try JSON format
	var data map[string]interface{}
	if err := json.Unmarshal(payload, &data); err == nil {
		if val, exists := data[field]; exists {
			return fmt.Sprintf("%v", val)
		}
	}

	return ""
}

// extractIntField extracts an integer field from the raw payload
func (p *Processor) extractIntField(payload []byte, field string) int {
	val := p.extractField(payload, field)
	if val == "" {
		return 0
	}
	intVal, _ := strconv.Atoi(val)
	return intVal
}

// resolveNodeUUID resolves a node's logical name (e.g., "edge-node-1") to its database UUID.
// Uses a local cache to avoid repeated Quartermaster lookups (node IDs rarely change).
// Returns empty string if lookup fails or Quartermaster is unavailable.
func (p *Processor) resolveNodeUUID(nodeID string) string {
	if nodeID == "" {
		return ""
	}

	// Check local cache first
	p.nodeUUIDCacheMu.RLock()
	if uuid, ok := p.nodeUUIDCache[nodeID]; ok {
		p.nodeUUIDCacheMu.RUnlock()
		return uuid
	}
	p.nodeUUIDCacheMu.RUnlock()

	// Lookup from Quartermaster if client available
	if p.quartermasterClient == nil {
		return ""
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	node, err := p.quartermasterClient.GetNodeByLogicalName(ctx, nodeID)
	if err != nil {
		p.logger.WithFields(logging.Fields{
			"node_id": nodeID,
			"error":   err,
		}).Debug("Failed to resolve node UUID from Quartermaster")
		return ""
	}

	if node == nil || node.GetId() == "" {
		return ""
	}

	uuid := node.GetId()

	// Cache the result (node UUID rarely changes)
	p.nodeUUIDCacheMu.Lock()
	p.nodeUUIDCache[nodeID] = uuid
	p.nodeUUIDCacheMu.Unlock()

	return uuid
}

// GenerateAndSendStorageSnapshots generates and sends an hourly storage snapshot to Decklog
func (p *Processor) GenerateAndSendStorageSnapshots() error {
	p.logger.Info("Starting GenerateAndSendStorageSnapshots")
	snapshot := state.DefaultManager().GetBalancerSnapshotAtomic()
	if snapshot == nil {
		p.logger.Warn("Balancer snapshot is empty, skipping storage snapshot generation")
		return nil
	}

	// Map to store aggregated usage per tenant
	tenantUsageMap := make(map[string]*pb.TenantStorageUsage)

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

		// Iterate through artifacts to sum up usage per tenant
		for _, artifact := range nodeState.Artifacts {
			var tenantID string
			var contentType string

			// Resolve tenant and content type from artifact hash using unified resolver
			if target, err := control.ResolveStream(context.Background(), artifact.GetClipHash()); err == nil {
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

		// Construct the StorageSnapshot message
		var tenantUsages []*pb.TenantStorageUsage
		for _, tu := range tenantUsageMap {
			tenantUsages = append(tenantUsages, tu)
		}

		storageSnapshot := &pb.StorageSnapshot{
			NodeId:       nodeSnap.NodeID,
			Timestamp:    time.Now().Unix(),
			TenantId:     func() *string { s := nodeOwnerTenantID; return &s }(),
			Location:     func() *string { s := nodeLocation; return &s }(),
			Capabilities: nodeCapabilities,
			Usage:        tenantUsages,
		}

		// Send to Decklog
		trigger := &pb.MistTrigger{
			TriggerType: "STORAGE_SNAPSHOT",
			NodeId:      nodeSnap.NodeID,
			Timestamp:   time.Now().Unix(),
			TenantId:    func() *string { s := nodeOwnerTenantID; return &s }(),
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
	return nil
}

// extractInt64Field extracts an int64 field from the raw payload
func (p *Processor) extractInt64Field(payload []byte, field string) int64 {
	val := p.extractField(payload, field)
	if val == "" {
		return 0
	}
	int64Val, _ := strconv.ParseInt(val, 10, 64)
	return int64Val
}

// getStreamContext gets tenant and user IDs from cache, with fallback to Commodore
func (p *Processor) getStreamContext(ctx context.Context, streamName string) streamContext {
	if streamName == "" {
		return streamContext{}
	}

	// Extract internal name from MistServer stream name (e.g., "live+demo_stream" -> "demo_stream")
	// This handles both wildcard streams (live+X, vod+X) and plain stream names
	internalName := mist.ExtractInternalName(streamName)

	// Check cache first
	p.streamCacheMu.RLock()
	info, exists := p.streamCache[internalName]
	p.streamCacheMu.RUnlock()
	if exists {
		atomic.AddUint64(&p.streamCacheHits, 1)
		return info
	}
	atomic.AddUint64(&p.streamCacheMisses, 1)

	// For artifact hashes (VOD playback), check in-memory state first.
	// This avoids Commodore calls for artifacts we already know about.
	_, artifactInfo := state.DefaultManager().FindNodeByArtifactHash(internalName)
	if artifactInfo != nil && artifactInfo.GetStreamName() != "" {
		parentStream := artifactInfo.GetStreamName()

		// Check if we have the parent stream's tenant context cached
		p.streamCacheMu.RLock()
		parentInfo, parentExists := p.streamCache[parentStream]
		p.streamCacheMu.RUnlock()

		if parentExists && parentInfo.TenantID != "" {
			// Cache the artifact hash with the parent's tenant context
			now := time.Now()
			info := streamContext{
				TenantID:  parentInfo.TenantID,
				UserID:    parentInfo.UserID,
				Source:    "artifact_parent_cache",
				UpdatedAt: now,
			}
			p.streamCacheMu.Lock()
			p.streamCache[internalName] = info
			p.streamCacheLastAt = now
			p.streamCacheMu.Unlock()
			return info
		}
	}

	// Fallback: call Commodore's unified resolver (single call checks all registries)
	if p.commodoreClient == nil {
		return streamContext{}
	}

	resp, err := p.commodoreClient.ResolveIdentifier(ctx, internalName)
	if err != nil {
		atomic.AddUint64(&p.streamCacheResErr, 1)
		p.streamCacheMu.Lock()
		p.streamCacheLastAt = time.Now()
		p.streamCacheLastErr = err.Error()
		p.streamCacheMu.Unlock()
		p.logger.WithFields(logging.Fields{
			"identifier": internalName,
			"error":      err,
		}).Warn("Failed to resolve identifier from Commodore")
		return streamContext{}
	}

	if !resp.GetFound() {
		atomic.AddUint64(&p.streamCacheResErr, 1)
		p.streamCacheMu.Lock()
		p.streamCacheLastAt = time.Now()
		p.streamCacheLastErr = "not found"
		p.streamCacheMu.Unlock()
		p.logger.WithFields(logging.Fields{
			"identifier": internalName,
		}).Warn("Identifier not found in any Commodore registry")
		return streamContext{}
	}

	// Cache the result
	atomic.AddUint64(&p.streamCacheResInt, 1)
	now := time.Now()
	info = streamContext{
		TenantID:  resp.GetTenantId(),
		UserID:    resp.GetUserId(),
		Source:    "resolve_" + resp.GetIdentifierType(),
		UpdatedAt: now,
	}

	p.streamCacheMu.Lock()
	p.streamCache[internalName] = info
	// If this was a playback_id, also cache by the canonical internal_name
	if resp.GetIdentifierType() == "playback_id" && resp.GetInternalName() != "" {
		p.streamCache[resp.GetInternalName()] = info
	}
	p.streamCacheLastAt = now
	p.streamCacheLastErr = ""
	p.streamCacheMu.Unlock()

	p.logger.WithFields(logging.Fields{
		"identifier":      internalName,
		"identifier_type": resp.GetIdentifierType(),
		"tenant_id":       info.TenantID,
	}).Debug("Resolved identifier from Commodore")

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

// Legacy helper functions - no longer needed with structured protobuf data

// extractStreamHealthMetrics parses MistServer stream details JSON and extracts health metrics
func (p *Processor) extractStreamHealthMetrics(details map[string]interface{}) map[string]interface{} {
	metrics := make(map[string]interface{})
	var tracks []map[string]interface{}

	// Extract issues string if present
	if issues, ok := details["issues"].(string); ok {
		metrics["issues_description"] = issues
		metrics["has_issues"] = true
	} else {
		metrics["has_issues"] = false
	}

	// Process each track to extract codec, quality, and jitter metrics
	for trackID, trackData := range details {
		if trackID == "issues" {
			continue // Skip issues field
		}

		if track, ok := trackData.(map[string]interface{}); ok {
			trackInfo := map[string]interface{}{
				"track_id": trackID,
			}

			// Extract basic track info
			if codec, ok := track["codec"].(string); ok {
				trackInfo["codec"] = codec
			}
			if kbits, ok := track["kbits"].(float64); ok {
				trackInfo["bitrate"] = int(kbits)
			}
			if fpks, ok := track["fpks"].(float64); ok {
				trackInfo["fps"] = fpks / 1000.0 // Convert from fpks to fps
			}
			if height, ok := track["height"].(float64); ok {
				trackInfo["height"] = int(height)
			}
			if width, ok := track["width"].(float64); ok {
				trackInfo["width"] = int(width)
			}
			if channels, ok := track["channels"].(float64); ok {
				trackInfo["channels"] = int(channels)
			}
			if rate, ok := track["rate"].(float64); ok {
				trackInfo["sample_rate"] = int(rate)
			}

			// Extract frame stability metrics from keys
			if keys, ok := track["keys"].(map[string]interface{}); ok {
				if frameMax, ok := keys["frame_ms_max"].(float64); ok {
					trackInfo["frame_ms_max"] = frameMax
				}
				if frameMin, ok := keys["frame_ms_min"].(float64); ok {
					trackInfo["frame_ms_min"] = frameMin
				}
				if framesMax, ok := keys["frames_max"].(float64); ok {
					trackInfo["frames_max"] = int(framesMax)
				}
				if framesMin, ok := keys["frames_min"].(float64); ok {
					trackInfo["frames_min"] = int(framesMin)
				}
				if msMax, ok := keys["ms_max"].(float64); ok {
					trackInfo["keyframe_ms_max"] = msMax
				}
				if msMin, ok := keys["ms_min"].(float64); ok {
					trackInfo["keyframe_ms_min"] = msMin
				}

				// Calculate jitter metrics
				if frameMax, okMax := keys["frame_ms_max"].(float64); okMax {
					if frameMin, okMin := keys["frame_ms_min"].(float64); okMin {
						jitter := frameMax - frameMin
						trackInfo["frame_jitter_ms"] = jitter
					}
				}

				if msMax, okMax := keys["ms_max"].(float64); okMax {
					if msMin, okMin := keys["ms_min"].(float64); okMin {
						keyframeStability := msMax - msMin
						trackInfo["keyframe_stability_ms"] = keyframeStability
					}
				}
			}

			tracks = append(tracks, trackInfo)
		}
	}

	metrics["tracks"] = tracks
	metrics["track_count"] = len(tracks)

	return metrics
}

// extractTrackQualityMetrics parses LIVE_TRACK_LIST JSON and extracts quality metrics
func (p *Processor) extractTrackQualityMetrics(tracks []map[string]interface{}) map[string]interface{} {
	metrics := make(map[string]interface{})
	var videoTracks, audioTracks []map[string]interface{}

	// Process each track in the list
	for i, track := range tracks {
		trackInfo := map[string]interface{}{
			"track_index": i,
		}

		// Extract track ID if present
		if trackID, ok := track["trackid"].(float64); ok {
			trackInfo["track_id"] = int(trackID)
		}

		// Extract track type (video/audio)
		trackType := ""
		if typeVal, ok := track["type"].(string); ok {
			trackType = typeVal
			trackInfo["type"] = typeVal
		}

		// Extract codec
		if codec, ok := track["codec"].(string); ok {
			trackInfo["codec"] = codec
		}

		// Extract video-specific fields
		if width, ok := track["width"].(float64); ok {
			trackInfo["width"] = int(width)
		}
		if height, ok := track["height"].(float64); ok {
			trackInfo["height"] = int(height)
		}
		if fpks, ok := track["fpks"].(float64); ok {
			trackInfo["fps"] = fpks / 1000.0 // Convert from fpks to fps
		}

		// Extract audio-specific fields
		if channels, ok := track["channels"].(float64); ok {
			trackInfo["channels"] = int(channels)
		}
		if rate, ok := track["rate"].(float64); ok {
			trackInfo["sample_rate"] = int(rate)
		}

		// Extract bitrate if present
		if bps, ok := track["bps"].(float64); ok {
			trackInfo["bitrate"] = int(bps)
		}

		// Categorize by type
		if trackType == "video" {
			videoTracks = append(videoTracks, trackInfo)
		} else if trackType == "audio" {
			audioTracks = append(audioTracks, trackInfo)
		}
	}

	metrics["video_tracks"] = videoTracks
	metrics["audio_tracks"] = audioTracks
	metrics["total_tracks"] = len(tracks)
	metrics["video_track_count"] = len(videoTracks)
	metrics["audio_track_count"] = len(audioTracks)

	// Extract primary video quality if available
	if len(videoTracks) > 0 {
		primaryVideo := videoTracks[0]
		if width, ok := primaryVideo["width"].(int); ok {
			metrics["primary_width"] = width
		}
		if height, ok := primaryVideo["height"].(int); ok {
			metrics["primary_height"] = height

			// Calculate quality tier
			if height >= 1080 {
				metrics["quality_tier"] = "1080p+"
			} else if height >= 720 {
				metrics["quality_tier"] = "720p"
			} else if height >= 480 {
				metrics["quality_tier"] = "480p"
			} else {
				metrics["quality_tier"] = "SD"
			}
		}
		if fps, ok := primaryVideo["fps"].(float64); ok {
			metrics["primary_fps"] = fps
		}
		if codec, ok := primaryVideo["codec"].(string); ok {
			metrics["primary_video_codec"] = codec
		}
		if bitrate, ok := primaryVideo["bitrate"].(int); ok {
			metrics["primary_video_bitrate"] = bitrate
		}
	}

	// Extract primary audio info if available
	if len(audioTracks) > 0 {
		primaryAudio := audioTracks[0]
		if channels, ok := primaryAudio["channels"].(int); ok {
			metrics["primary_audio_channels"] = channels
		}
		if sampleRate, ok := primaryAudio["sample_rate"].(int); ok {
			metrics["primary_audio_sample_rate"] = sampleRate
		}
		if codec, ok := primaryAudio["codec"].(string); ok {
			metrics["primary_audio_codec"] = codec
		}
		if bitrate, ok := primaryAudio["bitrate"].(int); ok {
			metrics["primary_audio_bitrate"] = bitrate
		}
	}

	return metrics
}
