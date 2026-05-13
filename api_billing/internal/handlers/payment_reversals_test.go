package handlers

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/sirupsen/logrus"
)

func newReversalMock(t *testing.T) (sqlmock.Sqlmock, func()) {
	t.Helper()
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	db = mockDB
	logger = logrus.New()
	cleanup := func() {
		_ = mockDB.Close()
		db = nil
	}
	return mock, cleanup
}

func TestApplyProviderReversal_StripeRefundIdempotentReplay(t *testing.T) {
	mock, done := newReversalMock(t)
	defer done()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT p\.id, p\.invoice_id, i\.tenant_id, p\.currency`).
		WithArgs("pi_test").
		WillReturnRows(sqlmock.NewRows([]string{"id", "invoice_id", "tenant_id", "currency"}).
			AddRow("payment-1", "invoice-1", "tenant-1", "EUR"))
	// Replay: provider_reversal_id already present, ON CONFLICT returns no row.
	mock.ExpectQuery(`INSERT INTO purser\.payment_reversals`).
		WithArgs(
			"tenant-1", "payment-1", nil, "invoice-1",
			"stripe", "refund", "re_dup", "ch_test",
			int64(500), "EUR", "requested_by_customer",
		).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectCommit()

	applied, err := applyProviderReversal(context.Background(), providerReversalInput{
		provider:           "stripe",
		reversalType:       "refund",
		providerReversalID: "re_dup",
		providerChargeID:   "ch_test",
		providerPaymentID:  "pi_test",
		amountCents:        500,
		currency:           "EUR",
		reason:             "requested_by_customer",
	})
	if err != nil {
		t.Fatalf("applyProviderReversal: %v", err)
	}
	if applied {
		t.Fatal("expected applied=false on replay")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestApplyProviderReversal_StripeRefundReopensInvoiceWhenNetDropsBelowAmount(t *testing.T) {
	mock, done := newReversalMock(t)
	defer done()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT p\.id, p\.invoice_id, i\.tenant_id, p\.currency`).
		WithArgs("pi_full").
		WillReturnRows(sqlmock.NewRows([]string{"id", "invoice_id", "tenant_id", "currency"}).
			AddRow("payment-2", "invoice-2", "tenant-2", "EUR"))
	mock.ExpectQuery(`INSERT INTO purser\.payment_reversals`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("reversal-2"))
	mock.ExpectExec(`UPDATE purser\.billing_payments\s+SET reversed_amount_cents = reversed_amount_cents \+ \$1`).
		WithArgs(int64(1000), "payment-2").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE purser\.billing_invoices\s+SET reversed_paid_cents = reversed_paid_cents \+ \$1`).
		WithArgs(int64(1000), "invoice-2").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE purser\.billing_invoices i\s+SET status = 'pending'[\s\S]*reopened_at = NOW\(\)`).
		WithArgs("invoice-2", "EUR").
		WillReturnResult(sqlmock.NewResult(0, 1))
	// Operator clawback path: no marketplace lines on this invoice.
	mock.ExpectQuery(`SELECT \(amount \* 100\)::bigint FROM purser\.billing_invoices`).
		WithArgs("invoice-2").
		WillReturnRows(sqlmock.NewRows([]string{"cents"}).AddRow(int64(1000)))
	mock.ExpectQuery(`SELECT id, cluster_owner_tenant_id, cluster_id, currency, gross_cents, platform_fee_cents, payable_cents, period_start, period_end\s+FROM purser\.operator_credit_ledger`).
		WithArgs("invoice-2").
		WillReturnRows(sqlmock.NewRows([]string{"id", "owner", "cluster", "currency", "gross", "fee", "payable", "period_start", "period_end"}))
	mock.ExpectCommit()

	applied, err := applyProviderReversal(context.Background(), providerReversalInput{
		provider:           "stripe",
		reversalType:       "refund",
		providerReversalID: "re_full",
		providerChargeID:   "ch_full",
		providerPaymentID:  "pi_full",
		amountCents:        1000,
		currency:           "EUR",
		reason:             "requested_by_customer",
	})
	if err != nil {
		t.Fatalf("applyProviderReversal: %v", err)
	}
	if !applied {
		t.Fatal("expected applied=true on first delivery")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestApplyProviderReversal_TransitionsPendingDisputeToSucceeded(t *testing.T) {
	mock, done := newReversalMock(t)
	defer done()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT p\.id, p\.invoice_id, i\.tenant_id, p\.currency`).
		WithArgs("pi_dispute").
		WillReturnRows(sqlmock.NewRows([]string{"id", "invoice_id", "tenant_id", "currency"}).
			AddRow("payment-dispute", "invoice-dispute", "tenant-dispute", "EUR"))
	mock.ExpectQuery(`INSERT INTO purser\.payment_reversals[\s\S]*ON CONFLICT \(provider, provider_reversal_id\) DO UPDATE SET[\s\S]*status = 'succeeded'[\s\S]*WHERE purser\.payment_reversals\.status = 'pending'`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("reversal-dispute"))
	mock.ExpectExec(`UPDATE purser\.billing_payments\s+SET reversed_amount_cents = reversed_amount_cents \+ \$1`).
		WithArgs(int64(2500), "payment-dispute").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE purser\.billing_invoices\s+SET reversed_paid_cents = reversed_paid_cents \+ \$1`).
		WithArgs(int64(2500), "invoice-dispute").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE purser\.billing_invoices i\s+SET status = 'pending'[\s\S]*reopened_at = NOW\(\)`).
		WithArgs("invoice-dispute", "EUR").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT \(amount \* 100\)::bigint FROM purser\.billing_invoices`).
		WithArgs("invoice-dispute").
		WillReturnRows(sqlmock.NewRows([]string{"cents"}).AddRow(int64(10000)))
	mock.ExpectQuery(`SELECT id, cluster_owner_tenant_id, cluster_id, currency, gross_cents, platform_fee_cents, payable_cents, period_start, period_end\s+FROM purser\.operator_credit_ledger`).
		WithArgs("invoice-dispute").
		WillReturnRows(sqlmock.NewRows([]string{"id", "owner", "cluster", "currency", "gross", "fee", "payable", "period_start", "period_end"}))
	mock.ExpectCommit()

	applied, err := applyProviderReversal(context.Background(), providerReversalInput{
		provider:           "stripe",
		reversalType:       "dispute",
		providerReversalID: "du_pending",
		providerChargeID:   "ch_dispute",
		providerPaymentID:  "pi_dispute",
		amountCents:        2500,
		currency:           "EUR",
		reason:             "fraudulent",
	})
	if err != nil {
		t.Fatalf("applyProviderReversal: %v", err)
	}
	if !applied {
		t.Fatal("expected applied=true when pending dispute transitions")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestApplyOperatorCreditClawbackLinksReversalAuditRow(t *testing.T) {
	mock, done := newReversalMock(t)
	defer done()

	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT \(amount \* 100\)::bigint FROM purser\.billing_invoices`).
		WithArgs("invoice-op").
		WillReturnRows(sqlmock.NewRows([]string{"cents"}).AddRow(int64(10000)))
	mock.ExpectQuery(`SELECT id, cluster_owner_tenant_id, cluster_id, currency, gross_cents, platform_fee_cents, payable_cents, period_start, period_end\s+FROM purser\.operator_credit_ledger`).
		WithArgs("invoice-op").
		WillReturnRows(sqlmock.NewRows([]string{"id", "owner", "cluster", "currency", "gross", "fee", "payable", "period_start", "period_end"}).
			AddRow("accrual-1", "owner-1", "cluster-1", "EUR", int64(1000), int64(200), int64(800), now, now))
	mock.ExpectQuery(`WITH existing AS \(\s+SELECT operator_credit_ledger_id AS id\s+FROM purser\.operator_credit_clawback_reversals`).
		WithArgs("accrual-1", int64(1000), int64(200), int64(800), "reversal-1").
		WillReturnRows(sqlmock.NewRows([]string{"operator_credit_ledger_id"}).AddRow("clawback-1"))
	mock.ExpectExec(`UPDATE purser\.operator_credit_ledger\s+SET status = 'clawed_back'`).
		WithArgs("accrual-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE purser\.payment_reversals\s+SET operator_credit_ledger_id = \$1`).
		WithArgs("clawback-1", "reversal-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if err := applyOperatorCreditClawbackTx(context.Background(), tx, "invoice-op", "reversal-1", 10000); err != nil {
		t.Fatalf("applyOperatorCreditClawbackTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit tx: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestApplyProviderReversal_CurrencyMismatchRejected(t *testing.T) {
	mock, done := newReversalMock(t)
	defer done()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT p\.id, p\.invoice_id, i\.tenant_id, p\.currency`).
		WithArgs("pi_eur").
		WillReturnRows(sqlmock.NewRows([]string{"id", "invoice_id", "tenant_id", "currency"}).
			AddRow("payment-3", "invoice-3", "tenant-3", "EUR"))
	mock.ExpectRollback()

	applied, err := applyProviderReversal(context.Background(), providerReversalInput{
		provider:           "stripe",
		reversalType:       "refund",
		providerReversalID: "re_curr",
		providerPaymentID:  "pi_eur",
		amountCents:        100,
		currency:           "USD",
	})
	if err == nil {
		t.Fatal("expected error for currency mismatch")
	}
	if applied {
		t.Fatal("expected applied=false on error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestApplyProviderReversal_MissingLocalRefIsBlockedRetryable(t *testing.T) {
	mock, done := newReversalMock(t)
	defer done()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT p\.id, p\.invoice_id, i\.tenant_id, p\.currency`).
		WithArgs("pi_unknown").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT id, tenant_id, currency\s+FROM purser\.pending_topups`).
		WithArgs("pi_unknown").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	_, err := applyProviderReversal(context.Background(), providerReversalInput{
		provider:           "stripe",
		reversalType:       "refund",
		providerReversalID: "re_orphan",
		providerPaymentID:  "pi_unknown",
		amountCents:        100,
		currency:           "EUR",
	})
	if err == nil {
		t.Fatal("expected error for missing local reference")
	}
	if !errors.Is(err, errWebhookMissingLocalReference) {
		t.Fatalf("expected errWebhookMissingLocalReference, got: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestStripeOverageMinorUnitExponent(t *testing.T) {
	cases := []struct {
		currency string
		want     int
	}{
		{"EUR", 2},
		{"USD", 2},
		{"eur", 2}, // case insensitive
		{"JPY", 0},
		{"jpy", 0},
		{"BHD", 3},
		{"XAF", 0},
	}
	for _, tc := range cases {
		if got := stripeOverageMinorUnitExponent(tc.currency); got != tc.want {
			t.Fatalf("stripeOverageMinorUnitExponent(%q) = %d, want %d", tc.currency, got, tc.want)
		}
	}
}

func TestIsMollieMandateRevokedError(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"mandate is invalid", true},
		{"The mandate is revoked", true},
		{"410 Gone: mandate not found", true},
		{"insufficient_funds: card declined", false},
		{"network timeout", false},
		{"", false},
	}
	for _, tc := range cases {
		var err error
		if tc.msg != "" {
			err = errors.New(tc.msg)
		}
		got := isMollieMandateRevokedError(err)
		if got != tc.want {
			t.Fatalf("isMollieMandateRevokedError(%q) = %v, want %v", tc.msg, got, tc.want)
		}
	}
}

func TestMollieFailureCode(t *testing.T) {
	cases := []struct {
		msg, want string
	}{
		{"insufficient_funds: card declined", "insufficient_funds"},
		{"mandate revoked", "mandate revoked"},
		{"a-very-long-mollie-error-message-that-runs-on-without-any-punctuation-for-a-while", "a-very-long-mollie-error-message-that-runs-on-without-any-punctu"},
	}
	for _, tc := range cases {
		got := mollieFailureCode(errors.New(tc.msg))
		if got != tc.want {
			t.Fatalf("mollieFailureCode(%q) = %q, want %q", tc.msg, got, tc.want)
		}
	}
}

func TestMoneyConservationSyntheticProviderFlows(t *testing.T) {
	type flow struct {
		name                string
		currency            string
		providerPaidCents   int64
		providerRefundCents int64
		settledInvoiceCents int64
		prepaidDeltaCents   int64
		operatorDeltaCents  int64
	}
	cases := []flow{
		{
			name:                "subscription overage settles invoice",
			currency:            "EUR",
			providerPaidCents:   12500,
			settledInvoiceCents: 12500,
		},
		{
			name:                "partial payment then refund leaves net invoice value",
			currency:            "EUR",
			providerPaidCents:   15000,
			providerRefundCents: 2500,
			settledInvoiceCents: 12500,
		},
		{
			name:                "prepaid topup refund debits balance",
			currency:            "USD",
			providerPaidCents:   5000,
			providerRefundCents: 2000,
			prepaidDeltaCents:   3000,
		},
		{
			name:                "operator credit clawback offsets marketplace accrual",
			currency:            "EUR",
			providerPaidCents:   10000,
			providerRefundCents: 4000,
			settledInvoiceCents: 5000,
			operatorDeltaCents:  1000,
		},
		{
			name:     "failed mandate moves no money",
			currency: "EUR",
		},
	}

	for _, tc := range cases {
		left := tc.providerPaidCents - tc.providerRefundCents
		right := tc.settledInvoiceCents + tc.prepaidDeltaCents + tc.operatorDeltaCents
		if left != right {
			t.Fatalf("%s %s money conservation failed: provider net=%d local net=%d", tc.name, tc.currency, left, right)
		}
	}
}

// TestApplyProviderReversal_PrepaidTopupRefundFlagsNegativeBalance covers
// the operator-review default for prepaid refunds: if the refund would drop
// the prepaid balance below zero, operator_review_required is flipped TRUE
// so ops can decide whether to recollect or write off.
func TestApplyProviderReversal_PrepaidTopupRefundFlagsNegativeBalance(t *testing.T) {
	mock, done := newReversalMock(t)
	defer done()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT p\.id, p\.invoice_id, i\.tenant_id, p\.currency`).
		WithArgs("pi_topup").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT id, tenant_id, currency\s+FROM purser\.pending_topups`).
		WithArgs("pi_topup").
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "currency"}).AddRow("topup-1", "tenant-prepaid", "EUR"))
	mock.ExpectQuery(`INSERT INTO purser\.payment_reversals`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("00000000-0000-0000-0000-000000000001"))
	mock.ExpectExec(`UPDATE purser\.pending_topups\s+SET refunded_amount_cents`).
		WithArgs(int64(2000), "topup-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	// Current balance below refund amount → will go negative.
	mock.ExpectQuery(`SELECT balance_cents FROM purser\.prepaid_balances`).
		WithArgs("tenant-prepaid", "EUR").
		WillReturnRows(sqlmock.NewRows([]string{"balance"}).AddRow(int64(500)))
	mock.ExpectExec(`INSERT INTO purser\.balance_transactions`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE purser\.prepaid_balances\s+SET balance_cents = balance_cents - \$1`).
		WithArgs(int64(2000), "tenant-prepaid", "EUR").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE purser\.payment_reversals\s+SET operator_review_required = TRUE`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	applied, err := applyProviderReversal(context.Background(), providerReversalInput{
		provider:           "stripe",
		reversalType:       "refund",
		providerReversalID: "re_topup",
		providerChargeID:   "ch_topup",
		providerPaymentID:  "pi_topup",
		amountCents:        2000,
		currency:           "EUR",
		reason:             "duplicate",
	})
	if err != nil {
		t.Fatalf("applyProviderReversal: %v", err)
	}
	if !applied {
		t.Fatal("expected applied=true")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
