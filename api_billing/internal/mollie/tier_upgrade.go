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
	"strings"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/shopspring/decimal"
)

// UpgradeMonthlySubscription changes future recurring payments. Purser
// invoices the replacement month's fee minus the unused paid old fee; Mollie
// starts the next recurrence on its date-only monthly schedule. The receipt
// stored in provider metadata makes an ambiguous PATCH discoverable.
func (c *Client) UpgradeMonthlySubscription(ctx context.Context, request billing.TierUpgradeRequest) (billing.TierUpgradeReceipt, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	sub, err := c.GetSubscription(ctx, request.CustomerID, request.SubscriptionID)
	if err != nil {
		return billing.TierUpgradeReceipt{}, err
	}
	metadata := map[string]any{}
	raw, err := json.Marshal(sub.Metadata)
	if err != nil {
		return billing.TierUpgradeReceipt{}, err
	}
	if string(raw) != "null" {
		if err = json.Unmarshal(raw, &metadata); err != nil {
			return billing.TierUpgradeReceipt{}, err
		}
	}
	if metadata["tenant_id"] != request.TenantID {
		return billing.TierUpgradeReceipt{}, errors.New("mollie upgrade subscription does not belong to the tenant")
	}
	if metadata["billing_tier_upgrade_id"] == request.ID {
		encoded, ok := metadata["billing_tier_upgrade_receipt"].(string)
		if !ok {
			return billing.TierUpgradeReceipt{}, errors.New("mollie upgrade receipt is missing")
		}
		var receipt billing.TierUpgradeReceipt
		if err = json.Unmarshal([]byte(encoded), &receipt); err != nil {
			return receipt, err
		}
		return receipt, validateMollieUpgrade(sub, request, receipt, true)
	}
	if request.LocateOnly {
		return billing.TierUpgradeReceipt{}, fmt.Errorf("%w: no provider update exists for this purchase", billing.ErrTierUpgradeRejected)
	}
	if sub.Status != "active" && sub.Status != "pending" {
		return billing.TierUpgradeReceipt{}, fmt.Errorf("%w: Mollie subscription is %s", billing.ErrTierUpgradeRejected, sub.Status)
	}
	if !request.RequestedAt.Before(request.OldPeriodEnd) {
		return billing.TierUpgradeReceipt{}, fmt.Errorf("%w: %w; wait for the local billing period to roll over", billing.ErrTierUpgradeRejected, billing.ErrTierUpgradeUnsettled)
	}
	// Mollie may have collected the next calendar-date renewal before UTC
	// midnight. Wait for the local period writer rather than losing that fee.
	if sub.NextPaymentDate != nil && sub.NextPaymentDate.Format(time.DateOnly) > request.OldPeriodEnd.UTC().Format(time.DateOnly) {
		return billing.TierUpgradeReceipt{}, fmt.Errorf("%w: %w; Mollie has already advanced its renewal date", billing.ErrTierUpgradeRejected, billing.ErrTierUpgradeUnsettled)
	}
	if sub.Amount == nil || sub.Amount.Currency != "EUR" {
		return billing.TierUpgradeReceipt{}, fmt.Errorf("%w: fixed Mollie subscription has no EUR fee", billing.ErrTierUpgradeRejected)
	}
	oldFee, amountErr := decimal.NewFromString(sub.Amount.Value)
	if retained, ok := metadata["frameworks_net_minor"].(string); ok {
		oldFee, amountErr = decimal.NewFromString(retained)
		oldFee = oldFee.Shift(-2)
	}
	if amountErr != nil || oldFee.IsNegative() {
		return billing.TierUpgradeReceipt{}, fmt.Errorf("%w: fixed Mollie subscription has an invalid fee", billing.ErrTierUpgradeRejected)
	}
	var taxPaymentIDs []string
	paid, paymentIDs, basisStart, err := c.paidUpgradeBasis(ctx, request, sub, &taxPaymentIDs)
	if err != nil {
		if errors.Is(err, billing.ErrTierUpgradeUnsettled) {
			return billing.TierUpgradeReceipt{}, errors.Join(billing.ErrTierUpgradeRejected, err)
		}
		return billing.TierUpgradeReceipt{}, err
	}
	at := request.RequestedAt.UTC().Truncate(time.Microsecond)
	// Mollie accepts a calendar date for startDate, not a timestamp. Use that
	// authoritative next payment date as the replacement period's end.
	next := billing.NextMonthlyDateBoundary(at)
	remaining := max(request.OldPeriodEnd.Sub(at).Microseconds(), 0)
	whole := request.OldPeriodEnd.Sub(basisStart).Microseconds()
	if whole <= 0 {
		return billing.TierUpgradeReceipt{}, errors.New("mollie upgrade has no old billing period")
	}
	credit := decimal.NewFromInt(min(paid, oldFee.Shift(2).IntPart())).Mul(decimal.NewFromInt(min(remaining, whole))).Div(decimal.NewFromInt(whole)).Round(0).IntPart()
	receipt := billing.TierUpgradeReceipt{Applied: true, PeriodStart: at, PeriodEnd: next, PaidPeriodStart: basisStart, PaidPeriodEnd: request.OldPeriodEnd, UnusedPaidCents: credit, PaidReceiptID: strings.Join(paymentIDs, ","), TaxDocumentPaymentIDs: taxPaymentIDs}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return receipt, err
	}
	metadata["billing_tier_upgrade_id"] = request.ID
	metadata["billing_tier_upgrade_receipt"] = string(encoded)
	metadata["tier_id"] = request.TierID
	renewalAmount, err := assessedRenewalAmount(metadata, request.BaseCents)
	if err != nil {
		return receipt, err
	}
	body, err := json.Marshal(map[string]any{
		"amount":   map[string]string{"currency": "EUR", "value": renewalAmount.StringFixed(2)},
		"interval": "1 month", "startDate": next.Format(time.DateOnly), "metadata": metadata,
	})
	if err != nil {
		return receipt, err
	}
	u, err := c.mollieURL("v2/customers/" + url.PathEscape(request.CustomerID) + "/subscriptions/" + url.PathEscape(request.SubscriptionID))
	if err != nil {
		return receipt, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, u.String(), bytes.NewReader(body))
	if err != nil {
		return receipt, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "tier-upgrade:"+request.ID)
	response, err := c.httpClient.Do(req)
	if err != nil {
		return billing.TierUpgradeReceipt{}, err
	}
	defer response.Body.Close()
	result, err := io.ReadAll(response.Body)
	if err != nil {
		return billing.TierUpgradeReceipt{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		providerErr := &APIError{StatusCode: response.StatusCode, Message: string(result)}
		if response.StatusCode >= 400 && response.StatusCode < 500 && response.StatusCode != 408 && response.StatusCode != 409 && response.StatusCode != 429 {
			return billing.TierUpgradeReceipt{}, fmt.Errorf("%w: %w", billing.ErrTierUpgradeRejected, providerErr)
		}
		return billing.TierUpgradeReceipt{}, providerErr
	}
	var updated mollie.Subscription
	if err = json.Unmarshal(result, &updated); err != nil {
		return billing.TierUpgradeReceipt{}, err
	}
	return receipt, validateMollieUpgrade(&updated, request, receipt, false)
}

