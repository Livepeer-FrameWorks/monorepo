package metering

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"frameworks/pkg/logging"
	"frameworks/pkg/models"
)

type contextKey struct{}

type Context struct {
	TenantID string
	UserID   string
	Tracker  *UsageTracker
}

func WithContext(ctx context.Context, meterCtx *Context) context.Context {
	if meterCtx == nil {
		return ctx
	}
	return context.WithValue(ctx, contextKey{}, meterCtx)
}

func FromContext(ctx context.Context) (*Context, bool) {
	if ctx == nil {
		return nil, false
	}
	value := ctx.Value(contextKey{})
	if meterCtx, ok := value.(*Context); ok && meterCtx != nil {
		return meterCtx, true
	}
	return nil, false
}

func RecordLLMUsage(ctx context.Context, inputTokens, outputTokens int) {
	meterCtx, ok := FromContext(ctx)
	if !ok || meterCtx.Tracker == nil || meterCtx.TenantID == "" {
		return
	}
	meterCtx.Tracker.RecordLLMCall(meterCtx.TenantID, inputTokens, outputTokens)
}

func RecordSearchQuery(ctx context.Context) {
	meterCtx, ok := FromContext(ctx)
	if !ok || meterCtx.Tracker == nil || meterCtx.TenantID == "" {
		return
	}
	meterCtx.Tracker.RecordSearchQuery(meterCtx.TenantID)
}

func RecordEmbedding(ctx context.Context) {
	meterCtx, ok := FromContext(ctx)
	if !ok || meterCtx.Tracker == nil || meterCtx.TenantID == "" {
		return
	}
	meterCtx.Tracker.RecordEmbedding(meterCtx.TenantID)
}

type UsageTrackerConfig struct {
	DB            *sql.DB
	Publisher     *Publisher
	Logger        logging.Logger
	Model         string
	ClusterID     string
	FlushInterval time.Duration
}

type UsageTracker struct {
	db            *sql.DB
	publisher     *Publisher
	logger        logging.Logger
	model         string
	clusterID     string
	flushInterval time.Duration
	stopOnce      sync.Once
	stopCh        chan struct{}
	mu            sync.Mutex
	lastFlush     time.Time
	usageByTenant map[string]*tenantUsage
	pendingMu     sync.Mutex
	pending       []models.UsageSummary
}

type tenantUsage struct {
	llmCalls     int
	inputTokens  int
	outputTokens int
	searches     int
	embeddings   int
}

func NewUsageTracker(cfg UsageTrackerConfig) *UsageTracker {
	flushInterval := cfg.FlushInterval
	if flushInterval <= 0 {
		flushInterval = time.Minute
	}
	clusterID := cfg.ClusterID
	if clusterID == "" {
		clusterID = "skipper"
	}
	return &UsageTracker{
		db:            cfg.DB,
		publisher:     cfg.Publisher,
		logger:        cfg.Logger,
		model:         cfg.Model,
		clusterID:     clusterID,
		flushInterval: flushInterval,
		stopCh:        make(chan struct{}),
		lastFlush:     time.Now(),
		usageByTenant: make(map[string]*tenantUsage),
	}
}

func (t *UsageTracker) Start() {
	if t == nil {
		return
	}
	go t.loop()
}

func (t *UsageTracker) Stop() {
	if t == nil {
		return
	}
	t.stopOnce.Do(func() {
		close(t.stopCh)
	})
}

func (t *UsageTracker) RecordLLMCall(tenantID string, inputTokens, outputTokens int) {
	if t == nil || tenantID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	usage := t.ensureTenant(tenantID)
	usage.llmCalls++
	usage.inputTokens += inputTokens
	usage.outputTokens += outputTokens
}

func (t *UsageTracker) RecordSearchQuery(tenantID string) {
	if t == nil || tenantID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	usage := t.ensureTenant(tenantID)
	usage.searches++
}

func (t *UsageTracker) RecordEmbedding(tenantID string) {
	if t == nil || tenantID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	usage := t.ensureTenant(tenantID)
	usage.embeddings++
}

func (t *UsageTracker) loop() {
	ticker := time.NewTicker(t.flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			t.Flush(context.Background())
		case <-t.stopCh:
			t.Flush(context.Background())
			return
		}
	}
}

func (t *UsageTracker) Flush(ctx context.Context) {
	if t == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now()

	t.retryPendingSummaries(ctx)

	t.mu.Lock()
	if len(t.usageByTenant) == 0 {
		t.lastFlush = now
		t.mu.Unlock()
		return
	}
	snapshot := t.usageByTenant
	t.usageByTenant = make(map[string]*tenantUsage)
	windowStart := t.lastFlush
	t.lastFlush = now
	t.mu.Unlock()

	for tenantID, usage := range snapshot {
		t.flushTenant(ctx, tenantID, usage, windowStart, now)
	}
}

func (t *UsageTracker) flushTenant(ctx context.Context, tenantID string, usage *tenantUsage, windowStart, windowEnd time.Time) {
	if tenantID == "" || usage == nil {
		return
	}

	if usage.llmCalls == 0 && usage.searches == 0 && usage.embeddings == 0 {
		return
	}

	if err := t.persistUsage(ctx, tenantID, usage); err != nil {
		t.requeueUsage(tenantID, usage)
		return
	}

	if t.publisher != nil {
		summary := t.buildUsageSummary(tenantID, usage, windowStart, windowEnd)
		if err := t.publisher.PublishUsageSummary(summary); err != nil {
			t.enqueueSummary(summary)
			if t.logger != nil {
				t.logger.WithError(err).WithField("tenant_id", tenantID).Warn("Failed to publish Skipper usage summary")
			}
		}
	}
}

