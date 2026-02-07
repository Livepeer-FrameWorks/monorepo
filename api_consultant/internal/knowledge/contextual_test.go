package knowledge

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"frameworks/pkg/llm"
)

type mockStream struct {
	chunks []llm.Chunk
	idx    int
	err    error
}

func (m *mockStream) Recv() (llm.Chunk, error) {
	if m.err != nil {
		return llm.Chunk{}, m.err
	}
	if m.idx >= len(m.chunks) {
		return llm.Chunk{}, io.EOF
	}
	c := m.chunks[m.idx]
	m.idx++
	return c, nil
}

func (m *mockStream) Close() error { return nil }

type mockProvider struct {
	stream *mockStream
	err    error
}

func (m *mockProvider) Complete(_ context.Context, _ []llm.Message, _ []llm.Tool) (llm.Stream, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.stream, nil
}

func TestSummarizeChunksSuccess(t *testing.T) {
	provider := &mockProvider{
		stream: &mockStream{
			chunks: []llm.Chunk{
				{Content: "1. Context for chunk one about auth.\n2. Context for chunk two about billing.\n"},
			},
		},
	}
	summarizer := NewLLMContextualSummarizer(provider)

	results, err := summarizer.SummarizeChunks(context.Background(), "Test Doc", "Some prefix text", []string{
		"Chunk one about authentication",
		"Chunk two about billing",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0] != "Context for chunk one about auth." {
		t.Fatalf("unexpected result[0]: %q", results[0])
	}
	if results[1] != "Context for chunk two about billing." {
		t.Fatalf("unexpected result[1]: %q", results[1])
	}
}

func TestSummarizeChunksEmpty(t *testing.T) {
	summarizer := NewLLMContextualSummarizer(&mockProvider{})
	results, err := summarizer.SummarizeChunks(context.Background(), "", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if results != nil {
		t.Fatalf("expected nil results, got %v", results)
	}
}

func TestSummarizeChunksProviderError(t *testing.T) {
	provider := &mockProvider{err: errors.New("provider down")}
	summarizer := NewLLMContextualSummarizer(provider)

	_, err := summarizer.SummarizeChunks(context.Background(), "Title", "Prefix", []string{"chunk"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, provider.err) {
		t.Fatalf("expected wrapped provider error, got: %v", err)
	}
}

func TestSummarizeChunksStreamError(t *testing.T) {
	provider := &mockProvider{
		stream: &mockStream{err: errors.New("stream broke")},
	}
	summarizer := NewLLMContextualSummarizer(provider)

	_, err := summarizer.SummarizeChunks(context.Background(), "Title", "Prefix", []string{"chunk"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestSummarizeChunksTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()
	time.Sleep(5 * time.Millisecond)

	provider := &mockProvider{err: ctx.Err()}
	summarizer := NewLLMContextualSummarizer(provider)

	_, err := summarizer.SummarizeChunks(ctx, "Title", "Prefix", []string{"chunk"})
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestSummarizeChunksStreamedInParts(t *testing.T) {
	provider := &mockProvider{
		stream: &mockStream{
			chunks: []llm.Chunk{
				{Content: "1. First "},
				{Content: "context.\n"},
				{Content: "2. Second context.\n"},
			},
		},
	}
	summarizer := NewLLMContextualSummarizer(provider)

	results, err := summarizer.SummarizeChunks(context.Background(), "Title", "Prefix", []string{"a", "b"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if results[0] != "First context." {
		t.Fatalf("unexpected result[0]: %q", results[0])
	}
	if results[1] != "Second context." {
		t.Fatalf("unexpected result[1]: %q", results[1])
	}
}

func TestParseNumberedLines(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected int
		want     []string
	}{
		{
			name:     "numbered with dots",
			input:    "1. First\n2. Second\n3. Third",
			expected: 3,
			want:     []string{"First", "Second", "Third"},
		},
		{
			name:     "numbered with colons",
			input:    "1: Alpha\n2: Beta",
			expected: 2,
			want:     []string{"Alpha", "Beta"},
		},
		{
			name:     "numbered with parens",
			input:    "1) Foo\n2) Bar",
			expected: 2,
			want:     []string{"Foo", "Bar"},
		},
		{
			name:     "fewer lines than expected",
			input:    "1. Only one",
			expected: 3,
			want:     []string{"Only one", "", ""},
		},
		{
			name:     "more lines than expected",
			input:    "1. A\n2. B\n3. C\n4. D",
			expected: 2,
			want:     []string{"A", "B"},
		},
		{
			name:     "blank lines skipped",
			input:    "1. A\n\n\n2. B",
			expected: 2,
			want:     []string{"A", "B"},
		},
		{
			name:     "no number prefix",
			input:    "Just plain text\nAnother line",
			expected: 2,
			want:     []string{"Just plain text", "Another line"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseNumberedLines(tt.input, tt.expected)
			if len(got) != len(tt.want) {
				t.Fatalf("length mismatch: got %d, want %d", len(got), len(tt.want))
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("index %d: got %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestStripNumberPrefix(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"1. Hello", "Hello"},
		{"12: World", "World"},
		{"3) Foo", "Foo"},
		{"No prefix", "No prefix"},
		{"1.  Extra spaces", "Extra spaces"},
		{"", ""},
	}
	for _, tt := range tests {
		got := stripNumberPrefix(tt.input)
		if got != tt.want {
			t.Errorf("stripNumberPrefix(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestTruncateWords(t *testing.T) {
	tests := []struct {
		input string
		max   int
		want  string
	}{
		{"one two three four five", 3, "one two three..."},
		{"short", 10, "short"},
		{"", 5, ""},
		{"a b c", 3, "a b c"},
		{"a b c d", 3, "a b c..."},
	}
	for _, tt := range tests {
		got := truncateWords(tt.input, tt.max)
		if got != tt.want {
			t.Errorf("truncateWords(%q, %d) = %q, want %q", tt.input, tt.max, got, tt.want)
		}
	}
}
