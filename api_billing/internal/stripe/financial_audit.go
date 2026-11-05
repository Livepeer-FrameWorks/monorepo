package stripe

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/stripe/stripe-go/v85"
)

type FinancialAuditPage struct {
	Records    [][]byte
	NextCursor string
}

type financialAuditResult struct {
	stripe.APIResource
	Data    []json.RawMessage `json:"data"`
	HasMore bool              `json:"has_more"`
}

// FinancialEvidencePage uses GET-only allowlisted resources and preserves raw
// provider facts. Balance/account snapshots describe retrieval time, while list
// endpoints filter creation time to the requested interval.
func (c *Client) FinancialEvidencePage(ctx context.Context, source, after string, from, through time.Time) (*FinancialAuditPage, error) {
	paths := map[string]string{"stripe_invoices": "/v1/invoices", "stripe_charges": "/v1/charges", "stripe_balance_transactions": "/v1/balance_transactions", "stripe_payouts": "/v1/payouts", "stripe_balance_snapshot": "/v1/balance", "stripe_account": "/v1/account"}
	path, ok := paths[source]
	if !ok {
		return nil, errors.New("unsupported Stripe financial evidence source")
	}
	params := &stripe.Params{Context: ctx}
	snapshot := source == "stripe_balance_snapshot" || source == "stripe_account"
	if snapshot && after != "" {
		return nil, errors.New("snapshot source has no cursor")
	}
	if !snapshot {
		params.AddExtra("limit", "100")
		params.AddExtra("created[gte]", strconv.FormatInt(from.Unix(), 10))
		params.AddExtra("created[lt]", strconv.FormatInt(through.Unix(), 10))
		if after != "" {
			params.AddExtra("starting_after", after)
		}
	}
	var result financialAuditResult
	if err := stripe.GetBackend(stripe.APIBackend).Call("GET", path, c.secretKey, params, &result); err != nil {
		return nil, err
	}
	page := &FinancialAuditPage{}
	if snapshot {
		if result.LastResponse == nil || len(result.LastResponse.RawJSON) == 0 {
			return nil, errors.New("stripe snapshot response missing raw evidence")
		}
		page.Records = append(page.Records, append([]byte(nil), result.LastResponse.RawJSON...))
		return page, nil
	}
	for _, raw := range result.Data {
		page.Records = append(page.Records, append([]byte(nil), raw...))
	}
	if result.HasMore {
		if len(result.Data) == 0 {
			return nil, errors.New("stripe reported more data without a pagination record")
		}
		var last struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(result.Data[len(result.Data)-1], &last); err != nil {
			return nil, err
		}
		if last.ID == "" || last.ID == after {
			return nil, errors.New("stripe financial evidence cursor did not advance")
		}
		page.NextCursor = last.ID
	}
	return page, nil
}
