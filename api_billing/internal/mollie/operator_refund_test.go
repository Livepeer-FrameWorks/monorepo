package mollie

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
)

func TestCompletedInvoiceRefundUsesOriginalPayerAndDurableReference(t *testing.T) {
	for _, mode := range []string{"new pending", "settled receipt", "lost response", "different payer", "another refund"} {
		t.Run(mode, func(t *testing.T) {
			posts := 0
			receipt := `{"id":"refund","status":"refunded","paymentId":"payment","amount":{"currency":"EUR","value":"20.00"},"metadata":{"frameworks_operator_refund":"request"}}`
			c := newMollieContractClient(t, func(req *http.Request) (*http.Response, error) {
				if req.Method == http.MethodGet {
					switch req.URL.Path {
					case "/v2/payments/payment":
						payer := "customer"
						if mode == "different payer" {
							payer = "different"
						}
						return mollieJSONResponse(200, fmt.Sprintf(`{"id":"payment","customerId":%q,"status":"paid","amount":{"currency":"EUR","value":"20.00"},"metadata":{"tenant_id":"tenant","invoice_id":"invoice"}}`, payer)), nil
					case "/v2/payments/payment/refunds":
						if mode == "lost response" {
							return mollieJSONResponse(200, `{"_embedded":{"refunds":[`+receipt+`]}}`), nil
						}
						if mode == "another refund" {
							return mollieJSONResponse(200, `{"_embedded":{"refunds":[{"id":"other","status":"queued","amount":{"currency":"EUR","value":"20.00"}}]}}`), nil
						}
						return mollieJSONResponse(200, `{"_embedded":{"refunds":[]}}`), nil
					case "/v2/payments/payment/refunds/refund":
						return mollieJSONResponse(200, receipt), nil
					}
				}
				if req.Method == http.MethodPost && req.URL.Path == "/v2/payments/payment/refunds" {
					posts++
					var body struct {
						Amount   struct{ Currency, Value string }
						Metadata map[string]string
					}
					if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					if req.Header.Get("Idempotency-Key") != "operator-invoice-refund:request" || body.Amount.Currency != "EUR" || body.Amount.Value != "20.00" || body.Metadata["frameworks_operator_refund"] != "request" {
						t.Fatalf("refund lost amount, consent reference or idempotency: %+v", body)
					}
					return mollieJSONResponse(201, `{"id":"refund","status":"queued","paymentId":"payment","amount":{"currency":"EUR","value":"20.00"},"metadata":{"frameworks_operator_refund":"request"}}`), nil
				}
				return nil, fmt.Errorf("unexpected provider request: %s %s", req.Method, req.URL.Path)
			})
			in := billing.CompletedInvoiceRefund{TenantID: "tenant", InvoiceID: "invoice", RequestID: "request", PaymentID: "local-payment", ProviderPaymentID: "payment", ProviderCustomerID: "customer", AmountMinor: 2000, Currency: "EUR"}
			if mode == "settled receipt" {
				in.RefundID = "refund"
			}
			out, err := c.RefundCompletedInvoice(t.Context(), in)
			switch mode {
			case "different payer", "another refund":
				if err == nil || posts != 0 {
					t.Fatalf("unowned or already-returned payment moved cash: receipt=%+v posts=%d err=%v", out, posts, err)
				}
			case "new pending":
				if err != nil || out.Status != "submitted" || posts != 1 {
					t.Fatalf("pending return treated incorrectly: %+v posts=%d err=%v", out, posts, err)
				}
			default:
				if err != nil || out.Status != "succeeded" || posts != 0 {
					t.Fatalf("settled/replayed receipt issued another return: %+v posts=%d err=%v", out, posts, err)
				}
			}
		})
	}
}
