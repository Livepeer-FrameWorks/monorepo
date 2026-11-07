package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/fx"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
)

// ApplyCryptoAdvanceQuote fixes gross payment instructions and net service credit
// together, using the exchange rate already locked into the token quote.
func (h *X402Handler) ApplyCryptoAdvanceQuote(ctx context.Context, tenantID string, quote *DepositQuote, clientIP string) (CryptoDocumentRequirement, error) {
	if quote == nil || quote.ExpectedAmountBaseUnits == nil || quote.ExpectedAmountBaseUnits.Sign() <= 0 {
		return CryptoDocumentRequirement{}, fmt.Errorf("positive deposit quote required")
	}
	req, err := h.GetCryptoDocumentRequirement(ctx, tenantID, quote.FX.EURMinor, clientIP)
	if err != nil || req.RequiresCompleteProfile {
		return req, err
	}
	if err = validateCryptoAdvanceSupplier(req.Profile); err != nil {
		return req, err
	}
	money, cash, err := quoteAdvanceMoney(quote.FX.OriginalMinor, req.Profile.TaxAssessment.Decision.RateBPS, quote.FX.Rate())
	if err != nil {
		return req, err
	}
	units := new(big.Int).Mul(quote.ExpectedAmountBaseUnits, big.NewInt(money.GrossOriginal))
	units.Add(units, big.NewInt(money.NetOriginal-1))
	units.Quo(units, big.NewInt(money.NetOriginal))
	quote.ExpectedAmountBaseUnits = units
	quote.FX = cash
	req = bindCryptoAdvance(req, money)
	quote.TaxDocumentKind, quote.TaxProfile = req.DocumentKind, req.Profile
	return req, nil
}

func bindCryptoAdvance(req CryptoDocumentRequirement, money AdvanceMoney) CryptoDocumentRequirement {
	req.Profile.TaxAssessment.Advance = &money
	if money.GrossEUR > 10000 {
		req.NeedsFullDocument = true
		req.DocumentKind = "full"
		req.RequiresCompleteProfile = !req.Profile.Complete()
	}
	return req
}

func cryptoAdvanceAssessment(profile CryptoBillingProfile, kind string, cash fx.Record, at time.Time) (invoiceTaxAssessment, error) {
	if kind != "full" && kind != "simplified" {
		return invoiceTaxAssessment{}, fmt.Errorf("invalid crypto advance document kind")
	}
	if cash.EURMinor > 10000 && (kind != "full" || !profile.Complete()) {
		return invoiceTaxAssessment{}, fmt.Errorf("full crypto advance invoice requires complete retained profile")
	}
	if err := validateCryptoAdvanceSupplier(profile); err != nil {
		return invoiceTaxAssessment{}, err
	}
	frozen := profile.TaxAssessment
	if frozen == nil || frozen.PolicyVersion != billing.CurrentCryptoTopupTaxPolicy || frozen.Advance == nil {
		return invoiceTaxAssessment{}, fmt.Errorf("exclusive crypto advance assessment required")
	}
	decision, err := frozen.validatedDecision()
	if err != nil {
		return invoiceTaxAssessment{}, err
	}
	money := *frozen.Advance
	if money.NetEUR <= 0 || money.NetOriginal <= 0 || money.VATEUR < 0 || money.VATOriginal < 0 || money.NetEUR+money.VATEUR != money.GrossEUR || money.NetOriginal+money.VATOriginal != money.GrossOriginal || money.RateBPS != decision.RateBPS {
		return invoiceTaxAssessment{}, fmt.Errorf("inconsistent frozen crypto advance totals")
	}
	if cash.OriginalMinor != money.GrossOriginal || cash.EURMinor != money.GrossEUR || cash.OriginalCurrency != money.Currency {
		return invoiceTaxAssessment{}, fmt.Errorf("crypto cash does not match frozen advance")
	}
	a := invoiceTaxAssessment{Advance: &money, DocumentKind: kind, Decision: decision, Customer: profile, Supplier: frozen.Supplier, Net: money.NetEUR, VAT: money.VATEUR, Gross: money.GrossEUR, TaxPoint: at, Validation: frozen.VATValidation, ValidationMode: frozen.VATValidationMode}
	if frozen.VATValidationMode == "outage_grace" {
		a.ReviewReasons = append(a.ReviewReasons, "vies_outage_grace")
	}
	if decision.PolicyValidUntil != nil && !at.Before(*decision.PolicyValidUntil) {
		a.ReviewReasons = append(a.ReviewReasons, "tax_policy_expired")
	}
	a.ReviewRequired = len(a.ReviewReasons) > 0
	return a, nil
}

