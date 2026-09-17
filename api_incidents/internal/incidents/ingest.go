package incidents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"frameworks/api_incidents/internal/database/lookoutdb"
)

// Ingest outcomes reported to the webhook handler.
const (
	IngestCreated   = "created"
	IngestUpdated   = "updated"
	IngestResolved  = "resolved"
	IngestUnchanged = "unchanged"
	IngestIgnored   = "ignored"
)

// IngestResult describes what one Alertmanager notification changed.
type IngestResult struct {
	Outcome    string
	IncidentID string
}

// errOpenIncidentRace means a concurrent notification opened the group's
// incident between our lookup and insert; the transaction is retried so the
// new row is locked and updated instead.
var errOpenIncidentRace = errors.New("open incident created concurrently")

const maxIngestAttempts = 3

// IngestAlertmanager applies one Alertmanager notification.
//
// A firing alert with no open incident for the group opens one, unless every
// firing alert is a repeat (same fingerprint and startsAt) of an alert in an
// already resolved incident of that group; that keeps a manually resolved
// incident closed while Alertmanager keeps re-notifying. Alerts join the open
// incident, and the incident auto-resolves once none of its alerts fire.
//
// The incident takes the cluster's stored scope, read under a share lock in the
// ingest transaction so an ownership reconciliation either sees the incident
// or runs first. A cluster without a verified stored scope is looked up in
// Quartermaster first; when Quartermaster cannot answer, the incident opens in
// platform scope and a later verified answer moves it.
func (s *Service) IngestAlertmanager(ctx context.Context, hook AlertmanagerWebhook) (IngestResult, error) {
	if err := hook.Validate(); err != nil {
		return IngestResult{}, err
	}
	facts := hook.facts()
	if err := s.verifyClusterScope(ctx, facts.ClusterID); err != nil {
		return IngestResult{}, err
	}
	now := s.now().UTC()

	var (
		result      IngestResult
		changes     []realtimeChange
		transitions []string
		scope       Scope
	)
	for attempt := 1; ; attempt++ {
		err := s.inTx(ctx, func(q *lookoutdb.Queries) error {
			var (
				verified bool
				txErr    error
			)
			scope, verified, txErr = lockClusterScope(ctx, q, facts.ClusterID)
			if txErr != nil {
				return txErr
			}
			if s.afterClusterScopeLock != nil {
				s.afterClusterScopeLock(facts.ClusterID)
			}
			result, changes, transitions, txErr = s.ingestTx(ctx, q, hook, facts, scope, verified, now)
			return txErr
		})
		if errors.Is(err, errOpenIncidentRace) && attempt < maxIngestAttempts {
			continue
		}
		if err != nil {
			return IngestResult{}, err
		}
		break
	}
	incidentScope := scope.Kind
	if len(changes) > 0 {
		incidentScope = changes[len(changes)-1].incident.Scope
	}
	for _, transition := range transitions {
		s.Metrics.observeTransition(incidentScope, transition)
	}
	s.publish(changes)
	return result, nil
}

// verifyClusterScope stores Quartermaster's owner for a cluster that has no
// verified stored scope, moving any open incident the cluster already has, and
// finishes a move an earlier owner change left pending. A failed lookup is not
// an error: ingestion continues with the unverified platform scope.
func (s *Service) verifyClusterScope(ctx context.Context, clusterID string) error {
	if clusterID == "" {
		return nil
	}
	verified, movePending, err := clusterScopeState(ctx, lookoutdb.New(s.DB), clusterID)
	if err != nil {
		return err
	}
	if movePending {
		_, err = s.moveOpenClusterIncidents(ctx, clusterID)
		return err
	}
	if verified || s.Owners == nil {
		return nil
	}
	owner, lookupErr := s.Owners.LookupOwner(ctx, clusterID)
	if lookupErr == nil {
		_, err = s.applyClusterOwner(ctx, clusterID, owner)
	}
	return err
}

