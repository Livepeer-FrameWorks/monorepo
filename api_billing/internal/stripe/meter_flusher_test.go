package stripe

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	stripeapi "github.com/stripe/stripe-go/v85"
)

// pendingRowColumns mirrors the SELECT in Flush so each test seeds the same
// shape the scanner expects.
var pendingRowColumns = []string{
	"id", "tenant_id", "cluster_id", "meter", "stripe_meter_event_name",
	"quantity", "period_start", "attempt_count",
}

// TestFlush_NilDB guards the explicit nil-DB check: a misconfigured flusher
// must fail loudly rather than nil-panic mid-tick.
func TestFlush_NilDB(t *testing.T) {
	f := &MeterFlusher{}
	if _, _, err := f.Flush(context.Background()); err == nil {
		t.Fatal("Flush with nil DB must return an error")
	}
}

// TestFlush_ReadFailure: a failure reading the outbox is a Flush-level error
// (not a per-row deferral) so the caller knows the whole batch is unprocessed.
func TestFlush_ReadFailure(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`SELECT id, tenant_id, cluster_id, meter`).
		WithArgs(6, 100).
		WillReturnError(errors.New("connection reset"))

	f := &MeterFlusher{DB: db, MaxAttempts: 6, BatchSize: 100}
	sent, deferred, err := f.Flush(context.Background())
	if err == nil {
		t.Fatal("read failure must return an error")
	}
	if sent != 0 || deferred != 0 {
		t.Errorf("sent=%d deferred=%d, want 0/0 on read failure", sent, deferred)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestFlush_SuccessMarksSent: push succeeds, row is marked sent, counted once.
func TestFlush_SuccessMarksSent(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	periodStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`SELECT id, tenant_id, cluster_id, meter`).
		WithArgs(6, 100).
		WillReturnRows(sqlmock.NewRows(pendingRowColumns).
			AddRow("row-1", "tenant-1", "cluster-1", "delivered_minutes", "meter.delivered_minutes", "60000", periodStart, 0))
	mock.ExpectExec(`UPDATE purser\.stripe_meter_events_outbox\s+SET sent_at`).
		WithArgs("row-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	f := &MeterFlusher{
		DB:             db,
		MaxAttempts:    6,
		BatchSize:      100,
		TenantStripeID: func(context.Context, string) (string, error) { return "cus_1", nil },
		SendMeterEvent: func(context.Context, *stripeapi.BillingMeterEventParams) error { return nil },
	}
	sent, deferred, err := f.Flush(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sent != 1 || deferred != 0 {
		t.Errorf("sent=%d deferred=%d, want 1/0", sent, deferred)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestFlush_PushFailureDefers: a Stripe delivery error increments attempt_count
// and records last_error; the row is NOT marked sent so the next tick retries.
func TestFlush_PushFailureDefers(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	periodStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`SELECT id, tenant_id, cluster_id, meter`).
		WithArgs(6, 100).
		WillReturnRows(sqlmock.NewRows(pendingRowColumns).
			AddRow("row-1", "tenant-1", "cluster-1", "delivered_minutes", "meter.delivered_minutes", "60000", periodStart, 2))
	mock.ExpectExec(`UPDATE purser\.stripe_meter_events_outbox\s+SET attempt_count = attempt_count \+ 1`).
		WithArgs("row-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	f := &MeterFlusher{
		DB:             db,
		MaxAttempts:    6,
		BatchSize:      100,
		TenantStripeID: func(context.Context, string) (string, error) { return "cus_1", nil },
		SendMeterEvent: func(context.Context, *stripeapi.BillingMeterEventParams) error {
			return errors.New("stripe 503")
		},
	}
	sent, deferred, err := f.Flush(context.Background())
	if err != nil {
		t.Fatalf("a per-row push failure must not fail the whole Flush: %v", err)
	}
	if sent != 0 || deferred != 1 {
		t.Errorf("sent=%d deferred=%d, want 0/1", sent, deferred)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestFlush_MarkSentFailureDefers: Stripe accepted the event but we couldn't
// persist sent_at. This is safe (the row id is the idempotency key, so a retry
// collapses Stripe-side) but must be loud — counted as deferred and a failure
// recorded so ops can see it.
func TestFlush_MarkSentFailureDefers(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	periodStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`SELECT id, tenant_id, cluster_id, meter`).
		WithArgs(6, 100).
		WillReturnRows(sqlmock.NewRows(pendingRowColumns).
			AddRow("row-1", "tenant-1", "cluster-1", "delivered_minutes", "meter.delivered_minutes", "60000", periodStart, 0))
	mock.ExpectExec(`UPDATE purser\.stripe_meter_events_outbox\s+SET sent_at`).
		WithArgs("row-1").
		WillReturnError(errors.New("write conflict"))
	mock.ExpectExec(`UPDATE purser\.stripe_meter_events_outbox\s+SET attempt_count = attempt_count \+ 1`).
		WithArgs("row-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	f := &MeterFlusher{
		DB:             db,
		MaxAttempts:    6,
		BatchSize:      100,
		TenantStripeID: func(context.Context, string) (string, error) { return "cus_1", nil },
		SendMeterEvent: func(context.Context, *stripeapi.BillingMeterEventParams) error { return nil },
	}
	sent, deferred, err := f.Flush(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sent != 0 || deferred != 1 {
		t.Errorf("sent=%d deferred=%d, want 0/1 (accepted-but-unmarked)", sent, deferred)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestFlush_TenantResolveFailureDefers: if we can't resolve the tenant's Stripe
// customer, the event can't be delivered — defer it, and never call Stripe.
func TestFlush_TenantResolveFailureDefers(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	periodStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`SELECT id, tenant_id, cluster_id, meter`).
		WithArgs(6, 100).
		WillReturnRows(sqlmock.NewRows(pendingRowColumns).
			AddRow("row-1", "tenant-1", "cluster-1", "delivered_minutes", "meter.delivered_minutes", "60000", periodStart, 0))
	mock.ExpectExec(`UPDATE purser\.stripe_meter_events_outbox\s+SET attempt_count = attempt_count \+ 1`).
		WithArgs("row-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	sendCalled := false
	f := &MeterFlusher{
		DB:          db,
		MaxAttempts: 6,
		BatchSize:   100,
		TenantStripeID: func(context.Context, string) (string, error) {
			return "", errors.New("no active subscription")
		},
		SendMeterEvent: func(context.Context, *stripeapi.BillingMeterEventParams) error {
			sendCalled = true
			return nil
		},
	}
	_, deferred, err := f.Flush(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deferred != 1 {
		t.Errorf("deferred=%d, want 1", deferred)
	}
	if sendCalled {
		t.Error("must not call Stripe when the customer cannot be resolved")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestNewMeterFlusher_DefaultTenantResolver exercises the constructor's
// default TenantStripeID closure: it resolves the active subscription's Stripe
// customer id, and treats "no active subscription" as a hard error (a meter
// event for a tenant with no live sub has nowhere to go).
func TestNewMeterFlusher_DefaultTenantResolver(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	f := NewMeterFlusher(db)
	if f.MaxAttempts != 6 || f.BatchSize != 100 {
		t.Errorf("unexpected defaults: MaxAttempts=%d BatchSize=%d", f.MaxAttempts, f.BatchSize)
	}

	t.Run("active subscription resolves", func(t *testing.T) {
		mock.ExpectQuery(`SELECT stripe_customer_id\s+FROM purser\.tenant_subscriptions`).
			WithArgs("tenant-1").
			WillReturnRows(sqlmock.NewRows([]string{"stripe_customer_id"}).AddRow("cus_live"))
		got, err := f.TenantStripeID(context.Background(), "tenant-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "cus_live" {
			t.Errorf("got %q, want cus_live", got)
		}
	})

	t.Run("no active subscription is an error", func(t *testing.T) {
		mock.ExpectQuery(`SELECT stripe_customer_id\s+FROM purser\.tenant_subscriptions`).
			WithArgs("tenant-2").
			WillReturnError(sql.ErrNoRows)
		if _, err := f.TenantStripeID(context.Background(), "tenant-2"); err == nil {
			t.Fatal("no active subscription must return an error")
		}
	})

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestPushOne_ParamConstruction pins the at-least-once contract: the meter
// event identifier is the outbox row id (so Stripe collapses retries), and the
// cluster_id payload key appears only when a cluster is present.
func TestPushOne_ParamConstruction(t *testing.T) {
	periodStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name           string
		clusterID      string
		wantClusterKey bool
	}{
		{"with cluster", "cluster-9", true},
		{"without cluster", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got *stripeapi.BillingMeterEventParams
			f := &MeterFlusher{
				TenantStripeID: func(context.Context, string) (string, error) { return "cus_42", nil },
				SendMeterEvent: func(_ context.Context, p *stripeapi.BillingMeterEventParams) error {
					got = p
					return nil
				},
			}
			err := f.pushOne(context.Background(), "row-7", "tenant-1", tt.clusterID,
				"delivered_minutes", "meter.delivered_minutes", "60000", periodStart)
			if err != nil {
				t.Fatalf("pushOne: %v", err)
			}
			if got == nil {
				t.Fatal("SendMeterEvent was not called")
			}
			if got.Identifier == nil || *got.Identifier != "row-7" {
				t.Errorf("Identifier = %v, want row-7 (idempotency key)", got.Identifier)
			}
			if got.EventName == nil || *got.EventName != "meter.delivered_minutes" {
				t.Errorf("EventName = %v, want meter.delivered_minutes", got.EventName)
			}
			if got.Timestamp == nil || *got.Timestamp != periodStart.Unix() {
				t.Errorf("Timestamp = %v, want %d", got.Timestamp, periodStart.Unix())
			}
			if got.Payload["stripe_customer_id"] != "cus_42" {
				t.Errorf("payload stripe_customer_id = %q, want cus_42", got.Payload["stripe_customer_id"])
			}
			if got.Payload["meter"] != "delivered_minutes" || got.Payload["value"] != "60000" {
				t.Errorf("payload meter/value wrong: %v", got.Payload)
			}
			_, hasCluster := got.Payload["cluster_id"]
			if hasCluster != tt.wantClusterKey {
				t.Errorf("cluster_id present=%v, want %v", hasCluster, tt.wantClusterKey)
			}
		})
	}
}
