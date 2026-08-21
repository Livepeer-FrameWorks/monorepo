package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"frameworks/api_consultant/internal/database/skipperdb"
	"frameworks/api_consultant/internal/skipper"
)

var ErrConversationNotFound = errors.New("conversation not found")

type Conversation struct {
	ID        string
	TenantID  string
	UserID    string
	Title     string
	Summary   string
	CreatedAt time.Time
	UpdatedAt time.Time
	Messages  []Message
}

type ConversationSummary struct {
	ID            string
	TenantID      string
	UserID        string
	Title         string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	LastMessageAt sql.NullTime
	MessageCount  int
}

type Message struct {
	ID               string
	ConversationID   string
	Role             string
	Content          string
	Confidence       string
	Sources          json.RawMessage
	ToolsUsed        json.RawMessage
	ConfidenceBlocks json.RawMessage
	TokenCountInput  int
	TokenCountOutput int
	CreatedAt        time.Time
}

type TokenCounts struct {
	Input  int
	Output int
}

type ConversationStore struct {
	db      *sql.DB
	queries *skipperdb.Queries
}

func NewConversationStore(db *sql.DB) *ConversationStore {
	return &ConversationStore{db: db, queries: skipperdb.New(db)}
}

func (s *ConversationStore) CreateConversation(ctx context.Context, tenantID, userID string) (string, error) {
	if tenantID == "" {
		return "", fmt.Errorf("tenant ID is required")
	}

	conversationID, err := s.queries.CreateConversation(ctx, skipperdb.CreateConversationParams{
		TenantID: tenantID,
		UserID:   nullableString(userID),
	})
	if err != nil {
		return "", fmt.Errorf("create conversation: %w", err)
	}

	return conversationID, nil
}

func (s *ConversationStore) AddMessage(
	ctx context.Context,
	conversationID,
	role,
	content,
	confidence string,
	sources,
	toolsUsed,
	confidenceBlocks json.RawMessage,
	tokens TokenCounts,
) error {
	tenantID := skipper.GetTenantID(ctx)
	if tenantID == "" {
		return fmt.Errorf("tenant ID is required")
	}

	sourcesValue := normalizeJSONInput(sources)
	toolsValue := normalizeJSONInput(toolsUsed)
	blocksValue := normalizeJSONInput(confidenceBlocks)

	_, err := s.queries.AddMessage(ctx, skipperdb.AddMessageParams{
		Role:             role,
		Content:          content,
		Confidence:       confidence,
		Sources:          string(sourcesValue),
		ToolsUsed:        string(toolsValue),
		ConfidenceBlocks: string(blocksValue),
		TokenCountInput:  int32(tokens.Input),
		TokenCountOutput: int32(tokens.Output),
		ConversationID:   conversationID,
		TenantID:         tenantID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("conversation not found")
		}
		return fmt.Errorf("add message: %w", err)
	}

	err = s.queries.TouchConversation(ctx, skipperdb.TouchConversationParams{
		ConversationID: conversationID,
		TenantID:       tenantID,
	})
	if err != nil {
		return fmt.Errorf("update conversation timestamp: %w", err)
	}

	return nil
}

