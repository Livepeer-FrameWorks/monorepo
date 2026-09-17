package demo

import (
	"time"

	"frameworks/api_gateway/graph/model"
)

const (
	demoIncidentFiringID   = "1dc1de17-0000-4000-8000-000000000001"
	demoIncidentResolvedID = "1dc1de17-0000-4000-8000-000000000002"
)

func demoIncidentBase() time.Time {
	return time.Date(2026, time.September, 14, 9, 30, 0, 0, time.UTC)
}

// GenerateIncidents returns incidents on the demo tenant's self-hosted cluster.
func GenerateIncidents() []*model.Incident {
	base := demoIncidentBase()
	tenantID := DemoTenantID
	clusterID := DemoSelfHostedCluster
	region := "eu-west"
	firingSummary := "Edge node edge-2 has not reported a heartbeat for 5 minutes."
	resolvedSummary := "Ingest bitrate on edge-1 dropped below the configured floor."
	resolvedAt := base.Add(-22 * time.Hour)
	auto := model.IncidentResolutionAuto
	return []*model.Incident{
		{
			ID:               demoIncidentFiringID,
			Scope:            model.IncidentScopeTenant,
			TenantID:         &tenantID,
			ClusterID:        &clusterID,
			Region:           &region,
			Alertname:        "EdgeNodeHeartbeatMissing",
			Severity:         "critical",
			Status:           model.IncidentStatusFiring,
			Title:            "Edge node heartbeat missing",
			Summary:          &firingSummary,
			FiringAlertCount: 1,
			StartedAt:        base,
			LastAlertAt:      base.Add(5 * time.Minute),
			CreatedAt:        base,
			UpdatedAt:        base.Add(5 * time.Minute),
		},
		{
			ID:               demoIncidentResolvedID,
			Scope:            model.IncidentScopeTenant,
			TenantID:         &tenantID,
			ClusterID:        &clusterID,
			Region:           &region,
			Alertname:        "IngestBitrateLow",
			Severity:         "warning",
			Status:           model.IncidentStatusResolved,
			Resolution:       &auto,
			Title:            "Ingest bitrate low",
			Summary:          &resolvedSummary,
			FiringAlertCount: 0,
			StartedAt:        base.Add(-23 * time.Hour),
			LastAlertAt:      resolvedAt,
			ResolvedAt:       &resolvedAt,
			CreatedAt:        base.Add(-23 * time.Hour),
			UpdatedAt:        resolvedAt,
		},
	}
}

// GenerateIncidentDetail returns the alerts and timeline of a demo incident, or
// nil for an unknown ID.
func GenerateIncidentDetail(id string) *model.IncidentDetail {
	var incident *model.Incident
	for _, candidate := range GenerateIncidents() {
		if candidate.ID == id {
			incident = candidate
		}
	}
	if incident == nil {
		return nil
	}
	fingerprint := "demo-" + incident.Alertname
	alert := &model.IncidentAlert{
		Fingerprint: fingerprint,
		Status:      "firing",
		Labels: map[string]any{
			"alertname": incident.Alertname,
			"severity":  incident.Severity,
			"cluster":   DemoSelfHostedCluster,
		},
		Annotations: map[string]any{"summary": deref(incident.Summary)},
		StartsAt:    incident.StartedAt,
	}
	alertname := incident.Alertname
	timeline := []*model.IncidentTimelineEvent{{
		ID:               incident.ID + "-firing",
		Kind:             model.IncidentEventKindAlertFiring,
		CreatedAt:        incident.StartedAt,
		AlertFingerprint: &fingerprint,
		Alertname:        &alertname,
	}}
	if incident.Status == model.IncidentStatusResolved {
		alert.Status = "resolved"
		alert.EndsAt = incident.ResolvedAt
		timeline = append(timeline,
			&model.IncidentTimelineEvent{
				ID:               incident.ID + "-alert-resolved",
				Kind:             model.IncidentEventKindAlertResolved,
				CreatedAt:        *incident.ResolvedAt,
				AlertFingerprint: &fingerprint,
				Alertname:        &alertname,
			},
			&model.IncidentTimelineEvent{
				ID:         incident.ID + "-resolved",
				Kind:       model.IncidentEventKindResolved,
				CreatedAt:  *incident.ResolvedAt,
				Resolution: incident.Resolution,
			},
		)
	}
	return &model.IncidentDetail{
		Incident: incident,
		Alerts:   []*model.IncidentAlert{alert},
		Timeline: timeline,
	}
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
