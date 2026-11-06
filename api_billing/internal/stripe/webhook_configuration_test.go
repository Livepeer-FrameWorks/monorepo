package stripe

import (
	"context"
	"errors"
	"strings"
	"testing"

	api "github.com/stripe/stripe-go/v85"
)

func TestBillingWebhookConfigurationRejectsMissingDocumentEvents(t *testing.T) {
	backend := &financialAuditBackend{call: func(method, path, key string, p api.ParamsContainer, out api.LastResponseSetter) error {
		if method != "GET" || path != "/v1/webhook_endpoints" || key != "sk_test_hooks" {
			t.Fatalf("unexpected provider operation: %s %s", method, path)
		}
		out.(*billingWebhookEndpoints).Data = []*api.WebhookEndpoint{{
			URL: "https://gateway.example/webhooks/billing/stripe", Status: "enabled",
			EnabledEvents: []string{"invoice.paid", "invoice.payment_failed"},
		}}
		return nil
	}}
	installBackend(t, backend)
	err := (&Client{secretKey: "sk_test_hooks"}).CheckBillingWebhookEndpoint(context.Background(), "https://gateway.example/")
	for _, event := range []string{"invoice.finalized", "credit_note.created", "credit_note.updated", "credit_note.voided"} {
		if err == nil || !strings.Contains(err.Error(), event) {
			t.Fatalf("missing document event %q was not reported: %v", event, err)
		}
	}
}

func TestBillingWebhookConfigurationRequiresMatchingEnabledEndpoint(t *testing.T) {
	for _, endpoint := range []*api.WebhookEndpoint{
		{URL: "https://other.example/webhooks/billing/stripe", Status: "enabled", EnabledEvents: []string{"*"}},
		{URL: "https://gateway.example/webhooks/billing/stripe", Status: "disabled", EnabledEvents: []string{"*"}},
	} {
		installBackend(t, &financialAuditBackend{call: func(_, _, _ string, _ api.ParamsContainer, out api.LastResponseSetter) error {
			out.(*billingWebhookEndpoints).Data = []*api.WebhookEndpoint{endpoint}
			return nil
		}})
		if err := (&Client{}).CheckBillingWebhookEndpoint(context.Background(), "https://gateway.example"); err == nil {
			t.Fatalf("unusable webhook endpoint accepted: status=%s", endpoint.Status)
		}
	}
}

func TestBillingWebhookConfigurationPaginatesReadOnly(t *testing.T) {
	calls := 0
	installBackend(t, &financialAuditBackend{call: func(method, path, _ string, p api.ParamsContainer, out api.LastResponseSetter) error {
		calls++
		if method != "GET" || path != "/v1/webhook_endpoints" || p.GetParams().Extra.Get("limit") != "100" {
			t.Fatalf("unexpected webhook check operation: %s %s", method, path)
		}
		page := out.(*billingWebhookEndpoints)
		if calls == 1 {
			page.Data = []*api.WebhookEndpoint{{ID: "we_other", URL: "https://other.example/hook", Status: "enabled", EnabledEvents: []string{"*"}}}
			page.HasMore = true
			return nil
		}
		if p.GetParams().Extra.Get("starting_after") != "we_other" {
			t.Fatal("webhook pagination cursor was not retained")
		}
		page.Data = []*api.WebhookEndpoint{{ID: "we_billing", URL: "https://gateway.example/webhooks/billing/stripe", Status: "enabled", EnabledEvents: []string{"*"}}}
		return nil
	}})
	if err := (&Client{}).CheckBillingWebhookEndpoint(context.Background(), "https://gateway.example"); err != nil || calls != 2 {
		t.Fatalf("paginated webhook check: calls=%d err=%v", calls, err)
	}
}

func TestBillingWebhookConfigurationPropagatesProviderFailure(t *testing.T) {
	providerErr := errors.New("provider unavailable")
	installBackend(t, &financialAuditBackend{call: func(_, _, _ string, _ api.ParamsContainer, _ api.LastResponseSetter) error { return providerErr }})
	if err := (&Client{}).CheckBillingWebhookEndpoint(context.Background(), "https://gateway.example"); !errors.Is(err, providerErr) {
		t.Fatalf("provider check failure hidden: %v", err)
	}
}
