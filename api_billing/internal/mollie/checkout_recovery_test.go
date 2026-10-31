package mollie

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestMollieReservationRecoversLostBinding(t *testing.T) {
	var paths []string
	c := newMollieContractClient(t, func(req *http.Request) (*http.Response, error) {
		paths = append(paths, req.Method+" "+req.URL.Path)
		switch req.URL.Path {
		case "/v2/customers/customer/payments":
			return mollieJSONResponse(200, `{"_embedded":{"payments":[]},"_links":{}}`), nil
		case "/v2/customers/customer/subscriptions":
			return mollieJSONResponse(200, `{"_embedded":{"subscriptions":[{"id":"other","metadata":{"checkout_reservation_id":"other"}},{"id":"reserved","metadata":{"checkout_reservation_id":"reservation"}}]},"_links":{}}`), nil
		case "/v2/customers/customer/subscriptions/reserved/payments":
			return mollieJSONResponse(200, `{"_embedded":{"payments":[]},"_links":{}}`), nil
		case "/v2/customers/customer/subscriptions/reserved":
			return mollieJSONResponse(200, `{"id":"reserved","status":"canceled"}`), nil
		default:
			return nil, fmt.Errorf("unexpected request %s", req.URL.Path)
		}
	})
	// The SDK client uses the default transport when its caller supplies no HTTP client.
	previous := http.DefaultTransport
	http.DefaultTransport = http.DefaultClient.Transport
	t.Cleanup(func() { http.DefaultTransport = previous })
	if err := c.CancelReservedCheckout(context.Background(), "customer", "reservation", "", ""); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 3 || !strings.Contains(paths[1], "/reserved") {
		t.Fatalf("did not recover exact subscription: %v", paths)
	}
}

func TestMollieExpiredReservationRefundsPaidFirstPayment(t *testing.T) {
	testMollieExpiredReservationRefundsPaidPayment(t, false)
}

func TestMollieExpiredReservationPreservesPaidSubscriptionPayments(t *testing.T) {
	testMollieExpiredReservationRefundsPaidPayment(t, true)
}

func testMollieExpiredReservationRefundsPaidPayment(t *testing.T, subscription bool) {
	refunds := 0
	c := newMollieContractClient(t, func(req *http.Request) (*http.Response, error) {

		if req.Method == http.MethodGet {
			if req.URL.Path == "/v2/customers/customer/subscriptions/subscription" {
				return mollieJSONResponse(200, `{"id":"subscription","status":"canceled"}`), nil
			}
			if strings.HasSuffix(req.URL.Path, "/payments") {
				return mollieJSONResponse(200, `{"_embedded":{"payments":[{"id":"payment"}]},"_links":{}}`), nil
			}
			refunded := "0.00"
			if refunds > 0 {
				refunded = "10.00"
			}
			return mollieJSONResponse(200, fmt.Sprintf(`{"id":"payment","status":"paid","amount":{"value":"10.00","currency":"EUR"},"amountRefunded":{"value":%q,"currency":"EUR"}}`, refunded)), nil
		}

		if req.Method == http.MethodPost && req.URL.Path == "/v2/payments/payment/refunds" {
			refunds++
			payload := decodeMollieRequest(t, req)
			amount := payload["amount"].(map[string]any)
			if amount["value"] != "10.00" || req.Header.Get("Idempotency-Key") != "checkout-expiry-refund:reservation:payment" {
				t.Fatalf("incorrect refund: %v", payload)
			}
			return mollieJSONResponse(201, `{"id":"refund"}`), nil
		}
		return nil, fmt.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
	})
	previous := http.DefaultTransport
	http.DefaultTransport = http.DefaultClient.Transport
	t.Cleanup(func() { http.DefaultTransport = previous })

	paymentID, subscriptionID := "payment", ""
	if subscription {
		paymentID, subscriptionID = "", "subscription"
	}
	for range 2 {
		if err := c.CancelReservedCheckout(context.Background(), "customer", "reservation", paymentID, subscriptionID); err != nil {
			t.Fatal(err)
		}
	}

	wantRefunds := 1
	if subscription {
		wantRefunds = 0
	}
	if refunds != wantRefunds {
		t.Fatalf("refund calls=%d, want %d", refunds, wantRefunds)
	}
}

func TestMollieRecoveryCancelsSubscriptionBeforeWaitingForPendingPayment(t *testing.T) {
	cancels := 0
	c := newMollieContractClient(t, func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/v2/customers/customer/payments":
			return mollieJSONResponse(200, `{"_embedded":{"payments":[{"id":"pending","metadata":{"checkout_reservation_id":"reservation"}}]},"_links":{}}`), nil
		case "/v2/customers/customer/subscriptions":
			return mollieJSONResponse(200, `{"_embedded":{"subscriptions":[{"id":"subscription","metadata":{"checkout_reservation_id":"reservation"}}]},"_links":{}}`), nil
		case "/v2/customers/customer/subscriptions/subscription":
			if req.Method == http.MethodDelete {
				cancels++
				return mollieJSONResponse(200, `{"id":"subscription","status":"canceled"}`), nil
			}
			return mollieJSONResponse(200, `{"id":"subscription","status":"active"}`), nil
		case "/v2/customers/customer/subscriptions/subscription/payments":
			return mollieJSONResponse(200, `{"_embedded":{"payments":[{"id":"pending"}]},"_links":{}}`), nil
		case "/v2/payments/pending":
			return mollieJSONResponse(200, `{"id":"pending","status":"pending","isCancelable":false}`), nil
		default:
			return nil, fmt.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
		}
	})
	previous := http.DefaultTransport
	http.DefaultTransport = http.DefaultClient.Transport
	t.Cleanup(func() { http.DefaultTransport = previous })
	if err := c.CancelReservedCheckout(context.Background(), "customer", "reservation", "", ""); err == nil {
		t.Fatal("pending non-cancelable payment was silently settled")
	}
	if cancels != 1 {
		t.Fatalf("payment recovery left recurring billing active: cancels=%d", cancels)
	}
}

