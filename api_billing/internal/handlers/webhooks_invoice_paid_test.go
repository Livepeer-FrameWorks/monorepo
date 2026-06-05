package handlers

import (
	"encoding/json"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
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
	mock.ExpectQuery(`SELECT tenant_id FROM purser\.tenant_subscriptions WHERE stripe_customer_id`).
		WithArgs("cus_known").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}).AddRow(tenant))
	mock.ExpectExec(`UPDATE purser\.tenant_subscriptions\s+SET dunning_attempts = 0`).
		WithArgs(tenant).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO purser\.billing_event_outbox`).
		WithArgs("invoice_paid", tenant, "", "invoice", "in_known", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	if err := s.handleStripeInvoicePaid(invoicePaidPayload(t, "in_known", "cus_known", "")); err != nil {
		t.Fatalf("handleStripeInvoicePaid: %v", err)
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
	mock.ExpectQuery(`SELECT tenant_id FROM purser\.tenant_subscriptions WHERE stripe_customer_id`).
		WithArgs("cus_unknown").
		WillReturnError(sqlmock.ErrCancelled) // any error → fall back to metadata
	mock.ExpectExec(`UPDATE purser\.tenant_subscriptions\s+SET dunning_attempts = 0`).
		WithArgs(tenant).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`INSERT INTO purser\.billing_event_outbox`).
		WithArgs("invoice_paid", tenant, "", "invoice", "in_meta", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

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

	mock.ExpectQuery(`SELECT tenant_id FROM purser\.tenant_subscriptions WHERE stripe_customer_id`).
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
