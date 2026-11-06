//nolint:govet,errcheck,errorlint // VIES parsing keeps local error scopes and persists the authoritative result separately.
package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"frameworks/api_billing/internal/appconfig"
	"frameworks/api_billing/internal/database/purserdb"
	billingtax "frameworks/api_billing/internal/tax"
)

const viesOutageGraceHours = 168

const defaultVIESEndpoint = "https://ec.europa.eu/taxation_customs/vies/services/checkVatService"

type viesEnvelope struct {
	XMLName xml.Name `xml:"http://schemas.xmlsoap.org/soap/envelope/ Envelope"`
	Body    viesBody `xml:"Body"`
}

type viesBody struct {
	CheckVAT *viesCheckVAT `xml:"urn:ec.europa.eu:taxud:vies:services:checkVat:types checkVat,omitempty"`
	Response *viesResponse `xml:"checkVatResponse,omitempty"`
	Fault    *viesFault    `xml:"Fault,omitempty"`
}

type viesCheckVAT struct {
	CountryCode string `xml:"countryCode"`
	VATNumber   string `xml:"vatNumber"`
}

type viesResponse struct {
	CountryCode string `xml:"countryCode" json:"countryCode"`
	VATNumber   string `xml:"vatNumber" json:"vatNumber"`
	RequestDate string `xml:"requestDate" json:"requestDate"`
	Valid       bool   `xml:"valid" json:"valid"`
	Name        string `xml:"name" json:"name,omitempty"`
	Address     string `xml:"address" json:"address,omitempty"`
}

type viesFault struct {
	Code   string `xml:"faultcode"`
	String string `xml:"faultstring"`
}

type vatVerification struct {
	Valid    bool
	Snapshot json.RawMessage
	Mode     string
}

type viesUnavailableError struct{ cause error }

func (e *viesUnavailableError) Error() string { return "VIES verification is temporarily unavailable" }
func (e *viesUnavailableError) Unwrap() error { return e.cause }

func (h *X402Handler) validateVIESVATEvidence(ctx context.Context, tenantID, countryCode, vatNumber string) (vatVerification, error) {
	canonical, err := billingtax.CanonicalVATNumber(countryCode, vatNumber)
	if err != nil {
		return vatVerification{}, err
	}
	countryCode = billingtax.VIESCountryCode(countryCode)
	vatNumber = strings.TrimPrefix(canonical, countryCode)
	digest := sha256.Sum256([]byte(canonical))
	vatHash := hex.EncodeToString(digest[:])
	grace := viesOutageGraceHours
	cached, cacheErr := purserdb.New(h.taxDatabase()).GetCachedVATValidation(ctx, purserdb.GetCachedVATValidationParams{TenantID: tenantID, VatNumberHash: vatHash, OutageGraceHours: int32(grace)})
	if cacheErr == nil && cached.Fresh {
		return vatVerification{Valid: cached.Valid, Snapshot: cached.ValidationSnapshot, Mode: "fresh_cache"}, nil
	}
	if cacheErr != nil && !errors.Is(cacheErr, sql.ErrNoRows) {
		return vatVerification{}, cacheErr
	}
	live, refreshErr := h.refreshVIESVAT(ctx, tenantID, countryCode, vatNumber, vatHash)
	if refreshErr == nil {
		return live, nil
	}
	var unavailable *viesUnavailableError
	if ctx.Err() == nil && errors.As(refreshErr, &unavailable) && grace > 0 && cacheErr == nil && cached.Valid && cached.WithinOutageGrace {
		proof, _ := json.Marshal(map[string]any{"country": countryCode, "vat_number": canonical, "retained_validation": cached.ValidationSnapshot})
		if err := purserdb.New(h.taxDatabase()).RecordCryptoAccountingAnomaly(ctx, purserdb.RecordCryptoAccountingAnomalyParams{TenantID: tenantID, Kind: "vies_revalidation", Network: "vies", ReferenceType: "vat_validation", ReferenceID: tenantID + ":" + vatHash, Detail: "Retained positive VIES evidence used during outage; automatic revalidation pending", EvidenceJson: proof}); err != nil {
			return vatVerification{}, err
		}
		return vatVerification{Valid: true, Snapshot: cached.ValidationSnapshot, Mode: "outage_grace"}, nil
	}
	return vatVerification{}, refreshErr
}

