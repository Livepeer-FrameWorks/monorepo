package middleware

import (
	"sync"
	"sync/atomic"
	"time"

	"frameworks/pkg/clients/decklog"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

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
}

// NewUsageTracker creates a new usage tracker
func NewUsageTracker(config UsageTrackerConfig) *UsageTracker {
	if config.FlushInterval <= 0 {
		config.FlushInterval = 30 * time.Second
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
	// Collect all aggregates
	var aggregates []*pb.APIRequestAggregate

	ut.aggregates.Range(func(keyI, valueI interface{}) bool {
		key := keyI.(aggregateKey)
		agg := valueI.(*aggregate)

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
		}

		// Reset counters
		agg.RequestCount = 0
		agg.ErrorCount = 0
		agg.TotalDurationMs = 0
		agg.TotalComplexity = 0
		agg.UserHashes = nil
		agg.TokenHashes = nil
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
		var tenantID string
		if v := ut.serviceTenantID.Load(); v != nil {
			if s, ok := v.(string); ok {
				tenantID = s
			}
		}
		event := &pb.ServiceEvent{
			EventType: "api_request_batch",
			Timestamp: timestamppb.New(time.Unix(batch.GetTimestamp(), 0)),
			Source:    "bridge",
			TenantId:  tenantID,
			Payload:   &pb.ServiceEvent_ApiRequestBatch{ApiRequestBatch: batch},
		}

		if err := ut.config.Decklog.SendServiceEvent(event); err != nil {
			if ut.config.Logger != nil {
				ut.config.Logger.WithFields(logging.Fields{
					"aggregate_count": len(aggregates),
					"error":           err,
				}).Error("Failed to flush API usage batch to Decklog (service_events)")
			}
		} else {
			if ut.config.Logger != nil {
				ut.config.Logger.WithFields(logging.Fields{
					"aggregate_count": len(aggregates),
				}).Debug("Flushed API usage batch to Decklog (service_events)")
			}
		}
	}
}

// Record records a single API request
func (ut *UsageTracker) Record(tenantID, authType, opType, opName, userID string, tokenHash uint64, durationMs uint64, complexity uint32, errorCount uint32) {
	key := aggregateKey{
		TenantID:      tenantID,
		AuthType:      authType,
		OperationType: opType,
		OperationName: opName,
	}

	// Get or create aggregate
	aggI, _ := ut.aggregates.LoadOrStore(key, &aggregate{})
	agg := aggI.(*aggregate)

	agg.mu.Lock()
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
			tenantID = "anonymous"
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

		if opCtx := graphql.GetOperationContext(c.Request.Context()); opCtx != nil {
			if opCtx.Operation != nil {
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
		tracker.Record(tenantID, authType, opType, opName, userID, tokenHash, uint64(duration), complexity, errorCount)
	}
}
