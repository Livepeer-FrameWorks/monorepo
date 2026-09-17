package incidents

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// AlertmanagerWebhook is the Alertmanager webhook notification (payload version 4).
type AlertmanagerWebhook struct {
	Version           string              `json:"version"`
	GroupKey          string              `json:"groupKey"`
	TruncatedAlerts   int                 `json:"truncatedAlerts"`
	Status            string              `json:"status"`
	Receiver          string              `json:"receiver"`
	GroupLabels       map[string]string   `json:"groupLabels"`
	CommonLabels      map[string]string   `json:"commonLabels"`
	CommonAnnotations map[string]string   `json:"commonAnnotations"`
	ExternalURL       string              `json:"externalURL"`
	Alerts            []AlertmanagerAlert `json:"alerts"`
}

// AlertmanagerAlert is one alert inside a webhook notification.
type AlertmanagerAlert struct {
	Status       string            `json:"status"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     time.Time         `json:"startsAt"`
	EndsAt       time.Time         `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"`
	Fingerprint  string            `json:"fingerprint"`
}

const (
	alertStatusFiring   = "firing"
	alertStatusResolved = "resolved"
)

// ErrInvalidWebhook marks a notification Lookout cannot ingest.
var ErrInvalidWebhook = errors.New("invalid alertmanager webhook")

// Validate rejects notifications that cannot be keyed or ordered.
func (w AlertmanagerWebhook) Validate() error {
	if strings.TrimSpace(w.GroupKey) == "" {
		return fmt.Errorf("%w: groupKey is required", ErrInvalidWebhook)
	}
	if len(w.Alerts) == 0 {
		return fmt.Errorf("%w: alerts are required", ErrInvalidWebhook)
	}
	for i, alert := range w.Alerts {
		if strings.TrimSpace(alert.Fingerprint) == "" {
			return fmt.Errorf("%w: alerts[%d].fingerprint is required", ErrInvalidWebhook, i)
		}
		if alert.Status != alertStatusFiring && alert.Status != alertStatusResolved {
			return fmt.Errorf("%w: alerts[%d].status %q is not firing or resolved", ErrInvalidWebhook, i, alert.Status)
		}
		if alert.StartsAt.IsZero() {
			return fmt.Errorf("%w: alerts[%d].startsAt is required", ErrInvalidWebhook, i)
		}
	}
	return nil
}

// groupFacts are the incident attributes derived from one notification.
type groupFacts struct {
	GroupKey  string
	Alertname string
	ClusterID string
	Region    string
	Title     string
	Summary   string
}

func (w AlertmanagerWebhook) facts() groupFacts {
	label := func(key string) string {
		if v := strings.TrimSpace(w.GroupLabels[key]); v != "" {
			return v
		}
		return strings.TrimSpace(w.CommonLabels[key])
	}
	alertname := label("alertname")
	title := strings.TrimSpace(w.CommonAnnotations["summary"])
	if title == "" {
		title = alertname
	}
	summary := strings.TrimSpace(w.CommonAnnotations["description"])
	if summary == "" {
		summary = title
	}
	return groupFacts{
		GroupKey:  w.GroupKey,
		Alertname: alertname,
		ClusterID: label("cluster"),
		Region:    label("region"),
		Title:     title,
		Summary:   summary,
	}
}

// normalizedStart truncates to PostgreSQL's microsecond precision so the
// stored startsAt compares equal to the value Alertmanager repeats.
func (a AlertmanagerAlert) normalizedStart() time.Time {
	return a.StartsAt.UTC().Truncate(time.Microsecond)
}

// normalizedEnd returns nil for firing alerts, which Alertmanager sends with
// the zero time or a future endsAt.
func (a AlertmanagerAlert) normalizedEnd() *time.Time {
	if a.Status != alertStatusResolved || a.EndsAt.IsZero() || a.EndsAt.Year() <= 1 {
		return nil
	}
	end := a.EndsAt.UTC().Truncate(time.Microsecond)
	return &end
}

var severityRank = map[string]int{"critical": 3, "warning": 2, "info": 1}

// higherSeverity returns whichever severity ranks higher; unknown values rank
// below info and an empty current value always yields to the candidate.
func higherSeverity(current, candidate string) string {
	if current == "" {
		return candidate
	}
	if severityRank[candidate] > severityRank[current] {
		return candidate
	}
	return current
}
