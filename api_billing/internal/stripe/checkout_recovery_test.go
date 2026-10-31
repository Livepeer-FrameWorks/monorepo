package stripe

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	api "github.com/stripe/stripe-go/v85"
)

type checkoutRecoveryBackend struct {
	stripeMoneyBackend
	paths           []string
	paid, refunded  bool
	processing      bool
	sessionPayment  bool
	includeRenewals bool
	secondCharge    bool
	openInvoice     bool
	staleInvoice    bool
	paymentInvoices []string
	cancelParams    *api.SubscriptionCancelParams
	sessionInvoice  string
	chargeOverride  *api.Charge
	allocation      int64
	refundParams    []*api.RefundParams
	existingRefunds []*api.Refund
	refundStatus    api.RefundStatus
	disputes        []*api.Dispute
}

func (b *checkoutRecoveryBackend) Call(method, path, key string, p api.ParamsContainer, result api.LastResponseSetter) error {
	b.paths = append(b.paths, method+" "+path)
	if method == "DELETE" {
		b.cancelParams, _ = p.(*api.SubscriptionCancelParams)
	}
	switch v := result.(type) {
	case *api.CheckoutSession:
		*v = api.CheckoutSession{ID: "cs_reserved", Status: api.CheckoutSessionStatusOpen}
		if b.sessionInvoice != "" {
			v.Invoice = &api.Invoice{ID: b.sessionInvoice}
		}
		if b.paid {
			v.Status = api.CheckoutSessionStatusComplete
			v.Subscription = &api.Subscription{ID: "sub_reserved"}
			if b.sessionPayment {
				v.PaymentIntent = &api.PaymentIntent{ID: "pi_reserved"}
			}
		}
	case *api.Subscription:
		*v = api.Subscription{ID: "sub_reserved", Status: api.SubscriptionStatusActive}
	case *api.Charge:
		*v = api.Charge{ID: "ch_reserved", Currency: api.CurrencyEUR, Amount: 2000}
		if b.chargeOverride != nil {
			*v = *b.chargeOverride
		}

		if b.secondCharge && path == "/v1/charges/ch_other" {
			v.ID = "ch_other"
		}
	case *api.Invoice:
		*v = api.Invoice{ID: "in_reserved", Status: api.InvoiceStatusVoid}
	case *api.PaymentIntent:
		*v = api.PaymentIntent{ID: "pi_reserved", LatestCharge: &api.Charge{ID: "ch_reserved", Currency: api.CurrencyEUR, Amount: 2000}}
		if b.chargeOverride != nil {
			v.LatestCharge = b.chargeOverride
		}
		if b.processing {
			v.Status = api.PaymentIntentStatusProcessing
		}
		if b.openInvoice {
			v.Status = api.PaymentIntentStatusRequiresPaymentMethod
			v.LatestCharge = nil
		}
		if b.refunded {
			v.LatestCharge.AmountRefunded = 2000
		}
	case *api.Refund:
		params := p.(*api.RefundParams)
		b.refundParams = append(b.refundParams, params)
		v.ID = fmt.Sprintf("refund-%d", len(b.refundParams))
		v.Status = b.refundStatus
		if v.Status == "" {
			v.Status = api.RefundStatusPending
		}

		v.Amount = api.Int64Value(params.Amount)
		v.Currency = api.CurrencyEUR
		stored := *v
		b.existingRefunds = append(b.existingRefunds, &stored)
		if v.Status != api.RefundStatusFailed && v.Status != api.RefundStatusCanceled {
			b.refunded = true
		}
	}
	return nil
}

