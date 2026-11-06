package stripe

import (
	"context"
	"fmt"
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

// CheckBillingWebhookEndpoint verifies subscriptions using GET requests only.
func (c *Client) CheckBillingWebhookEndpoint(ctx context.Context, gatewayURL string) error {
	if strings.TrimSpace(gatewayURL) == "" {
		return fmt.Errorf("GATEWAY_PUBLIC_URL is required to verify the Stripe billing webhook endpoint")
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
			return fmt.Errorf("read Stripe billing webhook configuration: %w", err)
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
			return fmt.Errorf("stripe webhook configuration pagination did not advance")
		}
		after = page.Data[len(page.Data)-1].ID
	}
	if len(missing) != 0 {
		return fmt.Errorf("stripe billing webhook endpoint is missing required events: %s", strings.Join(missing, ", "))
	}
	return fmt.Errorf("no enabled Stripe billing webhook endpoint matches GATEWAY_PUBLIC_URL; configure /webhooks/billing/stripe")
}
