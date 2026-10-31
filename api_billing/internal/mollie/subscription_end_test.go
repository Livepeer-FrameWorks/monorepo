package mollie

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
)

func TestMollieFallbackRetainsSettledTermAndPendingDebits(t *testing.T) {
	anchor := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, status, payment, refunded string
		fail, payOnCancel               bool
		wantPending                     bool
	}{
		{name: "failed", status: "suspended", payment: "failed"},
		{name: "settlement", status: "suspended", payment: "pending", wantPending: true},
		{name: "paid while canceling", status: "suspended", payment: "pending", payOnCancel: true},
		{name: "already cancelled paid", status: "canceled", payment: "paid"},
		{name: "fully refunded", status: "canceled", payment: "paid", refunded: "20.00"},
		{name: "failed cancellation", status: "suspended", payment: "failed", fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, payment := tc.status, tc.payment
			cancels := 0
			c := newMollieContractClient(t, func(req *http.Request) (*http.Response, error) {
				switch req.URL.Path {
				case "/v2/customers/customer/subscriptions/sub":
					if req.Method == http.MethodDelete {
						cancels++
						if tc.fail {
							return nil, errors.New("cancel failed")
						}
						state = "canceled"
						if tc.payOnCancel {
							payment = "paid"
						}
					}
					return mollieJSONResponse(200, fmt.Sprintf(`{"id":"sub","status":%q,"startDate":"2026-01-31"}`, state)), nil
				case "/v2/customers/customer/payments":
					if req.Method != http.MethodGet {
						return nil, errors.New("fallback attempted a refund")
					}
					refund := "0.00"
					if tc.refunded != "" {
						refund = tc.refunded
					}
					// The retry was created after the capped February date. It still buys
					// February 28 through March 31, not a new month through April 5.
					body, _ := json.Marshal(map[string]any{"_embedded": map[string]any{"payments": []any{
						map[string]any{"id": "payment", "subscriptionId": "sub", "status": payment, "createdAt": "2026-03-05T12:00:00Z", "amount": map[string]string{"value": "20.00", "currency": "EUR"}, "amountRefunded": map[string]string{"value": refund, "currency": "EUR"}},
						map[string]any{"id": "other", "subscriptionId": "other", "status": "pending"},
					}}})
					return mollieJSONResponse(200, string(body)), nil
				}
				return nil, fmt.Errorf("unexpected provider operation: %s %s", req.Method, req.URL.Path)
			})
			previous := http.DefaultTransport
			http.DefaultTransport = http.DefaultClient.Transport
			t.Cleanup(func() { http.DefaultTransport = previous })
			got, err := c.EndSubscriptionForFallback(t.Context(), billing.SubscriptionEndRequest{CustomerID: "customer", SubscriptionID: "sub", PeriodEnd: anchor})
			wantCancels := 1
			if tc.status == "canceled" {
				wantCancels = 0
			}
			if (err != nil) != tc.fail || cancels != wantCancels {
				t.Fatalf("outcome=%+v cancels=%d err=%v", got, cancels, err)
			}
			if tc.fail {
				return
			}
			if got.Pending != tc.wantPending || got.Recovered {
				t.Fatalf("outcome=%+v", got)
			}
			wantPaid := (tc.payment == "paid" || tc.payOnCancel) && tc.refunded == ""
			end := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
			if wantPaid && !got.PaidUntil.Equal(end) || !wantPaid && !got.PaidUntil.IsZero() {
				t.Fatalf("paid term=%s want retained=%v through %s", got.PaidUntil, wantPaid, end)
			}
		})
	}
}

func TestMollieFallbackRecoversOnlyValidRenewalAuthority(t *testing.T) {
	now := time.Now().UTC()
	created := now.Add(-time.Hour)
	for _, valid := range []bool{false, true} {
		t.Run(fmt.Sprint(valid), func(t *testing.T) {
			canceled := false
			c := newMollieContractClient(t, func(req *http.Request) (*http.Response, error) {
				switch req.URL.Path {
				case "/v2/customers/customer/subscriptions/sub":
					if req.Method == http.MethodDelete {
						canceled = true
					}
					state := "active"
					if canceled {
						state = "canceled"
					}
					return mollieJSONResponse(200, fmt.Sprintf(`{"id":"sub","status":%q,"mandateId":"mandate","startDate":%q}`, state, created.Format(time.DateOnly))), nil
				case "/v2/customers/customer/payments":
					return mollieJSONResponse(200, fmt.Sprintf(`{"_embedded":{"payments":[{"id":"payment","subscriptionId":"sub","status":"paid","createdAt":%q,"amount":{"value":"20.00","currency":"EUR"}}]}}`, created.Format(time.RFC3339))), nil
				case "/v2/customers/customer/mandates/mandate":
					state := "revoked"
					if valid {
						state = "valid"
					}
					return mollieJSONResponse(200, fmt.Sprintf(`{"id":"mandate","status":%q}`, state)), nil
				}
				return nil, fmt.Errorf("unexpected %s %s", req.Method, req.URL.Path)
			})
			previous := http.DefaultTransport
			http.DefaultTransport = http.DefaultClient.Transport
			t.Cleanup(func() { http.DefaultTransport = previous })
			got, err := c.EndSubscriptionForFallback(t.Context(), billing.SubscriptionEndRequest{AllowRecovery: true, CustomerID: "customer", SubscriptionID: "sub", PeriodEnd: now})
			if err != nil || got.Recovered != valid || canceled == valid || !got.PaidUntil.After(now) {
				t.Fatalf("outcome=%+v canceled=%v err=%v", got, canceled, err)
			}
		})
	}
}

func TestMollieFallbackKeepsOnlyItsExactInitialPaidMonth(t *testing.T) {
	end := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, 20)
	created := end.AddDate(0, -1, 0)
	for _, matched := range []bool{false, true} {
		t.Run(fmt.Sprint(matched), func(t *testing.T) {
			c := newMollieContractClient(t, func(req *http.Request) (*http.Response, error) {
				switch req.URL.Path {
				case "/v2/customers/customer/subscriptions/sub":
					return mollieJSONResponse(200, fmt.Sprintf(`{"id":"sub","status":"canceled","startDate":%q}`, end.Format(time.DateOnly))), nil
				case "/v2/customers/customer/payments":
					return mollieJSONResponse(200, fmt.Sprintf(`{"_embedded":{"payments":[{"id":"initial","status":"paid","createdAt":%q,"amount":{"value":"20.00","currency":"EUR"}}]}}`, created.Format(time.RFC3339))), nil
				}
				return nil, fmt.Errorf("unexpected %s %s", req.Method, req.URL.Path)
			})
			previous := http.DefaultTransport
			http.DefaultTransport = http.DefaultClient.Transport
			t.Cleanup(func() { http.DefaultTransport = previous })
			id := "unrelated"
			if matched {
				id = "initial"
			}
			result, err := c.EndSubscriptionForFallback(t.Context(), billing.SubscriptionEndRequest{CustomerID: "customer", SubscriptionID: "sub", InitialPaymentID: id, PeriodEnd: end.AddDate(0, 4, 0)})
			if err != nil {
				t.Fatal(err)
			}
			if matched && !result.PaidUntil.Equal(end) || !matched && !result.PaidUntil.IsZero() {
				t.Fatalf("initial paid term=%s exact receipt=%v want=%s", result.PaidUntil, matched, end)
			}
		})
	}
}