func issueCryptoAdvanceTx(ctx context.Context, tx *sql.Tx, tenant, source, reference, transactionID string, profile CryptoBillingProfile, kind string, cash fx.Record, at time.Time, balanceBefore int64) error {
	a, err := cryptoAdvanceAssessment(profile, kind, cash, at)
	if err != nil {
		return err
	}
	return issueReviewedCryptoAdvanceTx(ctx, tx, tenant, source, reference, reference, transactionID, a, cash, balanceBefore)
}

func completeX402AdvanceTx(ctx context.Context, tx *sql.Tx, tenant, nonce, hash string, amount, balance int64) error {
	snapshot, current, err := loadExclusiveX402AdvanceTx(ctx, tx, tenant, hash)
	if err != nil {
		return err
	}
	if !current {
		return nil
	}
	a, err := cryptoAdvanceAssessment(snapshot.Profile, snapshot.DocumentKind, snapshot.FX, time.Now().UTC())
	if err != nil {
		return err
	}
	if amount != a.Net {
		return fmt.Errorf("settled net credit differs from frozen advance")
	}
	entry, err := purserdb.New(tx).GetBalanceTransactionByReference(ctx, purserdb.GetBalanceTransactionByReferenceParams{TenantID: tenant, ReferenceType: sql.NullString{String: "x402_payment", Valid: true}, ReferenceID: nonce})
	if err != nil {
		return err
	}
	return issueCryptoAdvanceTx(ctx, tx, tenant, "x402_payment", hash, entry.ID.String(), snapshot.Profile, snapshot.DocumentKind, snapshot.FX, time.Now().UTC(), balance-amount)
}

func decodeCryptoAdvance(raw []byte) (CryptoBillingProfile, error) {
	var p CryptoBillingProfile
	err := json.Unmarshal(raw, &p)
	return p, err
}

// resizeCryptoAdvance values every received token at the accepted rate. An
// overpayment buys additional net credit and retains its own share of VAT.
func resizeCryptoAdvance(profile *CryptoBillingProfile, cash fx.Record) error {
	if profile.TaxAssessment == nil || profile.TaxAssessment.Advance == nil {
		return fmt.Errorf("missing frozen crypto advance")
	}
	original := profile.TaxAssessment.Advance
	if cash.OriginalMinor == original.GrossOriginal && cash.EURMinor == original.GrossEUR {
		return nil
	}
	net, vat := extractVATInclusive(cash.OriginalMinor, original.RateBPS)
	convertedVAT, err := fx.RecordToEUR(vat, cash.Rate())
	if err != nil {
		return err
	}
	money := AdvanceMoney{NetOriginal: net, VATOriginal: vat, GrossOriginal: cash.OriginalMinor, NetEUR: cash.EURMinor - convertedVAT.EURMinor, VATEUR: convertedVAT.EURMinor, GrossEUR: cash.EURMinor, Currency: cash.OriginalCurrency, RateBPS: original.RateBPS}
	copyAssessment := *profile.TaxAssessment
	copyAssessment.Advance = &money
	profile.TaxAssessment = &copyAssessment
	return nil
}

func recoverX402AdvanceTx(ctx context.Context, tx *sql.Tx, tenant, nonce, hash, transactionID string, amount, balance int64) error {
	snapshot, current, err := loadExclusiveX402AdvanceTx(ctx, tx, tenant, hash)
	if err != nil {
		return err
	}
	if !current {
		return nil
	}
	a, err := cryptoAdvanceAssessment(snapshot.Profile, snapshot.DocumentKind, snapshot.FX, time.Now().UTC())
	if err != nil {
		return err
	}
	if amount != a.Net {
		return fmt.Errorf("recovered net credit differs from frozen advance")
	}
	return issueReviewedCryptoAdvanceTx(ctx, tx, tenant, "x402_recovery", nonce, hash, transactionID, a, snapshot.FX, balance-amount)
}

