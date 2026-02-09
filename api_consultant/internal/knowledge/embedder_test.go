package knowledge

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeEmbeddingClient struct {
	vectors [][]float32
}

func (f fakeEmbeddingClient) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	if len(f.vectors) == len(inputs) {
		return f.vectors, nil
	}
	vectors := make([][]float32, 0, len(inputs))
	for i := range inputs {
		vectors = append(vectors, []float32{float32(i)})
	}
	return vectors, nil
}

func TestEmbedderChunksDocument(t *testing.T) {
	embedder, err := NewEmbedder(fakeEmbeddingClient{}, WithTokenLimit(7), WithTokenOverlap(3))
	if err != nil {
		t.Fatalf("new embedder: %v", err)
	}

	content := "one two three four five six seven eight nine ten eleven twelve"
	chunks, err := embedder.EmbedDocument(context.Background(), "https://example.com", "Title", content)
	if err != nil {
		t.Fatalf("embed document: %v", err)
	}
	if len(chunks) != 4 {
		t.Fatalf("expected 4 chunks, got %d", len(chunks))
	}
	for i, chunk := range chunks {
		if chunk.Index != i {
			t.Fatalf("expected chunk index %d, got %d", i, chunk.Index)
		}
		if chunk.Text == "" {
			t.Fatalf("expected chunk text")
		}
	}
}

func TestEmbedderQuery(t *testing.T) {
	client := fakeEmbeddingClient{vectors: [][]float32{{0.5}}}
	embedder, err := NewEmbedder(client)
	if err != nil {
		t.Fatalf("new embedder: %v", err)
	}

	vector, err := embedder.EmbedQuery(context.Background(), "hello")
	if err != nil {
		t.Fatalf("embed query: %v", err)
	}
	if len(vector) != 1 || vector[0] != 0.5 {
		t.Fatalf("unexpected vector: %v", vector)
	}
}

func TestEmbedDocument_EmptyContentError(t *testing.T) {
	embedder, err := NewEmbedder(fakeEmbeddingClient{})
	if err != nil {
		t.Fatalf("new embedder: %v", err)
	}
	_, err = embedder.EmbedDocument(context.Background(), "https://example.com", "Title", "")
	if err == nil {
		t.Fatal("expected error for empty content")
	}
}

func TestEmbedDocument_AllChunksBelowMinTokens(t *testing.T) {
	embedder, err := NewEmbedder(fakeEmbeddingClient{})
	if err != nil {
		t.Fatalf("new embedder: %v", err)
	}
	// 5 words * 1.3 = 6.5 → 7 tokens, well below minChunkTokens (20)
	_, err = embedder.EmbedDocument(context.Background(), "https://example.com", "Title", "too few words here only")
	if !errors.Is(err, ErrNoChunks) {
		t.Fatalf("expected ErrNoChunks, got %v", err)
	}
}

func TestEmbedDocument_NavigationChunksFiltered(t *testing.T) {
	embedder, err := NewEmbedder(fakeEmbeddingClient{})
	if err != nil {
		t.Fatalf("new embedder: %v", err)
	}
	// 20 words, but >50% are ≤3 chars → navigation chunk
	nav := "go to the top of the my app for all the new hot big red one two old set"
	if estimateBPETokens(nav) < minChunkTokens {
		t.Fatalf("test content has %d tokens, need >= %d", estimateBPETokens(nav), minChunkTokens)
	}
	_, err = embedder.EmbedDocument(context.Background(), "https://example.com", "Title", nav)
	if !errors.Is(err, ErrNoChunks) {
		t.Fatalf("expected ErrNoChunks for navigation content, got %v", err)
	}
}

func TestEmbedDocument_DuplicateChunksDeduped(t *testing.T) {
	embedder, err := NewEmbedder(fakeEmbeddingClient{}, WithTokenLimit(30), WithTokenOverlap(0))
	if err != nil {
		t.Fatalf("new embedder: %v", err)
	}
	// Two identical paragraphs separated by blank line → two blocks, deduped to one chunk
	para := "This is a sufficiently long paragraph that has enough words to survive the minimum token threshold easily"
	content := para + "\n\n" + para
	chunks, err := embedder.EmbedDocument(context.Background(), "https://example.com", "Title", content)
	if err != nil {
		t.Fatalf("embed document: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk after dedup, got %d", len(chunks))
	}
}