func (s *Service) ingestTx(ctx context.Context, q *lookoutdb.Queries, hook AlertmanagerWebhook, facts groupFacts, scope Scope, verified bool, now time.Time) (IngestResult, []realtimeChange, []string, error) {
	var (
		transitions []string
		changes     []realtimeChange
	)
	inc, err := q.GetOpenIncidentByGroupKeyForUpdate(ctx, facts.GroupKey)
	created := false
	switch {
	case errors.Is(err, sql.ErrNoRows):
		opens, openErr := hasNewFiringAlert(ctx, q, hook, facts.GroupKey, scope)
		if openErr != nil {
			return IngestResult{}, nil, nil, openErr
		}
		if !opens {
			return IngestResult{Outcome: IngestIgnored}, nil, nil, nil
		}
		inc, err = q.InsertIncident(ctx, newIncidentParams(hook, facts, scope, now))
		if errors.Is(err, sql.ErrNoRows) {
			return IngestResult{}, nil, nil, errOpenIncidentRace
		}
		if err != nil {
			return IngestResult{}, nil, nil, fmt.Errorf("insert incident: %w", err)
		}
		created = true
		transitions = append(transitions, changeOpened)
	case err != nil:
		return IngestResult{}, nil, nil, fmt.Errorf("lock open incident: %w", err)
	}

	rescoped := false
	if !created && verified {
		var scopeChanges []realtimeChange
		inc, scopeChanges, err = s.rescopeOpenIncident(ctx, q, inc, scope)
		if err != nil {
			return IngestResult{}, nil, nil, err
		}
		if len(scopeChanges) > 0 || scopeDiffers(inc, scope) {
			rescoped = true
		}
		if rescoped {
			changes = append(changes, scopeChanges...)
			transitions = append(transitions, changeScopeChanged)
		}
	}

	existing, err := q.ListIncidentAlerts(ctx, lookoutdb.ListIncidentAlertsParams{IncidentID: inc.ID, TenantID: inc.TenantID})
	if err != nil {
		return IngestResult{}, nil, nil, fmt.Errorf("list incident alerts: %w", err)
	}
	previous := make(map[string]lookoutdb.LookoutIncidentAlert, len(existing))
	for _, alert := range existing {
		previous[alert.Fingerprint] = alert
	}

	severity := inc.Severity
	lastChange := ""
	firstFiringEventID := ""
	for _, alert := range hook.Alerts {
		prev, had := previous[alert.Fingerprint]
		if alert.Status == alertStatusResolved && !had {
			continue
		}
		start := alert.normalizedStart()
		if upsertErr := upsertAlert(ctx, q, inc.ID, alert, start); upsertErr != nil {
			return IngestResult{}, nil, nil, upsertErr
		}
		alertname := strings.TrimSpace(alert.Labels["alertname"])
		switch {
		case alert.Status == alertStatusFiring && (!had || prev.Status != alertStatusFiring || !prev.StartsAt.Equal(start)):
			row, eventErr := s.insertEvent(ctx, q, inc.ID, EventAlertFiring, "", map[string]any{
				"fingerprint": alert.Fingerprint,
				"alertname":   alertname,
				"starts_at":   start,
			})
			if eventErr != nil {
				return IngestResult{}, nil, nil, eventErr
			}
			if firstFiringEventID == "" {
				firstFiringEventID = row.ID
			}
			transitions = append(transitions, EventAlertFiring)
			lastChange = EventAlertFiring
		case alert.Status == alertStatusResolved && prev.Status == alertStatusFiring:
			body := map[string]any{"fingerprint": alert.Fingerprint, "alertname": alertname}
			if end := alert.normalizedEnd(); end != nil {
				body["ends_at"] = *end
			}
			if _, eventErr := s.insertEvent(ctx, q, inc.ID, EventAlertResolved, "", body); eventErr != nil {
				return IngestResult{}, nil, nil, eventErr
			}
			transitions = append(transitions, EventAlertResolved)
			lastChange = EventAlertResolved
		}
		if alert.Status == alertStatusFiring {
			severity = higherSeverity(severity, strings.TrimSpace(alert.Labels["severity"]))
		}
	}

	inc, err = q.UpdateIncidentAlerting(ctx, lookoutdb.UpdateIncidentAlertingParams{
		LastAlertAt: now,
		Severity:    severity,
		ID:          inc.ID,
		TenantID:    inc.TenantID,
	})
	if err != nil {
		return IngestResult{}, nil, nil, fmt.Errorf("update incident alerting: %w", err)
	}

	result := IngestResult{Outcome: IngestUnchanged, IncidentID: inc.ID}
	if created {
		result.Outcome = IngestCreated
		lastChange = changeOpened
		if notifyErr := s.enqueueOperatorNotification(ctx, q, inc, firstFiringEventID, DeliveryEventOpened); notifyErr != nil {
			return IngestResult{}, nil, nil, notifyErr
		}
		if publishErr := s.enqueueIncidentPublication(ctx, q, inc, firstFiringEventID); publishErr != nil {
			return IngestResult{}, nil, nil, publishErr
		}
	} else if lastChange != "" || rescoped {
		result.Outcome = IngestUpdated
	}

	firing, err := q.CountFiringIncidentAlerts(ctx, lookoutdb.CountFiringIncidentAlertsParams{IncidentID: inc.ID, TenantID: inc.TenantID})
	if err != nil {
		return IngestResult{}, nil, nil, fmt.Errorf("count firing alerts: %w", err)
	}
	if firing == 0 {
		inc, err = q.AutoResolveIncident(ctx, lookoutdb.AutoResolveIncidentParams{ID: inc.ID, TenantID: inc.TenantID})
		if err != nil {
			return IngestResult{}, nil, nil, fmt.Errorf("auto-resolve incident: %w", err)
		}
		row, eventErr := s.insertEvent(ctx, q, inc.ID, EventResolved, "", map[string]any{"resolution": ResolutionAuto})
		if eventErr != nil {
			return IngestResult{}, nil, nil, eventErr
		}
		if err := s.enqueueOperatorNotification(ctx, q, inc, row.ID, DeliveryEventResolved); err != nil {
			return IngestResult{}, nil, nil, err
		}
		result.Outcome = IngestResolved
		transitions = append(transitions, "auto_resolved")
		lastChange = EventResolved
	}

	if lastChange != "" {
		changes = append(changes, realtimeChange{incident: inc, change: lastChange})
	}
	return result, changes, transitions, nil
}

