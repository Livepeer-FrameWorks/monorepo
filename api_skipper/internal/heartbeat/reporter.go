package heartbeat

import (
	"context"
	"fmt"
	"strings"

	"frameworks/pkg/logging"
)

type Notifier interface {
	Notify(ctx context.Context, tenantID string, markdown string) error
}

type Recommendation struct {
	Text       string `json:"text"`
	Confidence string `json:"confidence"`
}

type Report struct {
	Summary         string           `json:"summary"`
	MetricsReviewed []string         `json:"metrics_reviewed"`
	RootCause       string           `json:"root_cause"`
	Recommendations []Recommendation `json:"recommendations"`
}

type Reporter struct {
	Notifier Notifier
	Logger   logging.Logger
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

func (r *Reporter) Send(ctx context.Context, tenantID string, report Report) error {
	if r == nil {
		return nil
	}
	markdown := report.FormatMarkdown()
	if r.Notifier != nil {
		if err := r.Notifier.Notify(ctx, tenantID, markdown); err != nil {
			if r.Logger != nil {
				r.Logger.WithError(err).WithField("tenant_id", tenantID).Warn("Failed to send heartbeat notification")
			}
			return err
		}
	}
	if r.Logger != nil {
		r.Logger.WithField("tenant_id", tenantID).Debug("Heartbeat report prepared")
	}
	return nil
}