func TestEmbedDocument_MixedFilterRetainsValid(t *testing.T) {
	// Use a small token limit so each block becomes its own chunk
	embedder, err := NewEmbedder(fakeEmbeddingClient{}, WithTokenLimit(30), WithTokenOverlap(0))
	if err != nil {
		t.Fatalf("new embedder: %v", err)
	}
	// Block 1: too short → filtered by minChunkTokens
	// Block 2: valid paragraph (>20 tokens, not navigation)
	// Block 3: navigation-like → filtered by isNavigationChunk
	content := "tiny\n\n" +
		"This is a proper paragraph with enough meaningful content to survive all the filtering stages applied by the embedder\n\n" +
		"go to the top of the my app for all the new hot big red one two old set"
	chunks, err := embedder.EmbedDocument(context.Background(), "https://example.com", "Title", content)
	if err != nil {
		t.Fatalf("embed document: %v", err)
	}
	foundValid := false
	for _, c := range chunks {
		if strings.Contains(c.Text, "proper paragraph") {
			foundValid = true
		}
	}
	if !foundValid {
		t.Fatalf("expected valid paragraph to survive among %d chunks", len(chunks))
	}
}

func TestEmbedDocument_MinTokensBypassForSmallLimit(t *testing.T) {
	// When tokenLimit < minChunkTokens, the min threshold drops to 1
	embedder, err := NewEmbedder(fakeEmbeddingClient{}, WithTokenLimit(5), WithTokenOverlap(0))
	if err != nil {
		t.Fatalf("new embedder: %v", err)
	}
	// 3 words → 4 tokens, below normal min (20) but above bypass min (1)
	chunks, err := embedder.EmbedDocument(context.Background(), "https://example.com", "Title", "three words here")
	if err != nil {
		t.Fatalf("expected success with small token limit, got %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected at least 1 chunk with small token limit bypass")
	}
}

func TestSplitBlocks_HeadingPrefix(t *testing.T) {
	content := "# Main Heading\n\nFirst paragraph text here.\n\n## Sub Heading\n\nSecond paragraph text here."
	blocks := splitBlocks(content)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d: %v", len(blocks), blocks)
	}
	if !strings.HasPrefix(blocks[0], "# Main Heading") {
		t.Fatalf("expected first block to start with heading, got %q", blocks[0])
	}
	if !strings.HasPrefix(blocks[1], "## Sub Heading") {
		t.Fatalf("expected second block to start with sub heading, got %q", blocks[1])
	}
	if !strings.Contains(blocks[0], "First paragraph") {
		t.Fatalf("expected first block to contain paragraph, got %q", blocks[0])
	}
}

func TestSplitBlocks_EmptyLines(t *testing.T) {
	content := "Block one.\n\n\n\nBlock two.\n\nBlock three."
	blocks := splitBlocks(content)
	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks, got %d: %v", len(blocks), blocks)
	}
	if strings.TrimSpace(blocks[0]) != "Block one." {
		t.Fatalf("unexpected first block: %q", blocks[0])
	}
}

func TestSplitLargeBlock(t *testing.T) {
	words := make([]string, 20)
	for i := range words {
		words[i] = "word"
	}
	chunks := splitLargeBlock(words, 8, 2)
	// step = 8 - 2 = 6, so chunks start at 0, 6, 12, 18
	if len(chunks) < 3 {
		t.Fatalf("expected at least 3 chunks, got %d", len(chunks))
	}
	for _, chunk := range chunks {
		wordCount := len(strings.Fields(chunk))
		if wordCount > 8 {
			t.Fatalf("chunk exceeds limit: %d words", wordCount)
		}
	}
}

func TestSplitLargeBlock_ZeroLimit(t *testing.T) {
	chunks := splitLargeBlock([]string{"a", "b"}, 0, 0)
	if len(chunks) != 0 {
		t.Fatalf("expected 0 chunks for zero limit, got %d", len(chunks))
	}
}

