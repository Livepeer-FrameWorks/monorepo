package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"frameworks/api_consultant/internal/skipper"
	"frameworks/pkg/database"
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
			confidence_blocks,
			token_count_input,
			token_count_output
		)
		SELECT c.id, $2, $3, $4, $5, $6, $7, $8, $9
		FROM skipper.skipper_conversations c
		WHERE c.id = $1 AND c.tenant_id = $10
		RETURNING id`,
		conversationID,
		role,
		content,
		confidence,
		sourcesValue,
		toolsValue,
		blocksValue,
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
	tenantID := skipper.GetTenantID(ctx)
	if tenantID == "" {
		return Conversation{}, fmt.Errorf("tenant ID is required")
	}
	userID := skipper.GetUserID(ctx)

	query := `SELECT id, tenant_id, user_id, title, COALESCE(summary, ''), created_at, updated_at
		 FROM skipper.skipper_conversations
		 WHERE id = $1 AND tenant_id = $2`
	args := []any{conversationID, tenantID}
	if userID != "" {
		query += " AND user_id = $3"
		args = append(args, userID)
	}

	var convo Conversation
	var title sql.NullString
	var uid sql.NullString
	err := s.db.QueryRowContext(ctx, query, args...).Scan(
		&convo.ID,
		&convo.TenantID,
		&uid,
		&title,
		&convo.Summary,
		&convo.CreatedAt,
		&convo.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Conversation{}, ErrConversationNotFound
	}
	if err != nil {
		return Conversation{}, fmt.Errorf("get conversation: %w", err)
	}

	convo.UserID = uid.String
	convo.Title = title.String

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

	query := `SELECT
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
		WHERE c.tenant_id = $1`
	args := []any{tenantID}
	argIdx := 2

	if userID != "" {
		query += fmt.Sprintf(" AND c.user_id = $%d", argIdx)
		args = append(args, userID)
		argIdx++
	}

	query += fmt.Sprintf(` GROUP BY c.id, c.tenant_id, c.user_id, c.title, c.created_at, c.updated_at
		ORDER BY COALESCE(MAX(m.created_at), c.created_at) DESC
		LIMIT $%d OFFSET $%d`, argIdx, argIdx+1)
	args = append(args, limit, offset)

	rows, err := s.db.QueryContext(ctx, query, args...)
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

func (s *ConversationStore) UpdateTitle(ctx context.Context, conversationID, title string) error {
	tenantID := skipper.GetTenantID(ctx)
	if tenantID == "" {
		return fmt.Errorf("tenant ID is required")
	}
	userID := skipper.GetUserID(ctx)

	query := `UPDATE skipper.skipper_conversations
		 SET title = $1, updated_at = NOW()
		 WHERE id = $2 AND tenant_id = $3`
	args := []any{title, conversationID, tenantID}
	if userID != "" {
		query += " AND user_id = $4"
		args = append(args, userID)
	}

	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("update title: %w", err)
	}
	rows, _ := result.RowsAffected()
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

	delMsgQuery := `DELETE FROM skipper.skipper_messages
		 WHERE conversation_id = $1
		   AND conversation_id IN (
		     SELECT id FROM skipper.skipper_conversations WHERE tenant_id = $2`
	delMsgArgs := []any{conversationID, tenantID}
	if userID != "" {
		delMsgQuery += " AND user_id = $3"
		delMsgArgs = append(delMsgArgs, userID)
	}
	delMsgQuery += ")"

	if _, execErr := tx.ExecContext(ctx, delMsgQuery, delMsgArgs...); execErr != nil {
		return fmt.Errorf("delete messages: %w", execErr)
	}

	delConvQuery := `DELETE FROM skipper.skipper_conversations
		 WHERE id = $1 AND tenant_id = $2`
	delConvArgs := []any{conversationID, tenantID}
	if userID != "" {
		delConvQuery += " AND user_id = $3"
		delConvArgs = append(delConvArgs, userID)
	}

	result, err := tx.ExecContext(ctx, delConvQuery, delConvArgs...)
	if err != nil {
		return fmt.Errorf("delete conversation: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
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
	query := `SELECT
		m.id,
		m.conversation_id,
		m.role,
		m.content,
		m.confidence,
		COALESCE(m.sources, 'null'),
		COALESCE(m.tools_used, 'null'),
		COALESCE(m.confidence_blocks, 'null'),
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
			&message.ConfidenceBlocks,
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

func (s *ConversationStore) GetSummary(ctx context.Context, conversationID string) (string, error) {
	tenantID := skipper.GetTenantID(ctx)
	if tenantID == "" {
		return "", fmt.Errorf("tenant ID is required")
	}
	var summary sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT summary FROM skipper.skipper_conversations WHERE id = $1 AND tenant_id = $2`,
		conversationID, tenantID,
	).Scan(&summary)
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
	_, err := s.db.ExecContext(ctx,
		`UPDATE skipper.skipper_conversations SET summary = $1, updated_at = NOW() WHERE id = $2 AND tenant_id = $3`,
		summary, conversationID, tenantID,
	)
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
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM skipper.skipper_messages m
		 JOIN skipper.skipper_conversations c ON m.conversation_id = c.id
		 WHERE m.conversation_id = $1 AND c.tenant_id = $2`,
		conversationID, tenantID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("message count: %w", err)
	}
	return count, nil
}

func normalizeJSONInput(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage("null")
	}
	return value
}
