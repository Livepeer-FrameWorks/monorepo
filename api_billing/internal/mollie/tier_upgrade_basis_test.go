package mollie

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
)

func TestTierUpgradeMollieCreditsThePaidContractNotTheCurrentListPrice(t *testing.T) {
	for _, first := range []bool{false, true} {
		t.Run(fmt.Sprintf("first_payment_%v", first), func(t *testing.T) {
			now := time.Now().UTC().Truncate(time.Second)
			originalStart := now.Add(-15 * 24 * time.Hour)
			end := originalStart.Add(30 * 24 * time.Hour)
			c := newMollieContractClient(t, func(req *http.Request) (*http.Response, error) {
				if req.Method == http.MethodPatch {
					body := decodeMollieRequest(t, req)
					result, _ := json.Marshal(map[string]any{"id": "sub", "status": "active", "amount": body["amount"], "nextPaymentDate": body["startDate"], "metadata": body["metadata"]})
					return mollieJSONResponse(200, string(result)), nil
				}
				if req.URL.Path == "/v2/customers/customer/subscriptions/sub" {
					return mollieJSONResponse(200, fmt.Sprintf(`{"id":"sub","status":"active","amount":{"value":"20.00","currency":"EUR"},"createdAt":%q,"startDate":%q,"metadata":{"tenant_id":"tenant"}}`, originalStart.Format(time.RFC3339), end.Format(time.DateOnly))), nil
				}
				paymentStart, sub, metadata := originalStart, "sub", `{}`
				if first {
					paymentStart = originalStart.Add(-time.Hour)
					sub = ""
					metadata = `{"tenant_id":"tenant","purpose":"mandate_setup","tier_id":"old"}`
				}
				return mollieJSONResponse(200, fmt.Sprintf(`{"_embedded":{"payments":[{"id":"old-fee","status":"paid","subscriptionId":%q,"createdAt":%q,"metadata":%s,"amount":{"value":"20.00","currency":"EUR"}}]},"_links":{}}`, sub, paymentStart.Format(time.RFC3339), metadata)), nil
			})
			receipt, err := c.UpgradeMonthlySubscription(t.Context(), billing.TierUpgradeRequest{ID: "upgrade", TenantID: "tenant", OldTierID: "old", TierID: "new", CustomerID: "customer", SubscriptionID: "sub", RequestedAt: now,
				OldPeriodStart: now.Add(-5 * 24 * time.Hour), OldPeriodEnd: end, BaseCents: 5000})
			if err != nil || receipt.UnusedPaidCents != 1000 || !receipt.PaidPeriodStart.Equal(originalStart) {
				t.Fatalf("paid original month was credited from the changed list price or split phase: receipt=%+v err=%v", receipt, err)
			}
		})
	}
}
