package stripe

import (
	"context"
	"fmt"
	"strings"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/stripe/stripe-go/v85"
	"github.com/stripe/stripe-go/v85/charge"
	checkoutsession "github.com/stripe/stripe-go/v85/checkout/session"
	"github.com/stripe/stripe-go/v85/invoice"
	"github.com/stripe/stripe-go/v85/invoicepayment"
	"github.com/stripe/stripe-go/v85/paymentintent"
	"github.com/stripe/stripe-go/v85/refund"
)

// CancelReservedCheckout finds even an unbound provider response by its durable metadata.
// An unactivated subscription is cancelled and only its checkout invoice is refunded.
func (c *Client) CancelReservedCheckout(ctx context.Context, customerID, intentID, sessionID string) error {
	_, err := c.RecoverReservedCheckout(ctx, customerID, intentID, sessionID)
	return err
}

// RecoverReservedCheckout reports cancellation and refund outcomes for an exact reservation.
func (c *Client) RecoverReservedCheckout(ctx context.Context, customerID, intentID, sessionID string) (billing.CheckoutRecoveryResult, error) {
	var result billing.CheckoutRecoveryResult
	if sessionID != "" {
		p := &stripe.CheckoutSessionParams{}
		p.Context = ctx
		sess, err := checkoutsession.Get(sessionID, p)
		if err != nil {
			return result, err
		}
		err = c.cancelReservedSession(ctx, sess, intentID, &result)
		return result, err
	}
	p := &stripe.CheckoutSessionListParams{Customer: stripe.String(customerID)}
	p.Context = ctx
	iter := checkoutsession.List(p)
	for iter.Next() {
		sess := iter.CheckoutSession()
		if sess.Metadata["checkout_reservation_id"] == intentID {
			if err := c.cancelReservedSession(ctx, sess, intentID, &result); err != nil {
				return result, err
			}
		}
	}
	return result, iter.Err()
}

func (c *Client) cancelReservedSession(ctx context.Context, sess *stripe.CheckoutSession, intentID string, result *billing.CheckoutRecoveryResult) error {
	if sess.Status == stripe.CheckoutSessionStatusOpen {
		return c.ExpireCheckoutSession(ctx, sess.ID)
	}
	if sess.Subscription == nil || sess.Subscription.ID == "" {
		return nil
	}
	sub, err := c.GetSubscription(ctx, sess.Subscription.ID)
	if err != nil {
		return err
	}
	if sub.Status != stripe.SubscriptionStatusCanceled && sub.Status != stripe.SubscriptionStatusIncompleteExpired {
		if _, err = c.CancelSubscriptionImmediately(ctx, sub.ID); err != nil {
			return err
		}
	}
	result.Cancelled = true
	if sess.PaymentIntent != nil && sess.PaymentIntent.ID != "" {
		payment, err := c.GetPaymentIntent(ctx, sess.PaymentIntent.ID)
		if err != nil {
			return err
		}
		if payment.Status == stripe.PaymentIntentStatusProcessing {
			return billing.ErrCheckoutSettlementPending
		}
	}
	p := &stripe.InvoiceListParams{Subscription: stripe.String(sub.ID)}
	p.Context = ctx
	invoices := invoice.List(p)
	for invoices.Next() {
		inv := invoices.Invoice()
		if (sess.Invoice != nil && sess.Invoice.ID != "" && inv.ID != sess.Invoice.ID) || (sess.Invoice == nil || sess.Invoice.ID == "") && inv.BillingReason != stripe.InvoiceBillingReasonSubscriptionCreate {
			continue
		}
		// A partially paid invoice can still own another processing debit.
		// Subscription sessions need not expose those invoice payment intents.
		if err := c.waitForCheckoutInvoiceDebit(ctx, inv.ID); err != nil {
			return err
		}
		// Cancelling renewal does not by itself close an existing invoice's
		// payment window. Close only this unactivated purchase's invoice before
		// releasing its reservation; a processing debit keeps recovery pending.
		switch inv.Status {
		case stripe.InvoiceStatusDraft:
			params := &stripe.InvoiceParams{}
			params.Context = ctx
			if _, err := invoice.Del(inv.ID, params); err != nil {
				return err
			}
		case stripe.InvoiceStatusOpen, stripe.InvoiceStatusUncollectible:
			params := &stripe.InvoiceVoidInvoiceParams{}
			params.Context = ctx
			params.SetIdempotencyKey("checkout-expiry-void:" + intentID + ":" + inv.ID)
			if _, err := invoice.VoidInvoice(inv.ID, params); err != nil {
				return err
			}
		}
		if err := c.refundInvoicePayments(ctx, inv.ID, "checkout-expiry-refund:"+intentID, result, "checkout", intentID); err != nil {
			return err
		}
	}
	return invoices.Err()
}

func (c *Client) waitForCheckoutInvoiceDebit(ctx context.Context, invoiceID string) error {
	p := &stripe.InvoicePaymentListParams{Invoice: stripe.String(invoiceID)}
	p.Context = ctx
	payments := invoicepayment.List(p)
	for payments.Next() {
		payment := payments.InvoicePayment().Payment
		if payment == nil || payment.PaymentIntent == nil || payment.PaymentIntent.ID == "" {
			continue
		}
		intent, err := c.GetPaymentIntent(ctx, payment.PaymentIntent.ID)
		if err != nil {
			return err
		}
		if intent.Status == stripe.PaymentIntentStatusProcessing {
			return billing.ErrCheckoutSettlementPending
		}
	}
	return payments.Err()
}

