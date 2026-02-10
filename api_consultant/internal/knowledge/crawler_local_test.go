package knowledge

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCrawlLocalFilesEmbedsMarkdown(t *testing.T) {
	dir := t.TempDir()
	mdPath := filepath.Join(dir, "test-faq.md")
	if err := os.WriteFile(mdPath, []byte("# Test FAQ\n\nThis is a test FAQ about streaming bitrate recommendations.\n"), 0644); err != nil {
		t.Fatal(err)
	}

	store := &fakeStore{}
	embedder := &fakeEmbedder{}
	crawler, err := NewCrawler(nil, embedder, store, withSkipURLValidation())
	if err != nil {
		t.Fatalf("new crawler: %v", err)
	}

	if err := crawler.CrawlLocalFiles(context.Background(), "tenant", []string{mdPath}); err != nil {
		t.Fatalf("crawl local files: %v", err)
	}

	if embedder.calls != 1 {
		t.Fatalf("expected 1 embed call, got %d", embedder.calls)
	}
	if len(store.upserted) != 1 {
		t.Fatalf("expected 1 upsert call, got %d", len(store.upserted))
	}
	if store.upserted[0][0].SourceTitle != "Test FAQ" {
		t.Fatalf("expected title 'Test FAQ', got %q", store.upserted[0][0].SourceTitle)
	}
}

func TestCrawlLocalFilesTitleFromFilename(t *testing.T) {
	dir := t.TempDir()
	mdPath := filepath.Join(dir, "bitrate-recommendations.md")
	if err := os.WriteFile(mdPath, []byte("No heading here, just content about bitrates.\n"), 0644); err != nil {
		t.Fatal(err)
	}

	store := &fakeStore{}
	embedder := &fakeEmbedder{}
	crawler, err := NewCrawler(nil, embedder, store, withSkipURLValidation())
	if err != nil {
		t.Fatalf("new crawler: %v", err)
	}

	if err := crawler.CrawlLocalFiles(context.Background(), "tenant", []string{mdPath}); err != nil {
		t.Fatalf("crawl local files: %v", err)
	}

	if embedder.calls != 1 {
		t.Fatalf("expected 1 embed call, got %d", embedder.calls)
	}
	if store.upserted[0][0].SourceTitle != "bitrate recommendations" {
		t.Fatalf("expected title 'bitrate recommendations', got %q", store.upserted[0][0].SourceTitle)
	}
}

func TestCrawlLocalFilesSkipsMissing(t *testing.T) {
	store := &fakeStore{}
	embedder := &fakeEmbedder{}
	crawler, err := NewCrawler(nil, embedder, store, withSkipURLValidation())
	if err != nil {
		t.Fatalf("new crawler: %v", err)
	}

	if err := crawler.CrawlLocalFiles(context.Background(), "tenant", []string{"/nonexistent/path.md"}); err != nil {
		t.Fatalf("crawl local files should not error on missing file: %v", err)
	}

	if embedder.calls != 0 {
		t.Fatalf("expected 0 embed calls for missing file, got %d", embedder.calls)
	}
}

func TestCrawlLocalFilesEmptyTenantID(t *testing.T) {
	crawler, err := NewCrawler(nil, &fakeEmbedder{}, &fakeStore{}, withSkipURLValidation())
	if err != nil {
		t.Fatalf("new crawler: %v", err)
	}

	if err := crawler.CrawlLocalFiles(context.Background(), "", []string{"file.md"}); err == nil {
		t.Fatal("expected error for empty tenant ID")
	}
}

func TestCrawlLocalFilesEmptyList(t *testing.T) {
	crawler, err := NewCrawler(nil, &fakeEmbedder{}, &fakeStore{}, withSkipURLValidation())
	if err != nil {
		t.Fatalf("new crawler: %v", err)
	}

	if err := crawler.CrawlLocalFiles(context.Background(), "tenant", nil); err != nil {
		t.Fatalf("expected no error for empty list: %v", err)
	}
}
