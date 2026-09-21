//go:build schema_verify

package notify

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_incidents/internal/incidents"
	"frameworks/api_incidents/internal/lookouttest"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/outbox"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/serviceevents"

	"github.com/prometheus/client_golang/prometheus"
	promtestutil "github.com/prometheus/client_golang/prometheus/testutil"
)

type slackOnlyRouter struct{}

func (slackOnlyRouter) ChannelsFor(string) []string { return []string{incidents.ChannelSlack} }

type allEnabled struct{}

func (allEnabled) Enabled(string) bool { return true }

func ingestPlatformIncident(t *testing.T, svc *incidents.Service, groupKey string) string {
	t.Helper()
	result, err := svc.IngestAlertmanager(context.Background(), incidents.AlertmanagerWebhook{
		GroupKey:    groupKey,
		GroupLabels: map[string]string{"alertname": "EdgeDown", "cluster": "platform-cluster"},
		Alerts: []incidents.AlertmanagerAlert{{
			Status:      "firing",
			Labels:      map[string]string{"alertname": "EdgeDown", "severity": "critical"},
			StartsAt:    time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC),
			Fingerprint: groupKey + "-fp",
		}},
	})
	if err != nil || result.Outcome != incidents.IngestCreated {
		t.Fatalf("ingest = %+v, %v", result, err)
	}
	return result.IncidentID
}

func claimOne(t *testing.T, store *Store, lease time.Duration, want int) []outbox.Claim[Delivery] {
	t.Helper()
	claims, err := store.ClaimBatch(context.Background(), 10, lease)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != want {
		t.Fatalf("claimed %d rows, want %d", len(claims), want)
	}
	return claims
}

func testMetrics() *incidents.Metrics {
	return &incidents.Metrics{
		Deliveries:    prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_deliveries_total"}, []string{"channel", "result"}),
		OutboxDeleted: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_outbox_rows_deleted_total"}, []string{"state"}),
	}
}

func TestLookoutDeliveryOutboxTokenFencing_RealPG(t *testing.T) {
	runDeliveryOutboxTokenFencing(t, lookouttest.StartPostgres(t))
}

func TestLookoutDeliveryOutboxTokenFencing_RealYugabyte(t *testing.T) {
	runDeliveryOutboxTokenFencing(t, lookouttest.StartYugabyte(t))
}

func TestLookoutDeliveryTerminalFailure_RealPG(t *testing.T) {
	runDeliveryTerminalFailure(t, lookouttest.StartPostgres(t))
}

func TestLookoutDeliveryTerminalFailure_RealYugabyte(t *testing.T) {
	runDeliveryTerminalFailure(t, lookouttest.StartYugabyte(t))
}

func TestLookoutDeliveryRetention_RealPG(t *testing.T) {
	runDeliveryRetention(t, lookouttest.StartPostgres(t))
}

func TestLookoutDeliveryRetention_RealYugabyte(t *testing.T) {
	runDeliveryRetention(t, lookouttest.StartYugabyte(t))
}

func TestLookoutOperatorActivityOutbox_RealPG(t *testing.T) {
	runOperatorActivityOutbox(t, lookouttest.StartPostgres(t))
}

func TestLookoutOperatorActivityOutbox_RealYugabyte(t *testing.T) {
	runOperatorActivityOutbox(t, lookouttest.StartYugabyte(t))
}

