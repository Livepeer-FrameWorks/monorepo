package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	eventoutbox "github.com/Livepeer-FrameWorks/monorepo/pkg/events/outbox"
	internalv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// DomainEventSchema is the schema of Bosun's domain event outbox.
const DomainEventSchema = "bosun"

// EventSource is the ce_source of the events Bosun emits.
const EventSource = "bosun"

// StoredEvent is the stored form of an event a delivery sends.
type StoredEvent struct {
	ID         string
	Type       string
	SchemaName string
	Payload    []byte
	OccurredAt time.Time
}

// Claim is a leased delivery ready to send.
type Claim struct {
	DeliveryID  string
	TenantID    string
	EndpointID  string
	EndpointURL string
	APIVersion  string
	LeaseToken  string
	LeasedUntil time.Time
	Event       StoredEvent
}

// Claim leases up to limit due event deliveries for Lease, never more than
// MaxInFlightPerEndpoint leased deliveries per endpoint across all replicas.
// A delivery whose lease expired is claimable again under a new token.
// Deliveries of an endpoint that is no longer enabled are skipped instead.
//
// Lock order is endpoint rows, then delivery rows, as in every other writer,
// so claims, settlements, and disables cannot deadlock on each other.
func (s *Store) Claim(ctx context.Context, limit int) ([]Claim, error) {
	if limit <= 0 {
		return nil, nil
	}
	var claims []Claim
	txErr := s.inTx(ctx, func(tx *sql.Tx) error {
		claims = nil
		candidates, err := dueEndpoints(ctx, tx, limit)
		if err != nil || len(candidates) == 0 {
			return err
		}
		locked, err := lockEndpoints(ctx, tx, candidates)
		if err != nil {
			return err
		}
		inflight, err := inflightCounts(ctx, tx, candidates)
		if err != nil {
			return err
		}
		token := uuid.Must(uuid.NewV7()).String()
		for _, endpointID := range candidates {
			ep, ok := locked[endpointID]
			if !ok {
				continue
			}
			if ep.status != StatusEnabled {
				if _, err := tx.ExecContext(ctx, `
					UPDATE bosun.webhook_deliveries
					SET status = 'skipped', finished_at = now(), lease_token = NULL, leased_until = NULL, updated_at = now()
					WHERE tenant_id = $1 AND endpoint_id = $2 AND status = 'pending'`, ep.tenantID, endpointID); err != nil {
					return err
				}
				continue
			}
			room := MaxInFlightPerEndpoint - inflight[endpointID]
			if room <= 0 || len(claims) >= limit {
				continue
			}
			leased, err := leaseEndpointDeliveries(ctx, tx, endpointID, ep, min(room, limit-len(claims)), token)
			if err != nil {
				return err
			}
			claims = append(claims, leased...)
		}
		for i := range claims {
			ev, err := loadDeliveryEvent(ctx, tx, claims[i].TenantID, claims[i].DeliveryID)
			if err != nil {
				return err
			}
			claims[i].Event = ev
		}
		return nil
	})
	return claims, txErr
}

