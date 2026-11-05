package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
	"github.com/DATA-DOG/go-sqlmock"
)

func TestVIESOutageGraceUsesOnlyPreviouslyVerifiedEvidence(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		grace                string
		valid, fresh, within bool
		status               int
		body                 string
		wantOK               bool
		wantMode             string
	}{
		{name: "bounded positive proof", grace: "168", valid: true, within: true, status: 503, wantOK: true, wantMode: "outage_grace"},
		{name: "built in outage handling", grace: "0", valid: true, within: true, status: 503, wantOK: true, wantMode: "outage_grace"},
		{name: "permanent SOAP fault despite HTTP 500", grace: "168", valid: true, within: true, status: 500, body: `<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"><soap:Body><soap:Fault><faultcode>soap:Server</faultcode><faultstring>VAT_BLOCKED</faultstring></soap:Fault></soap:Body></soap:Envelope>`},
		{name: "negative latest proof", grace: "168", within: true, status: 503},
		{name: "proof too old", grace: "168", valid: true, status: 503},
		{name: "malformed reply", grace: "168", valid: true, within: true, status: 200, body: "invalid XML"},
		{name: "fresh proof retains exact snapshot", grace: "0", valid: true, fresh: true, status: 503, wantOK: true, wantMode: "fresh_cache"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			appconfigtest.Set(t, "VIES_ENDPOINT", server.URL)

			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			grace := viesOutageGraceHours
			snapshot := json.RawMessage(`{"id":"exact-proof","valid":true}`)
			mock.ExpectQuery("-- name: GetCachedVATValidation").WithArgs(grace, "tenant-1", sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"valid", "validation_snapshot", "fresh", "within_outage_grace"}).AddRow(tc.valid, []byte(snapshot), tc.fresh, tc.within))
			if tc.wantMode == "outage_grace" {
				mock.ExpectExec("-- name: RecordCryptoAccountingAnomaly").WillReturnResult(sqlmock.NewResult(1, 1))
			}
			if strings.Contains(tc.body, "VAT_BLOCKED") {
				mock.ExpectExec("-- name: RecordPermanentVIESFault").WillReturnResult(sqlmock.NewResult(1, 1))
			}
			got, err := (&X402Handler{db: db}).validateVIESVATEvidence(context.Background(), "tenant-1", "NL", "NL123456789B01")
			if tc.wantOK {
				if err != nil || !got.Valid || got.Mode != tc.wantMode || string(got.Snapshot) != string(snapshot) {
					t.Fatalf("verification=%+v error=%v", got, err)
				}
			} else if err == nil {
				t.Fatalf("unverified outage claim accepted: %+v", got)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

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
	mock.ExpectQuery("-- name: GetCachedVATValidation").
		WithArgs(viesOutageGraceHours, "tenant-1", sqlmock.AnyArg()).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("INSERT INTO purser.vat_validation_evidence").
		WithArgs("tenant-1", "NL", sqlmock.AnyArg(), "NL********9B01", true,
			time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC), "Example BV", "Amsterdam", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"validation_snapshot"}).AddRow([]byte(`{"valid":true}`)))

	h := &X402Handler{db: db}
	valid, err := h.validateVIESVATEvidence(context.Background(), "tenant-1", "NL", "NL123456789B01")
	if err != nil || !valid.Valid {
		t.Fatalf("validateVIESVAT() = %v, %v", valid.Valid, err)
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
	mock.ExpectQuery("-- name: GetCachedVATValidation").WithArgs(viesOutageGraceHours, "tenant-gr", sqlmock.AnyArg()).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("INSERT INTO purser.vat_validation_evidence").WithArgs("tenant-gr", "EL", sqlmock.AnyArg(), "EL*****6789", true, time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC), "", "", sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"validation_snapshot"}).AddRow([]byte(`{"valid":true}`)))
	valid, err := (&X402Handler{db: db}).validateVIESVATEvidence(context.Background(), "tenant-gr", "GR", "EL123456789")
	if err != nil || !valid.Valid {
		t.Fatalf("Greek VIES validation=%v, %v", valid, err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestVIESAuthoritativeInvalidResponseDoesNotUseOutageGrace(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"><soap:Body><checkVatResponse xmlns="urn:ec.europa.eu:taxud:vies:services:checkVat:types"><countryCode>NL</countryCode><vatNumber>123456789B01</vatNumber><requestDate>2026-11-05</requestDate><valid>false</valid></checkVatResponse></soap:Body></soap:Envelope>`))
	}))
	defer server.Close()
	appconfigtest.Set(t, "VIES_ENDPOINT", server.URL)

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("-- name: GetCachedVATValidation").WithArgs(168, "tenant-1", sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"valid", "validation_snapshot", "fresh", "within_outage_grace"}).AddRow(true, []byte(`{"valid":true}`), false, true))
	mock.ExpectQuery("INSERT INTO purser.vat_validation_evidence").WillReturnRows(sqlmock.NewRows([]string{"validation_snapshot"}).AddRow([]byte(`{"valid":false}`)))
	got, err := (&X402Handler{db: db}).validateVIESVATEvidence(context.Background(), "tenant-1", "NL", "NL123456789B01")
	if err != nil || got.Valid || got.Mode != "live_vies" || string(got.Snapshot) != `{"valid":false}` {
		t.Fatalf("live invalid response replaced with old positive evidence: %+v, %v", got, err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