func (b *checkoutRecoveryBackend) CallRaw(method, path, key string, body []byte, p *api.Params, result api.LastResponseSetter) error {
	b.paths = append(b.paths, method+" "+path)
	switch v := result.(type) {
	case *api.RefundList:
		v.Data = b.existingRefunds
		query, _ := url.ParseQuery(string(body))
		if b.secondCharge && query.Get("charge") == "ch_other" {
			v.Data = nil
		}
	case *api.DisputeList:
		v.Data = b.disputes
	case *api.CheckoutSessionList:
		own := &api.CheckoutSession{ID: "cs_reserved", Status: api.CheckoutSessionStatusOpen, Metadata: map[string]string{"checkout_reservation_id": "reservation"}}
		if b.paid {
			own.Status = api.CheckoutSessionStatusComplete
			own.Subscription = &api.Subscription{ID: "sub_reserved"}
		}
		v.Data = []*api.CheckoutSession{{ID: "cs_other", Status: api.CheckoutSessionStatusOpen, Metadata: map[string]string{"checkout_reservation_id": "other"}}, own}
	case *api.InvoiceList:
		v.Data = []*api.Invoice{{ID: "in_reserved", AmountPaid: 2000, BillingReason: api.InvoiceBillingReasonSubscriptionCreate}}
		if b.processing || b.openInvoice || b.staleInvoice {
			v.Data[0].AmountPaid = 0
			v.Data[0].Status = api.InvoiceStatusOpen
		}
		if b.includeRenewals {
			v.Data = append(v.Data, &api.Invoice{ID: "in_renewal", AmountPaid: 2000, BillingReason: api.InvoiceBillingReasonSubscriptionCycle})
		}
	case *api.InvoicePaymentList:
		query, err := url.ParseQuery(string(body))
		if err != nil {
			return err
		}
		b.paymentInvoices = append(b.paymentInvoices, query.Get("invoice"))
		if b.openInvoice && query.Get("status") == "paid" {
			v.Data = nil
			return nil
		}
		v.Data = []*api.InvoicePayment{{ID: "ip_reserved", AmountPaid: b.allocation, Payment: &api.InvoicePaymentPayment{PaymentIntent: &api.PaymentIntent{ID: "pi_reserved"}}}}
		if b.secondCharge {
			v.Data = append(v.Data, &api.InvoicePayment{ID: "ip_other", AmountPaid: 2000, Payment: &api.InvoicePaymentPayment{Charge: &api.Charge{ID: "ch_other"}}})
		}
	}
	return nil
}

func TestCheckoutCompensationWaitsForInitialDebitSettlement(t *testing.T) {
	for _, sessionPayment := range []bool{false, true} {
		t.Run(fmt.Sprint("session payment reference=", sessionPayment), func(t *testing.T) {
			b := &checkoutRecoveryBackend{paid: true, processing: true, sessionPayment: sessionPayment, includeRenewals: true}
			installBackend(t, b)
			c := NewClient(Config{SecretKey: "sk_test", Logger: logging.NewLogger()})
			if _, err := c.RecoverReservedCheckout(t.Context(), "cus_reserved", "reservation", "cs_reserved"); err == nil {
				t.Fatal("processing first debit was marked fully compensated before it could be refunded")
			}
			if b.refunded {
				t.Fatal("processing debit was refunded before collection")
			}
			b.processing = false
			if _, err := c.RecoverReservedCheckout(t.Context(), "cus_reserved", "reservation", "cs_reserved"); err != nil {
				t.Fatal(err)
			}
			refunds := 0
			for _, path := range b.paths {
				if path == "POST /v1/refunds" {
					refunds++
				}
			}
			if refunds != 1 {
				t.Fatalf("settled initial debit must be refunded once: calls=%v", b.paths)
			}
		})
	}
}

func TestCheckoutCompensationReadsPaymentsAfterInvoiceSnapshot(t *testing.T) {
	b := &checkoutRecoveryBackend{paid: true, staleInvoice: true}
	installBackend(t, b)
	c := NewClient(Config{SecretKey: "sk_test", Logger: logging.NewLogger()})
	if _, err := c.RecoverReservedCheckout(t.Context(), "cus_reserved", "reservation", "cs_reserved"); err != nil {
		t.Fatal(err)
	}
	if !b.refunded {
		t.Fatal("a debit that settled after invoice listing was permanently skipped")
	}
}

func TestExpiredStripeReservationRecoversLostSessionBinding(t *testing.T) {
	b := &checkoutRecoveryBackend{}
	installBackend(t, b)
	c := NewClient(Config{SecretKey: "sk_test", Logger: logging.NewLogger()})
	if err := c.CancelReservedCheckout(context.Background(), "cus_reserved", "reservation", ""); err != nil {
		t.Fatal(err)
	}
	expired := false
	for _, path := range b.paths {
		if path == "POST /v1/checkout/sessions/cs_reserved/expire" {
			expired = true
		}
		if path == "POST /v1/checkout/sessions/cs_other/expire" {
			t.Fatal("cancelled another reservation")
		}
	}
	if !expired {
		t.Fatalf("lost binding was not expired: %v", b.paths)
	}
}