// Legacy quotes retain their accepted gross-credit contract even when they lack
// modern FX fields. Only an exclusive advance requires the new frozen totals.
func loadExclusiveX402AdvanceTx(ctx context.Context, tx *sql.Tx, tenant, hash string) (cryptoTaxSnapshot, bool, error) {
	row, err := purserdb.New(tx).GetX402TaxSnapshot(ctx, purserdb.GetX402TaxSnapshotParams{TenantID: tenant, TxHash: sql.NullString{String: hash, Valid: true}})
	if errors.Is(err, sql.ErrNoRows) {
		return cryptoTaxSnapshot{}, false, nil
	}
	if err != nil {
		return cryptoTaxSnapshot{}, false, err
	}
	var header struct {
		TaxAssessment *struct {
			PolicyVersion string `json:"policy_version"`
		} `json:"tax_assessment"`
	}
	if err = json.Unmarshal(row.TaxProfileSnapshot, &header); err != nil {
		return cryptoTaxSnapshot{}, false, err
	}
	if header.TaxAssessment == nil {
		return cryptoTaxSnapshot{}, false, nil
	}
	policy := header.TaxAssessment.PolicyVersion
	if !billing.IsSupportedCryptoTaxPolicy(policy) {
		return cryptoTaxSnapshot{}, false, fmt.Errorf("unsupported retained crypto tax policy %q", policy)
	}
	if policy != billing.CurrentCryptoTopupTaxPolicy {
		return cryptoTaxSnapshot{}, false, nil
	}
	profile, err := decodeCryptoAdvance(row.TaxProfileSnapshot)
	if err != nil {
		return cryptoTaxSnapshot{}, false, err
	}
	cash, err := storedFXRecord(row.OriginalAmountCents, row.OriginalCurrency, row.EurAmountCents, row.FxUnitsPerEur, row.FxSource, row.FxReferenceDate)
	if err != nil {
		return cryptoTaxSnapshot{}, false, err
	}
	if cash == nil {
		return cryptoTaxSnapshot{}, false, fmt.Errorf("exclusive crypto advance has no cash quote")
	}
	if row.TaxDocumentKind != "full" && row.TaxDocumentKind != "simplified" {
		return cryptoTaxSnapshot{}, false, fmt.Errorf("exclusive crypto advance has invalid document kind")
	}
	return cryptoTaxSnapshot{DocumentKind: row.TaxDocumentKind, Profile: profile, FX: *cash, FXRateObservedAt: row.CreatedAt}, true, nil
}

func validateCryptoAdvanceSupplier(profile CryptoBillingProfile) error {
	if profile.TaxAssessment == nil {
		return fmt.Errorf("crypto advance has no retained tax assessment")
	}
	if !profile.TaxAssessment.Supplier.Complete() {
		return fmt.Errorf("complete supplier identity required before crypto payment instructions")
	}
	return nil
}

// Confirmation records later evidence beside the accepted quote. Review never
// changes its supplier, buyer, VAT treatment, payment amount, or service credit.
func issueReviewedCryptoAdvanceTx(ctx context.Context, tx *sql.Tx, tenant, source, reference, paymentReference, transactionID string, a invoiceTaxAssessment, cash fx.Record, balanceBefore int64) error {
	q := purserdb.New(tx)
	if _, err := q.GetFundedAdvanceInvoice(ctx, purserdb.GetFundedAdvanceInvoiceParams{TenantID: tenant, SourceReferenceType: sql.NullString{String: source, Valid: true}, SourceReferenceID: sql.NullString{String: reference, Valid: true}}); err == nil {
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if len(a.Decision.BusinessEstablishment) > 0 {
		fingerprint, err := CryptoBusinessProfileFingerprint(a.Customer)
		if err == nil {
			h := &X402Handler{taxDB: tx}
			var proof json.RawMessage
			proof, _, err = h.loadBusinessReview(ctx, tenant, fingerprint, a.Decision.Country, a.TaxPoint)
			if err == nil {
				a.ConfirmationBusinessReview = proof
			} else if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("read confirmation business evidence: %w", err)
			}
		}
		if err != nil {
			a.ReviewReasons = append(a.ReviewReasons, "business_evidence_unavailable")
		}
	}
	a.ReviewRequired = len(a.ReviewReasons) > 0
	id, _, err := issueFundedAdvanceInvoiceTx(ctx, tx, tenant, source, reference, paymentReference, a, cash, a.TaxPoint, balanceBefore, transactionID)
	if err != nil {
		return err
	}
	if a.ReviewRequired && (len(a.ReviewReasons) != 1 || a.ReviewReasons[0] != "vies_outage_grace") {
		evidence, err := json.Marshal(map[string]any{"invoice_id": id, "accepted_assessment": a, "payment_reference": paymentReference})
		if err != nil {
			return err
		}
		return q.RecordCryptoAccountingAnomaly(ctx, purserdb.RecordCryptoAccountingAnomalyParams{TenantID: tenant, Kind: "tax_assessment_review", Network: "tax", ReferenceType: "invoice", ReferenceID: id, AmountCents: a.Gross, Detail: "Confirmed crypto advance retained its accepted tax claim; expired, revoked or outage evidence requires review", EvidenceJson: evidence})
	}
	return nil
}