func (t *UsageTracker) persistUsage(ctx context.Context, tenantID string, usage *tenantUsage) error {
	if t.db == nil {
		return nil
	}
	var errs []error
	if usage.llmCalls > 0 {
		if err := t.insertUsageRow(ctx, tenantID, "llm_call", usage.llmCalls, usage.inputTokens, usage.outputTokens, t.model); err != nil {
			errs = append(errs, err)
		}
	}
	if usage.searches > 0 {
		if err := t.insertUsageRow(ctx, tenantID, "search_query", usage.searches, 0, 0, ""); err != nil {
			errs = append(errs, err)
		}
	}
	if usage.embeddings > 0 {
		if err := t.insertUsageRow(ctx, tenantID, "embedding", usage.embeddings, 0, 0, ""); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("persist usage failed with %d error(s)", len(errs))
	}
	return nil
}

func (t *UsageTracker) insertUsageRow(ctx context.Context, tenantID, eventType string, count, inputTokens, outputTokens int, model string) error {
	if count <= 0 {
		return nil
	}
	var modelValue sql.NullString
	if model != "" {
		modelValue = sql.NullString{String: model, Valid: true}
	}
	_, err := t.db.ExecContext(ctx, `
		INSERT INTO skipper.skipper_usage (
			tenant_id,
			event_type,
			event_count,
			tokens_input,
			tokens_output,
			model,
			created_at
		) VALUES ($1, $2, $3, $4, $5, $6, NOW())
	`, tenantID, eventType, count, inputTokens, outputTokens, modelValue)
	if err != nil && t.logger != nil {
		t.logger.WithError(err).WithFields(logging.Fields{
			"tenant_id":  tenantID,
			"event_type": eventType,
		}).Warn("Failed to persist Skipper usage")
	}
	return err
}

func (t *UsageTracker) buildUsageSummary(tenantID string, usage *tenantUsage, windowStart, windowEnd time.Time) models.UsageSummary {
	totalRequests := usage.llmCalls + usage.searches + usage.embeddings
	totalTokens := usage.inputTokens + usage.outputTokens
	breakdown := make([]models.APIUsageBreakdown, 0, 3)

	if usage.llmCalls > 0 {
		breakdown = append(breakdown, models.APIUsageBreakdown{
			AuthType:      "skipper",
			OperationType: "llm_call",
			OperationName: "skipper_chat",
			Requests:      float64(usage.llmCalls),
			Errors:        0,
			DurationMs:    0,
			Complexity:    float64(totalTokens),
		})
	}
	if usage.searches > 0 {
		breakdown = append(breakdown, models.APIUsageBreakdown{
			AuthType:      "skipper",
			OperationType: "search_query",
			OperationName: "skipper_search",
			Requests:      float64(usage.searches),
			Errors:        0,
			DurationMs:    0,
			Complexity:    0,
		})
	}
	if usage.embeddings > 0 {
		breakdown = append(breakdown, models.APIUsageBreakdown{
			AuthType:      "skipper",
			OperationType: "embedding",
			OperationName: "skipper_embedding",
			Requests:      float64(usage.embeddings),
			Errors:        0,
			DurationMs:    0,
			Complexity:    0,
		})
	}

	period := fmt.Sprintf("%s/%s", windowStart.Format(time.RFC3339), windowEnd.Format(time.RFC3339))

	return models.UsageSummary{
		TenantID:      tenantID,
		ClusterID:     t.clusterID,
		Period:        period,
		Timestamp:     windowEnd,
		APIRequests:   float64(totalRequests),
		APIErrors:     0,
		APIDurationMs: 0,
		APIComplexity: float64(totalTokens),
		APIBreakdown:  breakdown,
	}
}

func (t *UsageTracker) ensureTenant(tenantID string) *tenantUsage {
	usage, ok := t.usageByTenant[tenantID]
	if !ok {
		usage = &tenantUsage{}
		t.usageByTenant[tenantID] = usage
	}
	return usage
}

func (t *UsageTracker) requeueUsage(tenantID string, usage *tenantUsage) {
	if t == nil || tenantID == "" || usage == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	current := t.ensureTenant(tenantID)
	current.llmCalls += usage.llmCalls
	current.inputTokens += usage.inputTokens
	current.outputTokens += usage.outputTokens
	current.searches += usage.searches
	current.embeddings += usage.embeddings
}

func (t *UsageTracker) enqueueSummary(summary models.UsageSummary) {
	if t == nil {
		return
	}
	t.pendingMu.Lock()
	t.pending = append(t.pending, summary)
	t.pendingMu.Unlock()
}

func (t *UsageTracker) retryPendingSummaries(ctx context.Context) {
	if t == nil || t.publisher == nil {
		return
	}
	t.pendingMu.Lock()
	pending := t.pending
	t.pending = nil
	t.pendingMu.Unlock()
	if len(pending) == 0 {
		return
	}
	var remaining []models.UsageSummary
	for _, summary := range pending {
		if err := t.publisher.PublishUsageSummary(summary); err != nil {
			remaining = append(remaining, summary)
			if t.logger != nil {
				t.logger.WithError(err).WithField("tenant_id", summary.TenantID).Warn("Failed to retry Skipper usage summary")
			}
		}
	}
	if len(remaining) > 0 {
		t.pendingMu.Lock()
		t.pending = append(t.pending, remaining...)
		t.pendingMu.Unlock()
	}
}