func TestExpiredStripeReservationRefundsUnactivatedSubscription(t *testing.T) {
	b := &checkoutRecoveryBackend{paid: true}
	installBackend(t, b)
	c := NewClient(Config{SecretKey: "sk_test", Logger: logging.NewLogger()})
	for range 2 {
		if err := c.CancelReservedCheckout(context.Background(), "cus_reserved", "reservation", "cs_reserved"); err != nil {
			t.Fatal(err)
		}
	}
	refunds, cancels := 0, 0
	for _, path := range b.paths {
		if path == "POST /v1/refunds" {
			refunds++
		}
		if path == "DELETE /v1/subscriptions/sub_reserved" {
			cancels++
		}
	}
	if refunds != 1 || cancels == 0 {
		t.Fatalf("compensation not idempotent: refunds=%d cancels=%d paths=%v", refunds, cancels, b.paths)
	}
}

func TestStripeCheckoutUsesReservationDeadline(t *testing.T) {
	b := &stripeMoneyBackend{}
	installBackend(t, b)
	c := NewClient(Config{SecretKey: "sk_test", Logger: logging.NewLogger()})
	deadline := time.Now().Add(time.Hour).Truncate(time.Second)
	if _, err := c.CreateCheckoutSession(context.Background(), CheckoutSessionParams{TenantID: "tenant", ReservationID: "reservation", ExpiresAt: deadline, IdempotencyKey: "key"}); err != nil {
		t.Fatal(err)
	}
	if b.checkoutParams.ExpiresAt == nil || *b.checkoutParams.ExpiresAt != deadline.Unix() || b.checkoutParams.Metadata["checkout_reservation_id"] != "reservation" {
		t.Fatalf("provider deadline/identity missing: %+v", b.checkoutParams)
	}
	if _, err := c.CreateSetupCheckoutSession(context.Background(), SetupCheckoutParams{CustomerID: "customer", Currency: "USD", TenantID: "tenant", ReservationID: "reservation", ExpiresAt: deadline, IdempotencyKey: "key"}); err != nil {
		t.Fatal(err)
	}
	if b.checkoutParams.ExpiresAt == nil || *b.checkoutParams.ExpiresAt != deadline.Unix() || b.checkoutParams.Metadata["checkout_reservation_id"] != "reservation" {
		t.Fatal("setup checkout omitted reservation deadline")
	}
}

