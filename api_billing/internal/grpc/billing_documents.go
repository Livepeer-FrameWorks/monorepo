package grpc

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"frameworks/api_billing/internal/appconfig"
	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/handlers"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const billingDocumentContentType = "application/pdf"

type billingDocumentRow struct {
	id             string
	kind           string
	number         string
	amountCents    int64
	currency       string
	status         string
	issuedAt       time.Time
	retentionUntil time.Time
	// EUR statement of the document; unset fields are not stated by its kind.
	eurAmountCents  sql.NullInt64
	netEURCents     sql.NullInt64
	vatEURCents     sql.NullInt64
	unitsPerEUR     string
	fxReferenceDate string
}

// billingDocumentData is everything a billing document states; every kind
// renders through renderBillingDocumentPDF from it.
type billingDocumentData struct {
	Title                string
	Number               string
	SupplierName         string
	SupplierAddress      string
	SupplierVAT          string
	SupplierRegistration string
	Customer             string
	CustomerCompany      string
	CustomerAddress      []string
	CustomerVAT          string
	CustomerEmail        string
	IssuedAt             string
	IssuedTime           time.Time
	RetentionUntil       string
	Status               string
	Currency             string
	Amount               string
	Fields               []billingDocumentField
	// LineCurrency is the currency of the line amounts.
	LineCurrency string
	Lines        []billingDocumentLine
	// Totals are drawn below the lines; the last one is the amount due.
	Totals []billingDocumentField
}

type billingDocumentField struct {
	Label string
	Value string
}

// billingDocumentLine is one line item: its description, a detail line with
// its dimensions and cluster, and its formatted quantity, unit price and
// amount.
type billingDocumentLine struct {
	Description string
	Detail      string
	Quantity    string
	UnitPrice   string
	Amount      string
}

func resolveBillingDocumentTenant(ctx context.Context, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	contextTenant := middleware.GetTenantID(ctx)
	if !middleware.IsServiceCall(ctx) {
		if contextTenant == "" {
			return "", status.Error(codes.PermissionDenied, "tenant context required")
		}
		if requested != "" && requested != contextTenant {
			return "", status.Error(codes.PermissionDenied, "cross-tenant document access denied")
		}
		requested = contextTenant
	}
	if _, err := uuid.Parse(requested); err != nil {
		return "", status.Error(codes.InvalidArgument, "valid tenant_id required")
	}
	return requested, nil
}

func billingDocumentProto(row billingDocumentRow) *purserpb.BillingDocument {
	document := &purserpb.BillingDocument{
		Id: row.id, Kind: row.kind, DocumentNumber: row.number,
		AmountCents: row.amountCents, Currency: row.currency, Status: row.status,
		IssuedAt: timestamppb.New(row.issuedAt), RetentionUntil: timestamppb.New(row.retentionUntil),
		DownloadFilename: row.number + ".pdf",
		UnitsPerEur:      decimalText(row.unitsPerEUR),
		FxReferenceDate:  row.fxReferenceDate,
	}
	if row.eurAmountCents.Valid {
		document.EurAmountCents = &row.eurAmountCents.Int64
	}
	if row.netEURCents.Valid {
		document.NetEurCents = &row.netEURCents.Int64
	}
	if row.vatEURCents.Valid {
		document.VatEurCents = &row.vatEURCents.Int64
	}
	return document
}

