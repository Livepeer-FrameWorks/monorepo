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
	Title           string
	Number          string
	SupplierName    string
	SupplierAddress string
	SupplierVAT     string
	Customer        string
	CustomerAddress string
	CustomerVAT     string
	IssuedAt        string
	RetentionUntil  string
	Status          string
	Currency        string
	Amount          string
	Fields          []billingDocumentHTMLField
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
<section class="parties"><div><h2>Supplier</h2><div>{{.SupplierName}}</div><div>{{.SupplierAddress}}</div><div>{{.SupplierVAT}}</div></div><div><h2>Customer</h2><div>{{.Customer}}</div><div>{{.CustomerAddress}}</div><div>{{.CustomerVAT}}</div></div></section>
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
	rows, err := s.db.QueryContext(ctx, `
		SELECT id::text, kind, document_number, amount_cents, currency, status, issued_at, retention_until
		FROM (
			SELECT id, 'invoice'::text AS kind, invoice_number AS document_number,
			       ROUND(amount * 100)::bigint AS amount_cents, currency, status,
			       COALESCE(created_at, NOW()) AS issued_at, retention_until
			FROM purser.billing_invoices WHERE tenant_id = $1 AND status <> 'draft'
			UNION ALL
			SELECT id, 'simplified_invoice', invoice_number, gross_amount_cents, currency,
			       tax_validation_status, issued_at, retention_until
			FROM purser.simplified_invoices
			WHERE tenant_id = $1 AND tax_validation_status <> 'location_review'
			UNION ALL
			SELECT payment.id, 'payment_receipt', 'PAY-' || UPPER(LEFT(REPLACE(payment.id::text, '-', ''), 12)),
			       ROUND(payment.amount * 100)::bigint, payment.currency, payment.status,
			       COALESCE(payment.confirmed_at, payment.created_at, NOW()), payment.retention_until
			FROM purser.billing_payments payment
			JOIN purser.billing_invoices invoice ON invoice.id = payment.invoice_id
			WHERE invoice.tenant_id = $1 AND payment.status = 'confirmed'
			UNION ALL
			SELECT id, 'credit_note', credit_note_number, amount_cents, currency, 'issued', issued_at, retention_until
			FROM purser.credit_notes WHERE tenant_id = $1
		) documents
		ORDER BY issued_at DESC, id DESC
		LIMIT 1000
	`, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list billing documents: %v", err)
	}
	defer rows.Close()
	response := &purserpb.ListBillingDocumentsResponse{}
	for rows.Next() {
		var row billingDocumentRow
		if err := rows.Scan(&row.id, &row.kind, &row.number, &row.amountCents, &row.currency, &row.status, &row.issuedAt, &row.retentionUntil); err != nil {
			return nil, status.Errorf(codes.Internal, "scan billing document: %v", err)
		}
		response.Documents = append(response.Documents, billingDocumentProto(row))
	}
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "list billing documents: %v", err)
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

func supplierDocumentFields() (string, string, string) {
	return config.GetEnv("SUPPLIER_NAME", ""), config.GetEnv("SUPPLIER_ADDRESS", ""), config.GetEnv("SUPPLIER_VAT_NUMBER", "")
}

