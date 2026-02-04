package middleware

import (
	"sync"
	"sync/atomic"
	"time"

	"frameworks/pkg/clients/decklog"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"
	"frameworks/pkg/tenants"

	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/gin-gonic/gin"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// UsageTrackerConfig configures the usage tracker
type UsageTrackerConfig struct {
	// Decklog client for sending batches
	Decklog *decklog.BatchedClient
	// Logger for usage tracking events
	Logger logging.Logger
	// FlushInterval is how often to flush aggregates (default: 30 seconds)
	FlushInterval time.Duration
	// RetryLimit is how many times to retry a failed batch before dropping (default: 3).
	RetryLimit int
	// SourceNode identifies this Gateway instance
	SourceNode string
	// ServiceTenantID is the owning tenant for this service's cluster
	ServiceTenantID string
}

// UsageTracker aggregates API request metrics and periodically flushes to Decklog
type UsageTracker struct {
	config          UsageTrackerConfig
	aggregates      sync.Map // map[aggregateKey]*aggregate
	stopCh          chan struct{}
	wg              sync.WaitGroup
	serviceTenantID atomic.Value
	failedMu        sync.Mutex
	failedBatches   []failedBatch
}

type failedBatch struct {
	event          *pb.ServiceEvent
	aggregateCount int
	attempts       int
}

// aggregateKey uniquely identifies an aggregate bucket
type aggregateKey struct {
	TenantID      string
	AuthType      string
	OperationType string
	OperationName string
}

// aggregate holds the accumulated metrics for a bucket
type aggregate struct {
	mu              sync.Mutex
	RequestCount    uint32
	ErrorCount      uint32
	TotalDurationMs uint64
	TotalComplexity uint32
	UserHashes      map[uint64]struct{}
	TokenHashes     map[uint64]struct{}
	FirstSeenAt     int64
}

// NewUsageTracker creates a new usage tracker
func NewUsageTracker(config UsageTrackerConfig) *UsageTracker {
	if config.FlushInterval <= 0 {
		config.FlushInterval = 30 * time.Second
	}
	if config.RetryLimit <= 0 {
		config.RetryLimit = 3
	}
	if config.SourceNode == "" {
		config.SourceNode = "bridge-unknown"
	}

	ut := &UsageTracker{
		config: config,
		stopCh: make(chan struct{}),
	}
	if config.ServiceTenantID != "" {
		ut.serviceTenantID.Store(config.ServiceTenantID)
	}

	// Start the flush goroutine
	ut.wg.Add(1)
	go ut.flushLoop()

	if config.Logger != nil {
		config.Logger.WithFields(logging.Fields{
			"flush_interval": config.FlushInterval,
			"retry_limit":    config.RetryLimit,
			"source_node":    config.SourceNode,
		}).Info("Usage tracker started")
	}

	return ut
}

// SetServiceTenantID updates the owning tenant ID for emitted service events.
func (ut *UsageTracker) SetServiceTenantID(tenantID string) {
	if tenantID != "" {
		ut.serviceTenantID.Store(tenantID)
	}
}

// Stop gracefully shuts down the usage tracker
func (ut *UsageTracker) Stop() {
	close(ut.stopCh)
	ut.wg.Wait()

	// Final flush
	ut.flush()

	if ut.config.Logger != nil {
		ut.config.Logger.Info("Usage tracker stopped")
	}
}

// flushLoop periodically flushes aggregates to Decklog
func (ut *UsageTracker) flushLoop() {
	defer ut.wg.Done()

	ticker := time.NewTicker(ut.config.FlushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			ut.flush()
		case <-ut.stopCh:
			return
		}
	}
}