func (s *ConversationStore) GetConversation(ctx context.Context, conversationID string) (Conversation, error) {
	tenantID := skipper.GetTenantID(ctx)
	if tenantID == "" {
		return Conversation{}, fmt.Errorf("tenant ID is required")
	}
	userID := skipper.GetUserID(ctx)

	var convo Conversation
	var err error
	if userID == "" {
		var row skipperdb.GetConversationRow
		row, err = s.queries.GetConversation(ctx, skipperdb.GetConversationParams{
			ConversationID: conversationID,
			TenantID:       tenantID,
		})
		if err == nil {
			convo = conversationFromRow(row.ID, row.TenantID, row.UserID, row.Title, row.Summary, row.CreatedAt, row.UpdatedAt)
		}
	} else {
		var row skipperdb.GetConversationForUserRow
		row, err = s.queries.GetConversationForUser(ctx, skipperdb.GetConversationForUserParams{
			ConversationID: conversationID,
			TenantID:       tenantID,
			UserID:         userID,
		})
		if err == nil {
			convo = conversationFromRow(row.ID, row.TenantID, row.UserID, row.Title, row.Summary, row.CreatedAt, row.UpdatedAt)
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		return Conversation{}, ErrConversationNotFound
	}
	if err != nil {
		return Conversation{}, fmt.Errorf("get conversation: %w", err)
	}

	messages, err := s.fetchMessages(ctx, tenantID, conversationID, 0)
	if err != nil {
		return Conversation{}, err
	}
	convo.Messages = messages

	return convo, nil
}

func (s *ConversationStore) ListConversations(ctx context.Context, tenantID, userID string, limit, offset int) ([]ConversationSummary, error) {
	if tenantID == "" {
		return nil, fmt.Errorf("tenant ID is required")
	}

	if limit <= 0 {
		limit = 25
	}

	var summaries []ConversationSummary
	if userID == "" {
		rows, err := s.queries.ListConversations(ctx, skipperdb.ListConversationsParams{
			TenantID: tenantID,
			RowLimit: int32(limit), RowOffset: int32(offset),
		})
		if err != nil {
			return nil, fmt.Errorf("list conversations: %w", err)
		}
		for _, row := range rows {
			summaries = append(summaries, conversationSummaryFromRow(row.ID, row.TenantID, row.UserID, row.Title, row.CreatedAt, row.UpdatedAt, row.LastMessageAt, row.MessageCount))
		}
	} else {
		rows, err := s.queries.ListConversationsForUser(ctx, skipperdb.ListConversationsForUserParams{
			TenantID: tenantID, UserID: userID,
			RowLimit: int32(limit), RowOffset: int32(offset),
		})
		if err != nil {
			return nil, fmt.Errorf("list conversations: %w", err)
		}
		for _, row := range rows {
			summaries = append(summaries, conversationSummaryFromRow(row.ID, row.TenantID, row.UserID, row.Title, row.CreatedAt, row.UpdatedAt, row.LastMessageAt, row.MessageCount))
		}
	}

	return summaries, nil
}

func (s *ConversationStore) UpdateTitle(ctx context.Context, conversationID, title string) error {
	tenantID := skipper.GetTenantID(ctx)
	if tenantID == "" {
		return fmt.Errorf("tenant ID is required")
	}
	userID := skipper.GetUserID(ctx)

	var rows int64
	var err error
	if userID != "" {
		rows, err = s.queries.UpdateConversationTitleForUser(ctx, skipperdb.UpdateConversationTitleForUserParams{
			Title: title, ConversationID: conversationID,
			TenantID: tenantID, UserID: userID,
		})
	} else {
		rows, err = s.queries.UpdateConversationTitle(ctx, skipperdb.UpdateConversationTitleParams{
			Title: title, ConversationID: conversationID, TenantID: tenantID,
		})
	}
	if err != nil {
		return fmt.Errorf("update title: %w", err)
	}
	if rows == 0 {
		return ErrConversationNotFound
	}
	return nil
}

func (s *ConversationStore) DeleteConversation(ctx context.Context, conversationID string) error {
	tenantID := skipper.GetTenantID(ctx)
	if tenantID == "" {
		return fmt.Errorf("tenant ID is required")
	}
	userID := skipper.GetUserID(ctx)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	queries := skipperdb.New(tx)
	if userID != "" {
		params := skipperdb.DeleteConversationMessagesForUserParams{
			ConversationID: conversationID, TenantID: tenantID, UserID: userID,
		}
		if execErr := queries.DeleteConversationMessagesForUser(ctx, params); execErr != nil {
			return fmt.Errorf("delete messages: %w", execErr)
		}
	} else if execErr := queries.DeleteConversationMessages(ctx, skipperdb.DeleteConversationMessagesParams{
		ConversationID: conversationID, TenantID: tenantID,
	}); execErr != nil {
		return fmt.Errorf("delete messages: %w", execErr)
	}

	var rows int64
	if userID != "" {
		rows, err = queries.DeleteConversationForUser(ctx, skipperdb.DeleteConversationForUserParams{
			ConversationID: conversationID, TenantID: tenantID, UserID: userID,
		})
	} else {
		rows, err = queries.DeleteConversation(ctx, skipperdb.DeleteConversationParams{
			ConversationID: conversationID, TenantID: tenantID,
		})
	}
	if err != nil {
		return fmt.Errorf("delete conversation: %w", err)
	}
	if rows == 0 {
		return ErrConversationNotFound
	}

	return tx.Commit()
}

func (s *ConversationStore) GetRecentMessages(ctx context.Context, conversationID string, limit int) ([]Message, error) {
	tenantID := skipper.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant ID is required")
	}

	if limit <= 0 {
		limit = 25
	}

	return s.fetchMessages(ctx, tenantID, conversationID, limit)
}