// ListBillingDocuments lists immutable customer-facing documents for one tenant.
func (s *PurserServer) ListBillingDocuments(ctx context.Context, req *purserpb.ListBillingDocumentsRequest) (*purserpb.ListBillingDocumentsResponse, error) {
	tenantID, err := resolveBillingDocumentTenant(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	rows, err := purserdb.New(s.db).ListBillingDocuments(ctx, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list billing documents: %v", err)
	}
	response := &purserpb.ListBillingDocumentsResponse{}
	for _, item := range rows {
		row := billingDocumentRow{
			id: item.ID, kind: item.Kind, number: item.DocumentNumber,
			amountCents: item.AmountCents, currency: item.Currency, status: item.Status,
			issuedAt: item.IssuedAt.Time, retentionUntil: item.RetentionUntil,
			eurAmountCents: sql.NullInt64{Int64: item.EurAmountCents, Valid: item.HasEurAmount},
			netEURCents:    item.NetEurCents, vatEURCents: item.VatEurCents,
			unitsPerEUR: item.UnitsPerEur, fxReferenceDate: item.FxReferenceDate,
		}
		response.Documents = append(response.Documents, billingDocumentProto(row))
	}
	return response, nil
}

// setEUR records the EUR total, net, VAT, and conversion rate a tax document
// states.
func (row *billingDocumentRow) setEUR(totalCents, netCents, vatCents int64, unitsPerEUR string, referenceDate time.Time) {
	row.eurAmountCents = sql.NullInt64{Int64: totalCents, Valid: true}
	row.netEURCents = sql.NullInt64{Int64: netCents, Valid: true}
	row.vatEURCents = sql.NullInt64{Int64: vatCents, Valid: true}
	row.unitsPerEUR, row.fxReferenceDate = unitsPerEUR, dateText(referenceDate)
}

func moneyString(cents int64) string {
	negative := cents < 0
	if negative {
		cents = -cents
	}
	value := fmt.Sprintf("%d.%02d", cents/100, cents%100)
	if negative {
		return "-" + value
	}
	return value
}

// documentTime states a document timestamp in UTC to the second.
func documentTime(at time.Time) string {
	return at.UTC().Format("2006-01-02 15:04:05") + " UTC"
}

// billingPeriodField states the period a document covers.
func billingPeriodField(start, end sql.NullTime) []billingDocumentField {
	if !start.Valid || !end.Valid {
		return nil
	}
	const minute = "2006-01-02 15:04"
	return []billingDocumentField{{
		Label: "Billing period",
		Value: start.Time.UTC().Format(minute) + " UTC to " + end.Time.UTC().Format(minute) + " UTC",
	}}
}

func renderBillingDocument(row billingDocumentRow, data billingDocumentData) (*purserpb.GetBillingDocumentResponse, error) {
	data.Number = row.number
	data.IssuedTime = row.issuedAt.UTC()
	data.IssuedAt = documentTime(row.issuedAt)
	data.RetentionUntil = row.retentionUntil.UTC().Format(time.DateOnly)
	data.Status = row.status
	data.Currency = row.currency
	data.Amount = moneyString(row.amountCents)
	content, err := renderBillingDocumentPDF(data)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "render billing document: %v", err)
	}
	digest := sha256.Sum256(content)
	return &purserpb.GetBillingDocumentResponse{
		Document: billingDocumentProto(row), ContentType: billingDocumentContentType,
		Content: content, Sha256: hex.EncodeToString(digest[:]),
	}, nil
}

// exchangeRateFields states the ECB rate a document's EUR amounts were
// converted at, as units of the document currency per euro.
func exchangeRateFields(currency, unitsPerEUR string, referenceDate time.Time) []billingDocumentField {
	return []billingDocumentField{
		{Label: "Exchange rate", Value: fmt.Sprintf("1 EUR = %s %s", decimalText(unitsPerEUR), currency)},
		{Label: "Rate reference date", Value: referenceDate.UTC().Format(time.DateOnly)},
	}
}

// documentLines states persisted invoice line items and their total in
// cents. Amounts are in the currency the lines were rated in; quantities
// carry their unit.
func documentLines(rows []purserdb.ListInvoiceEmailLineItemsRow) (string, []billingDocumentLine, int64) {
	currency := ""
	var totalCents int64
	lines := make([]billingDocumentLine, 0, len(rows))
	for _, row := range rows {
		currency = strings.TrimSpace(row.Currency)
		if amount, err := decimal.NewFromString(row.Amount); err == nil {
			totalCents += amount.Shift(2).Round(0).IntPart()
		}
		var detail []string
		if label := handlers.LineItemDimensionLabel(row.Dimensions); label != "" {
			detail = append(detail, label)
		}
		if row.ClusterID != "" {
			detail = append(detail, "cluster "+row.ClusterID)
		}
		quantity := quantityText(row.Quantity)
		if row.Unit != "" {
			quantity += " " + row.Unit
		}
		lines = append(lines, billingDocumentLine{
			Description: row.Description, Detail: strings.Join(detail, " · "),
			Quantity: quantity, UnitPrice: unitPriceText(row.UnitPrice), Amount: amountText(row.Amount),
		})
	}
	return currency, lines, totalCents
}

