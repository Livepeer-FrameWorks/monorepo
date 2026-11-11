package stripe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"

	api "github.com/stripe/stripe-go/v85"
)

type billingWebhookEndpoints struct {
	api.APIResource
	Data    []*api.WebhookEndpoint `json:"data"`
	HasMore bool                   `json:"has_more"`
}

var requiredBillingWebhookEvents = []string{
	"checkout.session.completed", "checkout.session.async_payment_succeeded",
	"checkout.session.async_payment_failed", "checkout.session.expired",
	"customer.subscription.created", "customer.subscription.updated", "customer.subscription.deleted",
	"customer.subscription.paused", "customer.subscription.resumed",
	"invoice.finalized", "invoice.paid", "invoice.payment_failed", "invoice.payment_action_required",
	"credit_note.created", "credit_note.updated", "credit_note.voided",
	"payment_intent.succeeded", "payment_intent.payment_failed",
	"charge.refunded", "charge.updated", "charge.dispute.created", "charge.dispute.closed",
	"charge.dispute.funds_withdrawn", "charge.dispute.funds_reinstated",
}

// CheckBillingWebhookEndpoint verifies subscriptions using GET requests only. Every failure is a
// *BillingWebhookError.
func (c *Client) CheckBillingWebhookEndpoint(ctx context.Context, gatewayURL string) error {
	if strings.TrimSpace(gatewayURL) == "" {
		return &BillingWebhookError{Reason: BillingWebhookGatewayURLMissing,
			Message: "GATEWAY_PUBLIC_URL is required to verify the Stripe billing webhook endpoint"}
	}
	wantURL := strings.TrimRight(gatewayURL, "/") + "/webhooks/billing/stripe"
	var after string
	var missing []string
	for {
		params := &api.Params{Context: ctx}
		params.AddExtra("limit", "100")
		if after != "" {
			params.AddExtra("starting_after", after)
		}
		var page billingWebhookEndpoints
		if err := api.GetBackend(api.APIBackend).Call("GET", "/v1/webhook_endpoints", c.secretKey, params, &page); err != nil {
			return billingWebhookProviderFailure(err, wantURL)
		}
		for _, endpoint := range page.Data {
			if endpoint == nil || endpoint.Status != "enabled" || strings.TrimRight(endpoint.URL, "/") != wantURL {
				continue
			}
			enabled := make(map[string]bool, len(endpoint.EnabledEvents))
			for _, event := range endpoint.EnabledEvents {
				enabled[event] = true
			}
			if enabled["*"] {
				return nil
			}
			var absent []string
			for _, event := range requiredBillingWebhookEvents {
				if !enabled[event] {
					absent = append(absent, event)
				}
			}
			if len(absent) == 0 {
				return nil
			}
			if missing == nil || len(absent) < len(missing) {
				missing = absent
			}
		}
		if !page.HasMore {
			break
		}
		if len(page.Data) == 0 || page.Data[len(page.Data)-1] == nil || page.Data[len(page.Data)-1].ID == "" || page.Data[len(page.Data)-1].ID == after {
			return &BillingWebhookError{Reason: BillingWebhookProviderError, ExpectedURL: wantURL,
				Message: "Stripe webhook endpoint pagination did not advance"}
		}
		after = page.Data[len(page.Data)-1].ID
	}
	if len(missing) != 0 {
		return &BillingWebhookError{Reason: BillingWebhookMissingEvents, ExpectedURL: wantURL, MissingEvents: missing,
			Message: fmt.Sprintf("Stripe webhook endpoint %s is missing required events: %s", wantURL, strings.Join(missing, ", "))}
	}
	return &BillingWebhookError{Reason: BillingWebhookNoEndpoint, ExpectedURL: wantURL,
		Message: fmt.Sprintf("no enabled Stripe webhook endpoint at %s; create or enable one for the billing events", wantURL)}
}

// billingWebhookProviderFailure classifies a failed Stripe read. Stripe's own message can echo
// part of the API key, so only its HTTP status, type and code reach the reason.
func billingWebhookProviderFailure(err error, wantURL string) *BillingWebhookError {
	reason := &BillingWebhookError{ExpectedURL: wantURL, cause: err}
	var stripeErr *api.Error
	var netErr net.Error
	switch {
	case errors.As(err, &stripeErr):
		reason.HTTPStatus, reason.StripeCode = stripeErr.HTTPStatusCode, string(stripeErr.Code)
		code := reason.StripeCode
		if code == "" {
			code = string(stripeErr.Type)
		}
		if stripeErr.HTTPStatusCode == http.StatusUnauthorized || stripeErr.HTTPStatusCode == http.StatusForbidden {
			reason.Reason = BillingWebhookPermissionDenied
			reason.Message = fmt.Sprintf("the configured Stripe API key cannot read webhook endpoints (HTTP %d, Stripe code %s); grant it read access to Webhook Endpoints", stripeErr.HTTPStatusCode, code)
		} else {
			reason.Reason = BillingWebhookProviderError
			reason.Message = fmt.Sprintf("Stripe returned HTTP %d (Stripe code %s) while reading webhook endpoints", stripeErr.HTTPStatusCode, code)
		}
	case errors.Is(err, context.DeadlineExceeded) || errors.As(err, &netErr) && netErr.Timeout():
		reason.Reason = BillingWebhookTimeout
		reason.Message = "timed out reading Stripe webhook endpoints"
	default:
		reason.Reason = BillingWebhookTransport
		reason.Message = "could not reach Stripe to read webhook endpoints: " + redactStripeKeys(err.Error())
	}
	return reason
}

var stripeKeyPattern = regexp.MustCompile(`\b(sk|rk|pk)_(live|test)_[A-Za-z0-9*]+`)

func redactStripeKeys(s string) string {
	return stripeKeyPattern.ReplaceAllString(s, "[redacted]")
}

// BillingWebhookReason classifies why the billing webhook endpoint check failed.
type BillingWebhookReason string

const (
	BillingWebhookGatewayURLMissing BillingWebhookReason = "gateway_url_missing"
	BillingWebhookNoEndpoint        BillingWebhookReason = "no_enabled_endpoint"
	BillingWebhookMissingEvents     BillingWebhookReason = "missing_events"
	BillingWebhookPermissionDenied  BillingWebhookReason = "permission_denied"
	BillingWebhookTimeout           BillingWebhookReason = "timeout"
	BillingWebhookTransport         BillingWebhookReason = "transport_error"
	BillingWebhookProviderError     BillingWebhookReason = "provider_error"
)

// BillingWebhookError is a failed billing webhook check. Error() never contains
// the API key or Stripe's free-text message, so it is safe to return to operators.
type BillingWebhookError struct {
	Reason        BillingWebhookReason
	Message       string
	ExpectedURL   string
	MissingEvents []string
	StripeCode    string
	HTTPStatus    int
	cause         error
}

func (e *BillingWebhookError) Error() string { return e.Message }

func (e *BillingWebhookError) Unwrap() error { return e.cause }