func (h *X402Handler) refreshVIESVAT(ctx context.Context, tenantID, countryCode, vatNumber, vatHash string) (vatVerification, error) {
	requestEnvelope := viesEnvelope{Body: viesBody{CheckVAT: &viesCheckVAT{CountryCode: countryCode, VATNumber: vatNumber}}}
	payload, err := xml.Marshal(requestEnvelope)
	if err != nil {
		return vatVerification{}, err
	}
	endpoint := appconfig.Runtime().VIESEndpoint
	if endpoint == "" {
		endpoint = defaultVIESEndpoint
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return vatVerification{}, err
	}
	request.Header.Set("Content-Type", "text/xml; charset=utf-8")
	client := &http.Client{Timeout: 8 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return vatVerification{}, &viesUnavailableError{cause: err}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return vatVerification{}, err
	}
	var resultEnvelope viesEnvelope
	parseErr := xml.Unmarshal(body, &resultEnvelope)
	// VIES also uses HTTP 500 for permanent SOAP faults; inspect the fault
	// before deciding whether dated positive evidence can bridge an outage.
	if parseErr == nil && resultEnvelope.Body.Fault != nil {
		fault := strings.ToUpper(strings.TrimSpace(resultEnvelope.Body.Fault.String))
		for _, code := range []string{"MS_UNAVAILABLE", "SERVICE_UNAVAILABLE", "TIMEOUT", "SERVER_BUSY", "MAX_CONCURRENT_REQ", "GLOBAL_MAX_CONCURRENT_REQ", "GLOBAL_MAX_CONCURRENT_REQ_TIME", "MS_MAX_CONCURRENT_REQ", "MS_MAX_CONCURRENT_REQ_TIME"} {
			if fault == code {
				return vatVerification{}, &viesUnavailableError{cause: fmt.Errorf("VIES service fault: %s", code)}
			}
		}
		raw, _ := json.Marshal(map[string]string{"verification_fault": fault})
		if err := purserdb.New(h.taxDatabase()).RecordPermanentVIESFault(ctx, purserdb.RecordPermanentVIESFaultParams{TenantID: tenantID, CountryCode: countryCode, VatNumberHash: vatHash, VatNumberMasked: maskVATNumber(countryCode, vatNumber), RawResponse: raw}); err != nil {
			return vatVerification{}, err
		}
		return vatVerification{}, fmt.Errorf("VIES rejected VAT verification")
	}
	if response.StatusCode >= 500 || response.StatusCode == http.StatusTooManyRequests {
		return vatVerification{}, &viesUnavailableError{cause: fmt.Errorf("VIES returned HTTP %d", response.StatusCode)}
	}
	if response.StatusCode != http.StatusOK {
		return vatVerification{}, fmt.Errorf("VIES returned HTTP %d", response.StatusCode)
	}
	if parseErr != nil {
		return vatVerification{}, parseErr
	}
	result := resultEnvelope.Body.Response
	if result == nil {
		return vatVerification{}, fmt.Errorf("VIES response missing checkVatResponse")
	}
	if result.CountryCode != countryCode || result.VATNumber != vatNumber {
		return vatVerification{}, fmt.Errorf("VIES response does not match the requested VAT number")
	}
	requestDate, err := parseVIESRequestDate(result.RequestDate)
	if err != nil {
		return vatVerification{}, fmt.Errorf("VIES response has invalid request date")
	}
	raw, _ := json.Marshal(result)
	masked := maskVATNumber(countryCode, vatNumber)
	snapshot, err := purserdb.New(h.taxDatabase()).UpsertVATValidationEvidence(ctx, purserdb.UpsertVATValidationEvidenceParams{
		TenantID: tenantID, CountryCode: countryCode, VatNumberHash: vatHash,
		VatNumberMasked: masked, Valid: result.Valid, RequestDate: requestDate,
		TraderName:    sql.NullString{String: result.Name, Valid: true},
		TraderAddress: sql.NullString{String: result.Address, Valid: true}, RawResponse: raw,
	})
	if err != nil {
		return vatVerification{}, err
	}
	return vatVerification{Valid: result.Valid, Snapshot: snapshot, Mode: "live_vies"}, nil
}

// VIES requestDate is an xsd:date. Its optional timezone belongs to the
// service's calendar date; converting midnight to UTC could change that day.
func parseVIESRequestDate(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	layout := time.DateOnly
	if len(value) > len(time.DateOnly) {
		layout += "Z07:00"
	}
	parsed, err := time.Parse(layout, value)
	if err != nil {
		return time.Time{}, err
	}
	if len(value) == len(time.DateOnly)+6 && value[len(value)-2:] > "59" {
		return time.Time{}, fmt.Errorf("invalid xsd:date timezone")
	}
	_, offset := parsed.Zone()
	if offset < -14*60*60 || offset > 14*60*60 {
		return time.Time{}, fmt.Errorf("invalid xsd:date timezone")
	}
	year, month, day := parsed.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC), nil
}

func maskVATNumber(country, number string) string {
	return country + strings.Repeat("*", max(0, len(number)-4)) + number[max(0, len(number)-4):]
}
