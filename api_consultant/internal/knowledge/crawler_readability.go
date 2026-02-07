package knowledge

import (
	"bytes"
	"net/url"
	"strings"

	readability "codeberg.org/readeck/go-readability/v2"
	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"golang.org/x/net/html"
)

const readabilityMinWords = 50

// extractContent tries go-readability first (Mozilla's Readability algorithm),
// converts the article to markdown for LLM-ready output, and falls back to
// the custom DOM walker when readability produces too little text.
func extractContent(data []byte, pageURL string) (title, content string) {
	parsedURL, _ := url.Parse(pageURL)
	article, err := readability.FromReader(bytes.NewReader(data), parsedURL)
	if err == nil && article.Node != nil {
		// Convert readability's cleaned HTML subtree to markdown,
		// preserving headings, code blocks, tables, and lists.
		md, mdErr := htmltomarkdown.ConvertNode(article.Node)
		if mdErr == nil {
			text := normalizeContent(string(md))
			if len(strings.Fields(text)) >= readabilityMinWords {
				return article.Title(), text
			}
		}
		// Fall back to plain text if markdown conversion fails
		var buf bytes.Buffer
		_ = article.RenderText(&buf)
		text := normalizeContent(buf.String())
		if len(strings.Fields(text)) >= readabilityMinWords {
			return article.Title(), text
		}
	}

	node, parseErr := html.Parse(bytes.NewReader(data))
	if parseErr != nil {
		return "", ""
	}
	return extractTitle(node), extractReadableText(node)
}

// extractPlainContent handles text/plain and text/markdown content.
// It returns the content as-is (normalized) and extracts a title from
// the first markdown heading if present.
func extractPlainContent(data []byte, _ string) (title, content string) {
	text := normalizeContent(string(data))
	if text == "" {
		return "", ""
	}
	for _, line := range strings.SplitN(text, "\n", 10) {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, "# ")), text
		}
	}
	return "", text
}

// looksLikeEmptyShell returns true when extracted text has very few words,
// suggesting a JavaScript-rendered SPA that delivered an empty shell.
func looksLikeEmptyShell(text string) bool {
	return len(strings.Fields(text)) < emptyShellThreshold
}
