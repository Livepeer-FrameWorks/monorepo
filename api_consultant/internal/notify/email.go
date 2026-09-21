package notify

import (
	"context"
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/email"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

type EmailNotifier struct {
	sender         *email.Sender
	smtpConfig     email.Config
	branding       config.EmailBranding
	brandingSource func() config.EmailBranding
	webAppURL      string
	logger         logging.Logger
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
		sender:         email.NewSender(cfg.SMTP),
		smtpConfig:     cfg.SMTP,
		branding:       cfg.Branding,
		brandingSource: cfg.BrandingSource,
		webAppURL:      cfg.WebAppURL,
		logger:         logger,
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
	webAppURL := n.webAppURL
	if n.brandingSource != nil {
		webAppURL = n.brandingSource().WebAppURL
	}
	if reportURL == "" && webAppURL != "" && report.InvestigationID != "" {
		reportURL = fmt.Sprintf("%s/skipper?report=%s", strings.TrimRight(webAppURL, "/"), report.InvestigationID)
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
	branding := n.branding
	if n.brandingSource != nil {
		branding = n.brandingSource()
	}
	funcs := template.FuncMap{
		"action": func(actionURL, label string) email.Action {
			return email.Action{URL: actionURL, Label: label}
		},
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

	return email.RenderLayout(email.LayoutData{
		LogoURL:      branding.Logo(),
		Preheader:    "Skipper completed an investigation of your streaming infrastructure.",
		Eyebrow:      "Skipper investigation",
		Title:        "Investigation report",
		SupportEmail: branding.Support(),
		Content:      data,
	}, investigationEmailTemplate, funcs)
}

const investigationEmailTemplate = `
        {{if .TenantName}}<p style="margin:0 0 16px; color:#24283b; font-size:15px; line-height:23px;">Hello {{.TenantName}},</p>{{else}}<p style="margin:0 0 16px; color:#24283b; font-size:15px; line-height:23px;">Hello,</p>{{end}}
        <p style="margin:0 0 18px; color:#24283b; font-size:15px; line-height:23px;">Skipper completed a heartbeat investigation. Here is a summary of what we found.</p>

        {{if .Summary}}
        <div style="background:#eef5f8; border-left:3px solid #0f4b6e; padding:13px 15px; margin:20px 0; color:#3d4a68; font-size:14px; line-height:21px;">
            <strong>Summary</strong>
            <p style="margin: 10px 0 0 0;">{{.Summary}}</p>
        </div>
        {{end}}

        {{if hasMetrics .Metrics}}
        <h2 style="color:#24283b; margin:28px 0 12px; font-size:18px; line-height:24px;">Key metrics</h2>
        <table width="100%" cellspacing="0" cellpadding="0" border="0" style="width:100%; border-collapse:collapse; margin-bottom:20px; font-size:13px;">
            <tr style="background:#eef5f8;">
                <th style="padding:9px 10px; text-align:left; color:#3d4a68; border-bottom:1px solid #ccdde5;">Metric</th>
                <th style="padding:9px 10px; text-align:left; color:#3d4a68; border-bottom:1px solid #ccdde5;">Value</th>
            </tr>
            {{range .Metrics}}
            <tr>
                <td style="padding:9px 10px; color:#24283b; border-bottom:1px solid #e7edf0;">
                    <strong>{{.Name}}</strong>
                    {{if .Description}}<div style="color:#667085; font-size:11px; line-height:16px;">{{.Description}}</div>{{end}}
                </td>
                <td style="padding:9px 10px; color:#24283b; border-bottom:1px solid #e7edf0;">{{formatMetricValue .}}</td>
            </tr>
            {{end}}
        </table>
        {{end}}

        {{if hasRecommendations .Recommendations}}
        <h2 style="color:#24283b; margin:28px 0 12px; font-size:18px; line-height:24px;">Recommendations</h2>
        <ul style="padding-left: 20px;">
            {{range .Recommendations}}
            <li style="margin-bottom:12px; color:#24283b; font-size:14px; line-height:21px;">
                <strong>{{.Title}}</strong>
                {{if .Priority}}<span style="color:#a66b16;">({{.Priority}})</span>{{end}}
                {{if .Detail}}<div style="color:#667085;">{{.Detail}}</div>{{end}}
            </li>
            {{end}}
        </ul>
        {{end}}

        <p style="color:#667085; font-size:12px; line-height:18px;">Generated at {{.GeneratedAt.Format "January 2, 2006 at 3:04 PM MST"}}</p>

        {{if .ReportURL}}{{template "action" (action .ReportURL "View full report")}}{{end}}`
