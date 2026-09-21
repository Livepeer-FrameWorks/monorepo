package grpc

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	documentTenantID = "71000000-0000-4000-8000-000000000001"
	documentOtherID  = "71000000-0000-4000-8000-000000000002"
	documentID       = "72000000-0000-4000-8000-000000000001"
)

func TestResolveBillingDocumentTenantEnforcesTenantBoundary(t *testing.T) {
	tenantCtx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, documentTenantID)
	tests := []struct {
		name      string
		ctx       context.Context
		requested string
		want      string
		wantCode  codes.Code
	}{
		{name: "tenant defaults from authenticated context", ctx: tenantCtx, want: documentTenantID},
		{name: "matching tenant", ctx: tenantCtx, requested: documentTenantID, want: documentTenantID},
		{name: "cross tenant denied", ctx: tenantCtx, requested: documentOtherID, wantCode: codes.PermissionDenied},
		{name: "missing tenant context denied", ctx: context.Background(), requested: documentTenantID, wantCode: codes.PermissionDenied},
		{name: "service may select tenant", ctx: serviceTestContext(), requested: documentOtherID, want: documentOtherID},
		{name: "service still requires valid uuid", ctx: serviceTestContext(), requested: "tenant-name", wantCode: codes.InvalidArgument},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := resolveBillingDocumentTenant(test.ctx, test.requested)
			if status.Code(err) != test.wantCode {
				t.Fatalf("code = %v, want %v (err=%v)", status.Code(err), test.wantCode, err)
			}
			if got != test.want {
				t.Fatalf("tenant = %q, want %q", got, test.want)
			}
		})
	}
}