func scanCustomer(company, address, vat *sql.NullString) (string, string, string) {
	return company.String, address.String, vat.String
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
	supplierName, supplierAddress, supplierVAT := supplierDocumentFields()
	base := billingDocumentHTMLData{SupplierName: supplierName, SupplierAddress: supplierAddress, SupplierVAT: supplierVAT}
	var row billingDocumentRow
	row.id, row.kind = documentID, kind
	var company, address, vat sql.NullString
	switch kind {
	case "invoice":
		var periodStart, periodEnd, dueAt sql.NullTime
		err = s.db.QueryRowContext(ctx, `
			SELECT invoice_number, ROUND(amount * 100)::bigint, currency, status,
			       COALESCE(invoice.created_at, NOW()), invoice.retention_until,
			       invoice.period_start, invoice.period_end, invoice.due_date,
			       subscription.billing_company, subscription.billing_address::text, subscription.tax_id
			FROM purser.billing_invoices invoice
			LEFT JOIN purser.tenant_subscriptions subscription ON subscription.tenant_id = invoice.tenant_id
			WHERE invoice.id = $1 AND invoice.tenant_id = $2 AND invoice.status <> 'draft'
		`, documentID, tenantID).Scan(&row.number, &row.amountCents, &row.currency, &row.status,
			&row.issuedAt, &row.retentionUntil, &periodStart, &periodEnd, &dueAt, &company, &address, &vat)
		base.Title = "Invoice"
		if periodStart.Valid {
			base.Fields = append(base.Fields, billingDocumentHTMLField{Label: "Period start", Value: periodStart.Time.UTC().Format(time.RFC3339)})
		}
		if periodEnd.Valid {
			base.Fields = append(base.Fields, billingDocumentHTMLField{Label: "Period end", Value: periodEnd.Time.UTC().Format(time.RFC3339)})
		}
		if dueAt.Valid {
			base.Fields = append(base.Fields, billingDocumentHTMLField{Label: "Due", Value: dueAt.Time.UTC().Format(time.RFC3339)})
		}
	case "simplified_invoice":
		var netCents, vatCents int64
		var vatRate int
		var referenceType, referenceID, taxStatus string
		err = s.db.QueryRowContext(ctx, `
			SELECT invoice_number, gross_amount_cents, currency, tax_validation_status, issued_at, retention_until,
			       net_amount_cents, vat_amount_cents, vat_rate_bps, reference_type, reference_id,
			       supplier_name, supplier_address, supplier_vat_number,
			       subscription.billing_company, subscription.billing_address::text, subscription.tax_id
			FROM purser.simplified_invoices invoice
			LEFT JOIN purser.tenant_subscriptions subscription ON subscription.tenant_id = invoice.tenant_id
			WHERE invoice.id = $1 AND invoice.tenant_id = $2 AND tax_validation_status <> 'location_review'
		`, documentID, tenantID).Scan(&row.number, &row.amountCents, &row.currency, &taxStatus, &row.issuedAt, &row.retentionUntil,
			&netCents, &vatCents, &vatRate, &referenceType, &referenceID,
			&base.SupplierName, &base.SupplierAddress, &base.SupplierVAT, &company, &address, &vat)
		row.status = taxStatus
		base.Title = "Simplified invoice"
		base.Fields = append(base.Fields,
			billingDocumentHTMLField{Label: "Net", Value: row.currency + " " + moneyString(netCents)},
			billingDocumentHTMLField{Label: "VAT", Value: fmt.Sprintf("%s %s (%0.2f%%)", row.currency, moneyString(vatCents), float64(vatRate)/100)},
			billingDocumentHTMLField{Label: "Settlement reference", Value: referenceType + ":" + referenceID},
		)
		if taxStatus == "reverse_charge" {
			base.Fields = append(base.Fields, billingDocumentHTMLField{Label: "VAT treatment", Value: "Reverse charge — btw verlegd"})
		}
	case "payment_receipt":
		var method string
		var txID sql.NullString
		err = s.db.QueryRowContext(ctx, `
			SELECT 'PAY-' || UPPER(LEFT(REPLACE(payment.id::text, '-', ''), 12)),
			       ROUND(payment.amount * 100)::bigint, payment.currency, payment.status,
			       COALESCE(payment.confirmed_at, payment.created_at, NOW()), payment.retention_until,
			       payment.method, payment.tx_id,
			       subscription.billing_company, subscription.billing_address::text, subscription.tax_id
			FROM purser.billing_payments payment
			JOIN purser.billing_invoices invoice ON invoice.id = payment.invoice_id
			LEFT JOIN purser.tenant_subscriptions subscription ON subscription.tenant_id = invoice.tenant_id
			WHERE payment.id = $1 AND invoice.tenant_id = $2 AND payment.status = 'confirmed'
		`, documentID, tenantID).Scan(&row.number, &row.amountCents, &row.currency, &row.status,
			&row.issuedAt, &row.retentionUntil, &method, &txID, &company, &address, &vat)
		base.Title = "Payment receipt"
		base.Fields = append(base.Fields, billingDocumentHTMLField{Label: "Method", Value: method})
		if txID.Valid {
			base.Fields = append(base.Fields, billingDocumentHTMLField{Label: "Settlement reference", Value: txID.String})
		}
	case "credit_note":
		var sourceType, sourceID, reversalType, reversalID, reason string
		err = s.db.QueryRowContext(ctx, `
			SELECT note.credit_note_number, note.amount_cents, note.currency, 'issued', note.issued_at, note.retention_until,
			       note.source_document_type, note.source_document_id::text,
			       note.reversal_reference_type, note.reversal_reference_id, note.reason,
			       subscription.billing_company, subscription.billing_address::text, subscription.tax_id
			FROM purser.credit_notes note
			LEFT JOIN purser.tenant_subscriptions subscription ON subscription.tenant_id = note.tenant_id
			WHERE note.id = $1 AND note.tenant_id = $2
		`, documentID, tenantID).Scan(&row.number, &row.amountCents, &row.currency, &row.status, &row.issuedAt, &row.retentionUntil,
			&sourceType, &sourceID, &reversalType, &reversalID, &reason, &company, &address, &vat)
		base.Title = "Credit note"
		base.Fields = append(base.Fields,
			billingDocumentHTMLField{Label: "Original document", Value: sourceType + ":" + sourceID},
			billingDocumentHTMLField{Label: "Reversal reference", Value: reversalType + ":" + reversalID},
			billingDocumentHTMLField{Label: "Reason", Value: reason},
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
	if strings.TrimSpace(base.SupplierName) == "" || strings.TrimSpace(base.SupplierAddress) == "" || strings.TrimSpace(base.SupplierVAT) == "" {
		return nil, status.Error(codes.FailedPrecondition, "supplier information is not configured for document rendering")
	}
	base.Customer, base.CustomerAddress, base.CustomerVAT = scanCustomer(&company, &address, &vat)
	return renderBillingDocument(row, base)
}