// leaseEndpointDeliveries leases up to n due unleased deliveries of one
// endpoint under token, skipping rows another claimer holds.
func leaseEndpointDeliveries(ctx context.Context, tx *sql.Tx, endpointID string, ep lockedEndpoint, n int, token string) ([]Claim, error) {
	rows, err := tx.QueryContext(ctx, `
		WITH picked AS (
			SELECT tenant_id, id FROM bosun.webhook_deliveries
			WHERE tenant_id = $1 AND endpoint_id = $2 AND kind = 'event' AND status = 'pending'
			  AND next_attempt_at <= now() AND (leased_until IS NULL OR leased_until <= now())
			ORDER BY next_attempt_at, id
			LIMIT $3
			FOR UPDATE SKIP LOCKED
		)
		UPDATE bosun.webhook_deliveries d
		SET lease_token = $4, leased_until = now() + make_interval(secs => $5), updated_at = now()
		FROM picked
		WHERE d.tenant_id = picked.tenant_id AND d.id = picked.id
		RETURNING d.id, d.leased_until`,
		ep.tenantID, endpointID, n, token, Lease.Seconds())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Claim
	for rows.Next() {
		c := Claim{TenantID: ep.tenantID, EndpointID: endpointID, EndpointURL: ep.url, APIVersion: ep.apiVersion, LeaseToken: token}
		if err := rows.Scan(&c.DeliveryID, &c.LeasedUntil); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// dueEndpoints returns, without locking, up to limit endpoints that have a
// due unleased delivery and fewer than MaxInFlightPerEndpoint leased ones,
// longest-waiting first. Endpoints at their in-flight limit are left out, so
// one endpoint's backlog cannot hold back the others.
func dueEndpoints(ctx context.Context, tx *sql.Tx, limit int) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT d.endpoint_id FROM bosun.webhook_deliveries d
		WHERE d.status = 'pending' AND d.kind = 'event' AND d.next_attempt_at <= now()
		  AND (d.leased_until IS NULL OR d.leased_until <= now())
		  AND (SELECT count(*) FROM bosun.webhook_deliveries l
		       WHERE l.endpoint_id = d.endpoint_id AND l.status = 'pending' AND l.lease_token IS NOT NULL
		         AND l.leased_until > now()) < $2
		GROUP BY d.endpoint_id
		ORDER BY min(d.next_attempt_at)
		LIMIT $1`, limit, MaxInFlightPerEndpoint)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

type lockedEndpoint struct {
	tenantID   string
	status     string
	url        string
	apiVersion string
}

func lockEndpoints(ctx context.Context, tx *sql.Tx, ids []string) (map[string]lockedEndpoint, error) {
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	rows, err := tx.QueryContext(ctx, `
		SELECT id, tenant_id, status, url, api_version FROM bosun.webhook_endpoints
		WHERE id = ANY((($1)::text)::uuid[])
		ORDER BY id
		FOR UPDATE`, arrayLiteral(sorted))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]lockedEndpoint{}
	for rows.Next() {
		var id string
		var ep lockedEndpoint
		if err := rows.Scan(&id, &ep.tenantID, &ep.status, &ep.url, &ep.apiVersion); err != nil {
			return nil, err
		}
		out[id] = ep
	}
	return out, rows.Err()
}

func inflightCounts(ctx context.Context, tx *sql.Tx, ids []string) (map[string]int, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT endpoint_id, count(*) FROM bosun.webhook_deliveries
		WHERE endpoint_id = ANY((($1)::text)::uuid[]) AND status = 'pending' AND leased_until > now()
		GROUP BY endpoint_id`, arrayLiteral(ids))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

func loadDeliveryEvent(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, tenantID, deliveryID string) (StoredEvent, error) {
	var ev StoredEvent
	err := q.QueryRowContext(ctx, `
		SELECT ev.event_id, ev.event_type, ev.schema_name, ev.payload, ev.occurred_at
		FROM bosun.webhook_deliveries d
		JOIN bosun.webhook_events ev ON ev.tenant_id = d.tenant_id AND ev.event_id = d.event_id
		WHERE d.tenant_id = $1 AND d.id = $2`, tenantID, deliveryID).
		Scan(&ev.ID, &ev.Type, &ev.SchemaName, &ev.Payload, &ev.OccurredAt)
	if err != nil {
		return StoredEvent{}, fmt.Errorf("load event of delivery %s: %w", deliveryID, err)
	}
	ev.OccurredAt = ev.OccurredAt.UTC()
	return ev, nil
}

// Outcome is the result of one HTTP attempt.
type Outcome struct {
	Success     bool
	StatusCode  int
	ErrorClass  string
	Latency     time.Duration
	Excerpt     string
	AttemptedAt time.Time
}

// SettleResult says what a settlement changed.
type SettleResult struct {
	// Settled is false when the claim's lease token no longer owns the
	// delivery: the lease expired and another worker reclaimed it, or the
	// endpoint was disabled. Nothing was written.
	Settled bool
	// Status is the delivery status after settlement.
	Status string
	// AutoDisabled is true when this failure disabled the endpoint.
	AutoDisabled bool
	// SkippedDeliveries counts the pending deliveries the disable skipped.
	SkippedDeliveries int
}

// Settle records the attempt of a claimed delivery, fenced on its lease
// token. A success finishes the delivery and ends the endpoint's failure
// streak. A failure schedules the next retry from RetrySchedule, or fails the
// delivery when none remains, and extends the streak; when the streak reaches
// AutoDisableFailures attempts over AutoDisableAfter, the endpoint is
// disabled, its pending deliveries are skipped, the billing contact email is
// queued, and a webhook.endpoint_auto_disabled audit event is enqueued, all in
// the same transaction.
func (s *Store) Settle(ctx context.Context, c Claim, out Outcome, jitter func() float64) (SettleResult, error) {
	var result SettleResult
	txErr := s.inTx(ctx, func(tx *sql.Tx) error {
		result = SettleResult{}
		var endpointStatus string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM bosun.webhook_endpoints WHERE tenant_id = $1 AND id = $2 FOR UPDATE`,
			c.TenantID, c.EndpointID).Scan(&endpointStatus); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		var attempts int
		err := tx.QueryRowContext(ctx, `
			SELECT attempts FROM bosun.webhook_deliveries
			WHERE tenant_id = $1 AND id = $2 AND lease_token = $3 AND status = 'pending'
			FOR UPDATE`, c.TenantID, c.DeliveryID, c.LeaseToken).Scan(&attempts)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		result.Settled = true
		attempts++
		switch {
		case out.Success:
			result.Status = DeliverySucceeded
			_, err = tx.ExecContext(ctx, `
				UPDATE bosun.webhook_deliveries
				SET status = 'succeeded', attempts = $3, delivered_at = now(), finished_at = now(),
				    lease_token = NULL, leased_until = NULL, last_status_code = $4, last_error_class = NULL, updated_at = now()
				WHERE tenant_id = $1 AND id = $2`, c.TenantID, c.DeliveryID, attempts, out.StatusCode)
		default:
			if delay, ok := RetryDelay(attempts, jitter); ok {
				result.Status = DeliveryPending
				_, err = tx.ExecContext(ctx, `
					UPDATE bosun.webhook_deliveries
					SET attempts = $3, next_attempt_at = now() + make_interval(secs => $4),
					    lease_token = NULL, leased_until = NULL, last_status_code = $5, last_error_class = $6, updated_at = now()
					WHERE tenant_id = $1 AND id = $2`, c.TenantID, c.DeliveryID, attempts, delay.Seconds(), out.StatusCode, out.ErrorClass)
			} else {
				result.Status = DeliveryFailed
				_, err = tx.ExecContext(ctx, `
					UPDATE bosun.webhook_deliveries
					SET status = 'failed', attempts = $3, finished_at = now(),
					    lease_token = NULL, leased_until = NULL, last_status_code = $4, last_error_class = $5, updated_at = now()
					WHERE tenant_id = $1 AND id = $2`, c.TenantID, c.DeliveryID, attempts, out.StatusCode, out.ErrorClass)
			}
		}
		if err != nil {
			return err
		}
		if err = insertAttempt(ctx, tx, c.TenantID, c.DeliveryID, out); err != nil {
			return err
		}
		if out.Success {
			_, err = tx.ExecContext(ctx, `
				UPDATE bosun.webhook_endpoints
				SET consecutive_failures = 0, failing_since = NULL, last_success_at = now(), updated_at = now()
				WHERE tenant_id = $1 AND id = $2`, c.TenantID, c.EndpointID)
			return err
		}
		var failures int
		var failingSince time.Time
		var due bool
		if err = tx.QueryRowContext(ctx, `
			UPDATE bosun.webhook_endpoints
			SET consecutive_failures = consecutive_failures + 1,
			    failing_since = COALESCE(failing_since, now()),
			    last_failure_at = now(), updated_at = now()
			WHERE tenant_id = $1 AND id = $2
			RETURNING consecutive_failures, failing_since,
			          consecutive_failures >= $3 AND failing_since <= now() - make_interval(secs => $4)`,
			c.TenantID, c.EndpointID, AutoDisableFailures, AutoDisableAfter.Seconds()).
			Scan(&failures, &failingSince, &due); err != nil {
			return err
		}
		if !due || endpointStatus != StatusEnabled {
			return nil
		}
		skipped, err := disableTx(ctx, tx, c.TenantID, c.EndpointID, DisabledByFailing)
		if err != nil {
			return err
		}
		result.AutoDisabled = true
		result.SkippedDeliveries = skipped
		if result.Status == DeliveryPending {
			result.Status = DeliverySkipped
		}
		if _, err = tx.ExecContext(ctx, `
			INSERT INTO bosun.webhook_notification_outbox (id, tenant_id, endpoint_id, kind, endpoint_url)
			VALUES ($1, $2, $3, 'endpoint_disabled', $4)`,
			uuid.Must(uuid.NewV7()).String(), c.TenantID, c.EndpointID, c.EndpointURL); err != nil {
			return err
		}
		ev, err := events.New(EventSource, c.TenantID, c.EndpointID, &internalv1.WebhookEndpointAutoDisabled{
			EndpointId:          c.EndpointID,
			ConsecutiveFailures: int32(failures),
			FailingSince:        timestamppb.New(failingSince),
			SkippedDeliveries:   int32(skipped),
		})
		if err != nil {
			return err
		}
		return eventoutbox.Enqueue(ctx, tx, DomainEventSchema, ev)
	})
	return result, txErr
}

// SettleInternal settles a claimed delivery that Bosun could not send for a
// reason of its own, fenced on its lease token like Settle. A permanent
// failure fails the delivery now; a transient one releases the lease and
// schedules the delivery after the delay that follows its next attempt, so it
// neither holds a lease slot nor spends an attempt. errorClass is stored as
// the delivery's last error class. No attempt row is written and the
// endpoint's failure streak is not touched, so a Bosun-side failure never
// counts toward disabling the tenant's endpoint.
func (s *Store) SettleInternal(ctx context.Context, c Claim, errorClass string, permanent bool, jitter func() float64) (SettleResult, error) {
	var result SettleResult
	txErr := s.inTx(ctx, func(tx *sql.Tx) error {
		result = SettleResult{}
		var endpointStatus string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM bosun.webhook_endpoints WHERE tenant_id = $1 AND id = $2 FOR UPDATE`,
			c.TenantID, c.EndpointID).Scan(&endpointStatus); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		var attempts int
		err := tx.QueryRowContext(ctx, `
			SELECT attempts FROM bosun.webhook_deliveries
			WHERE tenant_id = $1 AND id = $2 AND lease_token = $3 AND status = 'pending'
			FOR UPDATE`, c.TenantID, c.DeliveryID, c.LeaseToken).Scan(&attempts)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		result.Settled = true
		if permanent {
			result.Status = DeliveryFailed
			_, err = tx.ExecContext(ctx, `
				UPDATE bosun.webhook_deliveries
				SET status = 'failed', finished_at = now(), lease_token = NULL, leased_until = NULL,
				    last_status_code = NULL, last_error_class = $3, updated_at = now()
				WHERE tenant_id = $1 AND id = $2`, c.TenantID, c.DeliveryID, errorClass)
			return err
		}
		delay, ok := RetryDelay(attempts+1, jitter)
		if !ok {
			delay, _ = RetryDelay(len(RetrySchedule), jitter)
		}
		result.Status = DeliveryPending
		_, err = tx.ExecContext(ctx, `
			UPDATE bosun.webhook_deliveries
			SET next_attempt_at = now() + make_interval(secs => $3), lease_token = NULL, leased_until = NULL,
			    last_error_class = $4, updated_at = now()
			WHERE tenant_id = $1 AND id = $2`, c.TenantID, c.DeliveryID, delay.Seconds(), errorClass)
		return err
	})
	return result, txErr
}