func TestListBillingDocumentsReturnsImmutableAuditMetadata(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	mock.ExpectQuery("-- name: ListBillingDocuments").WithArgs(documentTenantID).WillReturnRows(
		sqlmock.NewRows([]string{"id", "kind", "document_number", "amount_cents", "currency", "status", "issued_at", "retention_until", "has_eur_amount", "eur_amount_cents", "net_eur_cents", "vat_eur_cents", "units_per_eur", "fx_reference_date"}).
			AddRow(documentID, "invoice", "INV-0001", int64(12345), "USD", "paid", now, now.AddDate(10, 0, 0), true, int64(11223), nil, nil, "1.1000000000", "2026-08-31").
			AddRow(documentOtherID, "credit_note", "CN-0001", int64(-500), "EUR", "issued", now.Add(-time.Hour), now.AddDate(10, 0, 0), false, int64(0), nil, nil, "", ""),
	)

	response, err := (&PurserServer{db: db}).ListBillingDocuments(serviceTestContext(), &purserpb.ListBillingDocumentsRequest{TenantId: documentTenantID})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.GetDocuments()) != 2 {
		t.Fatalf("documents = %d", len(response.GetDocuments()))
	}
	invoice := response.GetDocuments()[0]
	if invoice.GetId() != documentID || invoice.GetKind() != "invoice" || invoice.GetDocumentNumber() != "INV-0001" || invoice.GetAmountCents() != 12345 || invoice.GetDownloadFilename() != "INV-0001.html" {
		t.Fatalf("invoice metadata = %+v", invoice)
	}
	if !invoice.GetIssuedAt().AsTime().Equal(now) || !invoice.GetRetentionUntil().AsTime().Equal(now.AddDate(10, 0, 0)) {
		t.Fatalf("audit timestamps = %+v", invoice)
	}
	if invoice.EurAmountCents == nil || invoice.GetEurAmountCents() != 11223 || invoice.NetEurCents != nil || invoice.VatEurCents != nil ||
		invoice.GetUnitsPerEur() != "1.1" || invoice.GetFxReferenceDate() != "2026-08-31" {
		t.Fatalf("invoice EUR statement = %+v", invoice)
	}
	creditNote := response.GetDocuments()[1]
	if creditNote.EurAmountCents != nil || creditNote.GetUnitsPerEur() != "" || creditNote.GetFxReferenceDate() != "" {
		t.Fatalf("credit note states an EUR conversion: %+v", creditNote)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListBillingDocumentsDoesNotLeakDatabaseErrors(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("-- name: ListBillingDocuments").WithArgs(documentTenantID).WillReturnError(errors.New("database unavailable"))
	_, err = (&PurserServer{db: db}).ListBillingDocuments(serviceTestContext(), &purserpb.ListBillingDocumentsRequest{TenantId: documentTenantID})
	if status.Code(err) != codes.Internal || !strings.Contains(err.Error(), "list billing documents") {
		t.Fatalf("error = %v", err)
	}
}

func TestGetBillingDocumentRendersEveryMoneyDocumentKind(t *testing.T) { //nolint:funlen // Explicit rows document each persisted audit shape.
	appconfigtest.Set(t, "SUPPLIER_NAME", "FrameWorks B.V.")
	appconfigtest.Set(t, "SUPPLIER_ADDRESS", "Amsterdam, NL")
	appconfigtest.Set(t, "SUPPLIER_VAT_NUMBER", "NL000000000B01")
	appconfigtest.Set(t, "SUPPLIER_REGISTRATION_NUMBER", "12345678")
	now := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	retained := now.AddDate(10, 0, 0)

	tests := []struct {
		kind        string
		query       string
		columns     []string
		row         []driver.Value
		wantNumber  string
		wantAmount  int64
		wantEUR     int64 // -1 when the document states no EUR amount
		wantVAT     bool
		wantContent []string
	}{
		{
			kind: "invoice", query: "-- name: GetInvoiceDocument",
			columns:    []string{"invoice_number", "amount_cents", "currency", "status", "issued_at", "retention_until", "period_start", "period_end", "due_date", "eur_amount_cents", "presentment_units_per_eur", "presentment_reference_date", "customer_name", "customer_company", "customer_address", "customer_vat"},
			row:        []driver.Value{"INV-1", int64(1375), "USD", "paid", now, retained, now.AddDate(0, -1, 0), now, now.AddDate(0, 0, 14), int64(1250), "1.1000000000", now, "Ada", "Example BV", "Utrecht", "NL123"},
			wantNumber: "INV-1", wantAmount: 1375, wantEUR: 1250, wantContent: []string{"Invoice", "USD 13.75", "Period start", "Due", "Ada", "EUR 12.50", "1 EUR = 1.1000000000 USD", "2026-08-31"},
		},
		{
			kind: "simplified_invoice", query: "-- name: GetSimplifiedInvoiceDocument",
			columns:    []string{"invoice_number", "gross_amount_cents", "currency", "tax_validation_status", "issued_at", "retention_until", "net_amount_cents", "vat_amount_cents", "vat_rate_bps", "amount_eur_cents", "net_eur_cents", "vat_eur_cents", "fx_units_per_eur", "fx_reference_date", "reference_type", "reference_id", "supplier_name", "supplier_address", "supplier_vat_number", "supplier_registration_number", "service_description", "service_quantity", "service_date", "customer_name", "customer_company", "customer_address", "customer_vat"},
			row:        []driver.Value{"SI-1", int64(1210), "EUR", "issued", now, retained, int64(1000), int64(210), int32(2100), int64(1210), int64(1000), int64(210), "1", now, "topup", "topup-1", "FrameWorks B.V.", "Amsterdam", "NL000", "123", "Prepaid credit", int32(1), now, "Grace", "Example GmbH", "Berlin", "DE123"},
			wantNumber: "SI-1", wantAmount: 1210, wantEUR: 1210, wantVAT: true, wantContent: []string{"Simplified invoice", "Net", "EUR 10.00", "VAT", "topup:topup-1", "VAT (EUR)"},
		},
		{
			kind: "crypto_invoice", query: "-- name: GetCryptoInvoiceDocument",
			columns:    []string{"invoice_number", "gross_amount_cents", "currency", "tax_validation_status", "issued_at", "retention_until", "net_amount_cents", "vat_amount_cents", "vat_rate_bps", "amount_eur_cents", "net_eur_cents", "vat_eur_cents", "fx_units_per_eur", "fx_reference_date", "reference_type", "reference_id", "supplier_name", "supplier_address", "supplier_vat_number", "supplier_registration_number", "service_description", "service_quantity", "service_date", "customer_email", "customer_name", "customer_company", "customer_address", "customer_vat"},
			row:        []driver.Value{"CI-1", int64(5000), "USD", "reverse_charge", now, retained, int64(5000), int64(0), int32(0), int64(4545), int64(4545), int64(0), "1.1000000000", now, "tx", "0xabc", "FrameWorks B.V.", "Amsterdam", "NL000", "123", "Crypto credit", int32(1), now, "buyer@example.test", "Linus", "Kernel Oy", "Helsinki", "FI123"},
			wantNumber: "CI-1", wantAmount: 5000, wantEUR: 4545, wantVAT: true, wantContent: []string{"USD 50.00", "buyer@example.test", "tx:0xabc", "Reverse charge", "Net (EUR)", "EUR 45.45", "1 EUR = 1.1000000000 USD"},
		},
		{
			kind: "payment_receipt", query: "-- name: GetPaymentReceiptDocument",
			columns:    []string{"document_number", "amount_cents", "currency", "status", "issued_at", "retention_until", "method", "tx_id", "eur_amount_cents", "fx_units_per_eur", "fx_reference_date", "customer_name", "customer_company", "customer_address", "customer_vat"},
			row:        []driver.Value{"PAY-1", int64(999), "EUR", "confirmed", now, retained, "stripe_card", "pi_123", int64(999), "1.0000000000", now, "Margaret", "Compiler Ltd", "London", "GB123"},
			wantNumber: "PAY-1", wantAmount: 999, wantEUR: 999, wantContent: []string{"Payment receipt", "EUR 9.99", "stripe_card", "pi_123"},
		},
		{
			kind: "credit_note", query: "-- name: GetCreditNoteDocument",
			columns:    []string{"credit_note_number", "amount_cents", "currency", "issued_at", "retention_until", "source_document_type", "source_document_id", "reversal_reference_type", "reversal_reference_id", "reason", "customer_name", "customer_company", "customer_address", "customer_vat"},
			row:        []driver.Value{"CN-1", int64(-250), "EUR", now, retained, "invoice", "inv-1", "refund", "re_1", "customer refund", "Barbara", "COBOL Inc", "New York", "US123"},
			wantNumber: "CN-1", wantAmount: -250, wantEUR: -1, wantContent: []string{"Credit note", "-2.50", "invoice:inv-1", "refund:re_1", "customer refund"},
		},
	}

	for _, test := range tests {
		t.Run(test.kind, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mock.ExpectQuery(test.query).WithArgs(documentID, documentTenantID).WillReturnRows(sqlmock.NewRows(test.columns).AddRow(test.row...))
			response, err := (&PurserServer{db: db}).GetBillingDocument(serviceTestContext(), &purserpb.GetBillingDocumentRequest{
				TenantId: documentTenantID, DocumentId: documentID, Kind: test.kind,
			})
			if err != nil {
				t.Fatal(err)
			}
			if response.GetDocument().GetDocumentNumber() != test.wantNumber || response.GetDocument().GetAmountCents() != test.wantAmount || response.GetContentType() != billingDocumentContentType {
				t.Fatalf("metadata = %+v", response.GetDocument())
			}
			digest := sha256.Sum256(response.GetContent())
			if response.GetSha256() != hex.EncodeToString(digest[:]) {
				t.Fatalf("sha256 does not bind returned content")
			}
			document := response.GetDocument()
			if test.wantEUR < 0 {
				if document.EurAmountCents != nil || document.GetFxReferenceDate() != "" {
					t.Fatalf("document states an EUR conversion: %+v", document)
				}
			} else if document.EurAmountCents == nil || document.GetEurAmountCents() != test.wantEUR || document.GetUnitsPerEur() == "" || document.GetFxReferenceDate() != "2026-08-31" {
				t.Fatalf("EUR statement = %+v, want %d EUR", document, test.wantEUR)
			}
			if (document.VatEurCents != nil) != test.wantVAT || (document.NetEurCents != nil) != test.wantVAT {
				t.Fatalf("EUR net/VAT stated = %v/%v, want %v", document.NetEurCents != nil, document.VatEurCents != nil, test.wantVAT)
			}
			content := string(response.GetContent())
			for _, want := range test.wantContent {
				if !strings.Contains(content, want) {
					t.Fatalf("content missing %q: %s", want, content)
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGetBillingDocumentRejectsUnrenderableRequests(t *testing.T) {
	server := &PurserServer{}
	for _, test := range []struct {
		name string
		req  *purserpb.GetBillingDocumentRequest
		code codes.Code
	}{
		{name: "invalid document id", req: &purserpb.GetBillingDocumentRequest{TenantId: documentTenantID, DocumentId: "not-a-uuid", Kind: "invoice"}, code: codes.InvalidArgument},
		{name: "unsupported kind", req: &purserpb.GetBillingDocumentRequest{TenantId: documentTenantID, DocumentId: documentID, Kind: "bank_statement"}, code: codes.InvalidArgument},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := server.GetBillingDocument(serviceTestContext(), test.req)
			if status.Code(err) != test.code {
				t.Fatalf("code = %v, want %v (err=%v)", status.Code(err), test.code, err)
			}
		})
	}

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("-- name: GetInvoiceDocument").WithArgs(documentID, documentTenantID).WillReturnError(sql.ErrNoRows)
	_, err = (&PurserServer{db: db}).GetBillingDocument(serviceTestContext(), &purserpb.GetBillingDocumentRequest{TenantId: documentTenantID, DocumentId: documentID, Kind: "invoice"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("missing document code = %v (err=%v)", status.Code(err), err)
	}
}

func TestMoneyStringPreservesMinorUnitsAndSign(t *testing.T) {
	for cents, want := range map[int64]string{0: "0.00", 1: "0.01", 99: "0.99", 100: "1.00", 12345: "123.45", -1: "-0.01", -12345: "-123.45"} {
		if got := moneyString(cents); got != want {
			t.Errorf("moneyString(%d) = %q, want %q", cents, got, want)
		}
	}
}
