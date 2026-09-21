package handlers

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	"github.com/sirupsen/logrus"
)

// invoicePaidPayload builds an invoice.paid webhook payload for a plain
// (non-cluster) tenant invoice — no subscription id, so the cluster-credit and
// cluster-activation branches are no-ops and the handler reduces to its core
// contract: resolve tenant, reset dunning, enqueue the billing event.
func invoicePaidPayload(t *testing.T, invoiceID, customerID, metaTenant string) StripeWebhookPayload {
	t.Helper()
	obj := map[string]any{
		"id":          invoiceID,
		"customer":    customerID,
		"amount_paid": 1999,
		"amount_due":  1999,
		"currency":    "eur",
		"status":      "paid",
		"metadata":    map[string]string{"tenant_id": metaTenant},
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshal invoice object: %v", err)
	}
	var p StripeWebhookPayload
	p.ID = "evt_" + invoiceID
	p.Type = "invoice.paid"
	p.Data.Object = json.RawMessage(raw)
	return p
}

// stripeChargeFailure matches the billing.payment_failed payload of a failed
// Stripe subscription charge: no Purser identifiers, the Stripe invoice as the
// provider reference, and Stripe's amount in the invoice currency.
type stripeChargeFailure struct{ invoiceID string }

func (m stripeChargeFailure) Match(v driver.Value) bool {
	data, ok := v.([]byte)
	if !ok {
		return false
	}
	_, msg, err := events.Decode("billing.payment_failed", data)
	if err != nil {
		return false
	}
	failed := msg.(*publicv1.PaymentFailed)
	return failed.GetPaymentId() == "" && failed.GetInvoiceId() == "" &&
		failed.GetProvider() == "stripe" && failed.GetProviderReferenceId() == m.invoiceID &&
		failed.GetAmount().GetAmountMinor() == 1999 && failed.GetAmount().GetCurrency() == "EUR" &&
		failed.GetReason() == publicv1.PaymentFailureReason_PAYMENT_FAILURE_REASON_UNSPECIFIED
}

// TestHandleStripeInvoiceFailed_IncrementsDunningWithEventAtomically pins the
// shared transaction: the webhook row settles, the dunning increment commits
// with billing.payment_failed and its invoice_payment_failed legacy row under
// one event ID, a failed insert rolls all of it back so the webhook retries,
// and a delivery whose event is already settled records nothing.
func TestHandleStripeInvoiceFailed_IncrementsDunningWithEventAtomically(t *testing.T) {
	for _, tc := range []struct {
		name      string
		settled   int64
		outboxErr error
	}{
		{name: "commit", settled: 1},
		{name: "outbox failure rolls back", settled: 1, outboxErr: errors.New("outbox unavailable")},
		{name: "already settled records nothing", settled: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock, done := newWebhookService(t)
			defer done()

			const tenant = webhookTenantID
			mock.ExpectQuery(`SELECT tenant_id::text AS tenant_id`).
				WithArgs("cus_known").
				WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}).AddRow(tenant))
			mock.ExpectBegin()
			mock.ExpectExec(`UPDATE purser\.webhook_events\s+SET status = 'processed'`).
				WithArgs("stripe", "evt_in_failed").
				WillReturnResult(sqlmock.NewResult(0, tc.settled))
			if tc.settled == 0 {
				mock.ExpectCommit()
			} else {
				mock.ExpectExec(`UPDATE purser\.tenant_subscriptions\s+SET dunning_attempts = dunning_attempts \+ 1`).
					WithArgs(tenant).
					WillReturnResult(sqlmock.NewResult(0, 1))
				id := &domainEventID{}
				mock.ExpectExec(`INSERT INTO purser\.domain_event_outbox`).
					WithArgs(id, "billing.payment_failed", "purser", "payments", "in_failed", sqlmock.AnyArg(),
						"tenant", tenant, sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
						stripeChargeFailure{invoiceID: "in_failed"}).
					WillReturnResult(sqlmock.NewResult(0, 1))
				outbox := mock.ExpectExec(`INSERT INTO purser\.billing_event_outbox`).
					WithArgs(sameEventID{id}, "invoice_payment_failed", tenant, "", "invoice", "in_failed", sqlmock.AnyArg())
				if tc.outboxErr != nil {
					outbox.WillReturnError(tc.outboxErr)
					mock.ExpectRollback()
				} else {
					outbox.WillReturnResult(sqlmock.NewResult(1, 1))
					mock.ExpectCommit()
				}
			}

			// The payload builder's invoice.paid type is irrelevant here; the
			// handler reads only id, customer, amounts, and metadata.
			err := s.handleStripeInvoiceFailed(invoicePaidPayload(t, "in_failed", "cus_known", ""))
			if tc.outboxErr != nil {
				if err == nil || !strings.Contains(err.Error(), "outbox unavailable") {
					t.Fatalf("expected outbox insert error, got %v", err)
				}
			} else if err != nil {
				t.Fatalf("handleStripeInvoiceFailed: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet expectations: %v", err)
			}
		})
	}
}