func TestCheckoutRetryResumesExistingProviderSession(t *testing.T) {
	b := &checkoutRecoveryBackend{}
	old := api.GetBackend(api.APIBackend)
	api.SetBackend(api.APIBackend, b)
	t.Cleanup(func() { api.SetBackend(api.APIBackend, old) })
	c := NewClient(Config{SecretKey: "sk_test", Logger: logging.NewLogger()})
	for _, setup := range []bool{false, true} {
		var err error
		if setup {
			_, err = c.CreateSetupCheckoutSession(t.Context(), SetupCheckoutParams{CustomerID: "customer", ReservationID: "reservation", ExistingSessionID: "cs_reserved", ResumeOnly: true})
		} else {
			_, err = c.CreateCheckoutSession(t.Context(), CheckoutSessionParams{CustomerID: "customer", ReservationID: "reservation", ExistingSessionID: "cs_reserved", ResumeOnly: true})
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(b.paths) != 2 {
		t.Fatalf("unexpected provider calls: %v", b.paths)
	}
	for _, path := range b.paths {
		if path != "GET /v1/checkout/sessions/cs_reserved" {
			t.Fatalf("checkout retry created another subscription: %s", path)
		}
	}
}

func TestUnboundCheckoutRetryReplaysWithinProviderWindow(t *testing.T) {
	for _, setup := range []bool{false, true} {
		t.Run(fmt.Sprint("setup=", setup), func(t *testing.T) {
			b := &stripeMoneyBackend{checkoutSession: &api.CheckoutSession{ID: "same-session", Status: "open"}}
			installBackend(t, b)
			c := NewClient(Config{SecretKey: "sk_test", Logger: logging.NewLogger()})
			var err error
			if setup {
				_, err = c.CreateSetupCheckoutSession(t.Context(), SetupCheckoutParams{CustomerID: "customer", Currency: "USD", ReservationID: "reservation", IdempotencyKey: "original-key", ResumeOnly: true, ReplayBefore: time.Now().Add(time.Minute)})
			} else {
				_, err = c.CreateCheckoutSession(t.Context(), CheckoutSessionParams{CustomerID: "customer", Currency: "EUR", PriceID: "price", ReservationID: "reservation", IdempotencyKey: "original-key", ResumeOnly: true, ReplayBefore: time.Now().Add(time.Minute)})
			}
			if err != nil || b.checkoutParams == nil || *b.checkoutParams.IdempotencyKey != "original-key" {
				t.Fatalf("unbound attempt was not replayed with original identity: params=%v err=%v", b.checkoutParams, err)
			}
		})
	}
}

func TestRecoveryRefundsCheckoutInvoiceOnly(t *testing.T) {
	b := &checkoutRecoveryBackend{paid: true, includeRenewals: true}
	installBackend(t, b)
	c := NewClient(Config{SecretKey: "sk_test", Logger: logging.NewLogger()})
	if _, err := c.RecoverReservedCheckout(t.Context(), "cus_reserved", "reservation", "cs_reserved"); err != nil {
		t.Fatal(err)
	}
	for _, path := range b.paths {
		if path == "GET /v1/invoice_payments" {
			continue
		}
		if strings.Contains(path, "in_renewal") {
			t.Fatalf("recovery read delivered service: %s", path)
		}
	}
	if len(b.paymentInvoices) == 0 {
		t.Fatal("checkout payment allocations were not inspected")
	}
	for _, id := range b.paymentInvoices {
		if id != "in_reserved" {
			t.Fatalf("recovery reached delivered invoice %s", id)
		}
	}
}

func TestCheckoutCompensationClosesUnpaidInvoiceBeforeReleasingReservation(t *testing.T) {
	b := &checkoutRecoveryBackend{paid: true, openInvoice: true}
	installBackend(t, b)
	client := NewClient(Config{SecretKey: "sk_test", Logger: logging.NewLogger()})
	if _, err := client.RecoverReservedCheckout(t.Context(), "cus_reserved", "reservation", "cs_reserved"); err != nil {
		t.Fatal(err)
	}
	voided := false
	for _, path := range b.paths {
		if path == "POST /v1/invoices/in_reserved/void" {
			voided = true
		}
	}
	if !voided {
		t.Fatalf("released checkout while its unpaid initial invoice still accepted payment: %v", b.paths)
	}
}

func TestCheckoutRefundRetryUsesAmountBoundIdentity(t *testing.T) {
	b := &checkoutRecoveryBackend{refundStatus: api.RefundStatusFailed}
	installBackend(t, b)
	c := NewClient(Config{SecretKey: "sk_test", Logger: logging.NewLogger()})
	var result billing.CheckoutRecoveryResult
	if err := c.refundInvoicePayments(t.Context(), "in_reserved", "reservation", &result, "checkout", "intent"); err == nil {
		t.Fatal("failed first refund reported accepted")
	}
	b.existingRefunds = append(b.existingRefunds, &api.Refund{ID: "partial-bank-return", Amount: 500, Currency: api.CurrencyEUR, Status: api.RefundStatusSucceeded})
	b.refundStatus = api.RefundStatusPending
	b.chargeOverride = &api.Charge{ID: "ch_reserved", Currency: api.CurrencyEUR, Amount: 2000, AmountRefunded: 500}
	if err := c.refundInvoicePayments(t.Context(), "in_reserved", "reservation", &result, "checkout", "intent"); err != nil {
		t.Fatal(err)
	}
	if len(b.refundParams) != 2 || *b.refundParams[0].Amount != 2000 || *b.refundParams[1].Amount != 1500 || *b.refundParams[0].IdempotencyKey == *b.refundParams[1].IdempotencyKey {
		t.Fatalf("different amounts reused one provider identity: %+v", b.refundParams)
	}
	if err := c.refundInvoicePayments(t.Context(), "in_reserved", "reservation", &result, "checkout", "intent"); err != nil || len(b.refundParams) != 2 {
		t.Fatalf("listed replacement refund was retried: calls=%d err=%v", len(b.refundParams), err)
	}
}

func TestCheckoutFailedRefundIsRetriedAfterConfirmedFailure(t *testing.T) {
	b := &checkoutRecoveryBackend{refundStatus: api.RefundStatusFailed}
	installBackend(t, b)
	c := NewClient(Config{SecretKey: "sk_test", Logger: logging.NewLogger()})
	var result billing.CheckoutRecoveryResult
	if err := c.refundInvoicePayments(t.Context(), "in_reserved", "reservation", &result, "checkout", "intent"); err == nil || result.RefundRequested {
		t.Fatalf("failed refund reported accepted: %+v %v", result, err)
	}
	firstKey := *b.refundParams[0].IdempotencyKey
	b.existingRefunds = []*api.Refund{{ID: "refund-1", Status: api.RefundStatusFailed}}
	b.refundStatus = api.RefundStatusPending
	if err := c.refundInvoicePayments(t.Context(), "in_reserved", "reservation", &result, "checkout", "intent"); err != nil || !result.RefundRequested {
		t.Fatalf("confirmed failed refund cannot recover: %+v %v", result, err)
	}
	if *b.refundParams[1].IdempotencyKey == firstKey {
		t.Fatal("retry replays permanently failed refund")
	}
}

func TestCheckoutRefundRespectsAllocationAndBankReturns(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		returned, allocation, lost int64
		open                       bool
		want                       int64
		refuse                     bool
	}{
		{name: "partial refund", returned: 500, want: 1500},
		{name: "refund and lost dispute", returned: 500, lost: 500, want: 1000},
		{name: "fully lost dispute", lost: 2000, want: 0},
		{name: "open dispute", open: true, refuse: true},
		{name: "shared charge", allocation: 1000, refuse: true},
		{name: "already refunded", returned: 2000, want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ch := &api.Charge{ID: "ch_reserved", Currency: api.CurrencyEUR, Amount: 2000, AmountRefunded: tc.returned, Disputed: tc.lost > 0 || tc.open}
			b := &checkoutRecoveryBackend{chargeOverride: ch, allocation: tc.allocation}
			if ch.Disputed {
				state := api.DisputeStatusLost
				if tc.open {
					state = api.DisputeStatusNeedsResponse
				}
				b.disputes = []*api.Dispute{{ID: "dispute", Status: state, Amount: tc.lost, Currency: api.CurrencyEUR}}
			}
			installBackend(t, b)
			c := NewClient(Config{SecretKey: "sk_test", Logger: logging.NewLogger()})
			var result billing.CheckoutRecoveryResult
			err := c.refundInvoicePayments(t.Context(), "in_reserved", "reservation", &result, "checkout", "intent")
			if (err != nil) != tc.refuse {
				t.Fatalf("unexpected allocation result: %+v %v", result, err)
			}
			if tc.refuse || tc.want == 0 {
				if len(b.refundParams) != 0 {
					t.Fatalf("unsafe or duplicate refund: %+v", b.refundParams)
				}
			} else if len(b.refundParams) != 1 || *b.refundParams[0].Amount != tc.want {
				t.Fatalf("refund did not respect bank returns: %+v", b.refundParams)
			}
		})
	}
}

