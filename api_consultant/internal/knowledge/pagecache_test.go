package knowledge

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestPageCacheStoreGet(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	store := NewPageCacheStore(db)
	now := time.Now().UTC()

	rows := sqlmock.NewRows([]string{
		"tenant_id", "source_root", "page_url", "content_hash", "etag", "last_modified", "raw_size", "last_fetched_at",
	}).AddRow("tenant", "https://example.com/sitemap.xml", "https://example.com/page1", "abc123", "\"etag-1\"", "Mon, 01 Jan 2024 00:00:00 GMT", int64(4096), now)

	mock.ExpectQuery("SELECT tenant_id").WithArgs("tenant", "https://example.com/page1").WillReturnRows(rows)

	pc, err := store.Get(context.Background(), "tenant", "https://example.com/page1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if pc == nil {
		t.Fatal("expected non-nil page cache")
	}
	if pc.ContentHash != "abc123" {
		t.Fatalf("expected content hash abc123, got %q", pc.ContentHash)
	}
	if pc.ETag != "\"etag-1\"" {
		t.Fatalf("expected etag, got %q", pc.ETag)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestPageCacheStoreGetNotFound(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	store := NewPageCacheStore(db)

	mock.ExpectQuery("SELECT tenant_id").WithArgs("tenant", "https://example.com/missing").WillReturnError(sql.ErrNoRows)

	pc, err := store.Get(context.Background(), "tenant", "https://example.com/missing")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if pc != nil {
		t.Fatalf("expected nil, got %+v", pc)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestPageCacheStoreUpsert(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	store := NewPageCacheStore(db)
	now := time.Now().UTC()

	mock.ExpectExec("INSERT INTO skipper\\.skipper_page_cache").WithArgs(
		"tenant", "https://example.com/sitemap.xml", "https://example.com/page1",
		sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), now,
	).WillReturnResult(sqlmock.NewResult(1, 1))

	err = store.Upsert(context.Background(), PageCache{
		TenantID:      "tenant",
		SourceRoot:    "https://example.com/sitemap.xml",
		PageURL:       "https://example.com/page1",
		ContentHash:   "abc123",
		ETag:          "\"etag-1\"",
		LastFetchedAt: now,
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestPageCacheStoreUpsertValidation(t *testing.T) {
	store := NewPageCacheStore(&sql.DB{})
	if err := store.Upsert(context.Background(), PageCache{PageURL: "url"}); err == nil {
		t.Fatal("expected error for missing tenant id")
	}
	if err := store.Upsert(context.Background(), PageCache{TenantID: "t"}); err == nil {
		t.Fatal("expected error for missing page url")
	}
}

func TestPageCacheStoreLastFetchedForSource(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	store := NewPageCacheStore(db)
	now := time.Now().UTC()

	rows := sqlmock.NewRows([]string{"max"}).AddRow(now)
	mock.ExpectQuery("SELECT MAX").WithArgs("tenant", "https://example.com/sitemap.xml").WillReturnRows(rows)

	result, err := store.LastFetchedForSource(context.Background(), "tenant", "https://example.com/sitemap.xml")
	if err != nil {
		t.Fatalf("last fetched: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil time")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestPageCacheStoreLastFetchedForSourceNone(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	store := NewPageCacheStore(db)

	rows := sqlmock.NewRows([]string{"max"}).AddRow(nil)
	mock.ExpectQuery("SELECT MAX").WithArgs("tenant", "https://example.com/sitemap.xml").WillReturnRows(rows)

	result, err := store.LastFetchedForSource(context.Background(), "tenant", "https://example.com/sitemap.xml")
	if err != nil {
		t.Fatalf("last fetched: %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil, got %v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestPageCacheStoreDeleteBySource(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	store := NewPageCacheStore(db)

	mock.ExpectExec("DELETE FROM skipper\\.skipper_page_cache").WithArgs("tenant", "https://example.com/sitemap.xml").WillReturnResult(sqlmock.NewResult(1, 2))

	if err := store.DeleteBySource(context.Background(), "tenant", "https://example.com/sitemap.xml"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
