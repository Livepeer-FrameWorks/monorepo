package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"frameworks/api_consultant/internal/metering"
	"frameworks/api_consultant/internal/skipper"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/llm"
	"frameworks/pkg/logging"

	"github.com/gin-gonic/gin"
)

const maxMessageRunes = 10000

type ChatHandler struct {
	Conversations      *ConversationStore
	Orchestrator       *Orchestrator
	LLMProvider        llm.Provider
	UsageLogger        skipper.UsageLogger
	Logger             logging.Logger
	MaxHistoryMessages int

	// conversationLocks serializes concurrent requests to the same conversation.
	// For horizontal scaling, replace with pg_advisory_xact_lock.
	conversationLocks sync.Map
}

type ChatRequest struct {
	ConversationID string `json:"conversation_id,omitempty"`
	Message        string `json:"message"`
	PageURL        string `json:"pageUrl,omitempty"`
}

type citation struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

type sseMeta struct {
	Type          string       `json:"type"`
	Confidence    string       `json:"confidence"`
	Citations     []citation   `json:"citations,omitempty"`
	ExternalLinks []citation   `json:"externalLinks,omitempty"`
	Details       []ToolDetail `json:"details,omitempty"`
}

type sseToken struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

type sseDone struct {
	Type string `json:"type"`
}

const defaultMaxHistoryMessages = 20

func NewChatHandler(
	conversations *ConversationStore,
	orchestrator *Orchestrator,
	usageLogger skipper.UsageLogger,
	logger logging.Logger,
) *ChatHandler {
	return &ChatHandler{
		Conversations:      conversations,
		Orchestrator:       orchestrator,
		UsageLogger:        usageLogger,
		Logger:             logger,
		MaxHistoryMessages: defaultMaxHistoryMessages,
	}
}

func RegisterRoutes(router gin.IRoutes, handler *ChatHandler) {
	router.POST("/chat", handler.HandleChat)
	router.GET("/conversations", handler.HandleListConversations)
	router.GET("/conversations/:id", handler.HandleGetConversation)
	router.DELETE("/conversations/:id", handler.HandleDeleteConversation)
	router.PATCH("/conversations/:id", handler.HandleUpdateConversation)
}