func runOperatorActivityOutbox(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	store := &ActivityStore{DB: db, Metrics: testMetrics()}
	activity := OperatorActivity{
		SourceEventID: "event-signup-1",
		EventType:     "tenant_created",
		TenantID:      "11111111-1111-4111-8111-111111111111",
		Payload: ActivityPayload{
			Headline: "New tenant signup", Category: "growth",
		},
	}
	channels := []string{incidents.ChannelSlack, incidents.ChannelDiscord}
	if err := store.EnqueueActivity(ctx, activity, channels); err != nil {
		t.Fatal(err)
	}
	if err := store.EnqueueActivity(ctx, activity, channels); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM lookout.operator_activity_outbox WHERE source_event_id = $1`, activity.SourceEventID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Fatalf("outbox rows = %d, want one per channel", rows)
	}
	platformActivity := OperatorActivity{
		SourceEventID: "event-marketing-1",
		EventType:     serviceevents.MarketingContactDelivered,
		Payload: ActivityPayload{
			Headline: "Contact form delivered", Category: "growth",
		},
	}
	if err := store.EnqueueActivity(ctx, platformActivity, []string{incidents.ChannelSlack}); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM lookout.operator_activity_outbox WHERE source_event_id = $1 AND tenant_id IS NULL`, platformActivity.SourceEventID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("platform outbox rows = %d, want 1 with a NULL tenant", rows)
	}
	claims, err := store.ClaimBatch(ctx, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 3 {
		t.Fatalf("claims = %d, want 3", len(claims))
	}
	for _, claim := range claims {
		if err := store.MarkCompletedToken(ctx, claim.ID, "00000000-0000-4000-8000-000000000000"); !errors.Is(err, errLeaseLost) {
			t.Fatalf("wrong-token completion error = %v, want %v", err, errLeaseLost)
		}
		if err := store.MarkCompletedToken(ctx, claim.ID, claim.LeaseToken); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM lookout.operator_activity_outbox WHERE source_event_id = $1 AND delivered_at IS NOT NULL`, activity.SourceEventID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Fatalf("delivered rows = %d, want 2", rows)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM lookout.operator_activity_outbox WHERE source_event_id = $1 AND delivered_at IS NOT NULL`, platformActivity.SourceEventID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("delivered platform rows = %d, want 1", rows)
	}
}

func runDeliveryOutboxTokenFencing(t *testing.T, db *sql.DB) {
	ctx := context.Background()
	svc := &incidents.Service{DB: db, Router: slackOnlyRouter{}}
	incidentID := ingestPlatformIncident(t, svc, "fencing-group")
	store := &Store{DB: db, Channels: allEnabled{}}

	first := claimOne(t, store, time.Minute, 1)[0]
	claimOne(t, store, time.Minute, 0)
	time.Sleep(50 * time.Millisecond)
	second := claimOne(t, store, time.Millisecond, 1)[0]
	if second.LeaseToken == first.LeaseToken || second.ID != first.ID {
		t.Fatalf("re-claim after lease expiry = %+v, first = %+v", second, first)
	}
	if err := store.MarkCompletedToken(ctx, first.ID, first.LeaseToken); !errors.Is(err, errLeaseLost) {
		t.Fatalf("stale completion = %v, want errLeaseLost", err)
	}
	if err := store.RecordFailureToken(ctx, first.ID, 0, nil, errors.New("stale"), time.Second, first.LeaseToken); !errors.Is(err, errLeaseLost) {
		t.Fatalf("stale failure = %v, want errLeaseLost", err)
	}

	if err := store.RecordFailureToken(ctx, second.ID, second.Attempts, nil, errors.New("slack returned 500"), time.Hour, second.LeaseToken); err != nil {
		t.Fatal(err)
	}
	var (
		attempts int
		lastErr  sql.NullString
		token    sql.NullString
		failedAt sql.NullTime
	)
	if err := db.QueryRow(`SELECT attempts, last_error, lease_token::text, failed_at FROM lookout.notification_outbox WHERE incident_id = $1`, incidentID).Scan(&attempts, &lastErr, &token, &failedAt); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || lastErr.String != "slack returned 500" || token.Valid || failedAt.Valid {
		t.Fatalf("after failure attempts=%d last_error=%v lease_token=%v failed_at=%v", attempts, lastErr, token, failedAt)
	}
	claimOne(t, store, time.Minute, 0)

	if _, err := db.Exec(`UPDATE lookout.notification_outbox SET next_attempt_at = NOW() - interval '1 second' WHERE incident_id = $1`, incidentID); err != nil {
		t.Fatal(err)
	}
	third := claimOne(t, store, time.Minute, 1)[0]
	if third.Attempts != 1 {
		t.Fatalf("third claim attempts = %d, want 1", third.Attempts)
	}
	if err := store.MarkCompletedToken(ctx, third.ID, third.LeaseToken); err != nil {
		t.Fatal(err)
	}
	var notified int
	if err := db.QueryRow(`SELECT COUNT(*) FROM lookout.incident_events WHERE incident_id = $1 AND kind = 'notified' AND body->>'channel' = 'slack'`, incidentID).Scan(&notified); err != nil {
		t.Fatal(err)
	}
	if notified != 1 {
		t.Fatalf("notified events = %d, want 1", notified)
	}
	if err := store.MarkCompletedToken(ctx, third.ID, third.LeaseToken); !errors.Is(err, errLeaseLost) {
		t.Fatalf("second completion = %v, want errLeaseLost", err)
	}
	claimOne(t, store, time.Minute, 0)

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	retriedID := ingestPlatformIncident(t, svc, "worker-group")
	worker := &outbox.Worker[Delivery]{
		Config:     outbox.Config{BaseBackoff: time.Millisecond, MaxBackoff: time.Millisecond, BatchSize: 10, Lease: time.Minute},
		Store:      store,
		Dispatcher: settingsDispatcher(Settings{SlackWebhookURL: srv.URL}, srv.Client()),
	}
	worker.ProcessBatch(ctx)
	time.Sleep(50 * time.Millisecond)
	worker.ProcessBatch(ctx)
	var delivered sql.NullTime
	if err := db.QueryRow(`SELECT delivered_at FROM lookout.notification_outbox WHERE incident_id = $1`, retriedID).Scan(&delivered); err != nil {
		t.Fatal(err)
	}
	if !delivered.Valid || hits.Load() != 2 {
		t.Fatalf("worker delivery delivered=%v hits=%d, want delivered after one retry", delivered, hits.Load())
	}
}

