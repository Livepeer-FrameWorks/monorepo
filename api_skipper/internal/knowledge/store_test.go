package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestStoreSearch(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	store := NewStore(db)
	metadata := map[string]any{"title": "Example"}
	metadataBytes, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}

	rows := sqlmock.NewRows([]string{
		"id",
		"tenant_id",
		"source_url",
		"source_title",
		"chunk_text",
		"chunk_index",
		"metadata",
		"similarity",
	}).AddRow(
		"id",
		"tenant",
		"https://example.com",
		"Title",
		"chunk",
		1,
		metadataBytes,
		0.99,
	)

	mock.ExpectQuery("SELECT id").WithArgs("tenant", sqlmock.AnyArg(), 2).WillReturnRows(rows)

	results, err := store.Search(context.Background(), "tenant", []float32{0.1, 0.2}, 2)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Metadata["title"] != "Example" {
		t.Fatalf("unexpected metadata: %v", results[0].Metadata)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestStoreUpsert(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	store := NewStore(db)

	chunks := []Chunk{
		{
			TenantID:    "tenant",
			SourceURL:   "https://example.com",
			SourceTitle: "Title",
			Text:        "chunk one",
			Index:       0,
			Embedding:   []float32{0.1},
			Metadata:    map[string]any{"section": "one"},
		},
		{
			TenantID:    "tenant",
			SourceURL:   "https://example.com",
			SourceTitle: "Title",
			Text:        "chunk two",
			Index:       1,
			Embedding:   []float32{0.2},
			Metadata:    map[string]any{"section": "two"},
		},
	}

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM skipper\\.skipper_knowledge").WithArgs("tenant", "https://example.com").WillReturnResult(sqlmock.NewResult(1, 2))
	mock.ExpectPrepare("INSERT INTO skipper\\.skipper_knowledge")
	mock.ExpectExec("INSERT INTO skipper\\.skipper_knowledge").WithArgs(
		"tenant",
		"https://example.com",
		"Title",
		"chunk one",
		0,
		sqlmock.AnyArg(),
		sqlmock.AnyArg(),
	).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO skipper\\.skipper_knowledge").WithArgs(
		"tenant",
		"https://example.com",
		"Title",
		"chunk two",
		1,
		sqlmock.AnyArg(),
		sqlmock.AnyArg(),
	).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	if err := store.Upsert(context.Background(), chunks); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestStoreDeleteBySource(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	store := NewStore(db)

	mock.ExpectExec("DELETE FROM skipper\\.skipper_knowledge").WithArgs("https://example.com").WillReturnResult(sqlmock.NewResult(1, 1))

	if err := store.DeleteBySource(context.Background(), "https://example.com"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestStoreSearchRequiresTenant(t *testing.T) {
	store := NewStore(&sql.DB{})
	if _, err := store.Search(context.Background(), "", []float32{0.1}, 1); err == nil {
		t.Fatalf("expected error")
	}
}
