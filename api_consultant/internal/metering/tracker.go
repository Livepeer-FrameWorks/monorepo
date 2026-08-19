package metering

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"frameworks/api_consultant/internal/skipper"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/types/known/timestamppb"
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
	DB                *sql.DB
	Logger            logging.Logger
	Model             string
	Provider          string
	EmbeddingModel    string
	EmbeddingProvider string
	SearchProvider    string
	Publisher         UsagePublisher
	FlushInterval     time.Duration
}

type UsagePublisher interface {
	SendServiceEvent(event *ipcpb.ServiceEvent) error
}

type UsageTracker struct {
	db                *sql.DB
	logger            logging.Logger
	model             string
	provider          string
	embeddingModel    string
	embeddingProvider string
	searchProvider    string
	publisher         UsagePublisher
	flushInterval     time.Duration
	stopOnce          sync.Once
	stopCh            chan struct{}
	mu                sync.Mutex
	usageByTenant     map[string]*tenantUsage
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
	return &UsageTracker{
		db:                cfg.DB,
		logger:            cfg.Logger,
		model:             cfg.Model,
		provider:          cfg.Provider,
		embeddingModel:    cfg.EmbeddingModel,
		embeddingProvider: cfg.EmbeddingProvider,
		searchProvider:    cfg.SearchProvider,
		publisher:         cfg.Publisher,
		flushInterval:     flushInterval,
		stopCh:            make(chan struct{}),
		usageByTenant:     make(map[string]*tenantUsage),
	}
}

func (t *UsageTracker) Start() {
	if t == nil {
		return
	}
	go t.loop()
	if t.publisher != nil && t.db != nil {
		go t.publishLoop()
	}
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

// LogChatUsage makes the durable tracker the chat UsageLogger. HTTP and gRPC
// therefore share one persisted financial fact instead of a direct, lossy
// Decklog publication path.
func (t *UsageTracker) LogChatUsage(_ context.Context, event skipper.ChatUsageEvent) {
	t.RecordLLMCall(event.TenantID, event.TokensIn, event.TokensOut)
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
	t.mu.Lock()
	if len(t.usageByTenant) == 0 {
		t.mu.Unlock()
		return
	}
	snapshot := t.usageByTenant
	t.usageByTenant = make(map[string]*tenantUsage)
	t.mu.Unlock()

	for tenantID, usage := range snapshot {
		t.flushTenant(ctx, tenantID, usage)
	}
}

func (t *UsageTracker) flushTenant(ctx context.Context, tenantID string, usage *tenantUsage) {
	if tenantID == "" || usage == nil {
		return
	}

	if usage.llmCalls == 0 && usage.searches == 0 && usage.embeddings == 0 {
		return
	}

	failedUsage, err := t.persistUsage(ctx, tenantID, usage)
	if err != nil {
		// Avoid double-counting: only requeue the event types that failed to persist.
		t.requeueUsage(tenantID, failedUsage)
		return
	}

}

func (t *UsageTracker) persistUsage(ctx context.Context, tenantID string, usage *tenantUsage) (*tenantUsage, error) {
	if t.db == nil {
		return nil, nil
	}

	failed := &tenantUsage{}
	var errs []error

	if usage.llmCalls > 0 {
		if err := t.insertUsageRow(ctx, tenantID, "llm_call", usage.llmCalls, usage.inputTokens, usage.outputTokens, t.model, t.provider); err != nil {
			err = fmt.Errorf("llm_call: %w", err)
			errs = append(errs, err)
			failed.llmCalls = usage.llmCalls
			failed.inputTokens = usage.inputTokens
			failed.outputTokens = usage.outputTokens
		}
	}
	if usage.searches > 0 {
		if err := t.insertUsageRow(ctx, tenantID, "search_query", usage.searches, 0, 0, "", t.searchProvider); err != nil {
			err = fmt.Errorf("search_query: %w", err)
			errs = append(errs, err)
			failed.searches = usage.searches
		}
	}
	if usage.embeddings > 0 {
		if err := t.insertUsageRow(ctx, tenantID, "embedding", usage.embeddings, 0, 0, t.embeddingModel, t.embeddingProvider); err != nil {
			err = fmt.Errorf("embedding: %w", err)
			errs = append(errs, err)
			failed.embeddings = usage.embeddings
		}
	}

	if len(errs) > 0 {
		return failed, fmt.Errorf("persist usage failed with %d error(s)", len(errs))
	}
	return nil, nil
}

func (t *UsageTracker) insertUsageRow(ctx context.Context, tenantID, eventType string, count, inputTokens, outputTokens int, model, provider string) error {
	if count <= 0 {
		return nil
	}
	var modelValue sql.NullString
	if model != "" {
		modelValue = sql.NullString{String: model, Valid: true}
	}
	var providerValue sql.NullString
	if provider != "" {
		providerValue = sql.NullString{String: provider, Valid: true}
	}
	_, err := t.db.ExecContext(ctx, `
		INSERT INTO skipper.skipper_usage (
			tenant_id,
			event_type,
			event_count,
			tokens_input,
			tokens_output,
			model,
			provider,
			created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, NOW())
	`, tenantID, eventType, count, inputTokens, outputTokens, modelValue, providerValue)
	if err != nil && t.logger != nil {
		t.logger.WithError(err).WithFields(logging.Fields{
			"tenant_id":  tenantID,
			"event_type": eventType,
		}).Warn("Failed to persist Skipper usage")
	}
	return err
}

type persistedUsage struct {
	id, tenantID, eventType, model, provider string
	count, inputTokens, outputTokens         int
	createdAt                                time.Time
}

func (t *UsageTracker) publishLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		if err := t.publishPending(context.Background()); err != nil && t.logger != nil {
			t.logger.WithError(err).Warn("Failed to publish persisted Skipper usage")
		}
		select {
		case <-ticker.C:
		case <-t.stopCh:
			return
		}
	}
}

