package grpc

import (
	"io"
	"net/http"
	"strings"
	"testing"

	pb "frameworks/pkg/proto"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func testHTTPResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestScanAllocationDetails(t *testing.T) {
	got := scanAllocationDetails(nil)
	if got != nil {
		t.Fatalf("expected nil for empty input, got %+v", got)
	}

	got = scanAllocationDetails([]byte(`{"limit":12.5,"unit_price":0.42,"unit":"GB"}`))
	if got == nil {
		t.Fatal("expected parsed allocation details")
	}
	if got.Limit == nil || *got.Limit != 12.5 {
		t.Fatalf("Limit: got %v, want 12.5", got.Limit)
	}
	if got.UnitPrice != 0.42 {
		t.Fatalf("UnitPrice: got %v, want 0.42", got.UnitPrice)
	}
	if got.Unit != "GB" {
		t.Fatalf("Unit: got %q, want %q", got.Unit, "GB")
	}

	got = scanAllocationDetails([]byte(`{"limit":`))
	if got != nil {
		t.Fatalf("expected nil for malformed json, got %+v", got)
	}
}

func TestScanBillingFeatures(t *testing.T) {
	got := scanBillingFeatures(nil)
	if got != nil {
		t.Fatalf("expected nil for empty input, got %+v", got)
	}

	got = scanBillingFeatures([]byte(`{"recording":true,"analytics":true,"custom_branding":false,"api_access":true,"support_level":"priority","sla":true}`))
	if got == nil {
		t.Fatal("expected parsed billing features")
	}
	if !got.Recording || !got.Analytics || !got.ApiAccess || !got.Sla {
		t.Fatalf("unexpected boolean mapping: %+v", got)
	}
	if got.SupportLevel != "priority" {
		t.Fatalf("SupportLevel: got %q, want %q", got.SupportLevel, "priority")
	}

	got = scanBillingFeatures([]byte(`{"recording":`))
	if got != nil {
		t.Fatalf("expected nil for malformed json, got %+v", got)
	}
}

func TestScanOverageRates(t *testing.T) {
	got := scanOverageRates(nil)
	if got != nil {
		t.Fatalf("expected nil for empty input, got %+v", got)
	}

	got = scanOverageRates([]byte(`{
		"bandwidth":{"limit":1000,"unit_price":0.12,"unit":"GB"},
		"storage":{"limit":250,"unit_price":0.08,"unit":"GB"},
		"compute":{"limit":10,"unit_price":0.5,"unit":"hour"}
	}`))
	if got == nil {
		t.Fatal("expected parsed overage rates")
	}
	if got.Bandwidth == nil || got.Bandwidth.Limit == nil || *got.Bandwidth.Limit != 1000 {
		t.Fatalf("Bandwidth limit: got %+v", got.Bandwidth)
	}
	if got.Storage == nil || got.Storage.Unit != "GB" {
		t.Fatalf("Storage: got %+v", got.Storage)
	}
	if got.Compute == nil || got.Compute.UnitPrice != 0.5 {
		t.Fatalf("Compute: got %+v", got.Compute)
	}

	got = scanOverageRates([]byte(`{"bandwidth":`))
	if got != nil {
		t.Fatalf("expected nil for malformed json, got %+v", got)
	}
}

func TestScanCustomPricingAndBillingAddress(t *testing.T) {
	cp := scanCustomPricing(nil)
	if cp != nil {
		t.Fatalf("expected nil for empty custom pricing, got %+v", cp)
	}
	cp = scanCustomPricing([]byte(`{"base_price":49.9,"discount_rate":0.15}`))
	if cp == nil || cp.BasePrice != 49.9 || cp.DiscountRate != 0.15 {
		t.Fatalf("unexpected custom pricing mapping: %+v", cp)
	}
	cp = scanCustomPricing([]byte(`{"base_price":`))
	if cp != nil {
		t.Fatalf("expected nil for malformed custom pricing, got %+v", cp)
	}

	addr := scanBillingAddress(nil)
	if addr != nil {
		t.Fatalf("expected nil for empty billing address, got %+v", addr)
	}
	addr = scanBillingAddress([]byte(`{"street":"Main St","city":"Amsterdam","state":"NH","postal_code":"1000AA","country":"NL"}`))
	if addr == nil {
		t.Fatal("expected parsed billing address")
	}
	if addr.Street != "Main St" || addr.City != "Amsterdam" || addr.Country != "NL" {
		t.Fatalf("unexpected billing address mapping: %+v", addr)
	}
	addr = scanBillingAddress([]byte(`{"street":`))
	if addr != nil {
		t.Fatalf("expected nil for malformed billing address, got %+v", addr)
	}
}

func TestCreateStripePayment_ResponseDecodeAndValidation(t *testing.T) {
	t.Setenv("STRIPE_SECRET_KEY", "sk_test_123")
	t.Setenv("WEBAPP_PUBLIC_URL", "https://app.example.com")

	s := &PurserServer{}

	oldTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })

	t.Run("decode error", func(t *testing.T) {
		http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Host != "api.stripe.com" {
				t.Fatalf("unexpected host: %s", req.URL.Host)
			}
			return testHTTPResponse(http.StatusOK, `{"id":`), nil
		})

		_, _, err := s.createStripePayment("inv-1", "tenant-1", 10.0, "USD")
		if err == nil || !strings.Contains(err.Error(), "failed to decode stripe response") {
			t.Fatalf("expected decode error, got %v", err)
		}
	})

	t.Run("missing fields", func(t *testing.T) {
		http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return testHTTPResponse(http.StatusOK, `{"id":"pi_123"}`), nil
		})

		_, _, err := s.createStripePayment("inv-1", "tenant-1", 10.0, "USD")
		if err == nil || !strings.Contains(err.Error(), "invalid stripe response: missing payment intent ID or client secret") {
			t.Fatalf("expected missing fields error, got %v", err)
		}
	})

	t.Run("success", func(t *testing.T) {
		http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return testHTTPResponse(http.StatusOK, `{"id":"pi_123","client_secret":"sec_456"}`), nil
		})

		paymentURL, paymentID, err := s.createStripePayment("inv-1", "tenant-1", 10.0, "USD")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if paymentID != "pi_123" {
			t.Fatalf("paymentID: got %q, want %q", paymentID, "pi_123")
		}
		wantURL := "https://app.example.com/payment/stripe?client_secret=sec_456"
		if paymentURL != wantURL {
			t.Fatalf("paymentURL: got %q, want %q", paymentURL, wantURL)
		}
	})
}

