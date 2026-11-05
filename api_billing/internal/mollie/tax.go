package mollie

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	mollieapi "github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/shopspring/decimal"
)

type TaxTerms struct {
	ProfileSHA string `json:"profile_sha"`
	NetMinor   int64  `json:"net_minor"`
	RateBPS    int    `json:"rate_bps"`
	GrossMinor int64  `json:"gross_minor"`
}

func (t *TaxTerms) validateAmount(amount *mollieapi.Amount) error {
	if t == nil {
		return nil
	}
	calculated, err := NewTaxTerms(t.ProfileSHA, t.NetMinor, t.RateBPS)
	if err != nil {
		return err
	}
	if amount == nil || amount.Currency != "EUR" || amount.Value != decimal.New(calculated.GrossMinor, -2).StringFixed(2) || calculated.GrossMinor != t.GrossMinor {
		return errors.New("mollie payment does not match frozen exclusive-tax terms")
	}
	return nil
}

func NewTaxTerms(profile string, net int64, rate int) (*TaxTerms, error) {
	if len(profile) != 64 || net <= 0 || rate < 0 || rate > 10000 {
		return nil, errors.New("complete frozen Mollie tax terms required")
	}
	vat := decimal.NewFromInt(net).Mul(decimal.NewFromInt(int64(rate))).Div(decimal.NewFromInt(10000)).Round(0).IntPart()
	return &TaxTerms{ProfileSHA: profile, NetMinor: net, RateBPS: rate, GrossMinor: net + vat}, nil
}
func (t *TaxTerms) Metadata(metadata map[string]any) {
	if t == nil {
		return
	}
	metadata["frameworks_tax_profile"] = t.ProfileSHA
	metadata["frameworks_net_minor"] = strconv.FormatInt(t.NetMinor, 10)
	metadata["frameworks_vat_bps"] = strconv.Itoa(t.RateBPS)
}

func taxMetadata(meta any) map[string]any {
	encoded, err := json.Marshal(meta)
	if err != nil {
		return map[string]any{}
	}
	result := map[string]any{}
	if err = json.Unmarshal(encoded, &result); err != nil {
		return map[string]any{}
	}
	if result == nil {
		return map[string]any{}
	}
	return result
}

func assessedRenewalAmount(meta map[string]any, net int64) (decimal.Decimal, error) {
	sha, has := meta["frameworks_tax_profile"].(string)
	if !has || sha == "" {
		return decimal.New(net, -2), nil
	}
	rate, err := strconv.Atoi(fmt.Sprint(meta["frameworks_vat_bps"]))
	if err != nil {
		return decimal.Zero, err
	}
	terms, err := NewTaxTerms(sha, net, rate)
	if err != nil {
		return decimal.Zero, err
	}
	terms.Metadata(meta)
	return decimal.New(terms.GrossMinor, -2), nil
}

// SyncSubscriptionTax updates only later collections; already created payments
// keep the amount and assessment copied into their own metadata by Mollie.
func (c *Client) SyncSubscriptionTax(ctx context.Context, tenant, customer, id string, terms TaxTerms) error {
	sub, err := c.GetSubscription(ctx, customer, id)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(sub.Metadata)
	if err != nil {
		return err
	}
	metadata := map[string]any{}
	if string(raw) != "null" {
		if err = json.Unmarshal(raw, &metadata); err != nil {
			return err
		}
	}
	if metadata["tenant_id"] != tenant {
		return errors.New("mollie subscription tax owner mismatch")
	}
	if sub.Status == "canceled" || sub.Status == "completed" {
		return nil
	}
	if sub.Amount == nil || sub.Amount.Currency != "EUR" {
		return errors.New("native Mollie tax synchronization requires EUR")
	}
	restoreChanged := false
	if saved, ok := metadata[restoreAmountKey].(string); ok && metadata[restoreTierKey] != nil {
		var net int64
		if retained, hasNet := metadata["billing_restore_net_minor"].(string); hasNet {
			net, err = strconv.ParseInt(retained, 10, 64)
		} else {
			var amount decimal.Decimal
			amount, err = decimal.NewFromString(saved)
			net = amount.Shift(2).IntPart()
		}
		if err != nil {
			return fmt.Errorf("invalid retained Mollie restore amount: %w", err)
		}
		restore, restoreErr := NewTaxTerms(terms.ProfileSHA, net, terms.RateBPS)
		if restoreErr != nil {
			return restoreErr
		}
		restoreAmount := decimal.New(restore.GrossMinor, -2).StringFixed(2)
		restoreNet := strconv.FormatInt(net, 10)
		restoreChanged = saved != restoreAmount || metadata["billing_restore_net_minor"] != restoreNet
		metadata[restoreAmountKey], metadata["billing_restore_net_minor"] = restoreAmount, restoreNet
	}
	wanted := decimal.New(terms.GrossMinor, -2).StringFixed(2)
	if !restoreChanged && sub.Amount.Value == wanted && metadata["frameworks_tax_profile"] == terms.ProfileSHA && metadata["frameworks_net_minor"] == strconv.FormatInt(terms.NetMinor, 10) {
		return nil
	}
	terms.Metadata(metadata)
	body, err := json.Marshal(map[string]any{"amount": map[string]string{"value": wanted, "currency": "EUR"}, "metadata": metadata})
	if err != nil {
		return err
	}
	u, err := c.mollieURL("v2/customers/" + url.PathEscape(customer) + "/subscriptions/" + url.PathEscape(id))
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, u.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	result, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &APIError{StatusCode: response.StatusCode, Message: string(result)}
	}
	var updated struct {
		Amount struct {
			Value    string `json:"value"`
			Currency string `json:"currency"`
		} `json:"amount"`
	}
	if err = json.Unmarshal(result, &updated); err != nil {
		return err
	}
	if updated.Amount.Value != wanted || updated.Amount.Currency != "EUR" {
		return fmt.Errorf("mollie did not confirm exclusive-tax renewal amount")
	}
	return nil
}