func (t *UsageTracker) publishPending(ctx context.Context) error {
	rows, err := t.claimPending(ctx)
	if err != nil {
		return err
	}
	var publishErr error
	for _, row := range rows {
		if err := t.publisher.SendServiceEvent(row.serviceEvent()); err != nil {
			publishErr = errors.Join(publishErr, err)
			if _, updateErr := t.db.ExecContext(ctx, `
				UPDATE skipper.skipper_usage
				SET claimed_at = NULL, attempts = attempts + 1, last_error = $2
				WHERE id = $1::uuid AND published_at IS NULL
			`, row.id, err.Error()); updateErr != nil {
				publishErr = errors.Join(publishErr, fmt.Errorf("record usage publication failure: %w", updateErr))
			}
			continue
		}
		if _, err := t.db.ExecContext(ctx, `
			UPDATE skipper.skipper_usage
			SET published_at = NOW(), claimed_at = NULL, last_error = NULL
			WHERE id = $1::uuid
		`, row.id); err != nil {
			publishErr = errors.Join(publishErr, err)
		}
	}
	return publishErr
}

func (t *UsageTracker) claimPending(ctx context.Context) ([]persistedUsage, error) {
	tx, err := t.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	rows, err := tx.QueryContext(ctx, `
		SELECT id::text, tenant_id::text, event_type, event_count,
		       COALESCE(tokens_input, 0), COALESCE(tokens_output, 0),
		       COALESCE(model, ''), COALESCE(provider, ''), created_at
		FROM skipper.skipper_usage
		WHERE published_at IS NULL
		  AND (claimed_at IS NULL OR claimed_at < NOW() - INTERVAL '2 minutes')
		ORDER BY created_at
		FOR UPDATE SKIP LOCKED
		LIMIT 50
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []persistedUsage
	var ids []string
	for rows.Next() {
		var row persistedUsage
		if err := rows.Scan(&row.id, &row.tenantID, &row.eventType, &row.count,
			&row.inputTokens, &row.outputTokens, &row.model, &row.provider, &row.createdAt); err != nil {
			return nil, err
		}
		result = append(result, row)
		ids = append(ids, row.id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) > 0 {
		if _, err := tx.ExecContext(ctx, `
			UPDATE skipper.skipper_usage SET claimed_at = NOW()
			WHERE id = ANY($1::uuid[])
		`, postgresArray(ids)); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func (u persistedUsage) serviceEvent() *ipcpb.ServiceEvent {
	operationType := "skipper_" + u.eventType
	agg := &ipcpb.APIRequestAggregate{
		TenantId:        u.tenantID,
		AuthType:        "service",
		OperationType:   operationType,
		OperationName:   operationType,
		RequestCount:    uint32(max(u.count, 0)),
		Timestamp:       u.createdAt.Unix(),
		LlmInputTokens:  uint64(max(u.inputTokens, 0)),
		LlmOutputTokens: uint64(max(u.outputTokens, 0)),
		Model:           u.model,
		Provider:        u.provider,
	}
	return &ipcpb.ServiceEvent{
		EventId:   u.id,
		EventType: "api_request_batch",
		Timestamp: timestamppb.New(u.createdAt),
		Source:    "skipper",
		TenantId:  u.tenantID,
		Payload: &ipcpb.ServiceEvent_ApiRequestBatch{ApiRequestBatch: &ipcpb.APIRequestBatch{
			Timestamp: u.createdAt.Unix(), SourceNode: "skipper", Aggregates: []*ipcpb.APIRequestAggregate{agg},
		}},
	}
}

func postgresArray(values []string) string { return "{" + strings.Join(values, ",") + "}" }

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