func TestMollieRecoveryUsesRemainingRefundAmount(t *testing.T) {
	calls := 0
	c := newMollieContractClient(t, func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodGet {
			return mollieJSONResponse(200, `{"id":"payment","status":"paid","amount":{"value":"10.00","currency":"EUR"},"amountRefunded":{"value":"0.00","currency":"EUR"},"amountRemaining":{"value":"0.00","currency":"EUR"}}`), nil
		}
		calls++
		return nil, fmt.Errorf("fully refunded payment must not be refunded again")
	})
	previous := http.DefaultTransport
	http.DefaultTransport = http.DefaultClient.Transport
	t.Cleanup(func() { http.DefaultTransport = previous })
	if err := c.CancelReservedCheckout(t.Context(), "customer", "reservation", "payment", ""); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("refunded twice")
	}
}

func TestMollieRecoveryNeverRefundsMoreThanUnrefundedPayment(t *testing.T) {
	for _, tc := range []struct{ remaining, refunded, want string }{{"20.00", "0.00", "10.00"}, {"20.00", "4.00", "6.00"}, {"3.00", "4.00", "3.00"}} {
		t.Run(tc.remaining+"-"+tc.refunded, func(t *testing.T) {
			got := ""
			c := newMollieContractClient(t, func(req *http.Request) (*http.Response, error) {
				if req.Method == http.MethodGet {
					return mollieJSONResponse(200, fmt.Sprintf(`{"id":"payment","status":"paid","amount":{"value":"10.00","currency":"EUR"},"amountRefunded":{"value":%q,"currency":"EUR"},"amountRemaining":{"value":%q,"currency":"EUR"}}`, tc.refunded, tc.remaining)), nil
				}
				var body struct {
					Amount struct {
						Value string `json:"value"`
					} `json:"amount"`
				}
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				got = body.Amount.Value
				return mollieJSONResponse(201, `{"id":"refund"}`), nil
			})
			previous := http.DefaultTransport
			http.DefaultTransport = http.DefaultClient.Transport
			t.Cleanup(func() { http.DefaultTransport = previous })
			if err := c.CancelReservedCheckout(t.Context(), "customer", "reservation", "payment", ""); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("refund=%s, want capped %s", got, tc.want)
			}
		})
	}
}

func TestMollieUnboundRetryRespectsRetainedKeyWindow(t *testing.T) {
	for _, subscription := range []bool{false, true} {
		for _, retained := range []bool{false, true} {
			t.Run(fmt.Sprint(subscription, "/", retained), func(t *testing.T) {
				posts := 0
				c := newMollieContractClient(t, func(req *http.Request) (*http.Response, error) {
					if req.Method == http.MethodGet {
						return mollieJSONResponse(200, `{"_embedded":{"subscriptions":[],"payments":[]},"_links":{}}`), nil
					}
					posts++
					if req.Header.Get("Idempotency-Key") != "original-key" {
						t.Fatal("retry changed provider key")
					}
					return mollieJSONResponse(201, `{"id":"same-object","status":"open"}`), nil
				})
				previous := http.DefaultTransport
				http.DefaultTransport = http.DefaultClient.Transport
				t.Cleanup(func() { http.DefaultTransport = previous })
				bound := time.Now().Add(-time.Minute)
				if retained {
					bound = time.Now().Add(time.Minute)
				}
				var err error
				if subscription {
					_, err = c.CreateSubscription(t.Context(), SubscriptionParams{CustomerID: "customer", ReservationID: "reservation", IdempotencyKey: "original-key", ReplayBefore: bound, ResumeOnly: true})
				} else {
					_, err = c.CreateFirstPayment(t.Context(), FirstPaymentParams{CustomerID: "customer", ReservationID: "reservation", IdempotencyKey: "original-key", ReplayBefore: bound, ResumeOnly: true})
				}
				if retained && (err != nil || posts != 1) {
					t.Fatalf("retained retry posts=%d err=%v", posts, err)
				}
				if !retained && (err == nil || posts != 0) {
					t.Fatalf("expired key was reused: posts=%d err=%v", posts, err)
				}
			})
		}
	}
}

func TestSubscriptionRecoveryPreservesRecurringServicePayments(t *testing.T) {
	refunds := 0
	c := newMollieContractClient(t, func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/v2/customers/customer/subscriptions/old":
			return mollieJSONResponse(200, `{"id":"old","status":"canceled"}`), nil
		case "/v2/customers/customer/subscriptions/old/payments":
			return mollieJSONResponse(200, `{"_embedded":{"payments":[{"id":"renewal"}]},"_links":{}}`), nil
		case "/v2/payments/renewal":
			return mollieJSONResponse(200, `{"id":"renewal","status":"paid","sequenceType":"recurring","subscriptionId":"old","amount":{"value":"20.00","currency":"EUR"}}`), nil
		case "/v2/payments/renewal/refunds":
			refunds++
			return mollieJSONResponse(201, `{"id":"refund"}`), nil
		}
		return nil, fmt.Errorf("unexpected %s", req.URL.Path)
	})
	previous := http.DefaultTransport
	http.DefaultTransport = http.DefaultClient.Transport
	t.Cleanup(func() { http.DefaultTransport = previous })
	if _, err := c.RecoverReservedCheckout(t.Context(), "customer", "reservation", "", "old"); err != nil {
		t.Fatal(err)
	}
	if refunds != 0 {
		t.Fatalf("refunded %d payments for delivered recurring service", refunds)
	}
}