func TestCreateMolliePayment_ResponseDecodeAndCheckoutURL(t *testing.T) {
	t.Setenv("MOLLIE_API_KEY", "mollie_test_123")
	t.Setenv("WEBAPP_PUBLIC_URL", "https://app.example.com")
	t.Setenv("API_PUBLIC_URL", "https://api.example.com")

	s := &PurserServer{}

	oldTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })

	t.Run("decode error", func(t *testing.T) {
		http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Host != "api.mollie.com" {
				t.Fatalf("unexpected host: %s", req.URL.Host)
			}
			return testHTTPResponse(http.StatusCreated, `{"id":`), nil
		})

		_, _, err := s.createMolliePayment("inv-1", "tenant-1", 10.0, "EUR")
		if err == nil || !strings.Contains(err.Error(), "failed to decode mollie response") {
			t.Fatalf("expected decode error, got %v", err)
		}
	})

	t.Run("success", func(t *testing.T) {
		http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return testHTTPResponse(http.StatusCreated, `{
				"id":"tr_123",
				"_links":{"checkout":{"href":"https://checkout.mollie.com/pay/abc"}}
			}`), nil
		})

		checkoutURL, paymentID, err := s.createMolliePayment("inv-1", "tenant-1", 10.0, "EUR")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if paymentID != "tr_123" {
			t.Fatalf("paymentID: got %q, want %q", paymentID, "tr_123")
		}
		if checkoutURL != "https://checkout.mollie.com/pay/abc" {
			t.Fatalf("checkoutURL: got %q, want %q", checkoutURL, "https://checkout.mollie.com/pay/abc")
		}
	})
}

func TestMarshalRoundTripForStructBasedHelpers(t *testing.T) {
	limit := 15.0
	ad := &pb.AllocationDetails{Limit: &limit, UnitPrice: 0.2, Unit: "GB"}
	adJSON, err := marshalAllocationDetails(ad)
	if err != nil {
		t.Fatalf("marshalAllocationDetails: %v", err)
	}
	if got := scanAllocationDetails(adJSON); got == nil || got.Limit == nil || *got.Limit != limit || got.UnitPrice != 0.2 || got.Unit != "GB" {
		t.Fatalf("allocation roundtrip failed: %+v", got)
	}

	bf := &pb.BillingFeatures{
		Recording:      true,
		Analytics:      true,
		CustomBranding: true,
		ApiAccess:      true,
		SupportLevel:   "premium",
		Sla:            true,
	}
	bfJSON, err := marshalBillingFeatures(bf)
	if err != nil {
		t.Fatalf("marshalBillingFeatures: %v", err)
	}
	if got := scanBillingFeatures(bfJSON); got == nil || !got.Recording || !got.Analytics || !got.CustomBranding || !got.ApiAccess || got.SupportLevel != "premium" || !got.Sla {
		t.Fatalf("billing features roundtrip failed: %+v", got)
	}

	cp := &pb.CustomPricing{BasePrice: 50, DiscountRate: 0.1}
	cpJSON, err := marshalCustomPricing(cp)
	if err != nil {
		t.Fatalf("marshalCustomPricing: %v", err)
	}
	if got := scanCustomPricing(cpJSON); got == nil || got.BasePrice != 50 || got.DiscountRate != 0.1 {
		t.Fatalf("custom pricing roundtrip failed: %+v", got)
	}
}