func (s *ConversationStore) fetchMessages(ctx context.Context, tenantID, conversationID string, limit int) ([]Message, error) {
	var messages []Message
	if limit > 0 {
		rows, err := s.queries.ListRecentConversationMessages(ctx, skipperdb.ListRecentConversationMessagesParams{
			ConversationID: conversationID, TenantID: tenantID, RowLimit: int32(limit),
		})
		if err != nil {
			return nil, fmt.Errorf("get messages: %w", err)
		}
		for _, row := range rows {
			messages = append(messages, messageFromRow(row.ID, row.ConversationID, row.Role, row.Content, row.Confidence, row.Sources, row.ToolsUsed, row.ConfidenceBlocks, row.TokenCountInput, row.TokenCountOutput, row.CreatedAt))
		}
	} else {
		rows, err := s.queries.ListConversationMessages(ctx, skipperdb.ListConversationMessagesParams{
			ConversationID: conversationID, TenantID: tenantID,
		})
		if err != nil {
			return nil, fmt.Errorf("get messages: %w", err)
		}
		for _, row := range rows {
			messages = append(messages, messageFromRow(row.ID, row.ConversationID, row.Role, row.Content, row.Confidence, row.Sources, row.ToolsUsed, row.ConfidenceBlocks, row.TokenCountInput, row.TokenCountOutput, row.CreatedAt))
		}
	}

	return messages, nil
}

func (s *ConversationStore) GetSummary(ctx context.Context, conversationID string) (string, error) {
	tenantID := skipper.GetTenantID(ctx)
	if tenantID == "" {
		return "", fmt.Errorf("tenant ID is required")
	}
	summary, err := s.queries.GetConversationSummary(ctx, skipperdb.GetConversationSummaryParams{
		ConversationID: conversationID, TenantID: tenantID,
	})
	if err != nil {
		return "", fmt.Errorf("get summary: %w", err)
	}
	return summary.String, nil
}

func (s *ConversationStore) UpdateSummary(ctx context.Context, conversationID, summary string) error {
	tenantID := skipper.GetTenantID(ctx)
	if tenantID == "" {
		return fmt.Errorf("tenant ID is required")
	}
	err := s.queries.UpdateConversationSummary(ctx, skipperdb.UpdateConversationSummaryParams{
		Summary: summary, ConversationID: conversationID, TenantID: tenantID,
	})
	if err != nil {
		return fmt.Errorf("update summary: %w", err)
	}
	return nil
}

func (s *ConversationStore) MessageCount(ctx context.Context, conversationID string) (int, error) {
	tenantID := skipper.GetTenantID(ctx)
	if tenantID == "" {
		return 0, fmt.Errorf("tenant ID is required")
	}
	count, err := s.queries.CountConversationMessages(ctx, skipperdb.CountConversationMessagesParams{
		ConversationID: conversationID, TenantID: tenantID,
	})
	if err != nil {
		return 0, fmt.Errorf("message count: %w", err)
	}
	return int(count), nil
}

func nullableString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: value != ""}
}

func conversationFromRow(id, tenantID string, userID, title sql.NullString, summary string, createdAt, updatedAt time.Time) Conversation {
	return Conversation{
		ID: id, TenantID: tenantID, UserID: userID.String, Title: title.String,
		Summary: summary, CreatedAt: createdAt, UpdatedAt: updatedAt,
	}
}

func conversationSummaryFromRow(id, tenantID string, userID, title sql.NullString, createdAt, updatedAt time.Time, lastMessageAt any, messageCount int64) ConversationSummary {
	var last sql.NullTime
	if timestamp, ok := lastMessageAt.(time.Time); ok {
		last = sql.NullTime{Time: timestamp, Valid: true}
	}
	return ConversationSummary{
		ID: id, TenantID: tenantID, UserID: userID.String, Title: title.String,
		CreatedAt: createdAt, UpdatedAt: updatedAt, LastMessageAt: last, MessageCount: int(messageCount),
	}
}

func messageFromRow(id, conversationID, role, content, confidence string, sources, toolsUsed, confidenceBlocks json.RawMessage, tokenCountInput, tokenCountOutput int, createdAt time.Time) Message {
	return Message{
		ID: id, ConversationID: conversationID, Role: role, Content: content, Confidence: confidence,
		Sources: sources, ToolsUsed: toolsUsed, ConfidenceBlocks: confidenceBlocks,
		TokenCountInput: tokenCountInput, TokenCountOutput: tokenCountOutput, CreatedAt: createdAt,
	}
}

func normalizeJSONInput(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage("null")
	}
	return value
}