// flush sends all aggregates to Decklog and resets counters
func (ut *UsageTracker) flush() {
	// Skip flush if service tenant ID not yet set (Quartermaster bootstrap may still be in progress)
	if ut.config.Decklog != nil {
		v := ut.serviceTenantID.Load()
		tenantID, _ := v.(string)
		if tenantID == "" {
			if ut.config.Logger != nil {
				ut.config.Logger.Debug("Skipping usage flush: service tenant ID not yet set")
			}
			return
		}
	}

	ut.retryFailedBatches()

	// Collect all aggregates
	var aggregates []*pb.APIRequestAggregate

	ut.aggregates.Range(func(keyI, valueI interface{}) bool {
		key := keyI.(aggregateKey) //nolint:errcheck // type guaranteed by sync.Map usage
		agg := valueI.(*aggregate) //nolint:errcheck // type guaranteed by sync.Map usage

		// Lock and extract values, then reset
		agg.mu.Lock()
		if agg.RequestCount == 0 {
			agg.mu.Unlock()
			return true // Skip empty buckets
		}

		userHashes := make([]uint64, 0, len(agg.UserHashes))
		for h := range agg.UserHashes {
			userHashes = append(userHashes, h)
		}
		tokenHashes := make([]uint64, 0, len(agg.TokenHashes))
		for h := range agg.TokenHashes {
			tokenHashes = append(tokenHashes, h)
		}

		protoAgg := &pb.APIRequestAggregate{
			TenantId:        key.TenantID,
			AuthType:        key.AuthType,
			OperationType:   key.OperationType,
			OperationName:   key.OperationName,
			RequestCount:    agg.RequestCount,
			ErrorCount:      agg.ErrorCount,
			TotalDurationMs: agg.TotalDurationMs,
			TotalComplexity: agg.TotalComplexity,
			UserHashes:      userHashes,
			TokenHashes:     tokenHashes,
			Timestamp:       agg.FirstSeenAt,
		}

		// Reset counters after snapshotting so a failed send can be retried.
		agg.RequestCount = 0
		agg.ErrorCount = 0
		agg.TotalDurationMs = 0
		agg.TotalComplexity = 0
		agg.UserHashes = nil
		agg.TokenHashes = nil
		agg.FirstSeenAt = 0
		agg.mu.Unlock()

		aggregates = append(aggregates, protoAgg)
		return true
	})

	if len(aggregates) == 0 {
		return // Nothing to flush
	}

	// Build batch payload
	batch := &pb.APIRequestBatch{
		Timestamp:  time.Now().Unix(),
		SourceNode: ut.config.SourceNode,
		Aggregates: aggregates,
	}

	if ut.config.Decklog != nil {
		tenantID := ""
		if v := ut.serviceTenantID.Load(); v != nil {
			if s, ok := v.(string); ok && s != "" {
				tenantID = s
			}
		}
		if tenantID == "" {
			tenantID = tenants.SystemTenantID.String()
			if ut.config.Logger != nil {
				ut.config.Logger.WithField("tenant_id", tenantID).
					Debug("Usage tracker using system tenant for service event batch")
			}
		}
		event := &pb.ServiceEvent{
			EventType: "api_request_batch",
			Timestamp: timestamppb.New(time.Unix(batch.GetTimestamp(), 0)),
			Source:    "bridge",
			TenantId:  tenantID,
			Payload:   &pb.ServiceEvent_ApiRequestBatch{ApiRequestBatch: batch},
		}

		if err := ut.sendServiceEvent(event, len(aggregates)); err != nil {
			ut.enqueueFailedBatch(event, len(aggregates))
		}
	}
}

func (ut *UsageTracker) enqueueFailedBatch(event *pb.ServiceEvent, aggregateCount int) {
	if ut.config.Decklog == nil || event == nil {
		return
	}

	ut.failedMu.Lock()
	ut.failedBatches = append(ut.failedBatches, failedBatch{
		event:          event,
		aggregateCount: aggregateCount,
		// attempts counts retry attempts already performed (not the initial send)
		attempts: 0,
	})
	ut.failedMu.Unlock()
}

func (ut *UsageTracker) retryFailedBatches() {
	if ut.config.Decklog == nil {
		return
	}

	ut.failedMu.Lock()
	pending := ut.failedBatches
	ut.failedBatches = nil
	ut.failedMu.Unlock()

	if len(pending) == 0 {
		return
	}

	var remaining []failedBatch
	for _, batch := range pending {
		if err := ut.sendServiceEvent(batch.event, batch.aggregateCount); err != nil {
			batch.attempts++
			if batch.attempts <= ut.config.RetryLimit {
				remaining = append(remaining, batch)
				continue
			}
			if ut.config.Logger != nil {
				ut.config.Logger.WithFields(logging.Fields{
					"aggregate_count": batch.aggregateCount,
					"attempts":        batch.attempts,
				}).Error("Dropping API usage batch after retries")
			}
		}
	}

	if len(remaining) > 0 {
		ut.failedMu.Lock()
		ut.failedBatches = append(ut.failedBatches, remaining...)
		ut.failedMu.Unlock()
	}
}

func (ut *UsageTracker) sendServiceEvent(event *pb.ServiceEvent, aggregateCount int) error {
	if ut.config.Decklog == nil || event == nil {
		return nil
	}

	if err := ut.config.Decklog.SendServiceEvent(event); err != nil {
		if ut.config.Logger != nil {
			ut.config.Logger.WithFields(logging.Fields{
				"aggregate_count": aggregateCount,
				"error":           err,
			}).Error("Failed to flush API usage batch to Decklog (service_events)")
		}
		return err
	}

	if ut.config.Logger != nil {
		ut.config.Logger.WithFields(logging.Fields{
			"aggregate_count": aggregateCount,
		}).Debug("Flushed API usage batch to Decklog (service_events)")
	}

	return nil
}

