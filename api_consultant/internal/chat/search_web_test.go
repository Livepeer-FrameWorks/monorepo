package chat

import (
	"context"
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/search"
)

// fakeSearchProvider records the options it was called with and returns canned results,
// so tests can assert what limit/depth the tool resolved before calling out.
type fakeSearchProvider struct {
	results  []search.Result
	err      error
	called   bool
	gotQuery string
	gotOpts  search.SearchOptions
}

func (f *fakeSearchProvider) Search(_ context.Context, query string, opts search.SearchOptions) ([]search.Result, error) {
	f.called = true
	f.gotQuery = query
	f.gotOpts = opts
	return f.results, f.err
}

func TestSearchWebTool_Call_NilProvider(t *testing.T) {
	tool := NewSearchWebTool(nil)
	if _, err := tool.Call(context.Background(), `{"query":"hi"}`); err == nil {
		t.Fatal("expected error for nil provider")
	}
}

func TestSearchWebTool_Call_BadJSON(t *testing.T) {
	tool := NewSearchWebTool(&fakeSearchProvider{})
	if _, err := tool.Call(context.Background(), `{not json`); err == nil {
		t.Fatal("expected error for malformed arguments")
	}
}

func TestSearchWebTool_EmptyQueryRejected(t *testing.T) {
	fp := &fakeSearchProvider{}
	tool := NewSearchWebTool(fp)
	if _, err := tool.Search(context.Background(), SearchWebInput{Query: "   "}); err == nil {
		t.Fatal("expected error for blank query")
	}
	if fp.called {
		t.Fatal("provider must not be called for a blank query")
	}
}

func TestSearchWebTool_LimitAndDepthResolution(t *testing.T) {
	cases := []struct {
		name      string
		setLimit  int // 0 => leave tool default (defaultSearchLimit)
		input     SearchWebInput
		wantLimit int
		wantDepth string
	}{
		{
			name:      "zero limit falls back to tool default",
			input:     SearchWebInput{Query: "q"},
			wantLimit: defaultSearchLimit,
			wantDepth: defaultSearchDepth,
		},
		{
			name:      "over-max limit clamps to maxSearchLimit",
			input:     SearchWebInput{Query: "q", Limit: 999},
			wantLimit: maxSearchLimit,
			wantDepth: defaultSearchDepth,
		},
		{
			name:      "in-range limit is preserved",
			input:     SearchWebInput{Query: "q", Limit: 5},
			wantLimit: 5,
			wantDepth: defaultSearchDepth,
		},
		{
			name:      "explicit depth is preserved",
			input:     SearchWebInput{Query: "q", SearchDepth: "advanced"},
			wantLimit: defaultSearchLimit,
			wantDepth: "advanced",
		},
		{
			name:      "SetSearchLimit overrides the default for zero-limit input",
			setLimit:  3,
			input:     SearchWebInput{Query: "q"},
			wantLimit: 3,
			wantDepth: defaultSearchDepth,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fp := &fakeSearchProvider{}
			tool := NewSearchWebTool(fp)
			if tc.setLimit > 0 {
				tool.SetSearchLimit(tc.setLimit)
			}
			if _, err := tool.Search(context.Background(), tc.input); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if fp.gotOpts.Limit != tc.wantLimit {
				t.Errorf("provider Limit = %d, want %d", fp.gotOpts.Limit, tc.wantLimit)
			}
			if fp.gotOpts.SearchDepth != tc.wantDepth {
				t.Errorf("provider SearchDepth = %q, want %q", fp.gotOpts.SearchDepth, tc.wantDepth)
			}
		})
	}
}

func TestSearchWebTool_ResultMapping(t *testing.T) {
	fp := &fakeSearchProvider{
		results: []search.Result{
			{Title: "  Titled  ", URL: "https://a.example", Content: "  spaced   content  ", Score: 0.9},
			{Title: "", URL: "https://b.example", Content: ""},
		},
	}
	tool := NewSearchWebTool(fp)

	resp, err := tool.Search(context.Background(), SearchWebInput{Query: "  trimmed query  "})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fp.gotQuery != "trimmed query" {
		t.Errorf("provider query = %q, want trimmed %q", fp.gotQuery, "trimmed query")
	}
	if resp.Query != "trimmed query" {
		t.Errorf("response query = %q, want trimmed", resp.Query)
	}
	if len(resp.Results) != 2 || len(resp.Sources) != 2 {
		t.Fatalf("expected 2 results and 2 sources, got %d/%d", len(resp.Results), len(resp.Sources))
	}

	if resp.Results[0].Title != "Titled" {
		t.Errorf("title not trimmed: %q", resp.Results[0].Title)
	}
	if resp.Results[0].Snippet != "spaced content" {
		t.Errorf("snippet whitespace not collapsed: %q", resp.Results[0].Snippet)
	}
	if resp.Results[0].Score != 0.9 {
		t.Errorf("score not passed through: %v", resp.Results[0].Score)
	}

	// Blank title falls back to the URL.
	if resp.Results[1].Title != "https://b.example" {
		t.Errorf("blank title should fall back to URL, got %q", resp.Results[1].Title)
	}

	for i, s := range resp.Sources {
		if s.Type != SourceTypeWeb {
			t.Errorf("source[%d].Type = %q, want %q", i, s.Type, SourceTypeWeb)
		}
	}
}

func TestFormatSearchContext(t *testing.T) {
	if got := formatSearchContext(nil); got != "No web search results found." {
		t.Errorf("empty context = %q", got)
	}

	got := formatSearchContext([]SearchWebResult{
		{Title: "First", URL: "https://a", Snippet: "snip"},
		{Title: "Second"},
	})
	if !strings.HasPrefix(got, "Web search results:") {
		t.Errorf("missing header: %q", got)
	}
	for _, want := range []string{"1. First", "URL: https://a", "Snippet: snip", "2. Second"} {
		if !strings.Contains(got, want) {
			t.Errorf("context missing %q in:\n%s", want, got)
		}
	}
}

func TestSnippetFromContent(t *testing.T) {
	if got := snippetFromContent("   "); got != "" {
		t.Errorf("blank content snippet = %q, want empty", got)
	}
	if got := snippetFromContent("a\n\tb   c"); got != "a b c" {
		t.Errorf("whitespace not collapsed: %q", got)
	}
}

func TestTruncateRunes(t *testing.T) {
	cases := []struct {
		name  string
		input string
		limit int
		want  string
	}{
		{name: "non-positive limit yields empty", input: "abc", limit: 0, want: ""},
		{name: "shorter than limit unchanged", input: "abc", limit: 10, want: "abc"},
		{name: "limit one returns first rune", input: "abc", limit: 1, want: "a"},
		{name: "truncates with ellipsis", input: "abcdef", limit: 4, want: "abc…"},
		{name: "multibyte runes counted, not bytes", input: "héllo wörld", limit: 5, want: "héll…"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateRunes(tc.input, tc.limit)
			if got != tc.want {
				t.Fatalf("truncateRunes(%q, %d) = %q, want %q", tc.input, tc.limit, got, tc.want)
			}
			// Output never exceeds the rune budget.
			if tc.limit > 0 && len([]rune(got)) > tc.limit {
				t.Fatalf("output %q exceeds rune limit %d", got, tc.limit)
			}
		})
	}
}
