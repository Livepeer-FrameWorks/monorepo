package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"frameworks/api_billing/internal/appconfig"
	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/fx"
	billingtax "frameworks/api_billing/internal/tax"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/shopspring/decimal"
)

const invoiceTaxPolicyVersion = billing.CurrentInvoiceTaxPolicy

func (h *X402Handler) taxDatabase() purserdb.DBTX {
	if h.taxDB != nil {
		return h.taxDB
	}
	return h.db
}

type invoiceTaxAssessment struct {
	ReviewRequired             bool                    `json:"review_required,omitempty"`
	ReviewReasons              []string                `json:"review_reasons,omitempty"`
	ConfirmationBusinessReview json.RawMessage         `json:"confirmation_business_review,omitempty"`
	ProviderAssessment         json.RawMessage         `json:"provider_tax_assessment,omitempty"`
	Advances                   []advanceTaxApplication `json:"advances_applied,omitempty"`
	Advance                    *AdvanceMoney           `json:"advance_money,omitempty"`
	DocumentKind               string                  `json:"document_kind,omitempty"`
	Decision                   vatDecision             `json:"decision"`
	Customer                   CryptoBillingProfile    `json:"customer"`
	Supplier                   DocumentSupplier        `json:"supplier"`
	Net                        int64                   `json:"net_eur_cents"`
	VAT                        int64                   `json:"vat_eur_cents"`
	Gross                      int64                   `json:"gross_eur_cents"`
	TaxPoint                   time.Time               `json:"tax_point"`
	Validation                 json.RawMessage         `json:"vat_validation,omitempty"`
	ValidationMode             string                  `json:"vat_validation_mode,omitempty"`
}

func exclusiveTax(net int64, rate int) (vat, gross int64) {
	vat = decimal.NewFromInt(net).Mul(decimal.NewFromInt(int64(rate))).Div(decimal.NewFromInt(10000)).Round(0).IntPart()
	return vat, net + vat
}

func assessInvoiceTaxTx(ctx context.Context, tx *sql.Tx, tenant string, net int64, at time.Time) (invoiceTaxAssessment, error) {
	a := invoiceTaxAssessment{Net: net, TaxPoint: at, Supplier: ConfiguredDocumentSupplier()}
	if net < 0 {
		return a, fmt.Errorf("negative invoice tax base")
	}
	if net == 0 {
		a.Decision = vatDecision{Treatment: "no_supply", Source: "No chargeable supply"}
		return a, nil
	}
	if DocumentSupplierSnapshot() == "" {
		return a, fmt.Errorf("complete supplier identity required before issuing an invoice")
	}
	row, err := purserdb.New(tx).GetInvoiceTaxProfile(ctx, tenant)
	if err != nil {
		return a, fmt.Errorf("read invoice billing profile: %w", err)
	}
	a.Customer = CryptoBillingProfile{Email: row.Email, Name: row.Name, Company: row.Company, VATNumber: row.VatNumber, Address: row.Address, CustomerType: row.CustomerType}
	if !a.Customer.Complete() {
		return a, billingtax.Review("billing_profile_required", "Complete the saved billing name, email and address before invoicing")
	}
	if a.Customer.effectiveCustomerType() == "" {
		if strings.TrimSpace(a.Customer.VATNumber) != "" {
			a.Customer.CustomerType = "business"
		} else {
			a.Customer.CustomerType = "consumer"
		}
	}
	address, addressErr := parseBillingAddress(a.Customer.Address)
	if addressErr != nil {
		return a, addressErr
	}
	if strings.TrimSpace(a.Customer.VATNumber) != "" && billingtax.IsEU(address.Country) {
		canonical, canonicalErr := billingtax.CanonicalVATNumber(address.Country, a.Customer.VATNumber)
		if canonicalErr != nil {
			return a, canonicalErr
		}
		a.Customer.VATNumber = canonical
	}
	rt := appconfig.Runtime()
	h := &X402Handler{taxDB: tx, supplierCountry: rt.SupplierCountry}
	a.Decision, err = h.getVATDecisionForProfile(ctx, tenant, a.Customer, "", at)
	if err != nil {
		return a, err
	}
	a.VAT, a.Gross = exclusiveTax(a.Net, a.Decision.RateBPS)
	a.Validation = a.Decision.verification.Snapshot
	a.ValidationMode = a.Decision.verification.Mode
	a.retainOutageReview()
	return a, nil
}