func insertAttempt(ctx context.Context, tx *sql.Tx, tenantID, deliveryID string, out Outcome) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO bosun.webhook_delivery_attempts
			(id, tenant_id, delivery_id, attempt_number, status_code, error_class, latency_ms, response_excerpt, attempted_at)
		VALUES ($1, $2, $3,
			(SELECT count(*) + 1 FROM bosun.webhook_delivery_attempts WHERE tenant_id = $2 AND delivery_id = $3),
			$4, $5, $6, $7, $8)`,
		uuid.Must(uuid.NewV7()).String(), tenantID, deliveryID, out.StatusCode, out.ErrorClass,
		int(out.Latency/time.Millisecond), truncateExcerpt(out.Excerpt), out.AttemptedAt.UTC())
	return err
}

// MaxExcerptBytes bounds the stored response excerpt.
const MaxExcerptBytes = 1024

// truncateExcerpt cuts s to at most MaxExcerptBytes without splitting a UTF-8
// sequence, and drops invalid bytes and NULs, which PostgreSQL text rejects.
func truncateExcerpt(s string) string {
	out := make([]rune, 0, len(s))
	size := 0
	for _, r := range s {
		if r == 0 || r == '�' {
			continue
		}
		n := len(string(r))
		if size+n > MaxExcerptBytes {
			break
		}
		size += n
		out = append(out, r)
	}
	return string(out)
}

// RecordTestDelivery stores a finished test delivery and its attempt. Tests
// do not count toward the endpoint's failure streak.
func (s *Store) RecordTestDelivery(ctx context.Context, tenantID, endpointID, deliveryID string, out Outcome) (Delivery, Attempt, error) {
	status := DeliveryFailed
	if out.Success {
		status = DeliverySucceeded
	}
	var d Delivery
	var a Attempt
	txErr := s.inTx(ctx, func(tx *sql.Tx) error {
		var delivered any
		if out.Success {
			delivered = out.AttemptedAt.UTC()
		}
		var errorClass any
		if out.ErrorClass != "" {
			errorClass = out.ErrorClass
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO bosun.webhook_deliveries
				(id, tenant_id, endpoint_id, event_type, kind, status, attempts, last_status_code, last_error_class, delivered_at, finished_at)
			VALUES ($1, $2, $3, $4, 'test', $5, 1, $6, $7, $8, now())`,
			deliveryID, tenantID, endpointID, TestEventType, status, out.StatusCode, errorClass, delivered); err != nil {
			return err
		}
		if err := insertAttempt(ctx, tx, tenantID, deliveryID, out); err != nil {
			return err
		}
		var err error
		if d, err = getDelivery(ctx, tx, tenantID, deliveryID); err != nil {
			return err
		}
		attempts, err := listAttempts(ctx, tx, tenantID, deliveryID)
		if err != nil {
			return err
		}
		if len(attempts) > 0 {
			a = attempts[0]
		}
		return nil
	})
	return d, a, txErr
}

