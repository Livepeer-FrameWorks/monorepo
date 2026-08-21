package grpc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"strings"
	"time"

	"frameworks/api_billing/internal/database/purserdb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const billingDocumentContentType = "text/html; charset=utf-8"

type billingDocumentRow struct {
	id             string
	kind           string
	number         string
	amountCents    int64
	currency       string
	status         string
	issuedAt       time.Time
	retentionUntil time.Time
}

type billingDocumentHTMLData struct {
	Title                string
	Number               string
	SupplierName         string
	SupplierAddress      string
	SupplierVAT          string
	SupplierRegistration string
	Customer             string
	CustomerCompany      string
	CustomerAddress      string
	CustomerVAT          string
	IssuedAt             string
	RetentionUntil       string
	Status               string
	Currency             string
	Amount               string
	Fields               []billingDocumentHTMLField
}

type billingDocumentHTMLField struct {
	Label string
	Value string
}

var billingDocumentTemplate = template.Must(template.New("billing-document").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width">
<title>{{.Title}} {{.Number}}</title>
<style>body{font:15px system-ui,sans-serif;color:#18202a;max-width:820px;margin:48px auto;padding:0 24px}header{display:flex;justify-content:space-between;gap:32px;border-bottom:2px solid #18202a;padding-bottom:20px}h1{margin:0}.money{font-size:28px;font-weight:700}.parties{display:grid;grid-template-columns:1fr 1fr;gap:32px;margin:28px 0}dl{display:grid;grid-template-columns:180px 1fr;gap:8px 16px}dt{font-weight:600}dd{margin:0;overflow-wrap:anywhere}footer{margin-top:40px;padding-top:16px;border-top:1px solid #ccd3da;color:#53606d;font-size:12px}@media print{body{margin:0}}</style>
</head><body><header><div><h1>{{.Title}}</h1><div>{{.Number}}</div></div><div><div class="money">{{.Currency}} {{.Amount}}</div><div>{{.Status}}</div></div></header>
<section class="parties"><div><h2>Supplier</h2><div>{{.SupplierName}}</div><div>{{.SupplierAddress}}</div><div>VAT: {{.SupplierVAT}}</div><div>Registration: {{.SupplierRegistration}}</div></div><div><h2>Customer</h2><div>{{.Customer}}</div><div>{{.CustomerCompany}}</div><div>{{.CustomerAddress}}</div><div>{{if .CustomerVAT}}VAT: {{.CustomerVAT}}{{end}}</div></div></section>
<dl><dt>Issued</dt><dd>{{.IssuedAt}}</dd>{{range .Fields}}<dt>{{.Label}}</dt><dd>{{.Value}}</dd>{{end}}</dl>
<footer>Retained until at least {{.RetentionUntil}}. Document number and settlement references are immutable audit identifiers.</footer>
</body></html>`))

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
	return &purserpb.BillingDocument{
		Id: row.id, Kind: row.kind, DocumentNumber: row.number,
		AmountCents: row.amountCents, Currency: row.currency, Status: row.status,
		IssuedAt: timestamppb.New(row.issuedAt), RetentionUntil: timestamppb.New(row.retentionUntil),
		DownloadFilename: row.number + ".html",
	}
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
		}
		response.Documents = append(response.Documents, billingDocumentProto(row))
	}
	return response, nil
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

func renderBillingDocument(row billingDocumentRow, data billingDocumentHTMLData) (*purserpb.GetBillingDocumentResponse, error) {
	data.Number = row.number
	data.IssuedAt = row.issuedAt.UTC().Format(time.RFC3339)
	data.RetentionUntil = row.retentionUntil.UTC().Format("2006-01-02")
	data.Status = row.status
	data.Currency = row.currency
	data.Amount = moneyString(row.amountCents)
	var content bytes.Buffer
	if err := billingDocumentTemplate.Execute(&content, data); err != nil {
		return nil, status.Errorf(codes.Internal, "render billing document: %v", err)
	}
	digest := sha256.Sum256(content.Bytes())
	return &purserpb.GetBillingDocumentResponse{
		Document: billingDocumentProto(row), ContentType: billingDocumentContentType,
		Content: content.Bytes(), Sha256: hex.EncodeToString(digest[:]),
	}, nil
}

func supplierDocumentFields() (string, string, string, string) {
	return config.GetEnv("SUPPLIER_NAME", ""), config.GetEnv("SUPPLIER_ADDRESS", ""), config.GetEnv("SUPPLIER_VAT_NUMBER", ""), config.GetEnv("SUPPLIER_REGISTRATION_NUMBER", "")
}

func scanCustomer(name, company, address, vat *sql.NullString) (string, string, string, string) {
	return name.String, company.String, address.String, vat.String
}

// GetBillingDocument renders one tenant-owned document as a self-contained,
// printable HTML attachment. Rendering only uses persisted financial evidence.
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
	base := billingDocumentHTMLData{SupplierName: supplierName, SupplierAddress: supplierAddress, SupplierVAT: supplierVAT, SupplierRegistration: supplierRegistration}
	var row billingDocumentRow
	row.id, row.kind = documentID, kind
	var name, company, address, vat sql.NullString
	queries := purserdb.New(s.db)
	setCustomer := func(customerName, customerCompany, customerAddress, customerVAT string) {
		name = sql.NullString{String: customerName, Valid: customerName != ""}
		company = sql.NullString{String: customerCompany, Valid: customerCompany != ""}
		address = sql.NullString{String: customerAddress, Valid: customerAddress != ""}
		vat = sql.NullString{String: customerVAT, Valid: customerVAT != ""}
	}
	switch kind {
	case "invoice":
		var document purserdb.GetInvoiceDocumentRow
		document, err = queries.GetInvoiceDocument(ctx, purserdb.GetInvoiceDocumentParams{DocumentID: documentID, TenantID: tenantID})
		row.number, row.amountCents, row.currency, row.status = document.InvoiceNumber, document.AmountCents, document.Currency, document.Status
		row.issuedAt, row.retentionUntil = document.IssuedAt.Time, document.RetentionUntil
		setCustomer(document.CustomerName, document.CustomerCompany, document.CustomerAddress, document.CustomerVat)
		base.Title = "Invoice"
		if document.PeriodStart.Valid {
			base.Fields = append(base.Fields, billingDocumentHTMLField{Label: "Period start", Value: document.PeriodStart.Time.UTC().Format(time.RFC3339)})
		}
		if document.PeriodEnd.Valid {
			base.Fields = append(base.Fields, billingDocumentHTMLField{Label: "Period end", Value: document.PeriodEnd.Time.UTC().Format(time.RFC3339)})
		}
		base.Fields = append(base.Fields, billingDocumentHTMLField{Label: "Due", Value: document.DueDate.UTC().Format(time.RFC3339)})
	case "simplified_invoice":
		var document purserdb.GetSimplifiedInvoiceDocumentRow
		document, err = queries.GetSimplifiedInvoiceDocument(ctx, purserdb.GetSimplifiedInvoiceDocumentParams{DocumentID: documentID, TenantID: tenantID})
		row.number, row.amountCents, row.currency, row.status = document.InvoiceNumber, document.GrossAmountCents, document.Currency, document.TaxValidationStatus
		row.issuedAt, row.retentionUntil = document.IssuedAt, document.RetentionUntil
		base.SupplierName, base.SupplierAddress = document.SupplierName, document.SupplierAddress
		base.SupplierVAT, base.SupplierRegistration = document.SupplierVatNumber, document.SupplierRegistrationNumber
		setCustomer(document.CustomerName, document.CustomerCompany, document.CustomerAddress, document.CustomerVat)
		base.Title = "Simplified invoice"
		base.Fields = append(base.Fields,
			billingDocumentHTMLField{Label: "Net", Value: row.currency + " " + moneyString(document.NetAmountCents)},
			billingDocumentHTMLField{Label: "VAT", Value: fmt.Sprintf("%s %s (%0.2f%%)", row.currency, moneyString(document.VatAmountCents), float64(document.VatRateBps)/100)},
			billingDocumentHTMLField{Label: "Service", Value: document.ServiceDescription},
			billingDocumentHTMLField{Label: "Quantity", Value: fmt.Sprintf("%d", document.ServiceQuantity)},
			billingDocumentHTMLField{Label: "Supply date", Value: document.ServiceDate.Time.Format("2006-01-02")},
			billingDocumentHTMLField{Label: "Settlement reference", Value: document.ReferenceType + ":" + document.ReferenceID},
		)
		if document.TaxValidationStatus == "reverse_charge" {
			base.Fields = append(base.Fields, billingDocumentHTMLField{Label: "VAT treatment", Value: "Reverse charge — btw verlegd"})
		}
	case "crypto_invoice":
		var document purserdb.GetCryptoInvoiceDocumentRow
		document, err = queries.GetCryptoInvoiceDocument(ctx, purserdb.GetCryptoInvoiceDocumentParams{DocumentID: documentID, TenantID: tenantID})
		row.number, row.amountCents, row.currency, row.status = document.InvoiceNumber, document.GrossAmountCents, document.Currency, document.TaxValidationStatus
		row.issuedAt, row.retentionUntil = document.IssuedAt, document.RetentionUntil
		base.SupplierName, base.SupplierAddress = document.SupplierName, document.SupplierAddress
		base.SupplierVAT, base.SupplierRegistration = document.SupplierVatNumber, document.SupplierRegistrationNumber
		setCustomer(document.CustomerName, document.CustomerCompany, document.CustomerAddress, document.CustomerVat)
		base.Title = "Invoice"
		base.Fields = append(base.Fields,
			billingDocumentHTMLField{Label: "Billing email", Value: document.CustomerEmail},
			billingDocumentHTMLField{Label: "Net", Value: row.currency + " " + moneyString(document.NetAmountCents)},
			billingDocumentHTMLField{Label: "VAT", Value: fmt.Sprintf("%s %s (%0.2f%%)", row.currency, moneyString(document.VatAmountCents), float64(document.VatRateBps)/100)},
			billingDocumentHTMLField{Label: "Service", Value: document.ServiceDescription},
			billingDocumentHTMLField{Label: "Quantity", Value: fmt.Sprintf("%d", document.ServiceQuantity)},
			billingDocumentHTMLField{Label: "Supply date", Value: document.ServiceDate.Format("2006-01-02")},
			billingDocumentHTMLField{Label: "Settlement reference", Value: document.ReferenceType + ":" + document.ReferenceID},
		)
		if document.TaxValidationStatus == "reverse_charge" {
			base.Fields = append(base.Fields, billingDocumentHTMLField{Label: "VAT treatment", Value: "Reverse charge — btw verlegd"})
		}
	case "payment_receipt":
		var document purserdb.GetPaymentReceiptDocumentRow
		document, err = queries.GetPaymentReceiptDocument(ctx, purserdb.GetPaymentReceiptDocumentParams{DocumentID: documentID, TenantID: tenantID})
		row.number, row.amountCents, row.currency, row.status = document.DocumentNumber, document.AmountCents, document.Currency, document.Status
		row.issuedAt, row.retentionUntil = document.IssuedAt.Time, document.RetentionUntil
		setCustomer(document.CustomerName, document.CustomerCompany, document.CustomerAddress, document.CustomerVat)
		base.Title = "Payment receipt"
		base.Fields = append(base.Fields, billingDocumentHTMLField{Label: "Method", Value: document.Method})
		if document.TxID.Valid {
			base.Fields = append(base.Fields, billingDocumentHTMLField{Label: "Settlement reference", Value: document.TxID.String})
		}
	case "credit_note":
		var document purserdb.GetCreditNoteDocumentRow
		document, err = queries.GetCreditNoteDocument(ctx, purserdb.GetCreditNoteDocumentParams{DocumentID: documentID, TenantID: tenantID})
		row.number, row.amountCents, row.currency, row.status = document.CreditNoteNumber, document.AmountCents, document.Currency, "issued"
		row.issuedAt, row.retentionUntil = document.IssuedAt, document.RetentionUntil
		setCustomer(document.CustomerName, document.CustomerCompany, document.CustomerAddress, document.CustomerVat)
		base.Title = "Credit note"
		base.Fields = append(base.Fields,
			billingDocumentHTMLField{Label: "Original document", Value: document.SourceDocumentType + ":" + document.SourceDocumentID},
			billingDocumentHTMLField{Label: "Reversal reference", Value: document.ReversalReferenceType + ":" + document.ReversalReferenceID},
			billingDocumentHTMLField{Label: "Reason", Value: document.Reason},
		)
	default:
		return nil, status.Error(codes.InvalidArgument, "unsupported billing document kind")
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "billing document not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "load billing document: %v", err)
	}
	if strings.TrimSpace(base.SupplierName) == "" || strings.TrimSpace(base.SupplierAddress) == "" || strings.TrimSpace(base.SupplierVAT) == "" || strings.TrimSpace(base.SupplierRegistration) == "" {
		return nil, status.Error(codes.FailedPrecondition, "supplier information is not configured for document rendering")
	}
	base.Customer, base.CustomerCompany, base.CustomerAddress, base.CustomerVAT = scanCustomer(&name, &company, &address, &vat)
	return renderBillingDocument(row, base)
}
