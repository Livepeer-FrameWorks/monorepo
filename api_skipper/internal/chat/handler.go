package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"frameworks/api_skipper/internal/metering"
	"frameworks/pkg/clients/decklog"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/llm"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/gin-gonic/gin"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type ChatHandler struct {
	Conversations *ConversationStore
	Orchestrator  *Orchestrator
	Decklog       *decklog.BatchedClient
	Logger        logging.Logger
}

type ChatRequest struct {
	ConversationID string `json:"conversation_id,omitempty"`
	Message        string `json:"message"`
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

func NewChatHandler(
	conversations *ConversationStore,
	orchestrator *Orchestrator,
	decklogClient *decklog.BatchedClient,
	logger logging.Logger,
) *ChatHandler {
	return &ChatHandler{
		Conversations: conversations,
		Orchestrator:  orchestrator,
		Decklog:       decklogClient,
		Logger:        logger,
	}
}

func RegisterRoutes(router gin.IRoutes, handler *ChatHandler) {
	router.POST("/chat", handler.HandleChat)
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

	tenantID := c.GetString(string(ctxkeys.KeyTenantID))
	if tenantID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "tenant_id missing"})
		return
	}
	userID := c.GetString(string(ctxkeys.KeyUserID))

	ctx := h.buildContext(c.Request.Context(), tenantID, userID, c.GetHeader("Authorization"))

	conversationID := strings.TrimSpace(req.ConversationID)
	if conversationID == "" {
		var err error
		conversationID, err = h.Conversations.CreateConversation(ctx, tenantID, userID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create conversation"})
			return
		}
	} else if _, err := h.Conversations.GetConversation(ctx, conversationID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "conversation not found"})
		return
	}

	history, err := h.Conversations.GetRecentMessages(ctx, conversationID, 20)
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

	messages := buildPromptMessages(history, req.Message)

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

	result, err := h.Orchestrator.Run(ctx, messages, streamer)
	if err != nil {
		h.Logger.WithError(err).Warn("Skipper orchestrator failed")
		_ = streamer.SendDone()
		return
	}

	if err := streamer.SendMeta(buildMeta(result)); err != nil {
		h.Logger.WithError(err).Warn("Failed to send SSE metadata")
	}
	_ = streamer.SendDone()

	sourcesJSON, _ := json.Marshal(result.Sources)
	toolsJSON, _ := json.Marshal(result.ToolCalls)
	if err := h.Conversations.AddMessage(ctx, conversationID, "assistant", result.Content, string(result.Confidence), sourcesJSON, toolsJSON, result.TokenCounts); err != nil {
		h.Logger.WithError(err).Warn("Failed to store assistant response")
	}

	h.logUsage(ctx, tenantID, userID, startedAt, result.TokenCounts, false)
	metering.RecordLLMUsage(ctx, result.TokenCounts.Input, result.TokenCounts.Output)
}

func buildPromptMessages(history []Message, userMessage string) []llm.Message {
	messages := []llm.Message{
		{Role: "system", Content: SystemPrompt},
	}
	for _, msg := range history {
		if msg.Role == "" || msg.Content == "" {
			continue
		}
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

func (h *ChatHandler) buildContext(ctx context.Context, tenantID, userID, authHeader string) context.Context {
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, tenantID)
	if userID != "" {
		ctx = context.WithValue(ctx, ctxkeys.KeyUserID, userID)
	}
	if token := bearerToken(authHeader); token != "" {
		ctx = context.WithValue(ctx, ctxkeys.KeyJWTToken, token)
		ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, "jwt")
	}
	return ctx
}

func bearerToken(header string) string {
	parts := strings.Split(header, " ")
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		return parts[1]
	}
	return ""
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

func (h *ChatHandler) logUsage(ctx context.Context, tenantID, userID string, startedAt time.Time, tokens TokenCounts, hadError bool) {
	if h.Decklog == nil || tenantID == "" {
		return
	}
	duration := uint64(time.Since(startedAt).Milliseconds())
	totalTokens := uint32(tokens.Input + tokens.Output)
	agg := &pb.APIRequestAggregate{
		TenantId:        tenantID,
		AuthType:        resolveAuthType(ctx),
		OperationType:   "skipper_chat",
		OperationName:   "skipper_chat",
		RequestCount:    1,
		ErrorCount:      boolToCount(hadError),
		TotalDurationMs: duration,
		TotalComplexity: totalTokens,
		Timestamp:       startedAt.Unix(),
	}
	batch := &pb.APIRequestBatch{
		Timestamp:  time.Now().Unix(),
		SourceNode: "skipper",
		Aggregates: []*pb.APIRequestAggregate{agg},
	}
	event := &pb.ServiceEvent{
		EventType: "api_request_batch",
		Timestamp: timestamppb.Now(),
		Source:    "skipper",
		TenantId:  tenantID,
		UserId:    userID,
		Payload:   &pb.ServiceEvent_ApiRequestBatch{ApiRequestBatch: batch},
	}
	if err := h.Decklog.SendServiceEvent(event); err != nil && h.Logger != nil {
		h.Logger.WithError(err).Warn("Failed to emit Skipper usage event")
	}
}

func boolToCount(value bool) uint32 {
	if value {
		return 1
	}
	return 0
}

func resolveAuthType(ctx context.Context) string {
	if authType := ctxkeys.GetAuthType(ctx); authType != "" {
		return authType
	}
	if ctxkeys.GetJWTToken(ctx) != "" {
		return "jwt"
	}
	return "unknown"
}