// quantityText states a NUMERIC quantity without trailing zeros.
func quantityText(value string) string {
	quantity, err := decimal.NewFromString(value)
	if err != nil {
		return value
	}
	return quantity.String()
}

// unitPriceText states a NUMERIC unit price with at least two decimals and
// no trailing zeros beyond them.
func unitPriceText(value string) string {
	price, err := decimal.NewFromString(value)
	if err != nil {
		return value
	}
	if price.Equal(price.Round(2)) {
		return price.StringFixed(2)
	}
	return price.String()
}

func amountText(value string) string {
	amount, err := decimal.NewFromString(value)
	if err != nil {
		return value
	}
	return amount.StringFixed(2)
}

// invoiceTotals states an invoice's subtotal (its lines, or its base and
// metered amounts when it has none), the prepaid credit it applied, what the
// collection minimum carried, and the amount due in EUR and, when presented
// in another currency, in that currency.
func invoiceTotals(document purserdb.GetInvoiceDocumentRow, lines []billingDocumentLine, linesCents int64, currency string, amountCents int64) []billingDocumentField {
	eur := func(cents int64) string {
		if cents < 0 {
			return "-EUR " + moneyString(-cents)
		}
		return "EUR " + moneyString(cents)
	}
	subtotal := document.BaseAmountCents + document.MeteredAmountCents
	if len(lines) > 0 {
		subtotal = linesCents
	}
	totals := []billingDocumentField{{Label: "Subtotal", Value: eur(subtotal)}}
	if document.PrepaidCreditCents != 0 {
		totals = append(totals, billingDocumentField{Label: "Prepaid credit applied", Value: eur(-document.PrepaidCreditCents)})
	}
	if carried := document.EurAmountCents - max(subtotal-document.PrepaidCreditCents, 0); carried != 0 && document.CollectionMinimumApplied {
		label := "Carried from earlier invoices (collection minimum)"
		if carried < 0 {
			label = "Carried to a later invoice (collection minimum)"
		}
		totals = append(totals, billingDocumentField{Label: label, Value: eur(carried)})
	}
	totals = append(totals, billingDocumentField{Label: "Amount due (EUR)", Value: eur(document.EurAmountCents)})
	if currency != "EUR" {
		totals = append(totals, billingDocumentField{Label: "Amount due", Value: currency + " " + moneyString(amountCents)})
	}
	return totals
}

