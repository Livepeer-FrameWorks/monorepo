package handlers

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
	"github.com/DATA-DOG/go-sqlmock"
)

func TestValidateVIESVATPersistsAuthoritativeEvidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.Contains(r.Header.Get("Content-Type"), "text/xml") {
			t.Fatalf("unexpected VIES request: %s %s", r.Method, r.Header.Get("Content-Type"))
		}
		w.Header().Set("Content-Type", "text/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?><soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"><soap:Body><checkVatResponse xmlns="urn:ec.europa.eu:taxud:vies:services:checkVat:types"><countryCode>NL</countryCode><vatNumber>123456789B01</vatNumber><requestDate>2026-08-20</requestDate><valid>true</valid><name>Example BV</name><address>Amsterdam</address></checkVatResponse></soap:Body></soap:Envelope>`))
	}))
	defer server.Close()
	appconfigtest.Set(t, "VIES_ENDPOINT", server.URL)

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("SELECT valid FROM purser.vat_validation_evidence").
		WithArgs("tenant-1", sqlmock.AnyArg()).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO purser.vat_validation_evidence").
		WithArgs("tenant-1", "NL", sqlmock.AnyArg(), "NL********9B01", true,
			time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC), "Example BV", "Amsterdam", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	h := &X402Handler{db: db}
	valid, err := h.validateVIESVAT(context.Background(), "tenant-1", "NL", "NL123456789B01")
	if err != nil || !valid {
		t.Fatalf("validateVIESVAT() = %v, %v", valid, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateVIESVATUsesGreekVIESCountryCode(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			t.Error(readErr)
			return
		}
		if !strings.Contains(string(body), "<countryCode>EL</countryCode>") || !strings.Contains(string(body), "<vatNumber>123456789</vatNumber>") {
			t.Errorf("Greek VIES request did not translate ISO GR: %s", body)
		}
		_, _ = w.Write([]byte(`<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"><soap:Body><checkVatResponse xmlns="urn:ec.europa.eu:taxud:vies:services:checkVat:types"><countryCode>EL</countryCode><vatNumber>123456789</vatNumber><requestDate>2026-11-05</requestDate><valid>true</valid></checkVatResponse></soap:Body></soap:Envelope>`))
	}))
	defer server.Close()
	appconfigtest.Set(t, "VIES_ENDPOINT", server.URL)
	mock.ExpectQuery("SELECT valid FROM purser.vat_validation_evidence").WithArgs("tenant-gr", sqlmock.AnyArg()).WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO purser.vat_validation_evidence").WithArgs("tenant-gr", "EL", sqlmock.AnyArg(), "EL*****6789", true, time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC), "", "", sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(1, 1))
	valid, err := (&X402Handler{db: db}).validateVIESVAT(context.Background(), "tenant-gr", "GR", "EL123456789")
	if err != nil || !valid {
		t.Fatalf("Greek VIES validation=%v, %v", valid, err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