func (a *invoiceTaxAssessment) retainOutageReview() {
	if a.ValidationMode != "outage_grace" || !a.Decision.ReverseCharge {
		return
	}
	a.ReviewRequired = true
	for _, reason := range a.ReviewReasons {
		if reason == "vies_outage_grace" {
			return
		}
	}
	a.ReviewReasons = append(a.ReviewReasons, "vies_outage_grace")
}

// Each line uses the group rate. Residual cents are assigned by fractional
// remainder and stable line key, so every line sum equals the frozen group.
func allocateInvoiceLineVAT(net []int64, rate int) []int64 {
	total := int64(0)
	for _, n := range net {
		total += n
	}
	target, _ := exclusiveTax(total, rate)
	return allocateInvoiceLineVATToTotal(net, rate, target)
}

func allocateInvoiceLineVATToTotal(net []int64, rate int, target int64) []int64 {
	vat := make([]int64, len(net))
	residual := make([]decimal.Decimal, len(net))
	var allocated int64
	for i, n := range net {
		raw := decimal.NewFromInt(n).Mul(decimal.NewFromInt(int64(rate))).Div(decimal.NewFromInt(10000))
		vat[i] = raw.Round(0).IntPart()
		allocated += vat[i]
		residual[i] = raw.Sub(decimal.NewFromInt(vat[i]))
	}
	difference := target - allocated
	order := make([]int, len(net))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		if difference < 0 {
			return residual[order[i]].LessThan(residual[order[j]])
		}
		return residual[order[i]].GreaterThan(residual[order[j]])
	})
	step := int64(1)
	if difference < 0 {
		step = -1
	}
	for i := 0; difference != 0 && len(order) > 0; i++ {
		vat[order[i%len(order)]] += step
		difference -= step
	}
	return vat
}