func TestOverlapTokens(t *testing.T) {
	text := "alpha bravo charlie delta echo"

	got := overlapTokens(text, 2)
	if got != "delta echo" {
		t.Fatalf("expected last 2 words, got %q", got)
	}

	got = overlapTokens(text, 0)
	if got != "" {
		t.Fatalf("expected empty for overlap=0, got %q", got)
	}

	got = overlapTokens(text, 100)
	if got != text {
		t.Fatalf("expected full text when overlap > word count, got %q", got)
	}
}

func TestEstimateBPETokens(t *testing.T) {
	tests := []struct {
		text     string
		expected int
	}{
		{"", 0},
		{"hello", 2},       // 1 * 1.3 = 1.3 → ceil = 2
		{"hello world", 3}, // 2 * 1.3 = 2.6 → ceil = 3
		{"one two three four five six seven", 10}, // 7 * 1.3 = 9.1 → ceil = 10
	}
	for _, tt := range tests {
		got := estimateBPETokens(tt.text)
		if got != tt.expected {
			t.Errorf("estimateBPETokens(%q) = %d, want %d", tt.text, got, tt.expected)
		}
	}
}

func TestIsNavigationChunk(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{"all short words", "go to the top of the my app for all", true},
		{"all long words", "authentication documentation configuration implementation verification", false},
		{"fewer than 5 words", "go to it", false},
		{"exactly at boundary", "go to the long documentation really", false}, // 2/6 = 33% ≤ 50%
		{"just over boundary", "go to the run documentation act", true},       // 4/6 = 66% > 50%
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isNavigationChunk(tt.text)
			if got != tt.want {
				t.Errorf("isNavigationChunk(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

func TestSplitBlocks_CodeFences(t *testing.T) {
	content := "Intro paragraph here.\n\n```\ncode line 1\ncode line 2\n```\n\nAfter the code block."
	blocks := splitBlocks(content)
	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks, got %d: %v", len(blocks), blocks)
	}
	if !strings.Contains(blocks[0], "Intro paragraph") {
		t.Fatalf("expected intro in block 0, got %q", blocks[0])
	}
	if !strings.Contains(blocks[1], "code line 1") {
		t.Fatalf("expected code in block 1, got %q", blocks[1])
	}
	if !strings.Contains(blocks[2], "After the code") {
		t.Fatalf("expected after-text in block 2, got %q", blocks[2])
	}
}

func TestSplitBlocks_TildeFences(t *testing.T) {
	content := "Before.\n\n~~~\nfenced with tildes\n~~~\n\nAfter."
	blocks := splitBlocks(content)
	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks, got %d: %v", len(blocks), blocks)
	}
	if !strings.Contains(blocks[1], "fenced with tildes") {
		t.Fatalf("expected tilde-fenced content in block 1, got %q", blocks[1])
	}
}

func TestSplitBlocks_HorizontalRules(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"dashes", "Section one.\n\n---\n\nSection two."},
		{"asterisks", "Section one.\n\n***\n\nSection two."},
		{"underscores", "Section one.\n\n___\n\nSection two."},
		{"dashes with spaces", "Section one.\n\n- - -\n\nSection two."},
		{"long dashes", "Section one.\n\n----------\n\nSection two."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blocks := splitBlocks(tt.content)
			if len(blocks) != 2 {
				t.Fatalf("expected 2 blocks, got %d: %v", len(blocks), blocks)
			}
			if !strings.Contains(blocks[0], "Section one") {
				t.Fatalf("expected section one in block 0, got %q", blocks[0])
			}
			if !strings.Contains(blocks[1], "Section two") {
				t.Fatalf("expected section two in block 1, got %q", blocks[1])
			}
		})
	}
}

func TestSplitBlocks_HTMLBlockTags(t *testing.T) {
	content := "Before div.\n<div class=\"wrapper\">\nInside div.\n</div>\nAfter div."
	blocks := splitBlocks(content)
	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks, got %d: %v", len(blocks), blocks)
	}
	if !strings.Contains(blocks[0], "Before div") {
		t.Fatalf("expected before-div in block 0, got %q", blocks[0])
	}
	if !strings.Contains(blocks[1], "Inside div") {
		t.Fatalf("expected inside-div in block 1, got %q", blocks[1])
	}
	if !strings.Contains(blocks[2], "After div") {
		t.Fatalf("expected after-div in block 2, got %q", blocks[2])
	}
}

