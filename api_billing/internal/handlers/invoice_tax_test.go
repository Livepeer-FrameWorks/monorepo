package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
	"github.com/DATA-DOG/go-sqlmock"
)

func TestInvoiceTaxOutageGraceRequiresReview(t *testing.T) {
	for key, value := range map[string]string{"SUPPLIER_NAME": "DDVTech B.V.", "SUPPLIER_ADDRESS": "Leiden NL", "SUPPLIER_VAT_NUMBER": "NL853319145B01", "SUPPLIER_REGISTRATION_NUMBER": "59104694", "SUPPLIER_COUNTRY": "NL"} {
		appconfigtest.Set(t, key, value)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer server.Close()
	appconfigtest.Set(t, "VIES_ENDPOINT", server.URL)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	mock.ExpectQuery("-- name: GetInvoiceTaxProfile").WillReturnRows(sqlmock.NewRows([]string{"customer_snapshot", "email", "name", "company", "vat_number", "address", "customer_type"}).AddRow([]byte(`{}`), "buyer@example.test", "Buyer", "Buyer GmbH", "DE123456789", []byte(`{"street":"Main1","city":"Berlin","postal_code":"10115","country":"DE"}`), "business"))
	mock.ExpectQuery("-- name: GetCachedVATValidation").WillReturnRows(sqlmock.NewRows([]string{"valid", "validation_snapshot", "fresh", "within_outage_grace"}).AddRow(true, []byte(`{"valid":true}`), false, true))
	mock.ExpectExec("-- name: RecordCryptoAccountingAnomaly").WillReturnResult(sqlmock.NewResult(0, 1))
	a, err := assessInvoiceTaxTx(context.Background(), tx, "tenant", 10000, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if !a.Decision.ReverseCharge || a.ValidationMode != "outage_grace" || !a.ReviewRequired || len(a.ReviewReasons) != 1 || a.ReviewReasons[0] != "vies_outage_grace" {
		t.Fatalf("outage reverse charge was not retained for review: %+v", a)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestExclusiveInvoiceTaxAndLineRounding(t *testing.T) {
	vat, gross := exclusiveTax(10000, 2100)
	if vat != 2100 || gross != 12100 {
		t.Fatalf("100 net did not become 121 gross: VAT=%d gross=%d", vat, gross)
	}
	for _, net := range [][]int64{{1, 1, 1, 1, 1, 1, 1, 1, 1}, {100, -99}, {1000, 2000, -1500}} {
		allocated := allocateInvoiceLineVAT(net, 2100)
		var sumNet, sumVAT int64
		for i, n := range net {
			sumNet += n
			sumVAT += allocated[i]
		}
		want, _ := exclusiveTax(sumNet, 2100)
		if sumVAT != want {
			t.Fatalf("line VAT %v sums to %d instead of %d", allocated, sumVAT, want)
		}
	}
}
