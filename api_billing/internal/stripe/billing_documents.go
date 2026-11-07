package stripe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	api "github.com/stripe/stripe-go/v85"
)

type ProviderTaxDocument struct {
	ID              string
	Kind            string
	CustomerID      string
	Number          string
	SourceInvoiceID string
	Status          string
	Currency        string
	Net             *int64
	VAT             *int64
	Gross           int64
	Livemode        bool
	IssuedAt        time.Time
	TaxGroups       json.RawMessage
	Supplier        json.RawMessage
	Customer        json.RawMessage
	Raw             json.RawMessage
	PDFURL          string
	ProfileSHA      string
	Lines           []ProviderTaxLine
	MoreLines       bool
}

type providerCreditRate struct {
	ID      string `json:"id"`
	RateBPS int64  `json:"rate_bps"`
}
type ProviderTaxLine struct {
	Amount   int64             `json:"amount"`
	Metadata map[string]string `json:"metadata"`
	Taxes    []struct {
		Amount int64 `json:"amount"`
	} `json:"taxes"`
}

type providerDocumentPayload struct {
	ID                string            `json:"id"`
	Customer          json.RawMessage   `json:"customer"`
	Number            string            `json:"number"`
	Invoice           json.RawMessage   `json:"invoice"`
	Status            string            `json:"status"`
	Currency          string            `json:"currency"`
	Total             *int64            `json:"total"`
	TotalExcludingTax *int64            `json:"total_excluding_tax"`
	TotalTaxes        json.RawMessage   `json:"total_taxes"`
	TotalTaxAmounts   json.RawMessage   `json:"total_tax_amounts"`
	Livemode          bool              `json:"livemode"`
	Created           int64             `json:"created"`
	InvoicePDF        string            `json:"invoice_pdf"`
	PDF               string            `json:"pdf"`
	AccountName       string            `json:"account_name"`
	AccountCountry    string            `json:"account_country"`
	AccountTaxIDs     json.RawMessage   `json:"account_tax_ids"`
	Footer            string            `json:"footer"`
	CustomerName      string            `json:"customer_name"`
	CustomerEmail     string            `json:"customer_email"`
	CustomerAddress   json.RawMessage   `json:"customer_address"`
	CustomerTaxIDs    json.RawMessage   `json:"customer_tax_ids"`
	Metadata          map[string]string `json:"metadata"`
	Lines             struct {
		Data    []ProviderTaxLine `json:"data"`
		HasMore bool              `json:"has_more"`
	} `json:"lines"`
	StatusTransitions struct {
		FinalizedAt int64 `json:"finalized_at"`
	} `json:"status_transitions"`
	Parent struct {
		SubscriptionDetails struct {
			Metadata map[string]string `json:"metadata"`
		} `json:"subscription_details"`
	} `json:"parent"`
}

func providerID(raw json.RawMessage) string {
	var id string
	if json.Unmarshal(raw, &id) == nil {
		return id
	}
	var object struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &object); err != nil {
		return ""
	}
	return object.ID
}

