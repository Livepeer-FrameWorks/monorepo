package knowledge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeStore struct {
	upserted [][]Chunk
}

func (f *fakeStore) Upsert(_ context.Context, chunks []Chunk) error {
	f.upserted = append(f.upserted, chunks)
	return nil
}

func (f *fakeStore) DeleteBySource(_ context.Context, _ string) error {
	return nil
}

type fakeEmbedder struct {
	calls int
}

func (f *fakeEmbedder) EmbedDocument(_ context.Context, url, title, content string) ([]Chunk, error) {
	f.calls++
	return []Chunk{{SourceURL: url, SourceTitle: title, Text: content, Index: 0, Embedding: []float32{1}}}, nil
}

func TestCrawlerSitemapAndFetch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("User-agent: *\nCrawl-delay: 0"))
		case "/sitemap.xml":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<sitemapindex>
  <sitemap><loc>` + server.URL + `/sitemap-1.xml</loc></sitemap>
</sitemapindex>`))
		case "/sitemap-1.xml":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<urlset>
  <url><loc>` + server.URL + `/page1.html</loc></url>
  <url><loc>` + server.URL + `/page2.html</loc></url>
</urlset>`))
		case "/page1.html":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<!doctype html><html><head><title>Page One</title></head><body><h1>Heading One</h1><p>Body copy.</p></body></html>`))
		case "/page2.html":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<!doctype html><html><head><title>Page Two</title></head><body><p>Second page.</p></body></html>`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	store := &fakeStore{}
	embedder := &fakeEmbedder{}
	crawler, err := NewCrawler(server.Client(), embedder, store)
	if err != nil {
		t.Fatalf("new crawler: %v", err)
	}

	pages, err := crawler.CrawlSitemap(context.Background(), server.URL+"/sitemap.xml")
	if err != nil {
		t.Fatalf("crawl sitemap: %v", err)
	}
	if len(pages) != 2 {
		t.Fatalf("expected 2 pages, got %d", len(pages))
	}

	title, content, err := crawler.FetchPage(context.Background(), server.URL+"/page1.html")
	if err != nil {
		t.Fatalf("fetch page: %v", err)
	}
	if title != "Page One" {
		t.Fatalf("expected title, got %q", title)
	}
	if content == "" {
		t.Fatalf("expected content")
	}
}

func TestCrawlerCrawlAndEmbed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("User-agent: *\nCrawl-delay: 0"))
		case "/sitemap.xml":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<urlset>
  <url><loc>` + server.URL + `/page1.html</loc></url>
</urlset>`))
		case "/page1.html":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<!doctype html><html><head><title>Page</title></head><body><p>Body copy.</p></body></html>`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	store := &fakeStore{}
	embedder := &fakeEmbedder{}
	crawler, err := NewCrawler(server.Client(), embedder, store)
	if err != nil {
		t.Fatalf("new crawler: %v", err)
	}

	if err := crawler.CrawlAndEmbed(context.Background(), server.URL+"/sitemap.xml"); err != nil {
		t.Fatalf("crawl and embed: %v", err)
	}
	if embedder.calls != 1 {
		t.Fatalf("expected embedder to be called once, got %d", embedder.calls)
	}
	if len(store.upserted) != 1 {
		t.Fatalf("expected store upserted once, got %d", len(store.upserted))
	}
}
