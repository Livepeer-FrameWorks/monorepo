package chat

import (
	"context"
	"testing"

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