func freezeInvoiceTaxTx(ctx context.Context, tx *sql.Tx, tenant, invoice string, a invoiceTaxAssessment, record fx.Record) error {
	a.retainOutageReview()
	q := purserdb.New(tx)
	lines, err := q.ListInvoiceTaxLines(ctx, purserdb.ListInvoiceTaxLinesParams{TenantID: tenant, InvoiceID: invoice})
	if err != nil {
		return err
	}
	amounts := make([]int64, len(lines))
	var sum int64
	for i, line := range lines {
		amounts[i] = line.NetEurCents
		sum += line.NetEurCents
	}
	if sum != a.Net {
		return fmt.Errorf("invoice lines %d do not equal frozen tax base %d", sum, a.Net)
	}
	baseAmounts := append([]int64(nil), amounts...)
	overrides := make(map[string]advanceTaxApplication, len(a.Advances))
	baseVAT := a.VAT
	for _, advance := range a.Advances {
		overrides[advance.LineKey] = advance
		baseVAT += advance.VAT
	}
	for i, line := range lines {
		if _, ok := overrides[line.LineKey]; ok {
			baseAmounts[i] = 0
		}
	}
	vats := allocateInvoiceLineVATToTotal(baseAmounts, a.Decision.RateBPS, baseVAT)
	type taxGroup struct {
		Rate      int    `json:"rate_bps"`
		Treatment string `json:"treatment"`
		Net       int64  `json:"net_eur_cents"`
		VAT       int64  `json:"vat_eur_cents"`
		Gross     int64  `json:"gross_eur_cents"`
		Wording   string `json:"wording"`
	}
	groups := map[string]*taxGroup{}

	for i, line := range lines {
		rate, treatment := a.Decision.RateBPS, a.Decision.Treatment
		if advance, ok := overrides[line.LineKey]; ok {
			vats[i] = -advance.VAT
			rate = advance.Rate
			treatment = advance.Treatment
		}
		key := fmt.Sprintf("%d:%s", rate, treatment)
		if groups[key] == nil {
			groups[key] = &taxGroup{Rate: rate, Treatment: treatment, Wording: a.Decision.Wording}
		}
		group := groups[key]
		group.Net += amounts[i]
		group.VAT += vats[i]
		group.Gross += amounts[i] + vats[i]
		if _, err = q.FreezeInvoiceLineTax(ctx, purserdb.FreezeInvoiceLineTaxParams{TenantID: tenant, InvoiceID: invoice, ID: line.ID, NetEurCents: sql.NullInt64{Int64: amounts[i], Valid: true}, VatEurCents: sql.NullInt64{Int64: vats[i], Valid: true}, GrossEurCents: sql.NullInt64{Int64: amounts[i] + vats[i], Valid: true}, VatRateBps: sql.NullInt32{Int32: int32(rate), Valid: true}, TaxTreatment: sql.NullString{String: treatment, Valid: true}, TaxPointAt: sql.NullTime{Time: a.TaxPoint, Valid: true}}); err != nil {
			return err
		}
	}
	netFX, err := fx.RecordFromEUR(a.Net, record.Rate())
	if err != nil {
		return err
	}
	grossFX, err := fx.RecordFromEUR(a.Gross, record.Rate())
	if err != nil {
		return err
	}
	documentNet, documentVAT, documentGross := netFX.OriginalMinor, grossFX.OriginalMinor-netFX.OriginalMinor, grossFX.OriginalMinor
	if a.Advance != nil {
		documentNet = a.Advance.NetOriginal
		documentVAT = a.Advance.VATOriginal
		documentGross = a.Advance.GrossOriginal
	}
	decision, err := json.Marshal(a)
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	orderedGroups := make([]*taxGroup, 0, len(groups))
	for _, key := range keys {
		orderedGroups = append(orderedGroups, groups[key])
	}
	group, err := json.Marshal(orderedGroups)
	if err != nil {
		return err
	}
	customer, err := json.Marshal(map[string]any{"email": a.Customer.Email, "name": a.Customer.Name, "company": a.Customer.Company, "address": string(a.Customer.Address), "vat_number": a.Customer.VATNumber, "customer_type": a.Customer.CustomerType})
	if err != nil {
		return err
	}
	supplier, err := json.Marshal(a.Supplier)
	if err != nil {
		return err
	}
	status := "ready"
	if a.Gross == 0 {
		status = "no_supply"
	}
	rows, err := q.FreezeInvoiceTax(ctx, purserdb.FreezeInvoiceTaxParams{TenantID: tenant, InvoiceID: invoice, NetEurCents: sql.NullInt64{Int64: a.Net, Valid: true}, VatEurCents: sql.NullInt64{Int64: a.VAT, Valid: true}, GrossEurCents: sql.NullInt64{Int64: a.Gross, Valid: true}, DocumentNetMinor: sql.NullInt64{Int64: documentNet, Valid: true}, DocumentVatMinor: sql.NullInt64{Int64: documentVAT, Valid: true}, DocumentGrossMinor: sql.NullInt64{Int64: documentGross, Valid: true}, TaxStatus: status, TaxPolicyVersion: sql.NullString{String: invoiceTaxPolicyVersion, Valid: true}, TaxDecision: decision, TaxSummary: group, TaxPointAt: sql.NullTime{Time: a.TaxPoint, Valid: true}, CustomerSnapshot: customer, SupplierSnapshot: supplier})
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("invoice tax facts were already frozen or invoice is missing")
	}
	return nil
}