// runDeliveryTerminalFailure proves a webhook that keeps answering 4xx stops
// being retried once a row reaches maxDeliveryAttempts, while a row below the
// bound stays retryable.
func runDeliveryTerminalFailure(t *testing.T, db *sql.DB) {
	ctx := context.Background()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)

	svc := &incidents.Service{DB: db, Router: slackOnlyRouter{}}
	metrics := testMetrics()
	store := &Store{DB: db, Channels: allEnabled{}, Metrics: metrics}
	terminalFailureDispatcher := settingsDispatcher(Settings{SlackWebhookURL: srv.URL}, srv.Client())
	terminalFailureDispatcher.Metrics = metrics
	worker := &outbox.Worker[Delivery]{
		Config:     outbox.Config{BaseBackoff: time.Millisecond, MaxBackoff: time.Millisecond, BatchSize: 10, Lease: time.Minute},
		Store:      store,
		Dispatcher: terminalFailureDispatcher,
	}

	exhaustedID := ingestPlatformIncident(t, svc, "exhausted-group")
	if _, err := db.Exec(`UPDATE lookout.notification_outbox SET attempts = $1 WHERE incident_id = $2`, maxDeliveryAttempts-1, exhaustedID); err != nil {
		t.Fatal(err)
	}
	worker.ProcessBatch(ctx)

	var (
		attempts  int
		lastErr   sql.NullString
		failedAt  sql.NullTime
		delivered sql.NullTime
		token     sql.NullString
	)
	if err := db.QueryRow(`SELECT attempts, last_error, failed_at, delivered_at, lease_token::text FROM lookout.notification_outbox WHERE incident_id = $1`, exhaustedID).Scan(&attempts, &lastErr, &failedAt, &delivered, &token); err != nil {
		t.Fatal(err)
	}
	if attempts != maxDeliveryAttempts || !failedAt.Valid || delivered.Valid || token.Valid || !strings.Contains(lastErr.String, "unexpected status 400") {
		t.Fatalf("exhausted row attempts=%d failed_at=%v delivered_at=%v lease_token=%v last_error=%q", attempts, failedAt, delivered, token, lastErr.String)
	}
	if got := promtestutil.ToFloat64(metrics.Deliveries.WithLabelValues(incidents.ChannelSlack, "failed")); got != 1 {
		t.Fatalf("terminal failure metric = %v, want 1", got)
	}
	if _, err := db.Exec(`UPDATE lookout.notification_outbox SET next_attempt_at = NOW() - interval '1 second' WHERE incident_id = $1`, exhaustedID); err != nil {
		t.Fatal(err)
	}
	claimOne(t, store, time.Minute, 0)

	retryableID := ingestPlatformIncident(t, svc, "retryable-group")
	worker.ProcessBatch(ctx)
	failedAt = sql.NullTime{}
	if err := db.QueryRow(`SELECT attempts, failed_at FROM lookout.notification_outbox WHERE incident_id = $1`, retryableID).Scan(&attempts, &failedAt); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || failedAt.Valid {
		t.Fatalf("row below the attempt bound attempts=%d failed_at=%v, want retryable", attempts, failedAt)
	}
	if got := promtestutil.ToFloat64(metrics.Deliveries.WithLabelValues(incidents.ChannelSlack, "failed")); got != 1 {
		t.Fatalf("terminal failure metric after a retryable failure = %v, want 1", got)
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("webhook hits = %d, want 2", got)
	}

	// A Kafka publication at the same attempt count stays pending: produce
	// failures are platform outages and the row carries the tenant's Skipper
	// investigation trigger.
	kafkaIncident := ingestPlatformIncident(t, svc, "kafka-outage-group")
	var kafkaOutboxID string
	if err := db.QueryRow(`SELECT id::text FROM lookout.notification_outbox WHERE incident_id = $1`, kafkaIncident).Scan(&kafkaOutboxID); err != nil {
		t.Fatal(err)
	}
	const kafkaLease = "3c0ffee0-0000-4000-8000-000000000001"
	if _, err := db.Exec(`UPDATE lookout.notification_outbox SET channel = 'kafka', attempts = $1, claimed_at = NOW(), lease_token = $2 WHERE id = $3`,
		maxDeliveryAttempts-1, kafkaLease, kafkaOutboxID); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordFailureToken(ctx, claimID("", kafkaOutboxID), maxDeliveryAttempts-1, nil, errors.New("broker unavailable"), time.Millisecond, kafkaLease); err != nil {
		t.Fatal(err)
	}
	failedAt = sql.NullTime{}
	if err := db.QueryRow(`SELECT attempts, failed_at FROM lookout.notification_outbox WHERE id = $1`, kafkaOutboxID).Scan(&attempts, &failedAt); err != nil {
		t.Fatal(err)
	}
	if attempts != maxDeliveryAttempts || failedAt.Valid {
		t.Fatalf("kafka row at the attempt bound attempts=%d failed_at=%v, want still pending", attempts, failedAt)
	}
}

