package mollie

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	mollielib "github.com/VictorAvelar/mollie-api-go/v4/mollie"
)

type mollieRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn mollieRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func newMollieContractClient(t *testing.T, handler mollieRoundTripFunc) *Client {
	t.Helper()
	client, err := NewClient(Config{APIKey: "test_secret", Logger: logging.NewLogger()})
	if err != nil {
		t.Fatal(err)
	}
	client.client.BaseURL, err = url.Parse("https://mollie.example.test/")
	if err != nil {
		t.Fatal(err)
	}
	previous := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: handler}
	t.Cleanup(func() { http.DefaultClient = previous })
	return client
}

func mollieJSONResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func decodeMollieRequest(t *testing.T, req *http.Request) map[string]any {
	t.Helper()
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode request %q: %v", body, err)
	}
	return payload
}

func assertMollieMoneyHeaders(t *testing.T, req *http.Request, idempotencyKey string) {
	t.Helper()
	if req.Method != http.MethodPost || req.Header.Get("Authorization") != "Bearer test_secret" || req.Header.Get("Idempotency-Key") != idempotencyKey || req.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("request method/headers = %s %+v", req.Method, req.Header)
	}
}

func TestMollieWriteOperationsRequireIdempotencyBeforeIO(t *testing.T) {
	client := &Client{}
	checks := []struct {
		name string
		call func() error
	}{
		{name: "customer without tenant or key", call: func() error { _, err := client.CreateOrGetCustomer(context.Background(), CustomerInfo{}); return err }},
		{name: "first payment", call: func() error {
			_, err := client.CreateFirstPayment(context.Background(), FirstPaymentParams{})
			return err
		}},
		{name: "subscription", call: func() error {
			_, err := client.CreateSubscription(context.Background(), SubscriptionParams{})
			return err
		}},
		{name: "mandate charge", call: func() error {
			_, err := client.ChargeOnMandate(context.Background(), OnDemandChargeParams{})
			return err
		}},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.call(); err == nil || !strings.Contains(err.Error(), "IdempotencyKey") {
				t.Fatalf("idempotency error = %v", err)
			}
		})
	}
}

func TestCreateMollieCustomerDerivesTenantIdempotencyAndExactPayload(t *testing.T) {
	client := newMollieContractClient(t, func(req *http.Request) (*http.Response, error) {
		assertMollieMoneyHeaders(t, req, "mollie-customer:tenant-1")
		if req.URL.Path != "/v2/customers" {
			t.Fatalf("path = %q", req.URL.Path)
		}
		payload := decodeMollieRequest(t, req)
		if payload["email"] != "ada@example.test" || payload["name"] != "Ada" || payload["locale"] != "en_US" {
			t.Fatalf("customer payload = %#v", payload)
		}
		return mollieJSONResponse(http.StatusCreated, `{"id":"cst_1","name":"Ada","email":"ada@example.test"}`), nil
	})

	customer, err := client.CreateOrGetCustomer(context.Background(), CustomerInfo{TenantID: "tenant-1", Email: "ada@example.test", Name: "Ada"})
	if err != nil {
		t.Fatal(err)
	}
	if customer.ID != "cst_1" || customer.Email != "ada@example.test" {
		t.Fatalf("customer = %+v", customer)
	}
}

func TestCreateMollieFirstPaymentBindsMandateMetadata(t *testing.T) {
	client := newMollieContractClient(t, func(req *http.Request) (*http.Response, error) {
		assertMollieMoneyHeaders(t, req, "first:tenant-1:pro")
		if req.URL.Path != "/v2/customers/cst_1/payments" {
			t.Fatalf("path = %q", req.URL.Path)
		}
		payload := decodeMollieRequest(t, req)
		metadata := payload["metadata"].(map[string]any)
		if payload["sequenceType"] != "first" || metadata["purpose"] != "mandate_setup" || metadata["tenant_id"] != "tenant-1" || metadata["tier_id"] != "pro" || metadata["reference_id"] != "pro" {
			t.Fatalf("first payment payload = %#v", payload)
		}
		amount := payload["amount"].(map[string]any)
		if amount["value"] != "1.00" || amount["currency"] != "EUR" {
			t.Fatalf("amount = %#v", amount)
		}
		return mollieJSONResponse(http.StatusCreated, `{"id":"tr_first","status":"open"}`), nil
	})

	payment, err := client.CreateFirstPayment(context.Background(), FirstPaymentParams{
		CustomerID: "cst_1", TenantID: "tenant-1", TierID: "pro", Amount: Amount("1.00", "EUR"),
		Description: "Set up billing", Method: mollielib.IDeal, RedirectURL: "https://app/return", WebhookURL: "https://api/hook", IdempotencyKey: "first:tenant-1:pro",
	})
	if err != nil || payment.ID != "tr_first" {
		t.Fatalf("payment = %+v, err=%v", payment, err)
	}
}