const deliveryColumns = `d.id, d.tenant_id, d.endpoint_id, COALESCE(d.event_id::text, ''), d.event_type, d.kind, d.status, d.attempts,
	CASE WHEN d.status = 'pending' THEN d.next_attempt_at END, COALESCE(d.last_status_code, 0), COALESCE(d.last_error_class, ''),
	d.delivered_at, d.replay_count, d.last_replayed_at, d.created_at, d.updated_at`

func scanDelivery(row rowScanner) (Delivery, error) {
	var (
		d                             Delivery
		next, delivered, lastReplayed sql.NullTime
	)
	if err := row.Scan(&d.ID, &d.TenantID, &d.EndpointID, &d.EventID, &d.EventType, &d.Kind, &d.Status, &d.Attempts,
		&next, &d.LastStatusCode, &d.LastErrorClass, &delivered, &d.ReplayCount, &lastReplayed, &d.CreatedAt, &d.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Delivery{}, ErrNotFound
		}
		return Delivery{}, err
	}
	d.NextAttemptAt = timePtr(next)
	d.DeliveredAt = timePtr(delivered)
	d.LastReplayedAt = timePtr(lastReplayed)
	d.CreatedAt = d.CreatedAt.UTC()
	d.UpdatedAt = d.UpdatedAt.UTC()
	return d, nil
}

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func getDelivery(ctx context.Context, q queryRower, tenantID, deliveryID string) (Delivery, error) {
	return scanDelivery(q.QueryRowContext(ctx, `SELECT `+deliveryColumns+` FROM bosun.webhook_deliveries d WHERE d.tenant_id = $1 AND d.id = $2`, tenantID, deliveryID))
}

