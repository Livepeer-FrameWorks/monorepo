package email

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"

	htmlparser "golang.org/x/net/html"
)

const defaultLogoPath = "/frameworks-light-logomark.png"

type LayoutData struct {
	LogoURL      string
	Preheader    string
	Eyebrow      string
	Title        string
	SupportEmail string
	Tagline      string
	Content      any
}

type Action struct {
	URL   string
	Label string
}

type Notice struct {
	Title string
	Text  string
}

func PublicLogoURL(configured, publicBaseURL string) string {
	if configured = strings.TrimSpace(configured); configured != "" {
		return configured
	}
	base := strings.TrimRight(strings.TrimSpace(publicBaseURL), "/")
	if base == "" {
		return ""
	}
	return base + defaultLogoPath
}

func RenderLayout(layout LayoutData, contentTemplate string, funcs template.FuncMap) (string, error) {
	if layout.Tagline == "" {
		layout.Tagline = "Open streaming infrastructure, under your control."
	}
	tpl, err := template.New("email").Funcs(funcs).Parse(layoutTemplate)
	if err != nil {
		return "", fmt.Errorf("parse email layout: %w", err)
	}
	if _, err := tpl.Parse(`{{define "content"}}` + contentTemplate + `{{end}}`); err != nil {
		return "", fmt.Errorf("parse email content: %w", err)
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "layout", layout); err != nil {
		return "", fmt.Errorf("render email layout: %w", err)
	}
	return buf.String(), nil
}

func HTMLToText(source string) string {
	doc, err := htmlparser.Parse(strings.NewReader(source))
	if err != nil {
		return strings.TrimSpace(source)
	}
	var buf strings.Builder
	var lastByte byte
	writeString := func(value string) {
		buf.WriteString(value)
		if value != "" {
			lastByte = value[len(value)-1]
		}
	}
	writeByte := func(value byte) {
		buf.WriteByte(value)
		lastByte = value
	}
	var walk func(*htmlparser.Node)
	walk = func(node *htmlparser.Node) {
		if node.Type == htmlparser.ElementNode && plainTextHidden(node) {
			return
		}
		if node.Type == htmlparser.TextNode {
			text := strings.Join(strings.Fields(node.Data), " ")
			if text != "" {
				if buf.Len() > 0 && lastByte != '\n' && lastByte != ' ' && lastByte != '\t' {
					writeByte(' ')
				}
				writeString(text)
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
		if node.Type == htmlparser.ElementNode {
			if node.Data == "a" {
				for _, attr := range node.Attr {
					if attr.Key == "href" && attr.Val != "" && !strings.HasPrefix(attr.Val, "mailto:") {
						writeString(" (" + attr.Val + ")")
						break
					}
				}
			}
			switch node.Data {
			case "p", "div", "h1", "h2", "h3", "h4", "li", "tr", "br":
				if buf.Len() > 0 && lastByte != '\n' {
					writeByte('\n')
				}
			case "th", "td":
				if buf.Len() > 0 && lastByte != '\n' && lastByte != '\t' {
					writeByte('\t')
				}
			}
		}
	}
	walk(doc)
	lines := strings.Split(buf.String(), "\n")
	cleaned := lines[:0]
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			cleaned = append(cleaned, line)
		}
	}
	plain := strings.Join(cleaned, "\n\n")
	return strings.NewReplacer(" .", ".", " ,", ",", " :", ":", " ;", ";", " !", "!", " ?", "?").Replace(plain)
}

func plainTextHidden(node *htmlparser.Node) bool {
	switch node.Data {
	case "head", "script", "style", "template":
		return true
	}
	for _, attr := range node.Attr {
		if attr.Key == "hidden" || (attr.Key == "aria-hidden" && strings.EqualFold(attr.Val, "true")) {
			return true
		}
		if attr.Key == "style" {
			style := strings.ToLower(strings.ReplaceAll(attr.Val, " ", ""))
			if strings.Contains(style, "display:none") || strings.Contains(style, "visibility:hidden") || strings.Contains(style, "max-height:0") {
				return true
			}
		}
	}
	return false
}