// Record records a single API request
func (ut *UsageTracker) Record(startedAt time.Time, tenantID, authType, opType, opName, userID string, tokenHash uint64, durationMs uint64, complexity uint32, errorCount uint32) {
	key := aggregateKey{
		TenantID:      tenantID,
		AuthType:      authType,
		OperationType: opType,
		OperationName: opName,
	}

	// Get or create aggregate
	aggI, _ := ut.aggregates.LoadOrStore(key, &aggregate{})
	agg := aggI.(*aggregate) //nolint:errcheck // type guaranteed by sync.Map usage

	agg.mu.Lock()
	if agg.RequestCount == 0 {
		agg.FirstSeenAt = startedAt.Unix()
	}
	agg.RequestCount++
	agg.TotalDurationMs += durationMs
	agg.TotalComplexity += complexity
	if errorCount > 0 {
		agg.ErrorCount += errorCount
	}
	if userID != "" {
		userHash := hashIdentifier(userID)
		if userHash != 0 {
			if agg.UserHashes == nil {
				agg.UserHashes = make(map[uint64]struct{})
			}
			agg.UserHashes[userHash] = struct{}{}
		}
	}
	if tokenHash != 0 {
		if agg.TokenHashes == nil {
			agg.TokenHashes = make(map[uint64]struct{})
		}
		agg.TokenHashes[tokenHash] = struct{}{}
	}
	agg.mu.Unlock()
}

// UsageTrackerMiddleware creates a Gin middleware that records API requests
// This should be placed AFTER auth middleware (needs tenant_id) and AFTER GraphQL processing
func UsageTrackerMiddleware(tracker *UsageTracker) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		// Process request
		c.Next()

		// Skip tracking for demo mode requests (no useful observability data)
		if IsDemoMode(c.Request.Context()) {
			return
		}

		// Extract metrics after request completes
		duration := time.Since(start).Milliseconds()

		// Get tenant ID from context
		tenantID := ""
		if tid, exists := c.Get("tenant_id"); exists {
			if s, ok := tid.(string); ok {
				tenantID = s
			}
		}
		if tenantID == "" {
			tenantID = tenants.AnonymousTenantID.String()
		}

		// Determine auth type
		authType := "anonymous"
		if v, ok := c.Get("auth_type"); ok {
			if s, ok := v.(string); ok && s != "" {
				authType = s
			}
		} else if _, exists := c.Get("jwt_token"); exists {
			authType = "jwt"
		} else if _, exists := c.Get("api_token"); exists {
			authType = "api_token"
		} else if _, exists := c.Get("wallet_address"); exists {
			authType = "wallet"
		}

		userID := ""
		if v, ok := c.Get("user_id"); ok {
			if s, ok := v.(string); ok {
				userID = s
			}
		}
		var tokenHash uint64
		if v, ok := c.Get("api_token_hash"); ok {
			switch t := v.(type) {
			case uint64:
				tokenHash = t
			case uint32:
				tokenHash = uint64(t)
			case int64:
				if t > 0 {
					tokenHash = uint64(t)
				}
			case int:
				if t > 0 {
					tokenHash = uint64(t)
				}
			}
		}

		// Get GraphQL operation info from Gin context (set in gqlgen hooks), fallback to gqlgen context.
		opType := "unknown"
		opName := ""
		complexity := uint32(0)

		if v, ok := c.Get("graphql_operation_type"); ok {
			if s, ok := v.(string); ok && s != "" {
				opType = s
			}
		}
		if v, ok := c.Get("graphql_operation_name"); ok {
			if s, ok := v.(string); ok {
				opName = s
			}
		}

		if graphql.HasOperationContext(c.Request.Context()) {
			if opCtx := graphql.GetOperationContext(c.Request.Context()); opCtx.Operation != nil {
				if opType == "unknown" {
					opType = string(opCtx.Operation.Operation)
				}
				if opName == "" {
					opName = opCtx.Operation.Name
				}
			}
		}

		if v, ok := c.Get("graphql_complexity"); ok {
			switch t := v.(type) {
			case int:
				if t > 0 {
					complexity = uint32(t)
				}
			case int32:
				if t > 0 {
					complexity = uint32(t)
				}
			case int64:
				if t > 0 {
					complexity = uint32(t)
				}
			case uint32:
				complexity = t
			case uint64:
				if t > 0 {
					complexity = uint32(t)
				}
			}
		} else if stats := extension.GetComplexityStats(c.Request.Context()); stats != nil {
			complexity = uint32(stats.Complexity)
		}

		var errorCount uint32
		if v, ok := c.Get("graphql_error_count"); ok {
			switch t := v.(type) {
			case int:
				if t > 0 {
					errorCount = uint32(t)
				}
			case int32:
				if t > 0 {
					errorCount = uint32(t)
				}
			case int64:
				if t > 0 {
					errorCount = uint32(t)
				}
			case uint32:
				errorCount = t
			case uint64:
				if t > 0 {
					errorCount = uint32(t)
				}
			}
		}
		if errorCount == 0 && (len(c.Errors) > 0 || c.Writer.Status() >= 400) {
			errorCount = 1
		}

		// Record the request
		tracker.Record(start, tenantID, authType, opType, opName, userID, tokenHash, uint64(duration), complexity, errorCount)
	}
}
