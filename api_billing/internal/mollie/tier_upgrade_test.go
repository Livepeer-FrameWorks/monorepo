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

func TestTierUpgradeMollieCreditsOnlyPaidFeeAndLocatesLostPatch(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	start := now.Add(-15 * 24 * time.Hour)
	end := start.Add(30 * 24 * time.Hour)
	metadata := map[string]any{"tenant_id": "tenant", "checkout_reservation_id": "original"}
	patches := 0
	c := newMollieContractClient(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/v2/customers/customer/subscriptions/sub" {
			if req.Method == http.MethodPatch {
				patches++
				body := decodeMollieRequest(t, req)
				amount := body["amount"].(map[string]any)
				if amount["value"] != "50.00" || body["startDate"] != billing.NextMonthlyAnniversary(now).AddDate(0, 0, 1).Format(time.DateOnly) {
					t.Fatalf("wrong future monthly fee: %v", body)
				}
				metadata = body["metadata"].(map[string]any)
				return nil, errors.New("PATCH committed but response was lost")
			}
			value, next := "20.00", end.Format(time.DateOnly)
			if id, ok := metadata["billing_tier_upgrade_id"]; ok && id != "" {
				value, next = "50.00", billing.NextMonthlyAnniversary(now).AddDate(0, 0, 1).Format(time.DateOnly)
			}
			body, _ := json.Marshal(map[string]any{"id": "sub", "status": "active", "metadata": metadata, "amount": map[string]string{"value": value, "currency": "EUR"}, "nextPaymentDate": next})
			return mollieJSONResponse(200, string(body)), nil
		}
		if req.URL.Path == "/v2/customers/customer/payments" {
			return mollieJSONResponse(200, fmt.Sprintf(`{"_embedded":{"payments":[{"id":"paid","subscriptionId":"sub","customerId":"customer","status":"paid","createdAt":%q,"amount":{"value":"20.00","currency":"EUR"},"amountRefunded":{"value":"4.00","currency":"EUR"}}]},"_links":{}}`, start.Format(time.RFC3339))), nil
		}
		return nil, fmt.Errorf("unexpected provider operation: %s %s", req.Method, req.URL.Path)
	})
	request := billing.TierUpgradeRequest{ID: "upgrade", TenantID: "tenant", OldTierID: "old", TierID: "new", CustomerID: "customer", SubscriptionID: "sub", RequestedAt: now, OldPeriodStart: start, OldPeriodEnd: end, BaseCents: 5000}
	if _, err := c.UpgradeMonthlySubscription(t.Context(), request); err == nil {
		t.Fatal("lost PATCH response did not fail")
	}
	receipt, err := c.UpgradeMonthlySubscription(t.Context(), request)
	if err != nil || !receipt.Applied || receipt.UnusedPaidCents != 800 || patches != 1 || metadata["checkout_reservation_id"] != "original" {
		t.Fatalf("receipt=%+v patches=%d err=%v", receipt, patches, err)
	}
}

func TestTierUpgradeMollieWaitsForSettlement(t *testing.T) {
	for _, offset := range []time.Duration{-time.Hour, 0, time.Hour} {
		t.Run(offset.String(), func(t *testing.T) {
			now := time.Now().UTC()
			boundary := now.Add(-2 * time.Hour)
			mutations := 0
			c := newMollieContractClient(t, func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodGet {
					mutations++
					return nil, errors.New("unexpected mutation")
				}
				if req.URL.Path == "/v2/customers/customer/subscriptions/sub" {
					return mollieJSONResponse(200, `{"id":"sub","status":"active","metadata":{"tenant_id":"tenant"},"amount":{"value":"20.00","currency":"EUR"}}`), nil
				}
				return mollieJSONResponse(200, fmt.Sprintf(`{"_embedded":{"payments":[{"id":"waiting","subscriptionId":"sub","status":"pending","createdAt":%q}]},"_links":{}}`, boundary.Add(offset).Format(time.RFC3339Nano))), nil
			})
			_, err := c.UpgradeMonthlySubscription(t.Context(), billing.TierUpgradeRequest{ID: "upgrade", TenantID: "tenant", CustomerID: "customer", SubscriptionID: "sub", RequestedAt: now, OldPeriodStart: boundary.AddDate(0, -1, 0), OldPeriodEnd: boundary, BaseCents: 5000})
			if !errors.Is(err, billing.ErrTierUpgradeUnsettled) || mutations != 0 {
				t.Fatalf("unsettled fee changed pricing: mutations=%d err=%v", mutations, err)
			}
		})
	}
}