func ParseProviderTaxDocument(kind string, raw json.RawMessage) (ProviderTaxDocument, error) {
	var payload providerDocumentPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ProviderTaxDocument{}, err
	}
	if kind != "provider_invoice" && kind != "provider_credit_note" {
		return ProviderTaxDocument{}, errors.New("unsupported provider tax document kind")
	}
	if payload.ID == "" || payload.Number == "" || payload.Total == nil || payload.Currency == "" || payload.Created <= 0 {
		return ProviderTaxDocument{}, errors.New("provider tax document is not finalized")
	}
	if kind == "provider_invoice" && payload.Status == "draft" {
		return ProviderTaxDocument{}, errors.New("draft provider document is not a tax invoice")
	}
	at := payload.Created
	if payload.StatusTransitions.FinalizedAt > 0 {
		at = payload.StatusTransitions.FinalizedAt
	}
	groups := payload.TotalTaxes
	if len(groups) == 0 || string(groups) == "null" {
		groups = payload.TotalTaxAmounts
	}
	if len(groups) == 0 || string(groups) == "null" {
		groups = json.RawMessage(`[]`)
	}
	supplier, err := json.Marshal(map[string]any{"name": payload.AccountName, "country": payload.AccountCountry, "tax_ids": payload.AccountTaxIDs, "footer": payload.Footer})
	if err != nil {
		return ProviderTaxDocument{}, err
	}
	customer, err := json.Marshal(map[string]any{"id": providerID(payload.Customer), "name": payload.CustomerName, "email": payload.CustomerEmail, "address": payload.CustomerAddress, "tax_ids": payload.CustomerTaxIDs})
	if err != nil {
		return ProviderTaxDocument{}, err
	}
	doc := ProviderTaxDocument{ID: payload.ID, Kind: kind, CustomerID: providerID(payload.Customer), Number: payload.Number, SourceInvoiceID: providerID(payload.Invoice), Status: payload.Status, Currency: strings.ToUpper(payload.Currency), Net: payload.TotalExcludingTax, Gross: *payload.Total, Livemode: payload.Livemode, IssuedAt: time.Unix(at, 0).UTC(), TaxGroups: groups, Supplier: supplier, Customer: customer, Raw: append(json.RawMessage(nil), raw...), PDFURL: payload.InvoicePDF, ProfileSHA: payload.Parent.SubscriptionDetails.Metadata["frameworks_tax_profile"]}
	if doc.PDFURL == "" {
		doc.PDFURL = payload.PDF
	}
	if doc.ProfileSHA == "" || payload.Metadata["frameworks_tax_recovered_hold"] == "true" {
		doc.ProfileSHA = payload.Metadata["frameworks_tax_profile"]
	}
	if doc.Net != nil {
		vat := doc.Gross - *doc.Net
		doc.VAT = &vat
	}
	doc.Lines, doc.MoreLines = payload.Lines.Data, payload.Lines.HasMore
	return doc, nil
}

// Mixed-rate upgrade invoices include an original-period credit. Its frozen
// rate evidence is checked independently of the replacement month's VAT.
func ValidateProviderTaxTerms(doc ProviderTaxDocument, p TaxProfile) error {
	fallback := p.incompleteDutchVATFallback()
	if doc.ProfileSHA == "" || (!fallback && (p.Name == "" || p.Country == "")) || (p.IncompleteProfile && !fallback) || p.SupplierIdentity == "" || doc.Net == nil || doc.VAT == nil {
		return errors.New("provider invoice has no matching retained tax setup")
	}
	if doc.ProfileSHA != p.Fingerprint() && doc.ProfileSHA != p.ProfileSHA {
		return errors.New("provider invoice tax setup identity differs from retained terms")
	}
	vat := func(net, bps int64) int64 {
		return decimal.NewFromInt(net).Mul(decimal.NewFromInt(bps)).Div(decimal.NewFromInt(10000)).Round(0).IntPart()
	}
	if *doc.Net+*doc.VAT != doc.Gross {
		return errors.New("provider invoice tax totals do not balance")
	}
	if len(doc.Lines) == 0 && !doc.MoreLines && vat(*doc.Net, int64(p.RateBPS)) == *doc.VAT {
		return nil
	}
	if len(doc.Lines) == 0 || doc.MoreLines {
		return errors.New("provider invoice VAT differs from retained terms; complete source lines require review")
	}
	var net, totalVAT int64
	seen := map[string]bool{}
	for _, line := range doc.Lines {
		var lineVAT int64
		for _, tax := range line.Taxes {
			lineVAT += tax.Amount
		}
		if line.Amount >= 0 {
			if lineVAT != vat(line.Amount, int64(p.RateBPS)) {
				return errors.New("provider debit line VAT differs from retained terms")
			}
		} else {
			invoice, lineID := line.Metadata["frameworks_credit_invoice"], line.Metadata["frameworks_credit_line"]
			key := invoice + ":" + lineID
			originalNet, netErr := strconv.ParseInt(line.Metadata["frameworks_credit_original_net"], 10, 64)
			originalVAT, vatErr := strconv.ParseInt(line.Metadata["frameworks_credit_original_vat"], 10, 64)
			var rates []providerCreditRate
			if invoice == "" || invoice == doc.ID || lineID == "" || seen[key] || netErr != nil || vatErr != nil || originalNet <= 0 || originalVAT < 0 || -line.Amount > originalNet || lineVAT > 0 || -lineVAT > originalVAT || json.Unmarshal([]byte(line.Metadata["frameworks_credit_rates"]), &rates) != nil || len(rates) == 0 {
				return errors.New("provider credit has no bounded original tax evidence")
			}
			seen[key] = true
			var expected, sourceVAT int64
			for _, rate := range rates {
				if rate.ID == "" || rate.RateBPS < 0 || rate.RateBPS > 10000 {
					return errors.New("provider credit original rate is invalid")
				}
				expected += vat(-line.Amount, rate.RateBPS)
				sourceVAT += vat(originalNet, rate.RateBPS)
			}
			if sourceVAT != originalVAT || lineVAT != -expected {
				return errors.New("provider credit repriced its original tax")
			}
		}
		net += line.Amount
		totalVAT += lineVAT
	}
	if net != *doc.Net || totalVAT != *doc.VAT {
		return errors.New("provider invoice lines do not reconcile to original totals")
	}
	return nil
}