func listAttempts(ctx context.Context, q queryRower, tenantID, deliveryID string) ([]Attempt, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT id, attempt_number, status_code, error_class, latency_ms, response_excerpt, attempted_at
		FROM bosun.webhook_delivery_attempts
		WHERE tenant_id = $1 AND delivery_id = $2
		ORDER BY attempted_at, attempt_number`, tenantID, deliveryID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Attempt
	for rows.Next() {
		var a Attempt
		if err := rows.Scan(&a.ID, &a.AttemptNumber, &a.StatusCode, &a.ErrorClass, &a.LatencyMS, &a.ResponseExcerpt, &a.AttemptedAt); err != nil {
			return nil, err
		}
		a.AttemptedAt = a.AttemptedAt.UTC()
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetDelivery returns one delivery of the tenant with its attempts, oldest
// first.
func (s *Store) GetDelivery(ctx context.Context, tenantID, deliveryID string) (Delivery, []Attempt, error) {
	if _, err := uuid.Parse(deliveryID); err != nil {
		return Delivery{}, nil, ErrNotFound
	}
	d, err := getDelivery(ctx, s.DB, tenantID, deliveryID)
	if err != nil {
		return Delivery{}, nil, err
	}
	attempts, err := listAttempts(ctx, s.DB, tenantID, deliveryID)
	return d, attempts, err
}

// DeliveryFilter selects deliveries for ListDeliveries.
type DeliveryFilter struct {
	EndpointID    string
	Statuses      []string
	EventType     string
	EventID       string
	CreatedAfter  *time.Time
	CreatedBefore *time.Time
	// AfterCreatedAt and AfterID continue a newest-first listing after the
	// last row of the previous page.
	AfterCreatedAt *time.Time
	AfterID        string
	Limit          int
}

const deliveryFilterWhere = `d.tenant_id = $1
		  AND ($2::uuid IS NULL OR d.endpoint_id = $2::uuid)
		  AND ($3::text IS NULL OR d.status = ANY((($3)::text)::text[]))
		  AND ($4::text IS NULL OR d.event_type = $4::text)
		  AND ($5::uuid IS NULL OR d.event_id = $5::uuid)
		  AND ($6::timestamptz IS NULL OR d.created_at >= $6::timestamptz)
		  AND ($7::timestamptz IS NULL OR d.created_at < $7::timestamptz)`

// ListDeliveries returns the tenant's deliveries newest first, one more than
// Limit when more remain, and the number matching the filter on all pages.
func (s *Store) ListDeliveries(ctx context.Context, tenantID string, f DeliveryFilter) ([]Delivery, int, error) {
	var endpointID, eventID, statuses, eventType, after, afterID, createdAfter, createdBefore any
	if f.EndpointID != "" {
		if _, err := uuid.Parse(f.EndpointID); err != nil {
			return nil, 0, nil
		}
		endpointID = f.EndpointID
	}
	if f.EventID != "" {
		if _, err := uuid.Parse(f.EventID); err != nil {
			return nil, 0, nil
		}
		eventID = f.EventID
	}
	if len(f.Statuses) > 0 {
		statuses = arrayLiteral(f.Statuses)
	}
	if f.EventType != "" {
		eventType = f.EventType
	}
	if f.AfterCreatedAt != nil && f.AfterID != "" {
		after, afterID = f.AfterCreatedAt.UTC(), f.AfterID
	}
	if f.CreatedAfter != nil {
		createdAfter = f.CreatedAfter.UTC()
	}
	if f.CreatedBefore != nil {
		createdBefore = f.CreatedBefore.UTC()
	}
	var total int
	if err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM bosun.webhook_deliveries d WHERE `+deliveryFilterWhere,
		tenantID, endpointID, statuses, eventType, eventID, createdAfter, createdBefore).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.DB.QueryContext(ctx, `
		SELECT `+deliveryColumns+` FROM bosun.webhook_deliveries d
		WHERE `+deliveryFilterWhere+`
		  AND ($8::timestamptz IS NULL OR (d.created_at, d.id) < ($8::timestamptz, $9::uuid))
		ORDER BY d.created_at DESC, d.id DESC
		LIMIT $10`,
		tenantID, endpointID, statuses, eventType, eventID, createdAfter, createdBefore, after, afterID, f.Limit+1)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	var out []Delivery
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, d)
	}
	return out, total, rows.Err()
}