func TestTierUpgradeMollieRefusesPaidRenewalBeforeLocalRollover(t *testing.T) {
	now := time.Now().UTC()
	end := now.Add(-10 * time.Hour)
	mutations := 0
	c := newMollieContractClient(t, func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodPatch {
			mutations++
			body := decodeMollieRequest(t, req)
			raw, _ := json.Marshal(map[string]any{"id": "sub", "amount": body["amount"], "nextPaymentDate": body["startDate"]})
			return mollieJSONResponse(200, string(raw)), nil
		}
		if req.URL.Path == "/v2/customers/customer/subscriptions/sub" {
			return mollieJSONResponse(200, `{"id":"sub","status":"active","metadata":{"tenant_id":"tenant"},"amount":{"value":"20.00","currency":"EUR"}}`), nil
		}
		return mollieJSONResponse(200, fmt.Sprintf(`{"_embedded":{"payments":[{"id":"renewal","subscriptionId":"sub","status":"paid","createdAt":%q,"amount":{"value":"20.00","currency":"EUR"}}]},"_links":{}}`, end.Format(time.RFC3339))), nil
	})
	_, err := c.UpgradeMonthlySubscription(t.Context(), billing.TierUpgradeRequest{ID: "upgrade", TenantID: "tenant", CustomerID: "customer", SubscriptionID: "sub", RequestedAt: now, OldPeriodStart: end.AddDate(0, -1, 0), OldPeriodEnd: end, BaseCents: 5000})
	if !errors.Is(err, billing.ErrTierUpgradeRejected) || mutations != 0 {
		t.Fatalf("paid renewal lost at stale local boundary: mutations=%d err=%v", mutations, err)
	}
}

func TestTierUpgradeMollieDoesNotCreditChargedBackFee(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	start := now.Add(-15 * 24 * time.Hour)
	end := now.Add(15 * 24 * time.Hour)
	c := newMollieContractClient(t, func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodPatch {
			body := decodeMollieRequest(t, req)
			raw, _ := json.Marshal(map[string]any{"id": "sub", "amount": body["amount"], "metadata": body["metadata"], "nextPaymentDate": body["startDate"]})
			return mollieJSONResponse(200, string(raw)), nil
		}
		if req.URL.Path == "/v2/customers/customer/subscriptions/sub" {
			return mollieJSONResponse(200, `{"id":"sub","status":"active","metadata":{"tenant_id":"tenant"},"amount":{"value":"20.00","currency":"EUR"}}`), nil
		}
		return mollieJSONResponse(200, fmt.Sprintf(`{"_embedded":{"payments":[{"id":"old","status":"paid","subscriptionId":"sub","createdAt":%q,"amount":{"value":"20.00","currency":"EUR"},"amountRefunded":{"value":"4.00","currency":"EUR"},"amountChargedBack":{"value":"6.00","currency":"EUR"}}]},"_links":{}}`, start.Format(time.RFC3339))), nil
	})
	receipt, err := c.UpgradeMonthlySubscription(t.Context(), billing.TierUpgradeRequest{ID: "upgrade", TenantID: "tenant", CustomerID: "customer", SubscriptionID: "sub", RequestedAt: now, OldPeriodStart: start, OldPeriodEnd: end, BaseCents: 5000})
	if err != nil || receipt.UnusedPaidCents != 500 {
		t.Fatalf("credited money not retained: receipt=%+v err=%v", receipt, err)
	}
}

