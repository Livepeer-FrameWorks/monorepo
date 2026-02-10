package notify

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"strings"
	"time"

	"frameworks/pkg/email"
	"frameworks/pkg/logging"
)

type EmailNotifier struct {
	sender     *email.Sender
	smtpConfig email.Config
	webAppURL  string
	logger     logging.Logger
}

type emailReportData struct {
	TenantName      string
	InvestigationID string
	Summary         string
	Metrics         []Metric
	Recommendations []Recommendation
	ReportURL       string
	GeneratedAt     time.Time
}

func NewEmailNotifier(cfg Config, logger logging.Logger) *EmailNotifier {
	return &EmailNotifier{
		sender:     email.NewSender(cfg.SMTP),
		smtpConfig: cfg.SMTP,
		webAppURL:  cfg.WebAppURL,
		logger:     logger,
	}
}

func (n *EmailNotifier) IsConfigured() bool {
	return n.smtpConfig.Host != "" && n.smtpConfig.From != ""
}

func (n *EmailNotifier) Notify(ctx context.Context, report Report) error {
	if !n.IsConfigured() {
		n.logger.Warn("Email notifier not configured, skipping skipper investigation email")
		return nil
	}
	if report.RecipientEmail == "" {
		return fmt.Errorf("report recipient email missing")
	}

	generatedAt := report.GeneratedAt
	if generatedAt.IsZero() {
		generatedAt = time.Now().UTC()
	}

	reportURL := report.ReportURL
	if reportURL == "" && n.webAppURL != "" && report.InvestigationID != "" {
		reportURL = fmt.Sprintf("%s/skipper?report=%s", strings.TrimRight(n.webAppURL, "/"), report.InvestigationID)
	}

	subject := "Skipper Investigation Report"
	if report.InvestigationID != "" {
		subject = fmt.Sprintf("Skipper Investigation Report %s", report.InvestigationID)
	}

	data := emailReportData{
		TenantName:      report.TenantName,
		InvestigationID: report.InvestigationID,
		Summary:         report.Summary,
		Metrics:         report.Metrics,
		Recommendations: report.Recommendations,
		ReportURL:       reportURL,
		GeneratedAt:     generatedAt,
	}

	body, err := n.renderTemplate(data)
	if err != nil {
		return fmt.Errorf("render investigation report email: %w", err)
	}

	if err := n.sender.SendMail(ctx, report.RecipientEmail, subject, body); err != nil {
		n.logger.WithFields(logging.Fields{
			"error": err.Error(),
			"to":    report.RecipientEmail,
		}).Error("Failed to send skipper investigation report email")
		return err
	}

	n.logger.WithFields(logging.Fields{
		"to":        report.RecipientEmail,
		"tenant_id": report.TenantID,
	}).Info("Skipper investigation report email sent")

	return nil
}

func (n *EmailNotifier) renderTemplate(data emailReportData) (string, error) {
	funcs := template.FuncMap{
		"formatMetricValue": func(metric Metric) string {
			value := strings.TrimSpace(metric.Value)
			unit := strings.TrimSpace(metric.Unit)
			if value == "" {
				return "-"
			}
			if unit == "" {
				return value
			}
			return fmt.Sprintf("%s %s", value, unit)
		},
		"hasMetrics": func(metrics []Metric) bool {
			return len(metrics) > 0
		},
		"hasRecommendations": func(recs []Recommendation) bool {
			return len(recs) > 0
		},
	}

	tpl, err := template.New("investigation_report").Funcs(funcs).Parse(investigationEmailTemplate)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := tpl.Execute(&buf, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}

const investigationEmailTemplate = `
<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <title>Skipper Investigation Report</title>
</head>
<body style="font-family: Arial, sans-serif; line-height: 1.6; color: #333;">
    <div style="max-width: 640px; margin: 0 auto; padding: 24px;">
        <h2 style="color: #2c3e50;">Skipper Investigation Report</h2>

        {{if .TenantName}}
        <p>Hello {{.TenantName}},</p>
        {{else}}
        <p>Hello,</p>
        {{end}}

        <p>Skipper completed a heartbeat investigation. Here is a summary of what we found.</p>

        {{if .Summary}}
        <div style="background-color: #f8f9fa; padding: 16px; border-radius: 6px; margin: 20px 0;">
            <strong>Summary</strong>
            <p style="margin: 10px 0 0 0;">{{.Summary}}</p>
        </div>
        {{end}}

        {{if hasMetrics .Metrics}}
        <h3 style="color: #2c3e50; margin-top: 30px;">Key Metrics</h3>
        <table style="width: 100%; border-collapse: collapse; margin-bottom: 20px;">
            <tr style="background-color: #eef1f5;">
                <th style="padding: 10px; text-align: left; border-bottom: 1px solid #ddd;">Metric</th>
                <th style="padding: 10px; text-align: left; border-bottom: 1px solid #ddd;">Value</th>
            </tr>
            {{range .Metrics}}
            <tr>
                <td style="padding: 10px; border-bottom: 1px solid #eee;">
                    <strong>{{.Name}}</strong>
                    {{if .Description}}<div style="color: #6c757d; font-size: 12px;">{{.Description}}</div>{{end}}
                </td>
                <td style="padding: 10px; border-bottom: 1px solid #eee;">{{formatMetricValue .}}</td>
            </tr>
            {{end}}
        </table>
        {{end}}

        {{if hasRecommendations .Recommendations}}
        <h3 style="color: #2c3e50; margin-top: 30px;">Recommendations</h3>
        <ul style="padding-left: 20px;">
            {{range .Recommendations}}
            <li style="margin-bottom: 12px;">
                <strong>{{.Title}}</strong>
                {{if .Priority}}<span style="color: #e67e22;">({{.Priority}})</span>{{end}}
                {{if .Detail}}<div style="color: #555;">{{.Detail}}</div>{{end}}
            </li>
            {{end}}
        </ul>
        {{end}}

        <p style="color: #6c757d; font-size: 12px;">Generated at {{.GeneratedAt.Format "January 2, 2006 at 3:04 PM MST"}}</p>

        {{if .ReportURL}}
        <p style="text-align: center; margin: 30px 0;">
            <a href="{{.ReportURL}}" style="background-color: #3498db; color: white; padding: 12px 24px; text-decoration: none; border-radius: 5px; display: inline-block;">View Full Report</a>
        </p>
        {{end}}

        <p>If you have questions, reply to this email or reach out to the FrameWorks team.</p>
    </div>
</body>
</html>`
