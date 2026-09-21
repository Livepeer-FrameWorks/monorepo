package social

import (
	"fmt"
	"strings"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	emailpkg "github.com/Livepeer-FrameWorks/monorepo/pkg/email"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

func renderSocialEmail(post PostRecord, branding config.EmailBranding) (string, error) {
	data := socialEmailData{
		TweetText:      post.TweetText,
		TweetLength:    len(post.TweetText),
		ContentType:    formatContentType(post.ContentType),
		ContextSummary: post.ContextSummary,
		DataPoints:     formatDataPoints(post.TriggerData),
		GeneratedAt:    post.CreatedAt.UTC().Format("January 2, 2006 at 3:04 PM UTC"),
	}

	return emailpkg.RenderLayout(emailpkg.LayoutData{
		LogoURL:      branding.Logo(),
		Preheader:    "Skipper prepared a social post draft for review.",
		Eyebrow:      "Social publishing",
		Title:        "Social post draft",
		SupportEmail: branding.Support(),
		Content:      data,
	}, socialEmailTemplate, nil)
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

const socialEmailTemplate = `<p style="margin:0 0 18px; color:#24283b; font-size:15px; line-height:23px;">Skipper drafted a post for you. Review the text below, then copy it into X when you are ready.</p>

<div style="background:#f7fafb; border-left:3px solid #0f4b6e; padding:18px; margin:20px 0; color:#24283b; font-size:16px; line-height:24px;">
    {{.TweetText}}
</div>

<p style="color:#667085; font-size:12px; line-height:18px; margin:0 0 22px;">
    {{.TweetLength}}/280 characters
</p>

<h2 style="color:#24283b; margin:28px 0 12px; font-size:18px; line-height:24px;">Why this post?</h2>
<p style="margin:0 0 18px; color:#24283b; font-size:14px; line-height:21px;">{{.ContextSummary}}</p>

{{if .DataPoints}}
<h2 style="color:#24283b; margin:28px 0 12px; font-size:18px; line-height:24px;">Supporting data</h2>
<table width="100%" cellspacing="0" cellpadding="0" border="0" style="width:100%; border-collapse:collapse; margin-bottom:20px;">
    {{range .DataPoints}}
    <tr>
        <td style="padding:8px 10px; border-bottom:1px solid #e7edf0; color:#24283b; font-size:13px; line-height:19px;">{{.}}</td>
    </tr>
    {{end}}
</table>
{{end}}

<p style="color:#667085; font-size:12px; line-height:18px; margin-top:28px;">
    Generated at {{.GeneratedAt}}<br>
    Category: {{.ContentType}}
</p>`