// ReplayDelivery returns a finished event delivery to pending under the same
// ID, with its retry schedule restarted. The endpoint must be enabled.
func (s *Store) ReplayDelivery(ctx context.Context, tenantID, deliveryID string) (Delivery, error) {
	if _, err := uuid.Parse(deliveryID); err != nil {
		return Delivery{}, ErrNotFound
	}
	var d Delivery
	txErr := s.inTx(ctx, func(tx *sql.Tx) error {
		current, err := getDelivery(ctx, tx, tenantID, deliveryID)
		if err != nil {
			return err
		}
		if err = lockEnabledEndpoint(ctx, tx, tenantID, current.EndpointID); err != nil {
			return err
		}
		// Prune may have deleted the delivery while this waited for the
		// endpoint lock; read it again under the lock.
		if current, err = getDelivery(ctx, tx, tenantID, deliveryID); err != nil {
			return err
		}
		if current.Kind != KindEvent {
			return fmt.Errorf("%w: a test delivery cannot be replayed", ErrPrecondition)
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE bosun.webhook_deliveries
			SET status = 'pending', attempts = 0, next_attempt_at = now(), finished_at = NULL,
			    lease_token = NULL, leased_until = NULL, replay_count = replay_count + 1,
			    last_replayed_at = now(), updated_at = now()
			WHERE tenant_id = $1 AND id = $2 AND status <> 'pending'`, tenantID, deliveryID)
		if err != nil {
			return err
		}
		changed, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if changed == 0 {
			return fmt.Errorf("%w: the delivery is already pending", ErrPrecondition)
		}
		d, err = getDelivery(ctx, tx, tenantID, deliveryID)
		return err
	})
	return d, txErr
}

// ReplayRange returns up to ReplayRangeLimit failed or skipped event
// deliveries of one enabled endpoint, created in [from, to), to pending,
// oldest first. hasMore reports that more remain in the range.
func (s *Store) ReplayRange(ctx context.Context, tenantID, endpointID string, from, to time.Time) (int, bool, error) {
	if _, parseErr := uuid.Parse(endpointID); parseErr != nil {
		return 0, false, ErrNotFound
	}
	if !to.After(from) {
		return 0, false, fmt.Errorf("%w: the range end must be after its start", ErrPrecondition)
	}
	var replayed int
	var hasMore bool
	txErr := s.inTx(ctx, func(tx *sql.Tx) error {
		err := lockEnabledEndpoint(ctx, tx, tenantID, endpointID)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE bosun.webhook_deliveries d
			SET status = 'pending', attempts = 0, next_attempt_at = now(), finished_at = NULL,
			    lease_token = NULL, leased_until = NULL, replay_count = d.replay_count + 1,
			    last_replayed_at = now(), updated_at = now()
			FROM (
				SELECT tenant_id, id FROM bosun.webhook_deliveries
				WHERE tenant_id = $1 AND endpoint_id = $2 AND kind = 'event' AND status IN ('failed', 'skipped')
				  AND created_at >= $3 AND created_at < $4
				ORDER BY created_at, id
				LIMIT $5
				FOR UPDATE
			) picked
			WHERE d.tenant_id = picked.tenant_id AND d.id = picked.id`,
			tenantID, endpointID, from.UTC(), to.UTC(), ReplayRangeLimit)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		replayed = int(n)
		return tx.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM bosun.webhook_deliveries
				WHERE tenant_id = $1 AND endpoint_id = $2 AND kind = 'event' AND status IN ('failed', 'skipped')
				  AND created_at >= $3 AND created_at < $4)`,
			tenantID, endpointID, from.UTC(), to.UTC()).Scan(&hasMore)
	})
	return replayed, hasMore, txErr
}

func lockEnabledEndpoint(ctx context.Context, tx *sql.Tx, tenantID, endpointID string) error {
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM bosun.webhook_endpoints WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, tenantID, endpointID).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if status != StatusEnabled {
		return fmt.Errorf("%w: the endpoint is disabled; enable it before replaying", ErrPrecondition)
	}
	return nil
}

// OldestDueSeconds returns how long the oldest due pending delivery has been
// waiting, or 0 when none is due.
func (s *Store) OldestDueSeconds(ctx context.Context) (float64, error) {
	var seconds sql.NullFloat64
	err := s.DB.QueryRowContext(ctx, `
		SELECT EXTRACT(EPOCH FROM now() - min(next_attempt_at))::float8
		FROM bosun.webhook_deliveries
		WHERE status = 'pending' AND next_attempt_at <= now()`).Scan(&seconds)
	if err != nil {
		return 0, err
	}
	if !seconds.Valid || seconds.Float64 < 0 {
		return 0, nil
	}
	return seconds.Float64, nil
}