// scopeDiffers reports whether an incident's stored scope or tenant differs
// from a resolved scope.
func scopeDiffers(inc lookoutdb.LookoutIncident, scope Scope) bool {
	tenantID := ""
	if scope.Kind == ScopeTenant {
		tenantID = strings.TrimSpace(scope.TenantID)
	}
	storedTenant := ""
	if inc.TenantID.Valid {
		storedTenant = inc.TenantID.String
	}
	return inc.Scope != scope.Kind || storedTenant != tenantID
}

// rescopeOpenIncident moves a locked open incident to a verified scope that
// differs from its stored scope or tenant. The tenant it moved to receives the
// Kafka publication so Skipper can investigate; an unsettled publication for
// the tenant it moved away from is cancelled. Operator notifications already
// queued or sent are kept. It returns the realtime changes for both tenants.
func (s *Service) rescopeOpenIncident(ctx context.Context, q *lookoutdb.Queries, inc lookoutdb.LookoutIncident, scope Scope) (lookoutdb.LookoutIncident, []realtimeChange, error) {
	if !scopeDiffers(inc, scope) {
		return inc, nil, nil
	}
	newTenantID := ""
	if scope.Kind == ScopeTenant {
		newTenantID = scope.TenantID
	}
	previousTenant := inc.TenantID
	rescoped, err := q.RescopeIncident(ctx, lookoutdb.RescopeIncidentParams{
		Scope:       scope.Kind,
		NewTenantID: nullString(newTenantID),
		ID:          inc.ID,
		TenantID:    inc.TenantID,
	})
	if err != nil {
		return inc, nil, fmt.Errorf("rescope incident: %w", err)
	}
	// The timeline records only the scope kinds: the incident may now be visible
	// to a different tenant, which must not learn the previous owner.
	if _, err := s.insertEvent(ctx, q, rescoped.ID, EventScopeChanged, "", map[string]any{
		"from_scope": inc.Scope,
		"to_scope":   rescoped.Scope,
	}); err != nil {
		return inc, nil, err
	}

	var changes []realtimeChange
	if previousTenant.Valid {
		if _, err := q.CancelPendingIncidentPublications(ctx, lookoutdb.CancelPendingIncidentPublicationsParams{
			IncidentID: rescoped.ID,
			TenantID:   previousTenant.String,
		}); err != nil {
			return inc, nil, fmt.Errorf("cancel previous tenant incident publication: %w", err)
		}
		changes = append(changes, realtimeChange{incident: rescoped, change: changeScopeChanged, recipientTenantID: previousTenant.String})
	}
	if rescoped.Scope == ScopeTenant && rescoped.TenantID.Valid {
		eventID, err := q.GetIncidentOpeningEventID(ctx, lookoutdb.GetIncidentOpeningEventIDParams{
			IncidentID: rescoped.ID,
			TenantID:   rescoped.TenantID,
		})
		if err != nil {
			return inc, nil, fmt.Errorf("find incident opening event: %w", err)
		}
		raw, err := incidentPublicationPayload(rescoped)
		if err != nil {
			return inc, nil, err
		}
		if err := q.ArmIncidentPublication(ctx, lookoutdb.ArmIncidentPublicationParams{
			EventID:    eventID,
			IncidentID: rescoped.ID,
			TenantID:   rescoped.TenantID.String,
			Payload:    raw,
		}); err != nil {
			return inc, nil, fmt.Errorf("arm incident publication: %w", err)
		}
		changes = append(changes, realtimeChange{incident: rescoped, change: changeScopeChanged})
	}
	if s.Logger != nil {
		s.Logger.WithField("incident_id", rescoped.ID).
			WithField("from_scope", inc.Scope).
			WithField("to_scope", rescoped.Scope).
			Info("Moved open incident to the verified cluster owner scope")
	}
	return rescoped, changes, nil
}

