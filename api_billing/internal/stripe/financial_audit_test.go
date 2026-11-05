package stripe

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	api "github.com/stripe/stripe-go/v85"
)

type financialAuditBackend struct {
	fakeBackend
	call func(string, string, string, api.ParamsContainer, api.LastResponseSetter) error
}

func TestFinancialEvidenceUnavailableClientReturnsError(t *testing.T) {
	var client *Client
	if _, err := client.FinancialEvidencePage(context.Background(), "stripe_account", "", time.Unix(0, 0), time.Now()); err == nil {
		t.Fatal("unconfigured Stripe reader must return an error without calling the provider")
	}
}

func (b *financialAuditBackend) Call(method, path, key string, p api.ParamsContainer, out api.LastResponseSetter) error {
	b.calls++
	return b.call(method, path, key, p, out)
}

func TestFinancialEvidencePageUsesReadOnlyBoundedProviderPagination(t *testing.T) {
	from, through := time.Unix(1000, 0), time.Unix(2000, 0)
	b := &financialAuditBackend{call: func(method, path, key string, p api.ParamsContainer, out api.LastResponseSetter) error {
		if method != "GET" || path != "/v1/balance_transactions" || key != "sk_test_finance" {
			t.Fatalf("unexpected provider operation: %s %s", method, path)
		}
		extra := p.GetParams().Extra
		if extra == nil || extra.Get("limit") != "100" || extra.Get("created[gte]") != "1000" || extra.Get("created[lt]") != "2000" || extra.Get("starting_after") != "txn_previous" {
			t.Fatalf("pagination params=%+v", extra)
		}
		result := out.(*financialAuditResult)
		result.Data = []json.RawMessage{json.RawMessage(`{"id":"txn_next","amount":12100,"fee":300,"net":11800,"currency":"eur"}`)}
		result.HasMore = true
		return nil
	}}
	installBackend(t, b)
	page, err := (&Client{secretKey: "sk_test_finance"}).FinancialEvidencePage(context.Background(), "stripe_balance_transactions", "txn_previous", from, through)
	if err != nil || page.NextCursor != "txn_next" || len(page.Records) != 1 {
		t.Fatalf("page=%+v error=%v", page, err)
	}
}

func TestFinancialEvidenceSnapshotPreservesProviderFacts(t *testing.T) {
	raw := []byte(`{"object":"balance","available":[{"amount":11800,"currency":"eur"}],"pending":[]}`)
	b := &financialAuditBackend{call: func(method, path, _ string, p api.ParamsContainer, out api.LastResponseSetter) error {
		if method != "GET" || path != "/v1/balance" || p.GetParams().Extra != nil {
			t.Fatal("balance snapshot was filtered as historical activity")
		}
		out.SetLastResponse(&api.APIResponse{RawJSON: raw})
		return nil
	}}
	installBackend(t, b)
	page, err := (&Client{}).FinancialEvidencePage(context.Background(), "stripe_balance_snapshot", "", time.Unix(0, 0), time.Now())
	if err != nil || len(page.Records) != 1 || string(page.Records[0]) != string(raw) {
		t.Fatalf("page=%+v error=%v", page, err)
	}
}

func TestFinancialEvidenceRefusesFalseCompleteness(t *testing.T) {
	for _, test := range []struct {
		name   string
		source string
		call   func(string, string, string, api.ParamsContainer, api.LastResponseSetter) error
	}{
		{"missing cursor record", "stripe_invoices", func(_, _, _ string, _ api.ParamsContainer, out api.LastResponseSetter) error {
			out.(*financialAuditResult).HasMore = true
			return nil
		}},
		{"provider error", "stripe_invoices", func(_, _, _ string, _ api.ParamsContainer, _ api.LastResponseSetter) error {
			return errors.New("provider timeout")
		}},
		{"missing snapshot", "stripe_balance_snapshot", func(_, _, _ string, _ api.ParamsContainer, _ api.LastResponseSetter) error { return nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			installBackend(t, &financialAuditBackend{call: test.call})
			if _, err := (&Client{}).FinancialEvidencePage(context.Background(), test.source, "", time.Unix(0, 0), time.Now()); err == nil {
				t.Fatal("incomplete evidence accepted")
			}
		})
	}
	b := &financialAuditBackend{call: func(_, _, _ string, _ api.ParamsContainer, _ api.LastResponseSetter) error {
		t.Fatal("unknown source reached provider")
		return nil
	}}
	installBackend(t, b)
	if _, err := (&Client{}).FinancialEvidencePage(context.Background(), "/v1/payouts/create", "", time.Unix(0, 0), time.Now()); err == nil {
		t.Fatal("unapproved provider source accepted")
	}
}