func TestCreateMollieSubscriptionBindsMandateAndCommercialIdentity(t *testing.T) {
	client := newMollieContractClient(t, func(req *http.Request) (*http.Response, error) {
		assertMollieMoneyHeaders(t, req, "subscription:tenant-1:pro")
		if req.URL.Path != "/v2/customers/cst_1/subscriptions" {
			t.Fatalf("path = %q", req.URL.Path)
		}
		payload := decodeMollieRequest(t, req)
		metadata := payload["metadata"].(map[string]any)
		if payload["mandateId"] != "mdt_1" || payload["interval"] != "1 month" || payload["startDate"] != "2026-09-01" || metadata["tenant_id"] != "tenant-1" || metadata["reference_id"] != "pro" {
			t.Fatalf("subscription payload = %#v", payload)
		}
		return mollieJSONResponse(http.StatusCreated, `{"id":"sub_1","status":"active"}`), nil
	})

	subscription, err := client.CreateSubscription(context.Background(), SubscriptionParams{
		CustomerID: "cst_1", TenantID: "tenant-1", TierID: "pro", MandateID: "mdt_1",
		Amount: Amount("19.95", "EUR"), Interval: "1 month", Description: "Pro", StartDate: "2026-09-01",
		WebhookURL: "https://api/hook", IdempotencyKey: "subscription:tenant-1:pro",
	})
	if err != nil || subscription.ID != "sub_1" {
		t.Fatalf("subscription = %+v, err=%v", subscription, err)
	}
}

func TestChargeOnMollieMandateBindsInvoiceAndLocalPayment(t *testing.T) {
	client := newMollieContractClient(t, func(req *http.Request) (*http.Response, error) {
		assertMollieMoneyHeaders(t, req, "invoice:inv-1:attempt-1")
		payload := decodeMollieRequest(t, req)
		metadata := payload["metadata"].(map[string]any)
		if payload["sequenceType"] != "recurring" || payload["mandateId"] != "mdt_1" || metadata["purpose"] != "invoice" || metadata["tenant_id"] != "tenant-1" || metadata["invoice_id"] != "inv-1" || metadata["billing_payment_id"] != "pay-1" || metadata["reference_id"] != "inv-1" {
			t.Fatalf("mandate charge payload = %#v", payload)
		}
		return mollieJSONResponse(http.StatusCreated, `{"id":"tr_invoice","status":"pending"}`), nil
	})

	payment, err := client.ChargeOnMandate(context.Background(), OnDemandChargeParams{
		CustomerID: "cst_1", MandateID: "mdt_1", TenantID: "tenant-1", InvoiceID: "inv-1", PaymentID: "pay-1",
		Amount: Amount("5.25", "EUR"), Description: "Invoice INV-1", WebhookURL: "https://api/hook", IdempotencyKey: "invoice:inv-1:attempt-1",
	})
	if err != nil || payment.ID != "tr_invoice" {
		t.Fatalf("payment = %+v, err=%v", payment, err)
	}
}

func TestMollieWritesRejectProviderFailureAndMalformedEvidence(t *testing.T) {
	t.Run("provider failure", func(t *testing.T) {
		client := newMollieContractClient(t, func(_ *http.Request) (*http.Response, error) {
			return mollieJSONResponse(http.StatusUnprocessableEntity, `{"detail":"amount invalid"}`), nil
		})
		_, err := client.CreateFirstPayment(context.Background(), FirstPaymentParams{IdempotencyKey: "key"})
		if err == nil || !strings.Contains(err.Error(), "status 422") || !strings.Contains(err.Error(), "amount invalid") {
			t.Fatalf("provider error = %v", err)
		}
	})
	t.Run("malformed success", func(t *testing.T) {
		client := newMollieContractClient(t, func(_ *http.Request) (*http.Response, error) {
			return mollieJSONResponse(http.StatusCreated, `{not-json`), nil
		})
		_, err := client.CreateOrGetCustomer(context.Background(), CustomerInfo{TenantID: "tenant-1"})
		if err == nil || !strings.Contains(err.Error(), "decode Mollie customer") {
			t.Fatalf("decode error = %v", err)
		}
	})
}
