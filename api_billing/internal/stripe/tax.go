package stripe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	billingtax "frameworks/api_billing/internal/tax"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	api "github.com/stripe/stripe-go/v85"
	"github.com/stripe/stripe-go/v85/customer"
	"github.com/stripe/stripe-go/v85/subscription"
	"github.com/stripe/stripe-go/v85/subscriptionitem"
	"github.com/stripe/stripe-go/v85/taxid"
	"github.com/stripe/stripe-go/v85/taxrate"
)

type TaxProfile struct {
	Name             string          `json:"name"`
	Company          string          `json:"company,omitempty"`
	Email            string          `json:"email"`
	Street           string          `json:"street"`
	City             string          `json:"city"`
	PostalCode       string          `json:"postal_code"`
	Country          string          `json:"country"`
	VATNumber        string          `json:"vat_number,omitempty"`
	RateBPS          int             `json:"rate_bps"`
	ReverseCharge    bool            `json:"reverse_charge"`
	Wording          string          `json:"wording"`
	SupplierCountry  string          `json:"supplier_country"`
	SupplierIdentity string          `json:"supplier_identity"`
	Evidence         json.RawMessage `json:"evidence,omitempty"`
	ProfileSHA       string          `json:"profile_sha,omitempty"`
}

func (p TaxProfile) Fingerprint() string {
	p.ProfileSHA = ""
	// Validation history is retained separately and copied at issuance. Only
	// legal parties and charged terms identify the recurring provider setup.
	p.Evidence = nil
	encoded, err := json.Marshal(p)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func (c *Client) PrepareCustomerTax(ctx context.Context, tenant, customerID string, p TaxProfile) (string, error) {
	if tenant == "" || customerID == "" || p.Name == "" || p.Email == "" || p.Street == "" || p.City == "" || p.PostalCode == "" || p.Country == "" || p.SupplierCountry == "" || p.SupplierIdentity == "" || p.RateBPS < 0 || p.RateBPS > 10000 || (p.ReverseCharge && (p.RateBPS != 0 || p.VATNumber == "")) {
		return "", fmt.Errorf("complete assessed Stripe tax profile required")
	}
	get := &api.CustomerParams{}
	get.Context = ctx
	get.AddExpand("tax_ids")
	current, err := customer.Get(customerID, get)
	if err != nil {
		return "", err
	}
	if current.Metadata["tenant_id"] != tenant {
		return "", fmt.Errorf("stripe customer does not belong to the tenant")
	}
	if current.TaxIDs != nil && current.TaxIDs.HasMore {
		list := &api.TaxIDListParams{Customer: api.String(customerID)}
		list.Context = ctx
		list.Limit = api.Int64(100)
		ids := taxid.List(list)
		all := []*api.TaxID{}
		for ids.Next() {
			all = append(all, ids.TaxID())
		}
		if err = ids.Err(); err != nil {
			return "", err
		}
		current.TaxIDs.Data = all
	}
	legalName := p.Name
	if strings.TrimSpace(p.Company) != "" {
		legalName = p.Company
	}
	footer := p.SupplierIdentity
	if p.Wording != "" {
		footer += "\n" + p.Wording
	}
	exempt := "none"
	addressMatches := current.Address != nil && current.Address.Line1 == p.Street && current.Address.City == p.City && current.Address.PostalCode == p.PostalCode && current.Address.Country == p.Country
	footerMatches := current.InvoiceSettings != nil && current.InvoiceSettings.Footer == footer
	if current.Name != legalName || current.Email != p.Email || !addressMatches || string(current.TaxExempt) != exempt || !footerMatches {
		params := &api.CustomerParams{Name: api.String(legalName), Email: api.String(p.Email), Address: &api.AddressParams{Line1: api.String(p.Street), City: api.String(p.City), PostalCode: api.String(p.PostalCode), Country: api.String(p.Country)}, TaxExempt: api.String(exempt), InvoiceSettings: &api.CustomerInvoiceSettingsParams{Footer: api.String(footer)}}
		params.Context = ctx
		if _, err = customer.Update(customerID, params); err != nil {
			return "", err
		}
	}
	if current.TaxIDs != nil {
		for _, id := range current.TaxIDs.Data {
			if id.Type == api.TaxIDTypeEUVAT && (!billingtax.IsEU(p.Country) || id.Value != p.VATNumber) {
				params := &api.TaxIDParams{Customer: api.String(customerID)}
				params.Context = ctx
				if _, err = taxid.Del(id.ID, params); err != nil {
					return "", err
				}
			}
		}
	}
	if p.VATNumber != "" && billingtax.IsEU(p.Country) {
		found := false
		if current.TaxIDs != nil {
			for _, id := range current.TaxIDs.Data {
				if id.Type == api.TaxIDTypeEUVAT && id.Value == p.VATNumber {
					found = true
				}
			}
		}
		if !found {
			params := &api.TaxIDParams{Customer: api.String(customerID), Type: api.String("eu_vat"), Value: api.String(p.VATNumber)}
			params.Context = ctx
			digest := sha256.Sum256([]byte(customerID + ":" + p.VATNumber))
			params.SetIdempotencyKey("frameworks-customer-vat:" + hex.EncodeToString(digest[:]))
			if _, err = taxid.New(params); err != nil {
				return "", err
			}
		}
	}
	display := "VAT"
	if p.ReverseCharge {
		display = "VAT reverse charged / btw verlegd"
	}
	list := &api.TaxRateListParams{Active: api.Bool(true)}
	list.Context = ctx
	list.Limit = api.Int64(100)
	iter := taxrate.List(list)
	for iter.Next() {
		rate := iter.TaxRate()
		if !rate.Inclusive && rate.Country == p.SupplierCountry && rate.Percentage == float64(p.RateBPS)/100 && rate.DisplayName == display && rate.Metadata["frameworks_policy"] == billing.CurrentInvoiceTaxPolicy {
			return rate.ID, nil
		}
	}
	if err = iter.Err(); err != nil {
		return "", err
	}
	params := &api.TaxRateParams{Country: api.String(p.SupplierCountry), DisplayName: api.String(display), Inclusive: api.Bool(false), Percentage: api.Float64(float64(p.RateBPS) / 100), TaxType: api.String("vat"), Metadata: map[string]string{"frameworks_policy": billing.CurrentInvoiceTaxPolicy}}
	params.Context = ctx
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d:%t", billing.CurrentInvoiceTaxPolicy, p.SupplierCountry, p.RateBPS, p.ReverseCharge)))
	params.SetIdempotencyKey("frameworks-exclusive-vat:" + hex.EncodeToString(digest[:]))
	rate, err := taxrate.New(params)
	if err != nil {
		return "", err
	}
	if rate.ID == "" || rate.Inclusive || rate.Percentage != float64(p.RateBPS)/100 || rate.Country != p.SupplierCountry {
		return "", fmt.Errorf("provider did not create an exclusive tax rate")
	}
	return rate.ID, nil
}

// SyncSubscriptionTax changes future invoices only. It neither prorates nor
// adjusts a paid invoice, and it refuses subscriptions owned by another tenant.
func (c *Client) SyncSubscriptionTax(ctx context.Context, tenant, customerID, subscriptionID string, p TaxProfile) error {
	_, err := c.syncSubscriptionTax(ctx, tenant, customerID, subscriptionID, p)
	return err
}

type subscriptionTaxSyncState struct {
	Subscription    *api.Subscription
	RateID          string
	RecoveryPending bool
}

// ReconcileSubscriptionTax lets the billing worker repair older tax-held
// invoices independently of interactive checkout and upgrade preflights.
func (c *Client) ReconcileSubscriptionTax(ctx context.Context, tenant, customerID, subscriptionID string, p TaxProfile) (*TaxRecoveryReport, error) {
	report := &TaxRecoveryReport{TenantID: tenant, CustomerID: customerID, SubscriptionID: subscriptionID, ProfileSHA: p.Fingerprint()}
	state, err := c.syncSubscriptionTax(ctx, tenant, customerID, subscriptionID, p)
	if err != nil {
		report.RetryPending = true
		return report, err
	}
	report.RecoveryPending = state.RecoveryPending
	if !state.RecoveryPending {
		return report, nil
	}
	return recoverStripeTaxInvoices(ctx, state.Subscription, p, state.RateID)
}

func (c *Client) syncSubscriptionTax(ctx context.Context, tenant, customerID, subscriptionID string, p TaxProfile) (*subscriptionTaxSyncState, error) {
	get := &api.SubscriptionParams{}
	get.Context = ctx
	current, err := subscription.Get(subscriptionID, get)
	if err != nil {
		return nil, err
	}
	if current.Customer == nil || current.Customer.ID != customerID || current.Metadata["tenant_id"] != tenant {
		return nil, fmt.Errorf("stripe subscription tax owner mismatch")
	}
	activeReview := current.Metadata["frameworks_tax_review"] == "true"
	legacyHoldMarker := current.Metadata["frameworks_tax_hold_started"] != ""
	recoveryPending := activeReview || legacyHoldMarker || current.Metadata["frameworks_tax_recovery_pending"] == "true"
	var items []*api.SubscriptionItem
	if current.Items != nil {
		items = current.Items.Data
		if current.Items.HasMore {
			list := &api.SubscriptionItemListParams{Subscription: api.String(subscriptionID)}
			list.Context = ctx
			list.Limit = api.Int64(100)
			iter := subscriptionitem.List(list)
			items = nil
			for iter.Next() {
				items = append(items, iter.SubscriptionItem())
			}
			if err = iter.Err(); err != nil {
				return nil, err
			}
		}
	}
	rateID, err := c.PrepareCustomerTax(ctx, tenant, customerID, p)
	if err != nil {
		return nil, err
	}
	fingerprint := p.Fingerprint()
	var itemUpdates []*api.SubscriptionItemsParams
	for _, item := range items {
		if item == nil {
			return nil, fmt.Errorf("stripe subscription item identity missing")
		}
		if len(item.TaxRates) == 0 {
			continue
		}
		if len(item.TaxRates) == 1 && item.TaxRates[0] != nil && item.TaxRates[0].ID == rateID {
			continue
		}
		if item.ID == "" {
			return nil, fmt.Errorf("stripe subscription item identity missing")
		}
		itemUpdates = append(itemUpdates, &api.SubscriptionItemsParams{ID: api.String(item.ID), TaxRates: []*string{api.String(rateID)}})
	}
	if len(itemUpdates) > 20 {
		return nil, fmt.Errorf("too many explicit subscription tax overrides for an atomic update")
	}
	// Item rates take precedence over defaults. Reconcile existing overrides
	// in the same future-only update, without changing prices or quantities.
	params := &api.SubscriptionParams{Items: itemUpdates, DefaultTaxRates: []*string{api.String(rateID)}, AutomaticTax: &api.SubscriptionAutomaticTaxParams{Enabled: api.Bool(false)}, ProrationBehavior: api.String("none"), Metadata: map[string]string{"frameworks_tax_profile": fingerprint}}
	matched := len(itemUpdates) == 0 && len(current.DefaultTaxRates) == 1 && current.DefaultTaxRates[0] != nil && current.DefaultTaxRates[0].ID == rateID && current.AutomaticTax != nil && !current.AutomaticTax.Enabled && current.Metadata["frameworks_tax_profile"] == fingerprint
	if !matched {
		params.Context = ctx
		if _, err = subscription.Update(subscriptionID, params); err != nil {
			return nil, err
		}
	}
	// Correct future renewals can resume independently of older invoice debt.
	// A pending marker retains crash-safe worker repair without repeatedly
	// scanning every clean subscription or keeping an active hold forever.
	if activeReview || legacyHoldMarker {
		resume := &api.SubscriptionParams{Metadata: map[string]string{"frameworks_tax_review": "", "frameworks_tax_pause_owned": "", "frameworks_tax_hold_started": "", "frameworks_tax_recovery_pending": "true"}, ProrationBehavior: api.String("none")}
		if current.Metadata["frameworks_tax_pause_owned"] == "true" && current.PauseCollection != nil && current.PauseCollection.Behavior == api.SubscriptionPauseCollectionBehaviorKeepAsDraft && current.PauseCollection.ResumesAt == 0 {
			resume.UnsetFields = []api.SubscriptionParamsUnsetField{api.SubscriptionParamsUnsetFieldPauseCollection}
		}
		resume.Context = ctx
		if _, err = subscription.Update(subscriptionID, resume); err != nil {
			return nil, err
		}
	}
	return &subscriptionTaxSyncState{Subscription: current, RateID: rateID, RecoveryPending: recoveryPending}, nil
}

// HoldSubscriptionTaxReview prevents unproven recurring tax from being
// collected. Drafts and existing paid invoices retain their original facts.
func (c *Client) HoldSubscriptionTaxReview(ctx context.Context, tenant, customerID, subscriptionID string) error {
	get := &api.SubscriptionParams{}
	get.Context = ctx
	current, err := subscription.Get(subscriptionID, get)
	if err != nil {
		return err
	}
	if current.Customer == nil || current.Customer.ID != customerID || current.Metadata["tenant_id"] != tenant {
		return fmt.Errorf("stripe subscription tax owner mismatch")
	}
	var items []*api.SubscriptionItemsParams
	if current.Items != nil {
		for _, item := range current.Items.Data {
			if item != nil && item.ID != "" && len(item.TaxRates) > 0 {
				items = append(items, &api.SubscriptionItemsParams{ID: api.String(item.ID), UnsetFields: []api.SubscriptionItemsParamsUnsetField{api.SubscriptionItemsParamsUnsetFieldTaxRates}})
			}
		}
	}
	batch := items
	if len(batch) > 20 {
		batch = batch[:20]
	}
	p := &api.SubscriptionParams{Items: batch, AutomaticTax: &api.SubscriptionAutomaticTaxParams{Enabled: api.Bool(false)}, ProrationBehavior: api.String("none"), Metadata: map[string]string{"frameworks_tax_review": "true", "frameworks_tax_profile": "", "frameworks_tax_pause_owned": ""}, UnsetFields: []api.SubscriptionParamsUnsetField{api.SubscriptionParamsUnsetFieldDefaultTaxRates}}
	started := current.Metadata["frameworks_tax_hold_started"]
	if current.Metadata["frameworks_tax_review"] != "true" || started == "" {
		started = strconv.FormatInt(time.Now().UTC().Unix(), 10)
	}
	p.Metadata["frameworks_tax_hold_started"] = started
	if current.PauseCollection == nil {
		p.PauseCollection = &api.SubscriptionPauseCollectionParams{Behavior: api.String("keep_as_draft")}
		p.Metadata["frameworks_tax_pause_owned"] = "true"
	} else if current.Metadata["frameworks_tax_pause_owned"] == "true" && current.PauseCollection.Behavior == api.SubscriptionPauseCollectionBehaviorKeepAsDraft && current.PauseCollection.ResumesAt == 0 {
		p.Metadata["frameworks_tax_pause_owned"] = "true"
	}
	p.Context = ctx
	if _, err = subscription.Update(subscriptionID, p); err != nil {
		return err
	}
	var result error
	if current.Items != nil && current.Items.HasMore {
		list := &api.SubscriptionItemListParams{Subscription: api.String(subscriptionID)}
		list.Context = ctx
		list.Limit = api.Int64(100)
		iter := subscriptionitem.List(list)
		items = nil
		for iter.Next() {
			item := iter.SubscriptionItem()
			if item != nil && item.ID != "" && len(item.TaxRates) > 0 {
				items = append(items, &api.SubscriptionItemsParams{ID: api.String(item.ID), UnsetFields: []api.SubscriptionItemsParamsUnsetField{api.SubscriptionItemsParamsUnsetFieldTaxRates}})
			}
		}
		result = errors.Join(result, iter.Err())
	} else {
		items = items[len(batch):]
	}
	for len(items) > 0 {
		n := min(len(items), 20)
		update := &api.SubscriptionParams{Items: items[:n], ProrationBehavior: api.String("none")}
		update.Context = ctx
		_, updateErr := subscription.Update(subscriptionID, update)
		result = errors.Join(result, updateErr)
		items = items[n:]
	}
	customerGet := &api.CustomerParams{}
	customerGet.Context = ctx
	buyer, buyerErr := customer.Get(customerID, customerGet)
	if buyerErr != nil {
		return errors.Join(result, buyerErr)
	}
	if buyer.Metadata["tenant_id"] != tenant {
		return errors.Join(result, fmt.Errorf("stripe customer tax owner mismatch"))
	}
	if buyer.TaxExempt != api.CustomerTaxExemptNone {
		update := &api.CustomerParams{TaxExempt: api.String("none")}
		update.Context = ctx
		_, buyerErr = customer.Update(customerID, update)
		result = errors.Join(result, buyerErr)
	}
	return result
}

func SupplierTaxIdentity(name, address, vat, registration string) string {
	return strings.Join([]string{name, address, "VAT: " + vat, "Registration: " + registration}, "; ")
}