func validateMollieUpgrade(sub *mollie.Subscription, request billing.TierUpgradeRequest, receipt billing.TierUpgradeReceipt, replay bool) error {
	if sub.ID != request.SubscriptionID || sub.Amount == nil || sub.Amount.Currency != "EUR" || sub.NextPaymentDate == nil || sub.NextPaymentDate.Format(time.DateOnly) < receipt.PeriodEnd.Format(time.DateOnly) || !replay && sub.NextPaymentDate != nil && sub.NextPaymentDate.Format(time.DateOnly) != receipt.PeriodEnd.Format(time.DateOnly) {
		return errors.New("mollie upgrade has not confirmed its replacement amount and renewal date")
	}
	amount, err := decimal.NewFromString(sub.Amount.Value)
	wanted, taxErr := assessedRenewalAmount(taxMetadata(sub.Metadata), request.BaseCents)
	if err != nil || taxErr != nil || !amount.Equal(wanted) {
		return errors.New("mollie upgrade returned a different replacement fee")
	}
	return nil
}

func (c *Client) paidUpgradeBasis(ctx context.Context, request billing.TierUpgradeRequest, sub *mollie.Subscription, taxPaymentIDs *[]string) (int64, []string, time.Time, error) {
	basisStart := request.OldPeriodStart
	cycleStart := billing.PreviousMonthlyAnniversary(request.OldPeriodEnd)
	// Mollie creates calendar-date payments in the customer's billing timezone.
	// Admit that date's earliest UTC instant instead of dropping positive offsets.
	cycleStart = cycleStart.Add(-14 * time.Hour)
	if request.OldPeriodStart.Before(cycleStart) {
		cycleStart = request.OldPeriodStart
	}
	var paid int64
	var ids []string
	from := ""
	for {
		u, err := c.mollieURL("v2/customers/" + url.PathEscape(request.CustomerID) + "/payments")
		if err != nil {
			return 0, nil, basisStart, err
		}
		query := u.Query()
		query.Set("limit", "250")
		if from != "" {
			query.Set("from", from)
		}
		u.RawQuery = query.Encode()
		raw, err := c.getJSON(ctx, u.String())
		if err != nil {
			return 0, nil, basisStart, err
		}
		var page struct {
			Embedded struct {
				Payments []*mollie.Payment `json:"payments"`
			} `json:"_embedded"`
			Links struct {
				Next *struct {
					Href string `json:"href"`
				} `json:"next"`
			} `json:"_links"`
		}
		if err = json.Unmarshal(raw, &page); err != nil {
			return 0, nil, basisStart, err
		}
		for _, payment := range page.Embedded.Payments {
			if payment.SubscriptionID == request.SubscriptionID &&
				(payment.Status == "open" || payment.Status == "pending" || payment.Status == "authorized") {
				return 0, nil, basisStart, billing.ErrTierUpgradeUnsettled
			}
			if payment.CreatedAt == nil || !payment.CreatedAt.Before(request.OldPeriodEnd) {
				continue
			}
			first := false
			if metadata, ok := payment.Metadata.(map[string]any); ok {
				first = metadata["tenant_id"] == request.TenantID && metadata["purpose"] == "mandate_setup" && metadata["tier_id"] == request.OldTierID &&
					sub.StartDate != nil && sub.StartDate.After(request.OldPeriodStart) && sub.CreatedAT != nil && !payment.CreatedAt.After(*sub.CreatedAT)
			}
			recurring := payment.SubscriptionID == request.SubscriptionID && !payment.CreatedAt.Before(cycleStart)
			if !recurring && !first {
				continue
			}
			if first && sub.CreatedAT.Before(basisStart) {
				basisStart = *sub.CreatedAT
			}
			if recurring && payment.CreatedAt.Before(basisStart) {
				basisStart = *payment.CreatedAt
			}

			if first && (payment.Status == "open" || payment.Status == "pending" || payment.Status == "authorized") {
				return 0, nil, basisStart, billing.ErrTierUpgradeUnsettled
			}
			if payment.Status != "paid" || payment.Amount == nil || payment.Amount.Currency != "EUR" {
				continue
			}
			amount, amountErr := decimal.NewFromString(payment.Amount.Value)
			if amountErr != nil {
				return 0, nil, basisStart, amountErr
			}
			if payment.AmountRefunded != nil {
				refunded, parseErr := decimal.NewFromString(payment.AmountRefunded.Value)
				if parseErr != nil || payment.AmountRefunded.Currency != "EUR" {
					return 0, nil, basisStart, errors.New("mollie upgrade has invalid refunded amount")
				}
				amount = amount.Sub(refunded)
			}
			if payment.AmountChargedBack != nil {
				chargedBack, parseErr := decimal.NewFromString(payment.AmountChargedBack.Value)
				if parseErr != nil || payment.AmountChargedBack.Currency != "EUR" {
					return 0, nil, basisStart, errors.New("mollie upgrade has invalid chargeback amount")
				}
				amount = amount.Sub(chargedBack)
			}
			paidMinor := max(amount.Shift(2).IntPart(), 0)
			meta := taxMetadata(payment.Metadata)
			if meta["frameworks_tax_profile"] != nil || meta["frameworks_net_minor"] != nil {
				*taxPaymentIDs = append(*taxPaymentIDs, payment.ID)
			}
			if retained, ok := meta["frameworks_net_minor"].(string); ok {
				net, parseErr := decimal.NewFromString(retained)
				original, originalErr := decimal.NewFromString(payment.Amount.Value)
				if parseErr != nil || originalErr != nil || !original.IsPositive() {
					return 0, nil, basisStart, errors.New("mollie payment has invalid frozen net tax basis")
				}
				paidMinor = decimal.NewFromInt(paidMinor).Mul(net).Div(original.Shift(2)).Round(0).IntPart()
			}
			paid += paidMinor
			ids = append(ids, payment.ID)
		}
		if page.Links.Next == nil || page.Links.Next.Href == "" {
			return paid, ids, basisStart, nil
		}
		next, err := url.Parse(page.Links.Next.Href)
		if err != nil {
			return 0, nil, basisStart, err
		}
		nextFrom := next.Query().Get("from")
		if nextFrom == "" || nextFrom == from {
			return 0, nil, basisStart, errors.New("mollie upgrade payment pagination did not advance")
		}
		from = nextFrom
	}
}
