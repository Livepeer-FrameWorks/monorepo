package notify

import (
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"frameworks/api_incidents/internal/incidents"
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
	cluster := p.ClusterID
	if cluster == "" {
		cluster = "platform"
	}
	m.Fields = append(m.Fields, messageField{Name: "Cluster", Value: cluster})
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

func emailContent(m message) (subject, body string) {
	subject = truncate("[Lookout] "+m.Headline, 200)
	var b strings.Builder
	b.WriteString("<h2>")
	b.WriteString(html.EscapeString(m.Headline))
	b.WriteString("</h2>")
	if m.Summary != "" {
		b.WriteString("<p>")
		b.WriteString(html.EscapeString(m.Summary))
		b.WriteString("</p>")
	}
	b.WriteString("<table>")
	for _, f := range m.Fields {
		fmt.Fprintf(&b, "<tr><th align=\"left\">%s</th><td>%s</td></tr>", html.EscapeString(f.Name), html.EscapeString(f.Value))
	}
	b.WriteString("</table>")
	if m.Link != "" {
		fmt.Fprintf(&b, "<p><a href=\"%s\">Open incident</a></p>", html.EscapeString(m.Link))
	}
	return subject, b.String()
}

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
