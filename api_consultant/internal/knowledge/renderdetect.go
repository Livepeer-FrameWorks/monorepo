package knowledge

import (
	"bytes"
	"strings"

	"golang.org/x/net/html"
)

const renderScoreThreshold = 4

// needsRendering analyzes raw HTML to determine whether a page likely requires
// headless browser rendering (e.g. React/Vue/Angular SPA). It uses a scoring
// system based on framework markers, noscript tags, script-to-text ratio, and
// visible text density. A score >= 4 triggers rendering.
func needsRendering(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	score := 0

	lower := bytes.ToLower(data)

	// SPA framework mount-point divs: <div id="root">, <div id="app">, <div id="__next">
	if hasDivID(lower, "root") || hasDivID(lower, "app") || hasDivID(lower, "__next") {
		score += 3
	}

	// <noscript> tag — site knows it needs JS
	if bytes.Contains(lower, []byte("<noscript")) {
		score += 2
	}

	// Framework meta tags or data attributes
	if bytes.Contains(data, []byte(`content="Next.js"`)) ||
		bytes.Contains(data, []byte(`data-reactroot`)) ||
		bytes.Contains(data, []byte(`ng-app`)) ||
		bytes.Contains(data, []byte(`data-v-`)) {
		score += 3
	}

	// High script-to-text ratio: more script bytes than visible text
	scriptBytes, textBytes := measureContentRatio(data)
	if textBytes > 0 && scriptBytes > textBytes*3 {
		score += 2
	} else if textBytes == 0 && scriptBytes > 0 {
		score += 2
	}

	// Very little visible text in <body>
	bodyText := extractBodyText(data)
	if len(strings.Fields(bodyText)) < 30 {
		score += 2
	}

	return score >= renderScoreThreshold
}

// hasDivID checks for <div id="target"> in lowercased HTML.
func hasDivID(lower []byte, id string) bool {
	// Match both quote styles: id="root" and id='root'
	return bytes.Contains(lower, []byte(`<div id="`+id+`"`)) ||
		bytes.Contains(lower, []byte(`<div id='`+id+`'`))
}

// measureContentRatio estimates the byte ratio of <script> content vs visible text.
func measureContentRatio(data []byte) (scriptBytes, textBytes int) {
	node, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return 0, 0
	}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "script" {
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.TextNode {
					scriptBytes += len(c.Data)
				}
			}
			return
		}
		if n.Type == html.TextNode {
			trimmed := strings.TrimSpace(n.Data)
			if trimmed != "" {
				textBytes += len(trimmed)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(node)
	return
}

// extractBodyText extracts visible text from the <body> element.
func extractBodyText(data []byte) string {
	node, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return ""
	}
	body := findElement(node, "body")
	if body == nil {
		return ""
	}
	var buf strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "noscript":
				return
			}
		}
		if n.Type == html.TextNode {
			trimmed := strings.TrimSpace(n.Data)
			if trimmed != "" {
				buf.WriteString(trimmed)
				buf.WriteByte(' ')
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(body)
	return buf.String()
}

func findElement(n *html.Node, tag string) *html.Node {
	if n.Type == html.ElementNode && n.Data == tag {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findElement(c, tag); found != nil {
			return found
		}
	}
	return nil
}