func newWebhookService(t *testing.T) (*Service, sqlmock.Sqlmock, func()) {
	t.Helper()
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	return &Service{db: mockDB, logger: logrus.New()}, mock, func() { _ = mockDB.Close() }
}

// TestHandleStripeInvoicePaid_ResetsDunningForKnownCustomer pins the happy
// path: a customer we recognize gets dunning_attempts reset to 0 and a paid
// billing event enqueued.
func TestHandleStripeInvoicePaid_ResetsDunningForKnownCustomer(t *testing.T) {
	s, mock, done := newWebhookService(t)
	defer done()

	const tenant = "tenant-1"
	mock.ExpectQuery(`SELECT tenant_id::text AS tenant_id`).
		WithArgs("cus_known").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}).AddRow(tenant))
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE purser\.tenant_subscriptions\s+SET dunning_attempts = 0`).
		WithArgs(tenant).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO purser\.billing_event_outbox`).
		WithArgs(sqlmock.AnyArg(), "invoice_paid", tenant, "", "invoice", "in_known", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	if err := s.handleStripeInvoicePaid(invoicePaidPayload(t, "in_known", "cus_known", "")); err != nil {
		t.Fatalf("handleStripeInvoicePaid: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestHandleStripeInvoicePaid_RollsBackDunningResetWhenOutboxInsertFails pins
// atomicity: the dunning reset and the invoice_paid outbox row share one
// transaction, so a failed insert undoes the reset and the webhook retries.
func TestHandleStripeInvoicePaid_RollsBackDunningResetWhenOutboxInsertFails(t *testing.T) {
	s, mock, done := newWebhookService(t)
	defer done()

	const tenant = "tenant-1"
	mock.ExpectQuery(`SELECT tenant_id::text AS tenant_id`).
		WithArgs("cus_known").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}).AddRow(tenant))
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE purser\.tenant_subscriptions\s+SET dunning_attempts = 0`).
		WithArgs(tenant).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO purser\.billing_event_outbox`).
		WithArgs(sqlmock.AnyArg(), "invoice_paid", tenant, "", "invoice", "in_known", sqlmock.AnyArg()).
		WillReturnError(errors.New("outbox unavailable"))
	mock.ExpectRollback()

	err := s.handleStripeInvoicePaid(invoicePaidPayload(t, "in_known", "cus_known", ""))
	if err == nil || !strings.Contains(err.Error(), "outbox unavailable") {
		t.Fatalf("expected outbox insert error, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestHandleStripeInvoicePaid_FallsBackToMetadataTenant pins the resolution
// fallback: when the customer id is unknown, the handler trusts the
// platform-set metadata.tenant_id rather than dropping the event.
func TestHandleStripeInvoicePaid_FallsBackToMetadataTenant(t *testing.T) {
	s, mock, done := newWebhookService(t)
	defer done()

	const tenant = "tenant-meta"
	mock.ExpectQuery(`SELECT tenant_id::text AS tenant_id`).
		WithArgs("cus_unknown").
		WillReturnError(sqlmock.ErrCancelled) // any error → fall back to metadata
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE purser\.tenant_subscriptions\s+SET dunning_attempts = 0`).
		WithArgs(tenant).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`INSERT INTO purser\.billing_event_outbox`).
		WithArgs(sqlmock.AnyArg(), "invoice_paid", tenant, "", "invoice", "in_meta", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	if err := s.handleStripeInvoicePaid(invoicePaidPayload(t, "in_meta", "cus_unknown", tenant)); err != nil {
		t.Fatalf("handleStripeInvoicePaid: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestHandleStripeInvoicePaid_SkipsUnknownCustomerWithoutMetadata pins the
// drop path: no customer match and no metadata tenant → the handler returns
// nil without touching the DB further (no dunning write, no event).
func TestHandleStripeInvoicePaid_SkipsUnknownCustomerWithoutMetadata(t *testing.T) {
	s, mock, done := newWebhookService(t)
	defer done()

	mock.ExpectQuery(`SELECT tenant_id::text AS tenant_id`).
		WithArgs("cus_ghost").
		WillReturnError(sqlmock.ErrCancelled)
	// No further expectations: any extra query fails the test.

	if err := s.handleStripeInvoicePaid(invoicePaidPayload(t, "in_ghost", "cus_ghost", "")); err != nil {
		t.Fatalf("handleStripeInvoicePaid: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