func TestTierUpgradeMollieRefusesRenewalBeforeLocalMidnight(t *testing.T) {
	now := time.Date(2026, 10, 31, 23, 30, 0, 0, time.UTC)
	end := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	mutations := 0
	c := newMollieContractClient(t, func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodPatch {
			mutations++
			body := decodeMollieRequest(t, req)
			raw, _ := json.Marshal(map[string]any{"id": "sub", "amount": body["amount"], "metadata": body["metadata"], "nextPaymentDate": body["startDate"]})
			return mollieJSONResponse(200, string(raw)), nil
		}
		if req.URL.Path == "/v2/customers/customer/subscriptions/sub" {
			return mollieJSONResponse(200, `{"id":"sub","status":"active","nextPaymentDate":"2026-12-01","amount":{"value":"20.00","currency":"EUR"},"metadata":{"tenant_id":"tenant"}}`), nil
		}
		return mollieJSONResponse(200, `{"_embedded":{"payments":[{"id":"renewal","status":"paid","subscriptionId":"sub","createdAt":"2026-10-31T23:00:00Z","amount":{"value":"20.00","currency":"EUR"}}]},"_links":{}}`), nil
	})
	_, err := c.UpgradeMonthlySubscription(t.Context(), billing.TierUpgradeRequest{ID: "upgrade", TenantID: "tenant", CustomerID: "customer", SubscriptionID: "sub", RequestedAt: now, OldPeriodStart: end.AddDate(0, -1, 0), OldPeriodEnd: end, BaseCents: 5000})
	if !errors.Is(err, billing.ErrTierUpgradeRejected) || mutations != 0 {
		t.Fatalf("provider already renewed but stale local period lost its fee: mutations=%d err=%v", mutations, err)
	}
}

func TestMollieUpgradeCreditsFebruaryFeeAfterGrantSplitsMarchCycle(t *testing.T) {
	at := time.Date(2026, time.March, 17, 0, 0, 0, 0, time.UTC)
	c := newMollieContractClient(t, func(req *http.Request) (*http.Response, error) {
		switch req.Method + " " + req.URL.Path {
		case "GET /v2/customers/customer/subscriptions/sub":
			return mollieJSONResponse(200, `{"id":"sub","status":"active","metadata":{"tenant_id":"tenant"},"amount":{"value":"20.00","currency":"EUR"},"nextPaymentDate":"2026-03-31"}`), nil
		case "GET /v2/customers/customer/payments":
			return mollieJSONResponse(200, `{"_embedded":{"payments":[{"id":"february-fee","subscriptionId":"sub","status":"paid","createdAt":"2026-02-28T00:00:00Z","amount":{"value":"20.00","currency":"EUR"}}]}}`), nil
		case "PATCH /v2/customers/customer/subscriptions/sub":
			body := decodeMollieRequest(t, req)
			raw, err := json.Marshal(map[string]any{"id": "sub", "amount": body["amount"], "nextPaymentDate": body["startDate"]})
			if err != nil {
				return nil, err
			}
			return mollieJSONResponse(200, string(raw)), nil
		default:
			return nil, fmt.Errorf("unexpected provider operation: %s %s", req.Method, req.URL.Path)
		}
	})
	receipt, err := c.UpgradeMonthlySubscription(t.Context(), billing.TierUpgradeRequest{
		ID: "upgrade", TenantID: "tenant", OldTierID: "old", TierID: "new",
		CustomerID: "customer", SubscriptionID: "sub", BaseCents: 5000,
		RequestedAt: at, OldPeriodStart: at.AddDate(0, 0, -2),
		OldPeriodEnd: time.Date(2026, time.March, 31, 0, 0, 0, 0, time.UTC),
	})
	// The paid February 28–March 31 fee has fourteen of its thirty-one days left.
	if err != nil || receipt.UnusedPaidCents != 903 || receipt.PaidReceiptID != "february-fee" {
		t.Fatalf("lost paid February fee after a grant split: receipt=%+v err=%v; want EUR 9.03 credit", receipt, err)
	}
}
