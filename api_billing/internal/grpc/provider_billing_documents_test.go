package grpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestProviderPDFRPCServesOriginalAndChecksOwnershipAndHash(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tenant, id := "3e201e3a-e2b2-431a-b10a-d7795bb10459", "7e38b358-4bd2-481e-9ac9-e6d150231f9b"
	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, tenant)
	pdf := []byte("%PDF-1.4\nOriginal Stripe bytes\n%%EOF")
	digest := sha256.Sum256(pdf)
	hash := hex.EncodeToString(digest[:])
	columns := []string{"id", "document_kind", "provider_document_id", "original_number", "source_invoice_id", "status", "currency", "net_minor", "vat_minor", "gross_minor", "tax_status", "tax_groups", "supplier_snapshot", "customer_snapshot", "raw_facts", "tax_assessment", "original_pdf", "pdf_sha256", "issued_at", "retention_until"}
	rows := func(expectedHash string) *sqlmock.Rows {
		return sqlmock.NewRows(columns).AddRow(id, "provider_invoice", "in_original", "DDV-0042", nil, "paid", "EUR", 10000, 2100, 12100, "ready", []byte(`[]`), []byte(`{}`), []byte(`{}`), []byte(`{}`), []byte(`{}`), pdf, expectedHash, time.Now(), time.Now().AddDate(10, 0, 0))
	}
	request := &purserpb.GetBillingDocumentRequest{DocumentId: id, Kind: "provider_invoice"}
	mock.ExpectQuery("-- name: GetProviderBillingDocument").WithArgs(tenant, id, "provider_invoice").WillReturnRows(rows(hash))
	response, err := (&PurserServer{db: db}).GetBillingDocument(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if string(response.Content) != string(pdf) || response.Sha256 != hash || response.Document.DocumentNumber != "DDV-0042" || response.Document.GetNetEurCents() != 10000 {
		t.Fatal("provider original was re-rendered or lost tax totals")
	}
	mock.ExpectQuery("-- name: GetProviderBillingDocument").WithArgs(tenant, id, "provider_invoice").WillReturnRows(rows("invalid"))
	if _, err = (&PurserServer{db: db}).GetBillingDocument(ctx, request); status.Code(err) != codes.DataLoss {
		t.Fatal("corrupt original PDF served", err)
	}
	request.TenantId = "611cdd73-3a18-4f44-8eb9-115d2cb7d37b"
	if _, err = (&PurserServer{db: db}).GetBillingDocument(ctx, request); status.Code(err) != codes.PermissionDenied {
		t.Fatal("cross-tenant document RPC accepted", err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