type providerDocumentResource struct{ api.APIResource }

// ProviderDocumentFacts only retrieves invoice and credit-note resources. It
// cannot finalize, void, refund or collect a provider document.
func (c *Client) ProviderDocumentFacts(ctx context.Context, kind, id string) (json.RawMessage, error) {
	prefix := "/v1/invoices/"
	if kind == "provider_credit_note" {
		prefix = "/v1/credit_notes/"
	} else if kind != "provider_invoice" {
		return nil, errors.New("unsupported provider document")
	}
	if id == "" || strings.ContainsAny(id, "/?#") {
		return nil, errors.New("invalid provider document identity")
	}
	params := &api.Params{Context: ctx}
	var result providerDocumentResource
	if err := api.GetBackend(api.APIBackend).Call(http.MethodGet, prefix+id, c.secretKey, params, &result); err != nil {
		return nil, err
	}
	if result.LastResponse == nil || len(result.LastResponse.RawJSON) == 0 {
		return nil, errors.New("provider document lacks original payload")
	}
	return append(json.RawMessage(nil), result.LastResponse.RawJSON...), nil
}

func (c *Client) ProviderAccountID(ctx context.Context) (string, error) {
	page, err := c.FinancialEvidencePage(ctx, "stripe_account", "", time.Unix(0, 0), time.Now())
	if err != nil {
		return "", err
	}
	if len(page.Records) != 1 {
		return "", errors.New("provider account identity missing")
	}
	var account struct {
		ID string `json:"id"`
	}
	if err = json.Unmarshal(page.Records[0], &account); err != nil || account.ID == "" {
		return "", errors.New("provider account identity missing")
	}
	return account.ID, nil
}

func stripePDFURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return err
	}
	host := strings.ToLower(parsed.Hostname())
	if parsed.Scheme != "https" || parsed.User != nil || parsed.Fragment != "" || (host != "stripe.com" && !strings.HasSuffix(host, ".stripe.com")) {
		return errors.New("provider PDF must use an HTTPS Stripe host")
	}
	return nil
}

func (c *Client) DownloadOriginalPDF(ctx context.Context, rawURL string) ([]byte, string, error) {
	if err := stripePDFURL(rawURL); err != nil {
		return nil, "", err
	}
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many PDF redirects")
		}
		return stripePDFURL(req.URL.String())
	}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("provider PDF returned HTTP %d", response.StatusCode)
	}
	const limit = 20 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, "", err
	}
	if len(body) > limit || !strings.HasPrefix(string(body), "%PDF-") {
		return nil, "", errors.New("provider PDF has invalid content or exceeds size limit")
	}
	hash := sha256.Sum256(body)
	return body, hex.EncodeToString(hash[:]), nil
}
