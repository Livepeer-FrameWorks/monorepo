package mollie

import (
	"fmt"
	"net/http"
	"testing"
)

func TestMollieCheckoutRefundNeverRepeatsAChargeback(t *testing.T) {
	for _, tc := range []struct{ back, refunded, want string }{{"4.00", "2.00", "4.00"}, {"10.00", "0.00", ""}} {
		t.Run(tc.back, func(t *testing.T) {
			got := ""
			c := newMollieContractClient(t, func(req *http.Request) (*http.Response, error) {
				if req.Method == http.MethodGet {
					return mollieJSONResponse(200, fmt.Sprintf(`{"id":"payment","status":"paid","amount":{"value":"10.00","currency":"EUR"},"amountRefunded":{"value":%q,"currency":"EUR"},"amountChargedBack":{"value":%q,"currency":"EUR"},"amountRemaining":{"value":"20.00","currency":"EUR"}}`, tc.refunded, tc.back)), nil
				}
				body := decodeMollieRequest(t, req)
				got = body["amount"].(map[string]any)["value"].(string)
				return mollieJSONResponse(201, `{"id":"refund","status":"queued"}`), nil
			})
			if err := c.CancelReservedCheckout(t.Context(), "customer", "reservation", "payment", ""); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("refund=%s want %s after chargeback=%s refunded=%s", got, tc.want, tc.back, tc.refunded)
			}
		})
	}
}