func TestSplitBlocks_NoHeadingsOrBlanks(t *testing.T) {
	// Content with no blank lines or headings but has code fences → should still split
	content := "Text before code.\n```\nsome code here\n```\nText after code."
	blocks := splitBlocks(content)
	if len(blocks) < 2 {
		t.Fatalf("expected at least 2 blocks from code fence split, got %d: %v", len(blocks), blocks)
	}
}

func TestIsHorizontalRule(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		{"---", true},
		{"***", true},
		{"___", true},
		{"- - -", true},
		{"----------", true},
		{"-- ", false}, // only 2 chars
		{"--a", false}, // mixed chars
		{"# heading", false},
		{"some text", false},
		{"", false},
	}
	for _, tt := range tests {
		got := isHorizontalRule(tt.line)
		if got != tt.want {
			t.Errorf("isHorizontalRule(%q) = %v, want %v", tt.line, got, tt.want)
		}
	}
}

func TestEnforceCharLimit(t *testing.T) {
	t.Run("short chunks pass through", func(t *testing.T) {
		input := []string{"hello world", "foo bar baz"}
		result := enforceCharLimit(input, 100)
		if len(result) != 2 {
			t.Fatalf("expected 2 chunks, got %d", len(result))
		}
	})

	t.Run("long chunk split at word boundaries", func(t *testing.T) {
		// Create a chunk of ~100 chars from 10-char words
		words := make([]string, 20)
		for i := range words {
			words[i] = "abcdefghij"
		}
		longChunk := strings.Join(words, " ") // 20*10 + 19 spaces = 219 chars
		result := enforceCharLimit([]string{longChunk}, 100)
		if len(result) < 2 {
			t.Fatalf("expected at least 2 chunks from splitting, got %d", len(result))
		}
		for _, chunk := range result {
			if len(chunk) > 100 {
				t.Errorf("chunk exceeds limit: %d chars", len(chunk))
			}
		}
	})

	t.Run("single huge word stays as one chunk", func(t *testing.T) {
		blob := strings.Repeat("x", 50000)
		result := enforceCharLimit([]string{blob}, 24000)
		// A single word can't be split at word boundaries, so it stays as-is
		if len(result) != 1 {
			t.Fatalf("expected 1 chunk for unsplittable blob, got %d", len(result))
		}
	})
}

func TestChunkContent_CharLimitTriggered(t *testing.T) {
	embedder, err := NewEmbedder(fakeEmbeddingClient{}, WithTokenLimit(8000), WithTokenOverlap(0))
	if err != nil {
		t.Fatalf("new embedder: %v", err)
	}
	// Build content from many medium-length words that sum to >24000 chars.
	// Each word is 50 chars, so 600 words = 30000 chars + spaces.
	// Word-count estimate: 600 * 1.3 = 780 tokens → under 8000 token limit.
	// Char-count: ~30600 → over 24000 char limit → enforceCharLimit kicks in.
	var parts []string
	word := strings.Repeat("a", 50)
	for i := 0; i < 600; i++ {
		parts = append(parts, word)
	}
	content := strings.Join(parts, " ")
	chunks := embedder.chunkContent(content)
	if len(chunks) < 2 {
		t.Fatalf("expected char limit to split into 2+ chunks, got %d", len(chunks))
	}
	for _, chunk := range chunks {
		if len(chunk) > maxChunkChars {
			t.Errorf("chunk exceeds char limit: %d > %d", len(chunk), maxChunkChars)
		}
	}
}

func TestNormalizeForDedup(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Hello  World", "hello world"},
		{"  spaces   everywhere  ", "spaces everywhere"},
		{"UPPER case MIX", "upper case mix"},
		{"already normalized", "already normalized"},
	}
	for _, tt := range tests {
		got := normalizeForDedup(tt.input)
		if got != tt.expected {
			t.Errorf("normalizeForDedup(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}