// runDeliveryRetention proves settled rows past retention are deleted while
// recent settled rows and unsettled rows remain.
func runDeliveryRetention(t *testing.T, db *sql.DB) {
	ctx := context.Background()
	svc := &incidents.Service{DB: db, Router: slackOnlyRouter{}}
	now := time.Now().UTC()
	rows := map[string]string{
		"old-delivered":    `UPDATE lookout.notification_outbox SET delivered_at = $2 WHERE incident_id = $1`,
		"recent-delivered": `UPDATE lookout.notification_outbox SET delivered_at = $2 WHERE incident_id = $1`,
		"old-failed":       `UPDATE lookout.notification_outbox SET failed_at = $2, last_error = 'gone' WHERE incident_id = $1`,
		"recent-failed":    `UPDATE lookout.notification_outbox SET failed_at = $2, last_error = 'gone' WHERE incident_id = $1`,
	}
	ages := map[string]time.Duration{
		"old-delivered":    deliveredRetention + time.Hour,
		"recent-delivered": deliveredRetention - time.Hour,
		"old-failed":       failedRetention + time.Hour,
		"recent-failed":    failedRetention - time.Hour,
	}
	ids := map[string]string{}
	for label, update := range rows {
		ids[label] = ingestPlatformIncident(t, svc, label)
		if _, err := db.Exec(update, ids[label], now.Add(-ages[label])); err != nil {
			t.Fatalf("settle %s: %v", label, err)
		}
	}
	ids["pending"] = ingestPlatformIncident(t, svc, "pending")
	if _, err := db.Exec(`UPDATE lookout.notification_outbox SET created_at = $2 WHERE incident_id = $1`, ids["pending"], now.Add(-failedRetention*2)); err != nil {
		t.Fatal(err)
	}

	metrics := testMetrics()
	retention := &Retention{DB: db, Metrics: metrics, Now: func() time.Time { return now }}
	deleted, err := retention.Sweep(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 2 {
		t.Fatalf("deleted = %d, want 2", deleted)
	}
	remaining := func(label string) int {
		t.Helper()
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM lookout.notification_outbox WHERE incident_id = $1`, ids[label]).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	for label, want := range map[string]int{"old-delivered": 0, "old-failed": 0, "recent-delivered": 1, "recent-failed": 1, "pending": 1} {
		if got := remaining(label); got != want {
			t.Fatalf("%s outbox rows = %d, want %d", label, got, want)
		}
	}
	if got := promtestutil.ToFloat64(metrics.OutboxDeleted.WithLabelValues("delivered")); got != 1 {
		t.Fatalf("delivered deletion metric = %v, want 1", got)
	}
	if got := promtestutil.ToFloat64(metrics.OutboxDeleted.WithLabelValues("failed")); got != 1 {
		t.Fatalf("failed deletion metric = %v, want 1", got)
	}
	if again, err := retention.Sweep(ctx); err != nil || again != 0 {
		t.Fatalf("second sweep deleted = %d, err = %v, want 0", again, err)
	}
}
