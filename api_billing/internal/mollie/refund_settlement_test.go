package mollie

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
)

func TestRefundSettlementWaitsForBankAndIncludesChargeback(t *testing.T) {
	for _, state := range []string{"queued", "failed", "refunded", "charged_back", "chargeback_reversed"} {
		t.Run(state, func(t *testing.T) {
			c := newMollieContractClient(t, func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodGet {
					t.Fatalf("settlement inspection moved money: %s", req.Method)
				}
				switch req.URL.Path {
				case "/v2/payments/payment/refunds":
					if state == "charged_back" || state == "chargeback_reversed" {
						return mollieJSONResponse(200, `{"_embedded":{"refunds":[]}}`), nil
					}
					return mollieJSONResponse(200, fmt.Sprintf(`{"_embedded":{"refunds":[{"id":"refund","status":%q,"amount":{"currency":"EUR","value":"20.00"}}]}}`, state)), nil
				case "/v2/payments/payment":
					back := "0.00"
					if state == "charged_back" || state == "chargeback_reversed" {
						back = "20.00"
					}
					return mollieJSONResponse(200, fmt.Sprintf(`{"id":"payment","status":"paid","amountChargedBack":{"currency":"EUR","value":%q}}`, back)), nil
				case "/v2/payments/payment/chargebacks":
					reversed := "null"
					if state == "chargeback_reversed" {
						reversed = `"2026-10-30T00:00:00Z"`
					}
					return mollieJSONResponse(200, fmt.Sprintf(`{"_embedded":{"chargebacks":[{"id":"chargeback","amount":{"currency":"EUR","value":"20.00"},"reversedAt":%s}]}}`, reversed)), nil
				}
				return nil, fmt.Errorf("unexpected provider read: %s", req.URL.Path)
			})
			previous := http.DefaultTransport
			http.DefaultTransport = http.DefaultClient.Transport
			t.Cleanup(func() { http.DefaultTransport = previous })
			receipts, err := c.InspectRefundSettlement(t.Context(), billing.RefundSettlement{Provider: "mollie", PaymentID: "payment", ReferenceID: "purchase", AmountCents: 2000, Currency: "EUR"})
			switch state {
			case "queued":
				if !errors.Is(err, billing.ErrRefundSettling) || len(receipts) != 0 {
					t.Fatalf("pending bank return: receipts=%+v err=%v", receipts, err)
				}
			case "failed", "chargeback_reversed":
				if err == nil || errors.Is(err, billing.ErrRefundSettling) {
					t.Fatalf("missing bank return was silent: %+v %v", receipts, err)
				}
			default:
				if err != nil || len(receipts) != 1 || receipts[0].AmountCents != 2000 {
					t.Fatalf("settled return: %+v %v", receipts, err)
				}
			}
		})
	}
}