func (h *ChatHandler) HandleChat(c *gin.Context) {
	startedAt := time.Now()
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}
	if h.Orchestrator == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "orchestrator unavailable"})
		return
	}

	var req ChatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request payload"})
		return
	}
	req.Message = strings.TrimSpace(req.Message)
	if req.Message == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "message is required"})
		return
	}
	if len([]rune(req.Message)) > maxMessageRunes {
		c.JSON(http.StatusBadRequest, gin.H{"error": "message too long"})
		return
	}

	mode := c.Query("mode")
	if mode != "" && mode != "docs" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid mode"})
		return
	}

	tenantID := skipper.GetTenantID(c.Request.Context())
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "tenant_id missing"})
		return
	}
	userID := skipper.GetUserID(c.Request.Context())

	ctx := h.buildContext(c.Request.Context(), tenantID, userID)
	if mode != "" {
		ctx = skipper.WithMode(ctx, mode)
	}

	conversationID := strings.TrimSpace(req.ConversationID)
	isNewConversation := false
	if conversationID == "" {
		var err error
		conversationID, err = h.Conversations.CreateConversation(ctx, tenantID, userID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create conversation"})
			return
		}
		isNewConversation = true
	} else if _, err := h.Conversations.GetConversation(ctx, conversationID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "conversation not found"})
		return
	}

	lockVal, _ := h.conversationLocks.LoadOrStore(conversationID, &sync.Mutex{})
	convMu, ok := lockVal.(*sync.Mutex)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal lock error"})
		return
	}
	convMu.Lock()
	defer func() {
		convMu.Unlock()
		if convMu.TryLock() {
			h.conversationLocks.Delete(conversationID)
			convMu.Unlock()
		}
	}()

	historyLimit := h.MaxHistoryMessages
	if historyLimit <= 0 {
		historyLimit = defaultMaxHistoryMessages
	}
	history, err := h.Conversations.GetRecentMessages(ctx, conversationID, historyLimit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load conversation history"})
		return
	}

	if addErr := h.Conversations.AddMessage(ctx, conversationID, "user", req.Message, "", nil, nil, TokenCounts{
		Input:  estimateTokens(req.Message),
		Output: 0,
	}); addErr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to persist user message"})
		return
	}

	messages := buildPromptMessages(history, req.Message, req.PageURL, mode)

	// Inject conversation summary into system prompt for long conversations.
	if !isNewConversation && len(history) >= summaryThreshold {
		summary, _ := h.Conversations.GetSummary(ctx, conversationID)
		if summary != "" {
			for i, msg := range messages {
				if msg.Role == "system" {
					messages[i].Content += "\n\n--- Summary of earlier discussion ---\n" + summary
					break
				}
			}
		}
	}

	streamer, err := newSSEStreamer(c.Writer)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming unavailable"})
		return
	}
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Header("X-Conversation-ID", conversationID)
	c.Status(http.StatusOK)

	conversationsActive.Inc()
	result, err := h.Orchestrator.Run(ctx, messages, streamer)
	conversationsActive.Dec()
	if err != nil {
		h.Logger.WithError(err).Warn("Skipper orchestrator failed")
		_ = streamer.SendError("An error occurred processing your request.")
		_ = streamer.SendDone()
		return
	}

	if err := streamer.SendMeta(buildMeta(result)); err != nil {
		h.Logger.WithError(err).Warn("Failed to send SSE metadata")
	}
	_ = streamer.SendDone()

	sourcesJSON, _ := json.Marshal(result.Sources)
	toolData := struct {
		Calls   []ToolCallRecord `json:"calls,omitempty"`
		Details []ToolDetail     `json:"details,omitempty"`
	}{result.ToolCalls, result.Details}
	toolsJSON, _ := json.Marshal(toolData)
	if err := h.Conversations.AddMessage(ctx, conversationID, "assistant", result.Content, string(result.Confidence), sourcesJSON, toolsJSON, result.TokenCounts); err != nil {
		h.Logger.WithError(err).Warn("Failed to store assistant response")
	}

	if isNewConversation {
		title := truncateTitle(req.Message, 60)
		if err := h.Conversations.UpdateTitle(ctx, conversationID, title); err != nil {
			h.Logger.WithError(err).Warn("Failed to set conversation title")
		}
	}

	h.logUsage(ctx, tenantID, userID, startedAt, result.TokenCounts, false)
	metering.RecordLLMUsage(ctx, result.TokenCounts.Input, result.TokenCounts.Output)

	// Generate conversation summary asynchronously when message count crosses a threshold.
	if h.LLMProvider != nil {
		go h.maybeUpdateSummary(context.WithoutCancel(ctx), conversationID)
	}
}

func (h *ChatHandler) maybeUpdateSummary(ctx context.Context, conversationID string) {
	count, err := h.Conversations.MessageCount(ctx, conversationID)
	if err != nil || count < summaryThreshold || count%summaryUpdateInterval != 0 {
		return
	}

	messages, err := h.Conversations.GetRecentMessages(ctx, conversationID, count)
	if err != nil {
		return
	}

	// Summarize all but the last 5 messages.
	cutoff := len(messages) - summaryUpdateInterval
	if cutoff <= 0 {
		return
	}

	summary, err := generateSummary(ctx, h.LLMProvider, messages[:cutoff])
	if err != nil {
		h.Logger.WithError(err).Warn("Failed to generate conversation summary")
		return
	}
	if summary == "" {
		return
	}

	if err := h.Conversations.UpdateSummary(ctx, conversationID, summary); err != nil {
		h.Logger.WithError(err).Warn("Failed to store conversation summary")
	}
}

func (h *ChatHandler) HandleListConversations(c *gin.Context) {
	tenantID := skipper.GetTenantID(c.Request.Context())
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "tenant_id missing"})
		return
	}
	userID := skipper.GetUserID(c.Request.Context())
	limit := 50
	offset := 0
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if v := c.Query("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	if limit > 200 {
		limit = 200
	}
	summaries, err := h.Conversations.ListConversations(c.Request.Context(), tenantID, userID, limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list conversations"})
		return
	}
	c.JSON(http.StatusOK, summaries)
}

