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

func TestBillingWebhookConfigurationChecksEveryDocumentEvent(t *testing.T) {
	for _, absent := range []string{"", "invoice.finalized", "credit_note.created", "credit_note.updated", "credit_note.voided"} {
		t.Run("missing_"+absent, func(t *testing.T) {
			var events []string
			for _, event := range requiredBillingWebhookEvents {
				if event != absent {
					events = append(events, event)
				}
			}
			installBackend(t, &financialAuditBackend{call: func(method, path, _ string, _ api.ParamsContainer, out api.LastResponseSetter) error {
				if method != "GET" || path != "/v1/webhook_endpoints" {
					t.Fatalf("non-read-only webhook check: %s %s", method, path)
				}
				out.(*billingWebhookEndpoints).Data = []*api.WebhookEndpoint{{URL: "https://gateway.example/webhooks/billing/stripe", Status: "enabled", EnabledEvents: events}}
				return nil
			}})
			err := (&Client{}).CheckBillingWebhookEndpoint(context.Background(), "https://gateway.example")
			if absent == "" && err != nil || absent != "" && (err == nil || !strings.Contains(err.Error(), absent)) {
				t.Fatalf("absent=%q check=%v", absent, err)
			}
		})
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

func allBillingEventsExcept(absent string) []string {
	var events []string
	for _, event := range requiredBillingWebhookEvents {
		if event != absent {
			events = append(events, event)
		}
	}
	return events
}

func TestBillingWebhookConfigurationReportsTypedMissingEvent(t *testing.T) {
	installBackend(t, &financialAuditBackend{call: func(_, _, _ string, _ api.ParamsContainer, out api.LastResponseSetter) error {
		out.(*billingWebhookEndpoints).Data = []*api.WebhookEndpoint{{
			URL: "https://gateway.example/webhooks/billing/stripe", Status: "enabled", EnabledEvents: allBillingEventsExcept("charge.updated"),
		}}
		return nil
	}})
	err := (&Client{}).CheckBillingWebhookEndpoint(context.Background(), "https://gateway.example")
	var reason *BillingWebhookError
	if !errors.As(err, &reason) || reason.Reason != BillingWebhookMissingEvents ||
		len(reason.MissingEvents) != 1 || reason.MissingEvents[0] != "charge.updated" || !strings.Contains(reason.Error(), "charge.updated") {
		t.Fatalf("missing charge.updated not reported as a typed reason: %#v", err)
	}
}

func TestBillingWebhookConfigurationNamesExpectedURLWhenNoEndpointMatches(t *testing.T) {
	installBackend(t, &financialAuditBackend{call: func(_, _, _ string, _ api.ParamsContainer, out api.LastResponseSetter) error {
		out.(*billingWebhookEndpoints).Data = []*api.WebhookEndpoint{{URL: "https://other.example/webhooks/billing/stripe", Status: "enabled", EnabledEvents: []string{"*"}}}
		return nil
	}})
	err := (&Client{}).CheckBillingWebhookEndpoint(context.Background(), "https://gateway.example/")
	var reason *BillingWebhookError
	if !errors.As(err, &reason) || reason.Reason != BillingWebhookNoEndpoint ||
		reason.ExpectedURL != "https://gateway.example/webhooks/billing/stripe" || !strings.Contains(reason.Error(), reason.ExpectedURL) {
		t.Fatalf("no-endpoint failure does not name the expected URL: %#v", err)
	}
}

func TestBillingWebhookConfigurationReportsPermissionWithoutSecrets(t *testing.T) {
	for _, status := range []int{401, 403} {
		installBackend(t, &financialAuditBackend{call: func(_, _, _ string, _ api.ParamsContainer, _ api.LastResponseSetter) error {
			return &api.Error{HTTPStatusCode: status, Type: api.ErrorTypeInvalidRequest, Code: "secret_key_required",
				Msg: "The provided key 'rk_live_privatekey' does not have the required permissions"}
		}})
		err := (&Client{secretKey: "rk_live_privatekey"}).CheckBillingWebhookEndpoint(context.Background(), "https://gateway.example")
		var reason *BillingWebhookError
		if !errors.As(err, &reason) || reason.Reason != BillingWebhookPermissionDenied || reason.StripeCode != "secret_key_required" ||
			!strings.Contains(reason.Error(), "cannot read webhook endpoints") || !strings.Contains(reason.Error(), "secret_key_required") {
			t.Fatalf("HTTP %d permission failure not reported: %#v", status, err)
		}
		if strings.Contains(reason.Error(), "privatekey") {
			t.Fatalf("permission reason leaked the API key: %s", reason.Error())
		}
	}
}

func TestBillingWebhookConfigurationReportsTimeoutAndTransport(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want BillingWebhookReason
	}{
		{context.DeadlineExceeded, BillingWebhookTimeout},
		{errors.New("dial tcp: connection refused for sk_live_privatekey"), BillingWebhookTransport},
	} {
		installBackend(t, &financialAuditBackend{call: func(_, _, _ string, _ api.ParamsContainer, _ api.LastResponseSetter) error { return tc.err }})
		err := (&Client{}).CheckBillingWebhookEndpoint(context.Background(), "https://gateway.example")
		var reason *BillingWebhookError
		if !errors.As(err, &reason) || reason.Reason != tc.want || !errors.Is(err, tc.err) {
			t.Fatalf("provider failure %v: want reason %s, got %#v", tc.err, tc.want, err)
		}
		if strings.Contains(reason.Error(), "privatekey") {
			t.Fatalf("transport reason leaked the API key: %s", reason.Error())
		}
	}
}
