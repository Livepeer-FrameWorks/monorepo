package heartbeat

import (
	"context"
	"fmt"
	"strings"
	"time"

	"frameworks/api_consultant/internal/notify"
	"frameworks/pkg/logging"
)

type Recommendation struct {
	Text       string `json:"text"`
	Confidence string `json:"confidence"`
}

type Report struct {
	Trigger         string           `json:"trigger,omitempty"`
	Summary         string           `json:"summary"`
	MetricsReviewed []string         `json:"metrics_reviewed"`
	RootCause       string           `json:"root_cause"`
	Recommendations []Recommendation `json:"recommendations"`
}

type Reporter struct {
	Store       ReportStore
	Billing     BillingClient
	Dispatcher  NotificationDispatcher
	Logger      logging.Logger
	WebAppURL   string
	Preferences *notify.NotificationPreferences
}

func (r Report) FormatMarkdown() string {
	lines := []string{"## Skipper Investigation Report"}
	if strings.TrimSpace(r.Summary) != "" {
		lines = append(lines, fmt.Sprintf("**Summary:** %s", r.Summary))
	}
	if len(r.MetricsReviewed) > 0 {
		lines = append(lines, "\n**Metrics Reviewed:**")
		for _, metric := range r.MetricsReviewed {
			lines = append(lines, fmt.Sprintf("- %s", metric))
		}
	}
	if strings.TrimSpace(r.RootCause) != "" {
		lines = append(lines, fmt.Sprintf("\n**Root Cause:** %s", r.RootCause))
	}
	if len(r.Recommendations) > 0 {
		lines = append(lines, "\n**Recommendations:**")
		for _, rec := range r.Recommendations {
			confidence := strings.TrimSpace(rec.Confidence)
			if confidence == "" {
				confidence = "unknown"
			}
			lines = append(lines, fmt.Sprintf("- [%s] %s", confidence, rec.Text))
		}
	}
	return strings.Join(lines, "\n")
}

type NotificationDispatcher interface {
	Notify(ctx context.Context, report notify.Report) error
}

func (r *Reporter) Send(ctx context.Context, tenantID string, report Report) error {
	if r == nil {
		return nil
	}

	record := ReportRecord{
		TenantID:        tenantID,
		Trigger:         report.Trigger,
		Summary:         report.Summary,
		MetricsReviewed: report.MetricsReviewed,
		RootCause:       report.RootCause,
		Recommendations: report.Recommendations,
	}

	if r.Store != nil {
		stored, err := r.Store.Save(ctx, record)
		if err != nil {
			if r.Logger != nil {
				r.Logger.WithError(err).WithField("tenant_id", tenantID).Warn("Failed to persist heartbeat report")
			}
			// Best-effort persistence: still attempt notifications so operators get alerts
			// during transient storage outages.
		} else {
			record = stored
		}
	}

	if r.Dispatcher != nil {
		notification := r.buildNotification(ctx, record, report)
		if err := r.Dispatcher.Notify(ctx, notification); err != nil {
			if r.Logger != nil {
				r.Logger.WithError(err).WithField("tenant_id", tenantID).Warn("Failed to send heartbeat notifications")
			}
			return err
		}
	}

	if r.Logger != nil {
		r.Logger.WithField("tenant_id", tenantID).Debug("Heartbeat report prepared")
	}
	return nil
}

func (r *Reporter) buildNotification(ctx context.Context, record ReportRecord, report Report) notify.Report {
	recipientEmail := ""
	tenantName := ""
	if r.Billing != nil {
		if status, err := r.Billing.GetBillingStatus(ctx, record.TenantID); err == nil && status != nil {
			subscription := status.GetSubscription()
			if subscription != nil {
				recipientEmail = subscription.GetBillingEmail()
				tenantName = subscription.GetBillingCompany()
			}
		}
	}

	metrics := make([]notify.Metric, 0, len(report.MetricsReviewed))
	for _, metric := range report.MetricsReviewed {
		if strings.TrimSpace(metric) == "" {
			continue
		}
		metrics = append(metrics, notify.Metric{
			Name: metric,
		})
	}

	recs := make([]notify.Recommendation, 0, len(report.Recommendations))
	for _, rec := range report.Recommendations {
		if strings.TrimSpace(rec.Text) == "" {
			continue
		}
		recs = append(recs, notify.Recommendation{
			Title:    rec.Text,
			Priority: strings.TrimSpace(rec.Confidence),
		})
	}

	notification := notify.Report{
		TenantID:        record.TenantID,
		TenantName:      tenantName,
		RecipientEmail:  recipientEmail,
		InvestigationID: record.ID,
		Summary:         report.Summary,
		Metrics:         metrics,
		Recommendations: recs,
		GeneratedAt:     time.Now().UTC(),
		Preferences:     r.Preferences,
	}

	if !record.CreatedAt.IsZero() {
		notification.GeneratedAt = record.CreatedAt
	}

	if strings.TrimSpace(r.WebAppURL) != "" && record.ID != "" {
		notification.ReportURL = fmt.Sprintf("%s/skipper?report=%s", strings.TrimRight(r.WebAppURL, "/"), record.ID)
	}

	return notification
}
