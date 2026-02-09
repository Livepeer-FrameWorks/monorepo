package knowledge

import (
	"strings"
	"testing"
)

func TestExtractContent_ReadabilityFallback(t *testing.T) {
	// Readability produces < 50 words, so should fall back to DOM walker
	html := `<!doctype html><html><head><title>Short Page</title></head>
	<body><p>Just a few words here.</p></body></html>`
	title, content := extractContent([]byte(html), "https://example.com/short")
	if title == "" {
		t.Fatal("expected a title")
	}
	if content == "" {
		t.Fatal("expected content from DOM walker fallback")
	}
}

func TestExtractContent_ThinPage(t *testing.T) {
	// Simulates a tag index page with very little prose
	html := `<!doctype html><html><head><title>Tags</title></head>
	<body>
		<nav><a href="/tag/a">A</a> <a href="/tag/b">B</a></nav>
		<ul><li>AAC 15</li><li>api 81</li><li>SRT 3</li></ul>
	</body></html>`
	_, content := extractContent([]byte(html), "https://example.com/tags")
	wordCount := len(strings.Fields(content))
	if wordCount > 20 {
		t.Fatalf("expected thin content (≤20 words), got %d words: %q", wordCount, content)
	}
}

func TestExtractContent_RichPage(t *testing.T) {
	html := `<!doctype html><html><head><title>Documentation Guide</title></head>
	<body>
		<article>
			<h1>Getting Started</h1>
			<p>This comprehensive guide walks you through the complete setup process
			for configuring your streaming infrastructure from scratch. You will learn
			about authentication, stream configuration, viewer routing, and analytics
			collection across all supported platforms and deployment targets.</p>
			<h2>Prerequisites</h2>
			<p>Before you begin, ensure you have installed all required dependencies
			including the command line tools, database drivers, and container runtime
			environment needed for local development and testing purposes.</p>
		</article>
	</body></html>`
	title, content := extractContent([]byte(html), "https://example.com/guide")
	if title == "" {
		t.Fatal("expected title")
	}
	wordCount := len(strings.Fields(content))
	if wordCount < 50 {
		t.Fatalf("expected rich content (≥50 words), got %d words", wordCount)
	}
}

func TestLooksLikeEmptyShell(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{"empty", "", true},
		{"few words", "Loading app", true},
		{"exactly at threshold", "one two three four five six seven eight nine ten", false},
		{"above threshold", "one two three four five six seven eight nine ten eleven", false},
		{"below threshold", "just a few words", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := looksLikeEmptyShell(tt.text)
			if got != tt.want {
				t.Errorf("looksLikeEmptyShell(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

func TestExtractPlainContent_MarkdownTitle(t *testing.T) {
	data := "# My Guide\n\nThis is the content of the guide with enough words."
	title, content := extractPlainContent([]byte(data), "https://example.com/guide.md")
	if title != "My Guide" {
		t.Fatalf("expected title 'My Guide', got %q", title)
	}
	if content == "" {
		t.Fatal("expected content")
	}
}

func TestExtractPlainContent_NoTitle(t *testing.T) {
	data := "Just plain text without any heading markers."
	title, content := extractPlainContent([]byte(data), "https://example.com/plain.txt")
	if title != "" {
		t.Fatalf("expected empty title, got %q", title)
	}
	if content == "" {
		t.Fatal("expected content")
	}
}