func PlainTextAction(intro string, action Action, notice Notice, fallbackURL string) string {
	parts := []string{strings.TrimSpace(intro)}
	if action.Label != "" && action.URL != "" {
		parts = append(parts, action.Label+":\n"+action.URL)
	}
	if notice.Title != "" || notice.Text != "" {
		parts = append(parts, strings.TrimSpace(strings.Join([]string{notice.Title, notice.Text}, "\n")))
	}
	if fallbackURL != "" && fallbackURL != action.URL {
		parts = append(parts, fallbackURL)
	}
	return strings.Join(parts, "\n\n")
}

const layoutTemplate = `{{define "layout"}}<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>{{.Title}}</title>
</head>
<body style="margin:0; padding:0; background:#eef2f5; color:#24283b; font-family:Arial,Helvetica,sans-serif;">
  <div style="display:none; max-height:0; overflow:hidden; opacity:0; color:transparent;">{{.Preheader}}</div>
  <table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0" style="width:100%; background:#eef2f5;">
    <tr>
      <td align="center" style="padding:32px 12px;">
        <table role="presentation" width="600" cellspacing="0" cellpadding="0" border="0" style="width:100%; max-width:600px; background:#ffffff; border:1px solid #ccdde5;">
          <tr>
            <td style="padding:22px 30px; border-bottom:1px solid #ccdde5;">
	              <table role="presentation" cellspacing="0" cellpadding="0" border="0"><tr>{{if .LogoURL}}<td style="padding:0 12px 0 0;"><img src="{{.LogoURL}}" width="48" height="48" alt="" style="display:block; width:48px; height:48px; border:0;"></td>{{end}}<td><strong style="color:#0f4b6e; font-size:20px; line-height:24px; letter-spacing:0.04em;">FrameWorks</strong><br><span style="color:#3d4a68; font-size:11px; line-height:14px; letter-spacing:0.16em; text-transform:uppercase;">Network</span></td></tr></table>
            </td>
          </tr>
          <tr>
            <td style="padding:30px;">
              {{if .Eyebrow}}<div style="margin:0 0 8px; color:#0f4b6e; font-size:12px; line-height:18px; font-weight:bold; letter-spacing:0.08em; text-transform:uppercase;">{{.Eyebrow}}</div>{{end}}
              <h1 style="margin:0 0 18px; color:#24283b; font-size:28px; line-height:34px; font-weight:bold;">{{.Title}}</h1>
              {{template "content" .Content}}
            </td>
          </tr>
          <tr>
            <td style="padding:18px 30px 22px; border-top:1px solid #ccdde5; color:#667085; font-size:12px; line-height:18px;">
              <strong style="color:#24283b;">FrameWorks</strong><br>
              {{.Tagline}}{{if .SupportEmail}}<br>Need help? <a href="mailto:{{.SupportEmail}}" style="color:#0f4b6e; text-decoration:underline;">{{.SupportEmail}}</a>{{end}}
            </td>
          </tr>
        </table>
      </td>
    </tr>
  </table>
</body>
</html>{{end}}

{{define "action"}}{{if and .URL .Label}}<table role="presentation" cellspacing="0" cellpadding="0" border="0" style="margin:22px 0;"><tr><td bgcolor="#0f4b6e" style="background:#0f4b6e;"><a href="{{.URL}}" style="display:inline-block; padding:13px 20px; color:#ffffff; font-size:15px; line-height:20px; font-weight:bold; text-decoration:none;">{{.Label}}</a></td></tr></table>{{end}}{{end}}

{{define "notice"}}{{if or .Title .Text}}<div style="margin:20px 0; padding:13px 15px; border-left:3px solid #0f4b6e; background:#eef5f8; color:#3d4a68; font-size:14px; line-height:21px;">{{if .Title}}<strong>{{.Title}}</strong>{{if .Text}}<br>{{end}}{{end}}{{.Text}}</div>{{end}}{{end}}

{{define "fallbackURL"}}{{if .}}<p style="margin:20px 0 0; color:#667085; font-size:12px; line-height:18px; overflow-wrap:anywhere;">Button not working? Copy and paste this address into your browser:<br><a href="{{.}}" style="color:#0f4b6e; text-decoration:underline; word-break:break-all;">{{.}}</a></p>{{end}}{{end}}`
