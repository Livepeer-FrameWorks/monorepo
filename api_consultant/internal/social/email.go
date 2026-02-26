package social

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

func renderSocialEmail(post PostRecord) (string, error) {
	data := socialEmailData{
		TweetText:      post.TweetText,
		TweetLength:    len(post.TweetText),
		ContentType:    formatContentType(post.ContentType),
		ContextSummary: post.ContextSummary,
		DataPoints:     formatDataPoints(post.TriggerData),
		GeneratedAt:    post.CreatedAt.UTC().Format("January 2, 2006 at 3:04 PM UTC"),
	}

	tpl, err := template.New("social_draft").Parse(socialEmailTemplate)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}
	return buf.String(), nil
}

type socialEmailData struct {
	TweetText      string
	TweetLength    int
	ContentType    string
	ContextSummary string
	DataPoints     []string
	GeneratedAt    string
}

func formatContentType(ct ContentType) string {
	switch ct {
	case ContentPlatformStats:
		return "Platform Stats"
	case ContentFederation:
		return "Federation"
	case ContentKnowledge:
		return "Knowledge"
	default:
		return string(ct)
	}
}

func formatDataPoints(data map[string]any) []string {
	if len(data) == 0 {
		return nil
	}
	var points []string
	for k, v := range data {
		label := strings.ReplaceAll(k, "_", " ")
		label = cases.Title(language.English).String(label)
		switch val := v.(type) {
		case float64:
			if val == float64(int(val)) {
				points = append(points, fmt.Sprintf("%s: %.0f", label, val))
			} else {
				points = append(points, fmt.Sprintf("%s: %.2f", label, val))
			}
		case string:
			if len(val) > 100 {
				continue
			}
			points = append(points, fmt.Sprintf("%s: %s", label, val))
		default:
			points = append(points, fmt.Sprintf("%s: %v", label, v))
		}
	}
	return points
}

const socialEmailTemplate = `<!DOCTYPE html>
<html>
<head><meta charset="UTF-8"><title>Social Post Draft</title></head>
<body style="font-family: Arial, sans-serif; line-height: 1.6; color: #333; margin: 0; padding: 0;">
<div style="max-width: 640px; margin: 0 auto; padding: 24px;">

<div style="background-color: #1DA1F2; color: white; padding: 14px 20px; border-radius: 6px; margin-bottom: 20px;">
    <strong>FrameWorks — Social Post Draft</strong>
</div>

<p>Skipper drafted a tweet for you. Copy the text below and post it on X.</p>

<div style="background-color: #f8f9fa; border: 2px solid #1DA1F2; border-radius: 8px; padding: 20px; margin: 20px 0; font-size: 16px; line-height: 1.5;">
    {{.TweetText}}
</div>

<p style="color: #6c757d; font-size: 13px; margin-top: -10px;">
    {{.TweetLength}}/280 characters
</p>

<h3 style="color: #2c3e50; margin-top: 24px;">Why this post?</h3>
<p>{{.ContextSummary}}</p>

{{if .DataPoints}}
<h3 style="color: #2c3e50; margin-top: 24px;">Supporting data</h3>
<table style="width: 100%; border-collapse: collapse; margin-bottom: 20px;">
    {{range .DataPoints}}
    <tr>
        <td style="padding: 6px 10px; border-bottom: 1px solid #eee; font-size: 14px;">{{.}}</td>
    </tr>
    {{end}}
</table>
{{end}}

<p style="color: #6c757d; font-size: 12px; margin-top: 30px;">
    Generated at {{.GeneratedAt}}<br>
    Category: {{.ContentType}}
</p>

</div>
</body>
</html>`
