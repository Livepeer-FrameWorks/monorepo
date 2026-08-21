//go:build schema_verify

package knowledge

import (
	"context"
	"testing"
)

func TestKnowledgeRepository_RealPG(t *testing.T) {
	db := startSkipperPageCacheRealPG(t)
	store := NewStore(db)
	ctx := context.Background()
	tenantA := "10000000-0000-0000-0000-000000000001"
	tenantB := "20000000-0000-0000-0000-000000000002"
	embeddingA := make([]float32, 1536)
	embeddingA[0] = 1
	embeddingB := make([]float32, 1536)
	embeddingB[1] = 1

	chunks := []Chunk{
		{TenantID: tenantA, SourceURL: "https://example.test/docs/a", SourceTitle: "A", Text: "alpha streaming guide", Index: 0, Embedding: embeddingA, Metadata: map[string]any{"source_root": "https://example.test/docs", "source_type": "docs", "ingested_at": "2026-08-21T12:00:00Z"}},
		{TenantID: tenantA, SourceURL: "https://example.test/docs/a", SourceTitle: "A", Text: "alpha second chunk", Index: 1, Embedding: embeddingB, Metadata: map[string]any{"source_root": "https://example.test/docs", "source_type": "docs", "ingested_at": "2026-08-21T12:00:00Z"}},
		{TenantID: tenantA, SourceURL: "https://example.test/faq", SourceTitle: "FAQ", Text: "billing frequently asked question", Index: 0, Embedding: embeddingA, Metadata: map[string]any{"source_type": "faq"}},
	}
	if err := store.Upsert(ctx, chunks); err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(ctx, []Chunk{{TenantID: tenantB, SourceURL: "https://other.test/private", SourceTitle: "Private", Text: "private", Index: 0, Embedding: embeddingA}}); err != nil {
		t.Fatal(err)
	}

	rows, err := store.Search(ctx, tenantA, embeddingA, 10)
	if err != nil || len(rows) != 2 || rows[0].Similarity < 0.99 || rows[0].Metadata == nil {
		t.Fatalf("vector search = %#v, err = %v", rows, err)
	}
	hybrid, err := store.HybridSearch(ctx, tenantA, embeddingA, "streaming", 10)
	if err != nil || len(hybrid) == 0 || hybrid[0].TenantID != tenantA {
		t.Fatalf("hybrid search = %#v, err = %v", hybrid, err)
	}
	filtered, err := store.SearchFiltered(ctx, tenantA, embeddingA, "", 10, "docs")
	if err != nil || len(filtered) != 1 || filtered[0].SourceType != "docs" {
		t.Fatalf("filtered vector search = %#v, err = %v", filtered, err)
	}
	filteredHybrid, err := store.SearchFiltered(ctx, tenantA, embeddingA, "streaming", 10, "docs")
	if err != nil || len(filteredHybrid) != 1 || filteredHybrid[0].SourceType != "docs" {
		t.Fatalf("filtered hybrid search = %#v, err = %v", filteredHybrid, err)
	}
	sources, err := store.ListSources(ctx, tenantA)
	if err != nil || len(sources) != 2 || sources[0].SourceURL != "https://example.test/docs" || sources[0].PageCount != 1 || sources[0].LastCrawlAt == nil {
		t.Fatalf("sources = %#v, err = %v", sources, err)
	}

	if err := store.Upsert(ctx, []Chunk{{TenantID: tenantA, SourceURL: "https://example.test/docs/a", SourceTitle: "A2", Text: "replacement", Index: 0, Embedding: embeddingA, Metadata: map[string]any{"source_root": "https://example.test/docs", "source_type": "docs"}}}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM skipper.skipper_knowledge WHERE tenant_id = $1::uuid AND source_url = $2", tenantA, "https://example.test/docs/a").Scan(&count); err != nil || count != 1 {
		t.Fatalf("replacement chunk count = %d, err = %v", count, err)
	}
	if err := store.DeleteBySource(ctx, tenantA, "https://example.test/docs"); err != nil {
		t.Fatal(err)
	}
	if rows, err := store.Search(ctx, tenantB, embeddingA, 10); err != nil || len(rows) != 1 {
		t.Fatalf("other tenant search = %#v, err = %v", rows, err)
	}
}