func (h *ChatHandler) HandleGetConversation(c *gin.Context) {
	tenantID := skipper.GetTenantID(c.Request.Context())
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "tenant_id missing"})
		return
	}
	conversationID := c.Param("id")
	if conversationID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "conversation id is required"})
		return
	}
	ctx := skipper.WithTenantID(c.Request.Context(), tenantID)
	if userID := skipper.GetUserID(c.Request.Context()); userID != "" {
		ctx = skipper.WithUserID(ctx, userID)
	}
	convo, err := h.Conversations.GetConversation(ctx, conversationID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "conversation not found"})
		return
	}
	c.JSON(http.StatusOK, convo)
}

func (h *ChatHandler) HandleDeleteConversation(c *gin.Context) {
	tenantID := skipper.GetTenantID(c.Request.Context())
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "tenant_id missing"})
		return
	}
	conversationID := c.Param("id")
	if conversationID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "conversation id is required"})
		return
	}
	ctx := skipper.WithTenantID(c.Request.Context(), tenantID)
	if userID := skipper.GetUserID(c.Request.Context()); userID != "" {
		ctx = skipper.WithUserID(ctx, userID)
	}
	if err := h.Conversations.DeleteConversation(ctx, conversationID); err != nil {
		if strings.Contains(err.Error(), "not found") {
			c.JSON(http.StatusNotFound, gin.H{"error": "conversation not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete conversation"})
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *ChatHandler) HandleUpdateConversation(c *gin.Context) {
	tenantID := skipper.GetTenantID(c.Request.Context())
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "tenant_id missing"})
		return
	}
	conversationID := c.Param("id")
	if conversationID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "conversation id is required"})
		return
	}

	var req struct {
		Title string `json:"title"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request payload"})
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	if req.Title == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "title is required"})
		return
	}

	ctx := skipper.WithTenantID(c.Request.Context(), tenantID)
	if userID := skipper.GetUserID(c.Request.Context()); userID != "" {
		ctx = skipper.WithUserID(ctx, userID)
	}
	if err := h.Conversations.UpdateTitle(ctx, conversationID, req.Title); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update conversation"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"title": req.Title})
}

// maxPromptTokenBudget is a rough ceiling for the combined system + history
// messages. When exceeded, older history messages are trimmed first.
const maxPromptTokenBudget = 6000

func buildPromptMessages(history []Message, userMessage, pageURL, mode string) []llm.Message {
	systemContent := SystemPrompt
	if mode == "docs" {
		systemContent += DocsSystemPromptSuffix
	}
	if pageURL != "" {
		if u, err := url.Parse(pageURL); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
			truncated := pageURL
			if len(truncated) > 500 {
				truncated = truncated[:500]
			}
			systemContent += "\n\nThe user is currently reading the docs page: " + truncated + ". Use this context when relevant to their question."
		}
	}
	messages := []llm.Message{
		{Role: "system", Content: systemContent},
	}

	// Filter history: keep only user and assistant messages (strip tool-role
	// messages from previous turns to save tokens).
	var filtered []Message
	for _, msg := range history {
		if msg.Role == "" || msg.Content == "" {
			continue
		}
		if msg.Role == "tool" {
			continue
		}
		filtered = append(filtered, msg)
	}

	// Token budget: trim oldest history first to fit the context window.
	userTokens := estimateTokens(userMessage)
	systemTokens := estimateTokens(systemContent)
	budget := maxPromptTokenBudget - systemTokens - userTokens
	if budget < 0 {
		budget = 0
	}

	// Walk from newest to oldest, keeping messages that fit.
	kept := make([]Message, 0, len(filtered))
	used := 0
	for i := len(filtered) - 1; i >= 0; i-- {
		msgTokens := estimateTokens(filtered[i].Content)
		if used+msgTokens > budget {
			break
		}
		used += msgTokens
		kept = append(kept, filtered[i])
	}
	// Reverse to restore chronological order.
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}

	for _, msg := range kept {
		messages = append(messages, llm.Message{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}
	messages = append(messages, llm.Message{
		Role:    "user",
		Content: userMessage,
	})
	return messages
}

func (h *ChatHandler) buildContext(ctx context.Context, tenantID, userID string) context.Context {
	ctx = skipper.WithTenantID(ctx, tenantID)
	if userID != "" {
		ctx = skipper.WithUserID(ctx, userID)
	}
	if authType := skipper.GetAuthType(ctx); authType != "" {
		ctx = skipper.WithAuthType(ctx, authType)
	}
	if token := skipper.GetJWTToken(ctx); token != "" {
		ctx = context.WithValue(ctx, ctxkeys.KeyJWTToken, token)
	}
	return ctx
}

func buildMeta(result OrchestratorResult) sseMeta {
	citations := make([]citation, 0)
	external := make([]citation, 0)
	for _, source := range result.Sources {
		if source.URL == "" {
			continue
		}
		item := citation{Label: source.Title, URL: source.URL}
		switch source.Type {
		case SourceTypeKnowledgeBase:
			citations = append(citations, item)
		case SourceTypeWeb:
			external = append(external, item)
		}
	}
	if len(citations) == 0 && len(external) == 0 {
		for _, source := range result.Sources {
			if source.URL == "" {
				continue
			}
			citations = append(citations, citation{Label: source.Title, URL: source.URL})
		}
	}
	return sseMeta{
		Type:          "meta",
		Confidence:    string(result.Confidence),
		Citations:     citations,
		ExternalLinks: external,
		Details:       result.Details,
	}
}

type sseStreamer struct {
	writer  http.ResponseWriter
	flusher http.Flusher
}

func newSSEStreamer(writer http.ResponseWriter) (*sseStreamer, error) {
	flusher, ok := writer.(http.Flusher)
	if !ok {
		return nil, errors.New("response writer does not support streaming")
	}
	return &sseStreamer{writer: writer, flusher: flusher}, nil
}

func (s *sseStreamer) SendToken(token string) error {
	payload := sseToken{Type: "token", Content: token}
	return s.send(payload)
}

func (s *sseStreamer) SendMeta(meta sseMeta) error {
	return s.send(meta)
}

func (s *sseStreamer) SendError(msg string) error {
	return s.send(map[string]string{"type": "error", "message": msg})
}

func (s *sseStreamer) SendDone() error {
	if err := s.send(sseDone{Type: "done"}); err != nil {
		return err
	}
	_, err := fmt.Fprintf(s.writer, "data: [DONE]\n\n")
	if err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

func (s *sseStreamer) SendToolStart(toolName string) error {
	return s.send(map[string]string{"type": "tool_start", "tool": toolName})
}

func (s *sseStreamer) SendToolEnd(toolName string, errMsg string) error {
	evt := map[string]any{"type": "tool_end", "tool": toolName}
	if errMsg != "" {
		evt["error"] = errMsg
	}
	return s.send(evt)
}

func (s *sseStreamer) send(payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.writer, "data: %s\n\n", data); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

func truncateTitle(message string, maxLen int) string {
	message = strings.TrimSpace(message)
	runes := []rune(message)
	if len(runes) <= maxLen {
		return message
	}
	truncated := string(runes[:maxLen])
	if lastSpace := strings.LastIndex(truncated, " "); lastSpace > maxLen/2 {
		truncated = truncated[:lastSpace]
	}
	return truncated + "..."
}

func (h *ChatHandler) logUsage(ctx context.Context, tenantID, userID string, startedAt time.Time, tokens TokenCounts, hadError bool) {
	if h.UsageLogger == nil {
		return
	}
	h.UsageLogger.LogChatUsage(ctx, skipper.ChatUsageEvent{
		TenantID:  tenantID,
		UserID:    userID,
		StartedAt: startedAt,
		TokensIn:  tokens.Input,
		TokensOut: tokens.Output,
		HadError:  hadError,
	})
}
