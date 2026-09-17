package notify

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"frameworks/api_incidents/internal/incidents"
	emailpkg "github.com/Livepeer-FrameWorks/monorepo/pkg/email"
)

const (
	colorCritical = 0xE01E5A
	colorWarning  = 0xECB22E
	colorResolved = 0x2EB67D
)

type messageField struct {
	Name  string
	Value string
}

// message is the channel-neutral rendering of one delivery.
type message struct {
	Headline  string
	Summary   string
	Fields    []messageField
	Link      string
	Color     int
	Timestamp time.Time
}

func buildMessage(p incidents.DeliveryPayload, webappURL string) message {
	title := strings.TrimSpace(p.Title)
	if title == "" {
		title = strings.TrimSpace(p.Alertname)
	}
	if title == "" {
		title = "Incident " + p.IncidentID
	}
	severity := strings.ToUpper(strings.TrimSpace(p.Severity))
	if severity == "" {
		severity = "UNKNOWN"
	}
	m := message{
		Summary:   strings.TrimSpace(p.Summary),
		Timestamp: p.StartedAt,
	}
	switch p.Event {
	case incidents.DeliveryEventResolved:
		resolution := p.Resolution
		if resolution == "" {
			resolution = incidents.ResolutionAuto
		}
		m.Headline = fmt.Sprintf("[RESOLVED] %s (%s)", title, resolution)
		m.Color = colorResolved
		if p.ResolvedAt != nil {
			m.Timestamp = *p.ResolvedAt
		}
	default:
		m.Headline = fmt.Sprintf("[%s] %s", severity, title)
		m.Color = colorWarning
		if strings.EqualFold(p.Severity, "critical") {
			m.Color = colorCritical
		}
	}
	if p.ClusterID != "" {
		m.Fields = append(m.Fields, messageField{Name: "Cluster", Value: p.ClusterID})
	}
	if p.Region != "" {
		m.Fields = append(m.Fields, messageField{Name: "Region", Value: p.Region})
	}
	if p.Alertname != "" {
		m.Fields = append(m.Fields, messageField{Name: "Alert", Value: p.Alertname})
	}
	m.Fields = append(m.Fields, messageField{Name: "Severity", Value: severity})
	if !p.StartedAt.IsZero() {
		m.Fields = append(m.Fields, messageField{Name: "Started", Value: p.StartedAt.UTC().Format(time.RFC3339)})
	}
	if p.ResolvedAt != nil {
		m.Fields = append(m.Fields, messageField{Name: "Resolved", Value: p.ResolvedAt.UTC().Format(time.RFC3339)})
	}
	if base := strings.TrimRight(strings.TrimSpace(webappURL), "/"); base != "" {
		m.Link = base + "/admin/incidents/" + url.PathEscape(p.IncidentID)
	}
	return m
}

// slackBody renders an incoming-webhook payload with Block Kit blocks and a
// plain-text fallback.
func slackBody(m message) ([]byte, error) {
	blocks := []map[string]any{
		{"type": "header", "text": map[string]any{"type": "plain_text", "text": truncate(m.Headline, 150)}},
	}
	if m.Summary != "" {
		blocks = append(blocks, map[string]any{
			"type": "section",
			"text": map[string]any{"type": "mrkdwn", "text": truncate(slackEscape(m.Summary), 3000)},
		})
	}
	fields := make([]map[string]any, 0, len(m.Fields))
	for _, f := range m.Fields {
		fields = append(fields, map[string]any{
			"type": "mrkdwn",
			"text": truncate("*"+f.Name+"*\n"+slackEscape(f.Value), 2000),
		})
	}
	if len(fields) > 10 {
		fields = fields[:10]
	}
	if len(fields) > 0 {
		blocks = append(blocks, map[string]any{"type": "section", "fields": fields})
	}
	if m.Link != "" {
		blocks = append(blocks, map[string]any{
			"type": "actions",
			"elements": []map[string]any{{
				"type": "button",
				"text": map[string]any{"type": "plain_text", "text": "Open incident"},
				"url":  m.Link,
			}},
		})
	}
	return json.Marshal(map[string]any{
		"text":   truncate(slackEscape(m.Headline), 3000),
		"blocks": blocks,
	})
}

// discordBody renders a webhook payload with one embed. allowed_mentions is
// empty so alert text can never ping users or roles.
func discordBody(m message) ([]byte, error) {
	embed := map[string]any{
		"title": truncate(m.Headline, 256),
		"color": m.Color,
	}
	if m.Summary != "" {
		embed["description"] = truncate(m.Summary, 4096)
	}
	if m.Link != "" {
		embed["url"] = m.Link
	}
	if !m.Timestamp.IsZero() {
		embed["timestamp"] = m.Timestamp.UTC().Format(time.RFC3339)
	}
	fields := make([]map[string]any, 0, len(m.Fields))
	for _, f := range m.Fields {
		fields = append(fields, map[string]any{
			"name":   truncate(f.Name, 256),
			"value":  truncate(f.Value, 1024),
			"inline": true,
		})
	}
	if len(fields) > 0 {
		embed["fields"] = fields
	}
	return json.Marshal(map[string]any{
		"embeds":           []map[string]any{embed},
		"allowed_mentions": map[string]any{"parse": []string{}},
	})
}

func emailContent(m message) (subject, body string, err error) {
	subject = truncate("[Lookout] "+m.Headline, 200)
	status := "Incident update"
	if strings.HasPrefix(m.Headline, "[RESOLVED]") {
		status = "Incident resolved"
	} else if strings.HasPrefix(m.Headline, "[CRITICAL]") {
		status = "Critical incident"
	}
	body, err = emailpkg.RenderLayout(emailpkg.LayoutData{
		LogoURL:      emailpkg.PublicLogoURL(os.Getenv("EMAIL_LOGO_URL"), os.Getenv("WEBAPP_PUBLIC_URL")),
		Preheader:    m.Headline,
		Eyebrow:      "Lookout · " + status,
		Title:        m.Headline,
		SupportEmail: incidentSupportEmail(),
		Content:      m,
	}, incidentEmailTemplate, template.FuncMap{
		"action": func() emailpkg.Action {
			return emailpkg.Action{URL: m.Link, Label: "Open incident"}
		},
	})
	return subject, body, err
}

func incidentSupportEmail() string {
	if supportEmail := strings.TrimSpace(os.Getenv("SUPPORT_EMAIL")); supportEmail != "" {
		return supportEmail
	}
	return "support@frameworks.network"
}

const incidentEmailTemplate = `{{if .Summary}}<p style="margin:0 0 20px; color:#24283b; font-size:15px; line-height:23px;">{{.Summary}}</p>{{end}}
{{if .Fields}}<table width="100%" cellspacing="0" cellpadding="0" border="0" style="width:100%; border-collapse:collapse; margin:20px 0; font-size:13px;">
  {{range .Fields}}<tr>
    <th style="padding:9px 10px; text-align:left; color:#3d4a68; border-bottom:1px solid #e7edf0; background:#f7fafb;">{{.Name}}</th>
    <td style="padding:9px 10px; color:#24283b; border-bottom:1px solid #e7edf0;">{{.Value}}</td>
  </tr>{{end}}
</table>{{end}}
{{template "action" action}}`

// slackEscape escapes the three characters Slack mrkdwn treats as control
// sequences.
func slackEscape(value string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(value)
}

func truncate(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit-1]) + "…"
}