// customerAddressLines states a billing address stored as JSON (street,
// postal code and city, state, country), or the stored text when it is not
// JSON.
func customerAddressLines(stored string) []string {
	stored = strings.TrimSpace(stored)
	if stored == "" || stored == "{}" {
		return nil
	}
	address := scanBillingAddress([]byte(stored))
	if address == nil {
		return []string{stored}
	}
	var lines []string
	for _, line := range []string{
		address.GetStreet(),
		strings.TrimSpace(address.GetPostalCode() + " " + address.GetCity()),
		address.GetState(),
		strings.ToUpper(address.GetCountry()),
	} {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// prepaidStatementFields states a prepaid statement: not a bill, what the
// period's usage rated to, what the prepaid balance paid, and the balance.
func prepaidStatementFields(periodStart, periodEnd sql.NullTime, statement handlers.PrepaidStatementDetails) []billingDocumentField {
	eur := func(cents int64) string { return "EUR " + moneyString(cents) }
	fields := []billingDocumentField{
		{Label: "Document", Value: "Statement of a prepaid balance. Not an invoice: the prepaid balance paid for the usage stated as it was reported, and nothing is due."},
	}
	if statement.ClosesPrepaidPhase {
		fields = append(fields, billingDocumentField{Label: "Closes", Value: "Prepaid billing, at the switch to postpaid billing"})
	}
	fields = append(fields, billingPeriodField(periodStart, periodEnd)...)
	fields = append(fields,
		billingDocumentField{Label: "Usage at rated prices", Value: eur(statement.RatedUsageCents)},
		billingDocumentField{Label: "Paid from prepaid balance for this usage", Value: eur(statement.PaidFromBalanceCents)},
	)
	if statement.PeriodFeesCents != 0 {
		fields = append(fields, billingDocumentField{Label: "Monthly fees charged to prepaid balance", Value: eur(statement.PeriodFeesCents)})
	}
	fields = append(fields,
		billingDocumentField{Label: "Balance at period start", Value: eur(statement.OpeningBalanceCents)},
		billingDocumentField{Label: fmt.Sprintf("Top-ups (%d)", statement.Topups), Value: eur(statement.TopupCents)},
		billingDocumentField{Label: "Usage deducted in period", Value: eur(-statement.UsagePostedCents)},
	)
	if statement.OtherMovementsCents != 0 {
		fields = append(fields, billingDocumentField{Label: "Other balance changes", Value: eur(statement.OtherMovementsCents)})
	}
	fields = append(fields, billingDocumentField{Label: "Balance at period end", Value: eur(statement.PeriodEndBalanceCents)})
	if statement.PeriodFeesCents != 0 {
		fields = append(fields, billingDocumentField{Label: "Balance after monthly fees", Value: eur(statement.ClosingBalanceCents)})
	}
	return append(fields, billingDocumentField{Label: "Amount due", Value: "EUR 0.00, nothing to pay"})
}

func supplierDocumentFields() (string, string, string, string) {
	rt := appconfig.Runtime()
	return rt.SupplierName, rt.SupplierAddress, rt.SupplierVATNumber, rt.SupplierRegistrationNumber
}

// missingDocumentSupplierFields names the SUPPLIER_* keys whose values a
// document needs and lacks. Crypto documents carry the supplier identity they
// were issued with, which takes the place of the configured one.
func missingDocumentSupplierFields(base billingDocumentData) []string {
	var missing []string
	for _, field := range []struct{ key, value string }{
		{"SUPPLIER_NAME", base.SupplierName},
		{"SUPPLIER_ADDRESS", base.SupplierAddress},
		{"SUPPLIER_VAT_NUMBER", base.SupplierVAT},
		{"SUPPLIER_REGISTRATION_NUMBER", base.SupplierRegistration},
	} {
		if strings.TrimSpace(field.value) == "" {
			missing = append(missing, field.key)
		}
	}
	return missing
}

// GetBillingDocument renders one tenant-owned document as a PDF. Rendering
// only uses persisted financial evidence and the tenant's billing details.
func (s *PurserServer) GetBillingDocument(ctx context.Context, req *purserpb.GetBillingDocumentRequest) (*purserpb.GetBillingDocumentResponse, error) { //nolint:gocyclo,cyclop,funlen // Each document kind has a deliberately explicit tenant-scoped evidence query.
	tenantID, err := resolveBillingDocumentTenant(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	documentID := strings.TrimSpace(req.GetDocumentId())
	if _, parseErr := uuid.Parse(documentID); parseErr != nil {
		return nil, status.Error(codes.InvalidArgument, "valid document_id required")
	}
	kind := strings.TrimSpace(req.GetKind())
	supplierName, supplierAddress, supplierVAT, supplierRegistration := supplierDocumentFields()
	base := billingDocumentData{SupplierName: supplierName, SupplierAddress: supplierAddress, SupplierVAT: supplierVAT, SupplierRegistration: supplierRegistration}
	var row billingDocumentRow
	row.id, row.kind = documentID, kind
	queries := purserdb.New(s.db)
	setCustomer := func(name, company, address, vat, email string) {
		base.Customer, base.CustomerCompany = strings.TrimSpace(name), strings.TrimSpace(company)
		base.CustomerAddress = customerAddressLines(address)
		base.CustomerVAT, base.CustomerEmail = strings.TrimSpace(vat), strings.TrimSpace(email)
	}
	// setLines states the document's persisted line items.
	var linesCents int64
	setLines := func() error {
		items, linesErr := queries.ListInvoiceEmailLineItems(ctx, purserdb.ListInvoiceEmailLineItemsParams{InvoiceID: documentID, TenantID: tenantID})
		if linesErr != nil {
			return status.Errorf(codes.Internal, "load billing document lines: %v", linesErr)
		}
		base.LineCurrency, base.Lines, linesCents = documentLines(items)
		return nil
	}
	switch kind {
	case "invoice":
		var document purserdb.GetInvoiceDocumentRow
		document, err = queries.GetInvoiceDocument(ctx, purserdb.GetInvoiceDocumentParams{DocumentID: documentID, TenantID: tenantID})
		if err != nil {
			break
		}
		row.number, row.amountCents, row.currency, row.status = document.InvoiceNumber, document.AmountCents, document.Currency, document.Status
		row.issuedAt, row.retentionUntil = document.IssuedAt.Time, document.RetentionUntil
		setCustomer(document.CustomerName, document.CustomerCompany, document.CustomerAddress, document.CustomerVat, document.CustomerEmail)
		base.Title = "Invoice"
		base.Fields = append(base.Fields, billingPeriodField(document.PeriodStart, document.PeriodEnd)...)
		base.Fields = append(base.Fields, billingDocumentField{Label: "Due", Value: document.DueDate.UTC().Format(time.DateOnly)})
		if document.PresentmentUnitsPerEur != "" && document.PresentmentReferenceDate.Valid {
			base.Fields = append(base.Fields, exchangeRateFields(row.currency, document.PresentmentUnitsPerEur, document.PresentmentReferenceDate.Time)...)
			row.eurAmountCents = sql.NullInt64{Int64: document.EurAmountCents, Valid: true}
			row.unitsPerEUR, row.fxReferenceDate = document.PresentmentUnitsPerEur, dateText(document.PresentmentReferenceDate.Time)
		}
		if err = setLines(); err != nil {
			return nil, err
		}
		if base.LineCurrency == "" {
			base.LineCurrency = "EUR"
		}
		base.Totals = invoiceTotals(document, base.Lines, linesCents, row.currency, row.amountCents)
	case "prepaid_statement":
		var document purserdb.GetPrepaidStatementDocumentRow
		document, err = queries.GetPrepaidStatementDocument(ctx, purserdb.GetPrepaidStatementDocumentParams{DocumentID: documentID, TenantID: tenantID})
		if err != nil {
			break
		}
		var details struct {
			Statement handlers.PrepaidStatementDetails `json:"statement"`
		}
		if err = json.Unmarshal(document.UsageDetails, &details); err != nil {
			return nil, status.Errorf(codes.Internal, "decode prepaid statement: %v", err)
		}
		row.number, row.amountCents, row.currency, row.status = document.InvoiceNumber, 0, "EUR", document.Status
		row.issuedAt, row.retentionUntil = document.IssuedAt.Time, document.RetentionUntil
		setCustomer(document.CustomerName, document.CustomerCompany, document.CustomerAddress, document.CustomerVat, document.CustomerEmail)
		base.Title = "Prepaid balance statement"
		base.Fields = append(base.Fields, prepaidStatementFields(document.PeriodStart, document.PeriodEnd, details.Statement)...)
		if err = setLines(); err != nil {
			return nil, err
		}
	case "simplified_invoice":
		var document purserdb.GetSimplifiedInvoiceDocumentRow
		document, err = queries.GetSimplifiedInvoiceDocument(ctx, purserdb.GetSimplifiedInvoiceDocumentParams{DocumentID: documentID, TenantID: tenantID})
		row.number, row.amountCents, row.currency, row.status = document.InvoiceNumber, document.GrossAmountCents, document.Currency, document.TaxValidationStatus
		row.issuedAt, row.retentionUntil = document.IssuedAt, document.RetentionUntil
		base.SupplierName, base.SupplierAddress = document.SupplierName, document.SupplierAddress
		base.SupplierVAT, base.SupplierRegistration = document.SupplierVatNumber, document.SupplierRegistrationNumber
		setCustomer(document.CustomerName, document.CustomerCompany, document.CustomerAddress, document.CustomerVat, document.CustomerEmail)
		base.Title = "Simplified invoice"
		base.Fields = append(base.Fields,
			billingDocumentField{Label: "Net", Value: row.currency + " " + moneyString(document.NetAmountCents)},
			billingDocumentField{Label: "VAT", Value: fmt.Sprintf("%s %s (%0.2f%%)", row.currency, moneyString(document.VatAmountCents), float64(document.VatRateBps)/100)},
			billingDocumentField{Label: "Total (EUR)", Value: "EUR " + moneyString(document.AmountEurCents)},
			billingDocumentField{Label: "Net (EUR)", Value: "EUR " + moneyString(document.NetEurCents)},
			billingDocumentField{Label: "VAT (EUR)", Value: "EUR " + moneyString(document.VatEurCents)},
		)
		if document.FxUnitsPerEur != "" {
			base.Fields = append(base.Fields, exchangeRateFields(row.currency, document.FxUnitsPerEur, document.FxReferenceDate)...)
		}
		row.setEUR(document.AmountEurCents, document.NetEurCents, document.VatEurCents, document.FxUnitsPerEur, document.FxReferenceDate)
		base.Fields = append(base.Fields,
			billingDocumentField{Label: "Service", Value: document.ServiceDescription},
			billingDocumentField{Label: "Quantity", Value: fmt.Sprintf("%d", document.ServiceQuantity)},
			billingDocumentField{Label: "Supply date", Value: document.ServiceDate.Time.Format("2006-01-02")},
			billingDocumentField{Label: "Settlement reference", Value: document.ReferenceType + ":" + document.ReferenceID},
		)
		if document.TaxValidationStatus == "reverse_charge" {
			base.Fields = append(base.Fields, billingDocumentField{Label: "VAT treatment", Value: "Reverse charge — btw verlegd"})
		}
	case "crypto_invoice":
		var document purserdb.GetCryptoInvoiceDocumentRow
		document, err = queries.GetCryptoInvoiceDocument(ctx, purserdb.GetCryptoInvoiceDocumentParams{DocumentID: documentID, TenantID: tenantID})
		row.number, row.amountCents, row.currency, row.status = document.InvoiceNumber, document.GrossAmountCents, document.Currency, document.TaxValidationStatus
		row.issuedAt, row.retentionUntil = document.IssuedAt, document.RetentionUntil
		base.SupplierName, base.SupplierAddress = document.SupplierName, document.SupplierAddress
		base.SupplierVAT, base.SupplierRegistration = document.SupplierVatNumber, document.SupplierRegistrationNumber
		setCustomer(document.CustomerName, document.CustomerCompany, document.CustomerAddress, document.CustomerVat, document.CustomerEmail)
		base.Title = "Invoice"
		base.Fields = append(base.Fields,
			billingDocumentField{Label: "Net", Value: row.currency + " " + moneyString(document.NetAmountCents)},
			billingDocumentField{Label: "VAT", Value: fmt.Sprintf("%s %s (%0.2f%%)", row.currency, moneyString(document.VatAmountCents), float64(document.VatRateBps)/100)},
			billingDocumentField{Label: "Total (EUR)", Value: "EUR " + moneyString(document.AmountEurCents)},
			billingDocumentField{Label: "Net (EUR)", Value: "EUR " + moneyString(document.NetEurCents)},
			billingDocumentField{Label: "VAT (EUR)", Value: "EUR " + moneyString(document.VatEurCents)},
		)
		if document.FxUnitsPerEur != "" {
			base.Fields = append(base.Fields, exchangeRateFields(row.currency, document.FxUnitsPerEur, document.FxReferenceDate)...)
		}
		row.setEUR(document.AmountEurCents, document.NetEurCents, document.VatEurCents, document.FxUnitsPerEur, document.FxReferenceDate)
		base.Fields = append(base.Fields,
			billingDocumentField{Label: "Service", Value: document.ServiceDescription},
			billingDocumentField{Label: "Quantity", Value: fmt.Sprintf("%d", document.ServiceQuantity)},
			billingDocumentField{Label: "Supply date", Value: document.ServiceDate.Format("2006-01-02")},
			billingDocumentField{Label: "Settlement reference", Value: document.ReferenceType + ":" + document.ReferenceID},
		)
		if document.TaxValidationStatus == "reverse_charge" {
			base.Fields = append(base.Fields, billingDocumentField{Label: "VAT treatment", Value: "Reverse charge — btw verlegd"})
		}
	case "payment_receipt":
		var document purserdb.GetPaymentReceiptDocumentRow
		document, err = queries.GetPaymentReceiptDocument(ctx, purserdb.GetPaymentReceiptDocumentParams{DocumentID: documentID, TenantID: tenantID})
		row.number, row.amountCents, row.currency, row.status = document.DocumentNumber, document.AmountCents, document.Currency, document.Status
		row.issuedAt, row.retentionUntil = document.IssuedAt.Time, document.RetentionUntil
		setCustomer(document.CustomerName, document.CustomerCompany, document.CustomerAddress, document.CustomerVat, document.CustomerEmail)
		base.Title = "Payment receipt"
		base.Fields = append(base.Fields, billingDocumentField{Label: "Method", Value: document.Method})
		if document.TxID.Valid {
			base.Fields = append(base.Fields, billingDocumentField{Label: "Settlement reference", Value: document.TxID.String})
		}
		row.eurAmountCents = sql.NullInt64{Int64: document.EurAmountCents, Valid: true}
		row.unitsPerEUR, row.fxReferenceDate = document.FxUnitsPerEur, dateText(document.FxReferenceDate)
	case "credit_note":
		var document purserdb.GetCreditNoteDocumentRow
		document, err = queries.GetCreditNoteDocument(ctx, purserdb.GetCreditNoteDocumentParams{DocumentID: documentID, TenantID: tenantID})
		row.number, row.amountCents, row.currency, row.status = document.CreditNoteNumber, document.AmountCents, document.Currency, "issued"
		row.issuedAt, row.retentionUntil = document.IssuedAt, document.RetentionUntil
		setCustomer(document.CustomerName, document.CustomerCompany, document.CustomerAddress, document.CustomerVat, document.CustomerEmail)
		base.Title = "Credit note"
		base.Fields = append(base.Fields,
			billingDocumentField{Label: "Original document", Value: document.SourceDocumentType + ":" + document.SourceDocumentID},
			billingDocumentField{Label: "Reversal reference", Value: document.ReversalReferenceType + ":" + document.ReversalReferenceID},
			billingDocumentField{Label: "Reason", Value: document.Reason},
		)
		base.Totals = []billingDocumentField{{Label: "Total credited", Value: row.currency + " " + moneyString(row.amountCents)}}
	default:
		return nil, status.Error(codes.InvalidArgument, "unsupported billing document kind")
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "billing document not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "load billing document: %v", err)
	}
	if missing := missingDocumentSupplierFields(base); len(missing) > 0 {
		s.logger.WithFields(logging.Fields{"document_id": documentID, "kind": kind, "missing": missing}).Error("Billing document refused: the supplier identity is not configured")
		return nil, status.Errorf(codes.FailedPrecondition, "supplier information is not configured for document rendering: %s", strings.Join(missing, ", "))
	}
	return renderBillingDocument(row, base)
}
