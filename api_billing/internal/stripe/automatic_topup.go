package stripe

import (
	"context"
	"errors"
	"fmt"
	api "github.com/stripe/stripe-go/v85"
	"github.com/stripe/stripe-go/v85/paymentintent"
	"strings"
	"time"
)

var ErrAutomaticTopupNeedsReview = errors.New("automatic payment requires manual reconciliation")

type AutomaticTopupCharge struct {
	TenantID, TopupID, AttemptID, CustomerID, PaymentMethodID, Currency, PaymentIntentID string
	AmountMinor                                                                          int64
	CreatedAt                                                                            time.Time
}

// ChargeAutomaticTopup requires a specifically consented saved card. An ambiguous
// creation is replayed only within Stripe's idempotency retention window.
func (c *Client) ChargeAutomaticTopup(ctx context.Context, in AutomaticTopupCharge) (*OffSessionChargeResult, error) {
	var pi *api.PaymentIntent
	var err error
	if in.PaymentIntentID != "" {
		params := &api.PaymentIntentParams{}
		params.Context = ctx
		pi, err = paymentintent.Get(in.PaymentIntentID, params)
	} else {
		if time.Since(in.CreatedAt) > 20*time.Hour {
			return nil, ErrAutomaticTopupNeedsReview
		}
		params := &api.PaymentIntentParams{Amount: api.Int64(in.AmountMinor), Currency: api.String(strings.ToLower(in.Currency)), Customer: api.String(in.CustomerID), PaymentMethod: api.String(in.PaymentMethodID), Confirm: api.Bool(true), OffSession: api.Bool(true), Description: api.String("FrameWorks automatic prepaid top-up")}
		params.Context = ctx
		params.SetIdempotencyKey("automatic-prepaid-topup:" + in.AttemptID)
		params.Metadata = map[string]string{"tenant_id": in.TenantID, "topup_id": in.TopupID, "purpose": "automatic_prepaid", "automatic_topup_attempt_id": in.AttemptID}
		pi, err = paymentintent.New(params)
		if err != nil {
			var apiErr *api.Error
			if errors.As(err, &apiErr) {
				if id := extractStripePaymentIntentID(apiErr); id != "" {
					get := &api.PaymentIntentParams{}
					get.Context = ctx
					pi, err = paymentintent.Get(id, get)
				} else if apiErr.HTTPStatusCode >= 400 && apiErr.HTTPStatusCode < 500 && apiErr.HTTPStatusCode != 409 && apiErr.HTTPStatusCode != 429 {
					return &OffSessionChargeResult{Status: "failed", FailureCode: string(apiErr.Code)}, nil
				}
			}
		}
	}
	if err != nil {
		return nil, err
	}
	if pi == nil || pi.Customer == nil || pi.Customer.ID != in.CustomerID || pi.PaymentMethod == nil || pi.PaymentMethod.ID != in.PaymentMethodID || pi.Amount != in.AmountMinor || !strings.EqualFold(string(pi.Currency), in.Currency) || pi.Metadata["tenant_id"] != in.TenantID || pi.Metadata["topup_id"] != in.TopupID || pi.Metadata["automatic_topup_attempt_id"] != in.AttemptID {
		return nil, fmt.Errorf("automatic Stripe payment does not match its consent and reservation")
	}
	state := string(pi.Status)
	if state == "requires_action" || state == "requires_payment_method" || state == "requires_confirmation" {
		cancel := &api.PaymentIntentCancelParams{}
		cancel.Context = ctx
		cancel.SetIdempotencyKey("cancel-automatic-topup:" + in.AttemptID)
		cancelled, cancelErr := paymentintent.Cancel(pi.ID, cancel)
		if cancelErr != nil {
			return nil, cancelErr
		}
		if cancelled.Status != api.PaymentIntentStatusCanceled {
			return nil, ErrAutomaticTopupNeedsReview
		}
		state = "failed"
	}
	if state == "canceled" {
		state = "failed"
	}
	return &OffSessionChargeResult{PaymentIntentID: pi.ID, Status: state, AmountReceived: pi.AmountReceived}, nil
}
