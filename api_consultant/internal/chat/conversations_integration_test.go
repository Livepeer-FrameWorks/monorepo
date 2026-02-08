package chat

import (
	"context"
	"testing"
	"time"

	"frameworks/api_consultant/internal/skipper"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestConversationAddMessageScopesTenant(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	store := NewConversationStore(db)
	ctx := skipper.WithTenantID(context.Background(), "tenant-a")

	mock.ExpectQuery("INSERT INTO skipper\\.skipper_messages").WithArgs(
		"conversation-id",
		"assistant",
		"hello",
		"verified",
		sqlmock.AnyArg(),
		sqlmock.AnyArg(),
		2,
		3,
		"tenant-a",
	).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("message-id"))
	mock.ExpectExec("UPDATE skipper\\.skipper_conversations").WithArgs(
		"conversation-id",
		"tenant-a",
	).WillReturnResult(sqlmock.NewResult(1, 1))

	err = store.AddMessage(ctx, "conversation-id", "assistant", "hello", "verified", nil, nil, TokenCounts{Input: 2, Output: 3})
	if err != nil {
		t.Fatalf("add message: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestConversationGetConversationScopesUser(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	store := NewConversationStore(db)
	ctx := skipper.WithTenantID(context.Background(), "tenant-a")
	ctx = skipper.WithUserID(ctx, "user-a")

	mock.ExpectQuery("SELECT id, tenant_id, user_id, title, COALESCE\\(summary, ''\\), created_at, updated_at").
		WithArgs("conversation-id", "tenant-a", "user-a").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "tenant_id", "user_id", "title", "summary", "created_at", "updated_at",
		}).AddRow("conversation-id", "tenant-a", "user-a", "Title", "Summary", time.Now(), time.Now()))

	mock.ExpectQuery("SELECT m\\.id, m\\.conversation_id, m\\.role, m\\.content, m\\.confidence, m\\.sources, m\\.tools_used, m\\.token_count_input, m\\.token_count_output, m\\.created_at").
		WithArgs("conversation-id", "tenant-a").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "conversation_id", "role", "content", "confidence", "sources", "tools_used", "token_count_input", "token_count_output", "created_at",
		}))

	convo, err := store.GetConversation(ctx, "conversation-id")
	if err != nil {
		t.Fatalf("get conversation: %v", err)
	}
	if convo.UserID != "user-a" {
		t.Fatalf("expected user_id user-a, got %q", convo.UserID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestConversationUpdateTitleScopesUser(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	store := NewConversationStore(db)
	ctx := skipper.WithTenantID(context.Background(), "tenant-a")
	ctx = skipper.WithUserID(ctx, "user-a")

	mock.ExpectExec("UPDATE skipper\\.skipper_conversations").
		WithArgs("New Title", "conversation-id", "tenant-a", "user-a").
		WillReturnResult(sqlmock.NewResult(1, 1))

	if err := store.UpdateTitle(ctx, "conversation-id", "New Title"); err != nil {
		t.Fatalf("update title: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestConversationDeleteConversationScopesUser(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	store := NewConversationStore(db)
	ctx := skipper.WithTenantID(context.Background(), "tenant-a")
	ctx = skipper.WithUserID(ctx, "user-a")

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM skipper\\.skipper_messages").
		WithArgs("conversation-id", "tenant-a", "user-a").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("DELETE FROM skipper\\.skipper_conversations").
		WithArgs("conversation-id", "tenant-a", "user-a").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	if err := store.DeleteConversation(ctx, "conversation-id"); err != nil {
		t.Fatalf("delete conversation: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
