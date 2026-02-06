package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/database"
)

type Conversation struct {
	ID        string
	TenantID  string
	UserID    string
	Title     string
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
	TokenCountInput  int
	TokenCountOutput int
	CreatedAt        time.Time
}

type TokenCounts struct {
	Input  int
	Output int
}

type ConversationStore struct {
	db *sql.DB
}

func NewConversationStore(db *sql.DB) *ConversationStore {
	return &ConversationStore{db: db}
}

func (s *ConversationStore) CreateConversation(ctx context.Context, tenantID, userID string) (string, error) {
	if tenantID == "" {
		return "", fmt.Errorf("tenant ID is required")
	}

	var conversationID string
	var userIDValue any
	if userID == "" {
		userIDValue = nil
	} else {
		userIDValue = userID
	}

	err := s.db.QueryRowContext(
		ctx,
		`INSERT INTO skipper.skipper_conversations (tenant_id, user_id)
		 VALUES ($1, $2)
		 RETURNING id`,
		tenantID,
		userIDValue,
	).Scan(&conversationID)
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
	toolsUsed json.RawMessage,
	tokens TokenCounts,
) error {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return fmt.Errorf("tenant ID is required")
	}

	sourcesValue := normalizeJSONInput(sources)
	toolsValue := normalizeJSONInput(toolsUsed)

	var messageID string
	err := s.db.QueryRowContext(
		ctx,
		`INSERT INTO skipper.skipper_messages (
			conversation_id,
			role,
			content,
			confidence,
			sources,
			tools_used,
			token_count_input,
			token_count_output
		)
		SELECT c.id, $2, $3, $4, $5, $6, $7, $8
		FROM skipper.skipper_conversations c
		WHERE c.id = $1 AND c.tenant_id = $9
		RETURNING id`,
		conversationID,
		role,
		content,
		confidence,
		sourcesValue,
		toolsValue,
		tokens.Input,
		tokens.Output,
		tenantID,
	).Scan(&messageID)
	if err != nil {
		if errors.Is(err, database.ErrNoRows) {
			return fmt.Errorf("conversation not found")
		}
		return fmt.Errorf("add message: %w", err)
	}

	_, err = s.db.ExecContext(
		ctx,
		`UPDATE skipper.skipper_conversations
		 SET updated_at = NOW()
		 WHERE id = $1 AND tenant_id = $2`,
		conversationID,
		tenantID,
	)
	if err != nil {
		return fmt.Errorf("update conversation timestamp: %w", err)
	}

	return nil
}

func (s *ConversationStore) GetConversation(ctx context.Context, conversationID string) (Conversation, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return Conversation{}, fmt.Errorf("tenant ID is required")
	}

	var convo Conversation
	var title sql.NullString
	var userID sql.NullString
	err := s.db.QueryRowContext(
		ctx,
		`SELECT id, tenant_id, user_id, title, created_at, updated_at
		 FROM skipper.skipper_conversations
		 WHERE id = $1 AND tenant_id = $2`,
		conversationID,
		tenantID,
	).Scan(
		&convo.ID,
		&convo.TenantID,
		&userID,
		&title,
		&convo.CreatedAt,
		&convo.UpdatedAt,
	)
	if err != nil {
		return Conversation{}, fmt.Errorf("get conversation: %w", err)
	}

	convo.UserID = userID.String
	convo.Title = title.String

	messages, err := s.fetchMessages(ctx, tenantID, conversationID, 0)
	if err != nil {
		return Conversation{}, err
	}
	convo.Messages = messages

	return convo, nil
}

func (s *ConversationStore) ListConversations(ctx context.Context, tenantID string, limit, offset int) ([]ConversationSummary, error) {
	if tenantID == "" {
		return nil, fmt.Errorf("tenant ID is required")
	}

	if limit <= 0 {
		limit = 25
	}

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT
			c.id,
			c.tenant_id,
			c.user_id,
			c.title,
			c.created_at,
			c.updated_at,
			MAX(m.created_at) AS last_message_at,
			COUNT(m.id) AS message_count
		FROM skipper.skipper_conversations c
		LEFT JOIN skipper.skipper_messages m ON m.conversation_id = c.id
		WHERE c.tenant_id = $1
		GROUP BY c.id, c.tenant_id, c.user_id, c.title, c.created_at, c.updated_at
		ORDER BY COALESCE(MAX(m.created_at), c.created_at) DESC
		LIMIT $2 OFFSET $3`,
		tenantID,
		limit,
		offset,
	)
	if err != nil {
		return nil, fmt.Errorf("list conversations: %w", err)
	}
	defer rows.Close()

	var summaries []ConversationSummary
	for rows.Next() {
		var summary ConversationSummary
		var userID sql.NullString
		var title sql.NullString
		if err := rows.Scan(
			&summary.ID,
			&summary.TenantID,
			&userID,
			&title,
			&summary.CreatedAt,
			&summary.UpdatedAt,
			&summary.LastMessageAt,
			&summary.MessageCount,
		); err != nil {
			return nil, fmt.Errorf("scan conversation summary: %w", err)
		}
		summary.UserID = userID.String
		summary.Title = title.String
		summaries = append(summaries, summary)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list conversations rows: %w", err)
	}

	return summaries, nil
}

func (s *ConversationStore) GetRecentMessages(ctx context.Context, conversationID string, limit int) ([]Message, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant ID is required")
	}

	if limit <= 0 {
		limit = 25
	}

	return s.fetchMessages(ctx, tenantID, conversationID, limit)
}

func (s *ConversationStore) fetchMessages(ctx context.Context, tenantID, conversationID string, limit int) ([]Message, error) {
	query := `SELECT
		m.id,
		m.conversation_id,
		m.role,
		m.content,
		m.confidence,
		m.sources,
		m.tools_used,
		m.token_count_input,
		m.token_count_output,
		m.created_at
	FROM skipper.skipper_messages m
	JOIN skipper.skipper_conversations c ON m.conversation_id = c.id
	WHERE m.conversation_id = $1 AND c.tenant_id = $2`

	var rows *sql.Rows
	var err error
	if limit > 0 {
		rows, err = s.db.QueryContext(
			ctx,
			`SELECT * FROM (`+query+` ORDER BY m.created_at DESC LIMIT $3) recent ORDER BY created_at ASC`,
			conversationID,
			tenantID,
			limit,
		)
	} else {
		rows, err = s.db.QueryContext(
			ctx,
			query+` ORDER BY m.created_at ASC`,
			conversationID,
			tenantID,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("get messages: %w", err)
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		var message Message
		if err := rows.Scan(
			&message.ID,
			&message.ConversationID,
			&message.Role,
			&message.Content,
			&message.Confidence,
			&message.Sources,
			&message.ToolsUsed,
			&message.TokenCountInput,
			&message.TokenCountOutput,
			&message.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get messages rows: %w", err)
	}

	return messages, nil
}

func normalizeJSONInput(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	return value
}
