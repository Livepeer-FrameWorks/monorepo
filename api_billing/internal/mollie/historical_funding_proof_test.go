package mollie

import (
	"fmt"
	"net/http"
	"testing"
)

func TestHistoricalMollieProvenFundingSurvivesOtherUnprovenSubscription(t *testing.T) {
	for _, order := range []string{
		`{"id":"unproven","mandateId":"mandate","createdAt":"2026-01-02T00:00:00Z"},{"id":"delivered","mandateId":"mandate","createdAt":"2026-01-03T00:00:00Z"}`,
		`{"id":"delivered","mandateId":"mandate","createdAt":"2026-01-03T00:00:00Z"},{"id":"unproven","mandateId":"mandate","createdAt":"2026-01-02T00:00:00Z"}`,
	} {
		t.Run(order[:20], func(t *testing.T) {
			cancels := 0
			client := newMollieContractClient(t, func(r *http.Request) (*http.Response, error) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/v2/payments/initial":
					return mollieJSONResponse(200, `{"id":"initial","customerId":"customer","mandateId":"mandate","status":"paid","sequenceType":"first","createdAt":"2026-01-01T00:00:00Z","amount":{"value":"20.00","currency":"EUR"}}`), nil
				case r.Method == http.MethodGet && r.URL.Path == "/v2/customers/customer/subscriptions":
					return mollieJSONResponse(200, `{"_embedded":{"subscriptions":[`+order+`]},"_links":{}}`), nil
				case r.Method == http.MethodGet && r.URL.Path == "/v2/customers/customer/subscriptions/unproven":
					return mollieJSONResponse(200, `{"id":"unproven","status":"active"}`), nil
				case r.Method == http.MethodDelete && r.URL.Path == "/v2/customers/customer/subscriptions/unproven":
					cancels++
					return mollieJSONResponse(200, `{"id":"unproven","status":"canceled"}`), nil
				default:
					return nil, fmt.Errorf("unexpected provider operation: %s %s", r.Method, r.URL.Path)
				}
			})
			funded, orphans, err := client.InspectFirstPaymentFunding(t.Context(), "customer", "initial", "delivered", func(id string) (bool, error) { return id == "delivered", nil })
			if err != nil || !funded {
				t.Fatalf("proven delivered service was blocked by unrelated history: funded=%v err=%v", funded, err)
			}
			if cancels != 0 || len(orphans) != 1 || orphans[0] != "unproven" {
				t.Fatalf("read-only orphan inspection: cancellations=%d orphans=%v", cancels, orphans)
			}
		})
	}
}