func TestRecoveryUsesExplicitSessionInvoice(t *testing.T) {
	b := &checkoutRecoveryBackend{paid: true, includeRenewals: true, sessionInvoice: "in_renewal"}
	installBackend(t, b)
	c := NewClient(Config{SecretKey: "sk_test", Logger: logging.NewLogger()})
	if _, err := c.RecoverReservedCheckout(t.Context(), "cus_reserved", "intent", "cs_reserved"); err != nil {
		t.Fatal(err)
	}
	if len(b.paymentInvoices) == 0 {
		t.Fatal("explicit checkout invoice was not read")
	}
	for _, id := range b.paymentInvoices {
		if id != "in_renewal" {
			t.Fatalf("refunded another invoice: %s", id)
		}
	}
}

func TestCheckoutRecoveryWithoutCustomerDoesNotScanAllSessions(t *testing.T) {
	b := &checkoutRecoveryBackend{}
	installBackend(t, b)
	c := NewClient(Config{SecretKey: "sk_test", Logger: logging.NewLogger()})
	if _, err := c.RecoverReservedCheckout(t.Context(), "", "intent", ""); err == nil {
		t.Fatal("unbound recovery scanned sessions without a customer")
	}
	if _, err := c.ReservedCheckout(t.Context(), "", "intent", ""); err == nil {
		t.Fatal("preview scanned sessions without a customer")
	}
	if len(b.paths) != 0 {
		t.Fatalf("customer-wide request escaped validation: %v", b.paths)
	}
}