func hasNewFiringAlert(ctx context.Context, q *lookoutdb.Queries, hook AlertmanagerWebhook, groupKey string, scope Scope) (bool, error) {
	for _, alert := range hook.Alerts {
		if alert.Status != alertStatusFiring {
			continue
		}
		repeated, err := q.ResolvedIncidentAlertRepeatExists(ctx, lookoutdb.ResolvedIncidentAlertRepeatExistsParams{
			GroupKey:    groupKey,
			TenantID:    nullString(scope.TenantID),
			Fingerprint: alert.Fingerprint,
			StartsAt:    alert.normalizedStart(),
		})
		if err != nil {
			return false, fmt.Errorf("check resolved alert repeat: %w", err)
		}
		if !repeated {
			return true, nil
		}
	}
	return false, nil
}

func newIncidentParams(hook AlertmanagerWebhook, facts groupFacts, scope Scope, now time.Time) lookoutdb.InsertIncidentParams {
	severity := ""
	var startedAt time.Time
	for _, alert := range hook.Alerts {
		if alert.Status != alertStatusFiring {
			continue
		}
		severity = higherSeverity(severity, strings.TrimSpace(alert.Labels["severity"]))
		if start := alert.normalizedStart(); startedAt.IsZero() || start.Before(startedAt) {
			startedAt = start
		}
	}
	if startedAt.IsZero() {
		startedAt = now
	}
	tenantID := ""
	if scope.Kind == ScopeTenant {
		tenantID = scope.TenantID
	}
	return lookoutdb.InsertIncidentParams{
		Scope:       scope.Kind,
		TenantID:    nullString(tenantID),
		ClusterID:   facts.ClusterID,
		Region:      facts.Region,
		GroupKey:    facts.GroupKey,
		Alertname:   facts.Alertname,
		Severity:    severity,
		Title:       facts.Title,
		Summary:     facts.Summary,
		StartedAt:   startedAt,
		LastAlertAt: now,
	}
}

func upsertAlert(ctx context.Context, q *lookoutdb.Queries, incidentID string, alert AlertmanagerAlert, start time.Time) error {
	labels, err := json.Marshal(nonNilMap(alert.Labels))
	if err != nil {
		return fmt.Errorf("encode alert labels: %w", err)
	}
	annotations, err := json.Marshal(nonNilMap(alert.Annotations))
	if err != nil {
		return fmt.Errorf("encode alert annotations: %w", err)
	}
	var endsAt sql.NullTime
	if end := alert.normalizedEnd(); end != nil {
		endsAt = sql.NullTime{Time: *end, Valid: true}
	}
	if err := q.UpsertIncidentAlert(ctx, lookoutdb.UpsertIncidentAlertParams{
		IncidentID:   incidentID,
		Fingerprint:  alert.Fingerprint,
		Status:       alert.Status,
		Alertname:    strings.TrimSpace(alert.Labels["alertname"]),
		Labels:       labels,
		Annotations:  annotations,
		StartsAt:     start,
		EndsAt:       endsAt,
		GeneratorUrl: alert.GeneratorURL,
	}); err != nil {
		return fmt.Errorf("upsert incident alert: %w", err)
	}
	return nil
}

func nonNilMap(values map[string]string) map[string]string {
	if values == nil {
		return map[string]string{}
	}
	return values
}