// ReservedCheckout retrieves the exact attempt before recovery chooses activation or compensation.
func (c *Client) ReservedCheckout(ctx context.Context, customerID, intentID, sessionID string) (*stripe.CheckoutSession, error) {
	if sessionID != "" {
		p := &stripe.CheckoutSessionParams{}
		p.Context = ctx
		return checkoutsession.Get(sessionID, p)
	}
	p := &stripe.CheckoutSessionListParams{Customer: stripe.String(customerID)}
	p.Context = ctx
	it := checkoutsession.List(p)
	var found *stripe.CheckoutSession
	for it.Next() {
		s := it.CheckoutSession()
		if s.Metadata["checkout_reservation_id"] == intentID {
			if found != nil {
				return nil, fmt.Errorf("multiple sessions for checkout reservation %s", intentID)
			}
			found = s
		}
	}
	return found, it.Err()
}

// GetPaymentIntent retrieves the settlement state of a completed checkout.
func (c *Client) GetPaymentIntent(ctx context.Context, id string) (*stripe.PaymentIntent, error) {
	p := &stripe.PaymentIntentParams{}
	p.Context = ctx
	return paymentintent.Get(id, p)
}

// RefundUnappliedTierUpgrade compensates only the invoice collected for a
// replacement month that starts after the account's permanent billing cutoff.
func (c *Client) RefundUnappliedTierUpgrade(ctx context.Context, upgradeID, invoiceID string) (billing.CheckoutRecoveryResult, error) {
	var result billing.CheckoutRecoveryResult
	if invoiceID == "" {
		return result, fmt.Errorf("upgrade %s has no replacement invoice reference", upgradeID)
	}
	err := c.refundInvoicePayments(ctx, invoiceID, "unapplied-upgrade-refund:"+upgradeID, &result, "tier_upgrade", upgradeID)
	return result, err
}

func (c *Client) refundInvoicePayments(ctx context.Context, invoiceID, key string, result *billing.CheckoutRecoveryResult, kind, referenceID string) error {
	pp := &stripe.InvoicePaymentListParams{Invoice: stripe.String(invoiceID), Status: stripe.String("paid")}
	pp.Context = ctx
	payments := invoicepayment.List(pp)
	for payments.Next() {
		paid := payments.InvoicePayment()
		if paid.Payment == nil {
			return fmt.Errorf("paid invoice %s has no payment reference", invoiceID)
		}
		rp := &stripe.RefundParams{}
		var collected *stripe.Charge
		paymentID := ""
		if paid.Payment.PaymentIntent != nil {
			get := &stripe.PaymentIntentParams{}
			get.Context = ctx
			get.AddExpand("latest_charge")
			pi, err := paymentintent.Get(paid.Payment.PaymentIntent.ID, get)
			if err != nil {
				return err
			}
			collected = pi.LatestCharge
			paymentID = pi.ID
			rp.PaymentIntent = stripe.String(pi.ID)
		} else if paid.Payment.Charge != nil {
			get := &stripe.ChargeParams{}
			get.Context = ctx
			ch, err := charge.Get(paid.Payment.Charge.ID, get)
			if err != nil {
				return err
			}
			collected = ch
			if ch.PaymentIntent != nil {
				paymentID = ch.PaymentIntent.ID
			}
			rp.Charge = stripe.String(ch.ID)
		} else {
			return fmt.Errorf("unsupported payment for checkout invoice %s", invoiceID)
		}
		if collected == nil || collected.ID == "" || collected.Amount <= 0 || len(collected.Currency) != 3 {
			return fmt.Errorf("paid invoice %s has no captured charge", invoiceID)
		}
		target := collected.Amount
		if paid.AmountPaid > 0 {
			target = min(target, paid.AmountPaid)
			if paid.AmountPaid != collected.Amount && collected.AmountRefunded < collected.Amount {
				return fmt.Errorf("checkout invoice %s shares a charge; reconcile its payment allocation before refunding", invoiceID)
			}
		}
		if paymentID == "" {
			paymentID = collected.ID
		}
		result.RefundSettlements = append(result.RefundSettlements, billing.RefundSettlement{Provider: "stripe", ReferenceKind: kind, ReferenceID: referenceID, InvoiceID: invoiceID, PaymentID: paymentID, ChargeID: collected.ID, AmountCents: target, Currency: string(collected.Currency)})
		returned := collected.AmountRefunded
		if collected.Disputed {
			lost, open, err := lostDisputeReceipts(ctx, collected.ID)
			if err != nil {
				return err
			}
			if open {
				return fmt.Errorf("checkout charge %s has an unresolved dispute; resolve it before refunding", collected.ID)
			}
			for _, receipt := range lost {
				if receipt.Currency != strings.ToUpper(string(collected.Currency)) {
					return fmt.Errorf("checkout dispute has a different currency")
				}
				returned += receipt.AmountCents
			}
			if returned >= target {
				result.ReturnedByChargeback = true
				continue
			}
		}
		if collected.Refunded || returned >= target {
			result.RefundAlreadyComplete = true
			continue
		}
		rp.Amount = stripe.Int64(min(target-returned, collected.Amount-returned))
		rp.Context = ctx
		rp.SetIdempotencyKey(key + ":" + paid.ID)
		if _, err := refund.New(rp); err != nil {
			return err
		}
		result.RefundRequested = true
	}
	if err := payments.Err(); err != nil {
		return err
	}
	return nil
}