func TestCheckoutRefundFailureIdentityIncludesEveryConfirmedFailure(t *testing.T) {
	b := &checkoutRecoveryBackend{existingRefunds: []*api.Refund{{ID: "z-first", Created: 10, Status: api.RefundStatusFailed}}}
	installBackend(t, b)
	first, err := checkoutRefundHistory(t.Context(), "charge", api.CurrencyEUR)
	if err != nil {
		t.Fatal(err)
	}
	b.existingRefunds = append(b.existingRefunds, &api.Refund{ID: "a-second", Created: 10, Status: api.RefundStatusFailed})
	second, err := checkoutRefundHistory(t.Context(), "charge", api.CurrencyEUR)
	if err != nil || first.failureIdentity == second.failureIdentity {
		t.Fatalf("equal-time failed retry kept the prior identity: first=%+v second=%+v err=%v", first, second, err)
	}
	b.existingRefunds[0], b.existingRefunds[1] = b.existingRefunds[1], b.existingRefunds[0]
	reordered, err := checkoutRefundHistory(t.Context(), "charge", api.CurrencyEUR)
	if err != nil || second.failureIdentity != reordered.failureIdentity {
		t.Fatalf("pagination order changed failed identity: %+v %+v %v", second, reordered, err)
	}
}

func TestCheckoutRefundLostResponseDoesNotRepeatPendingReturn(t *testing.T) {
	b := &checkoutRecoveryBackend{existingRefunds: []*api.Refund{{ID: "in-flight", Status: api.RefundStatusPending, Amount: 2000, Currency: api.CurrencyEUR}}}
	installBackend(t, b)
	c := NewClient(Config{SecretKey: "sk_test", Logger: logging.NewLogger()})
	var result billing.CheckoutRecoveryResult
	if err := c.refundInvoicePayments(t.Context(), "in_reserved", "reservation", &result, "checkout", "intent"); err != nil {
		t.Fatal(err)
	}
	if len(b.refundParams) != 0 {
		t.Fatal("a stale charge snapshot caused a second refund despite an existing bank return")
	}
}

func TestRefundSettlementRetriesAsynchronousFailure(t *testing.T) {
	b := &checkoutRecoveryBackend{existingRefunds: []*api.Refund{{ID: "initial-refund", Amount: 2000, Currency: api.CurrencyEUR, Status: api.RefundStatusPending}}}
	installBackend(t, b)
	c := NewClient(Config{SecretKey: "sk_test", Logger: logging.NewLogger()})
	plan := billing.RefundSettlement{Provider: "stripe", ReferenceKind: "checkout", ReferenceID: "reservation", InvoiceID: "in_reserved", PaymentID: "pi_reserved", ChargeID: "ch_reserved", AmountCents: 2000, Currency: "EUR"}
	if _, err := c.InspectRefundSettlement(t.Context(), plan); !errors.Is(err, billing.ErrRefundSettling) || len(b.refundParams) != 0 {
		t.Fatalf("pending refund retried: calls=%d err=%v", len(b.refundParams), err)
	}
	b.existingRefunds[0].Status = api.RefundStatusFailed
	if _, err := c.InspectRefundSettlement(t.Context(), plan); !errors.Is(err, billing.ErrRefundSettling) || len(b.refundParams) != 1 {
		t.Fatalf("asynchronous failure did not re-enter compensation: calls=%d err=%v", len(b.refundParams), err)
	}
	b.existingRefunds[len(b.existingRefunds)-1].Status = api.RefundStatusSucceeded
	receipts, err := c.InspectRefundSettlement(t.Context(), plan)
	if err != nil || len(receipts) != 1 || receipts[0].AmountCents != 2000 || len(b.refundParams) != 1 {
		t.Fatalf("replacement refund did not settle once: receipts=%+v calls=%d err=%v", receipts, len(b.refundParams), err)
	}
}

func TestRefundSettlementRetryStaysOnItsRecordedCharge(t *testing.T) {
	b := &checkoutRecoveryBackend{secondCharge: true, existingRefunds: []*api.Refund{{ID: "failed", Amount: 2000, Currency: api.CurrencyEUR, Status: api.RefundStatusFailed}}}
	installBackend(t, b)
	c := NewClient(Config{SecretKey: "sk_test", Logger: logging.NewLogger()})
	plan := billing.RefundSettlement{Provider: "stripe", ReferenceKind: "checkout", ReferenceID: "reservation", InvoiceID: "in_reserved", PaymentID: "pi_reserved", ChargeID: "ch_reserved", AmountCents: 2000, Currency: "EUR"}
	if _, err := c.InspectRefundSettlement(t.Context(), plan); !errors.Is(err, billing.ErrRefundSettling) {
		t.Fatal(err)
	}
	if len(b.refundParams) != 1 || api.StringValue(b.refundParams[0].PaymentIntent) != "pi_reserved" {
		t.Fatalf("one settlement retried another charge without recording its plan: %+v", b.refundParams)
	}
}
