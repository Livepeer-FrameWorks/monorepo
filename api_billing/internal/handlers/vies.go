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
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
)

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

func (h *X402Handler) validateVIESVAT(ctx context.Context, tenantID, countryCode, vatNumber string) (bool, error) {
	countryCode = strings.ToUpper(strings.TrimSpace(countryCode))
	vatNumber = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(vatNumber), " ", ""))
	vatNumber = strings.TrimPrefix(vatNumber, countryCode)
	if len(countryCode) != 2 || vatNumber == "" {
		return false, fmt.Errorf("invalid VAT identifier")
	}
	hashBytes := sha256.Sum256([]byte(countryCode + vatNumber))
	vatHash := hex.EncodeToString(hashBytes[:])
	cached, err := purserdb.New(h.db).GetCachedVATValidation(ctx, purserdb.GetCachedVATValidationParams{
		TenantID: tenantID, VatNumberHash: vatHash,
	})
	if err == nil {
		return cached, nil
	}
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}

	requestEnvelope := viesEnvelope{Body: viesBody{CheckVAT: &viesCheckVAT{CountryCode: countryCode, VATNumber: vatNumber}}}
	payload, err := xml.Marshal(requestEnvelope)
	if err != nil {
		return false, err
	}
	endpoint := strings.TrimSpace(os.Getenv("VIES_ENDPOINT"))
	if endpoint == "" {
		endpoint = defaultVIESEndpoint
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return false, err
	}
	request.Header.Set("Content-Type", "text/xml; charset=utf-8")
	client := &http.Client{Timeout: 8 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return false, err
	}
	if response.StatusCode != http.StatusOK {
		return false, fmt.Errorf("VIES returned HTTP %d", response.StatusCode)
	}
	var resultEnvelope viesEnvelope
	if err := xml.Unmarshal(body, &resultEnvelope); err != nil {
		return false, err
	}
	if resultEnvelope.Body.Fault != nil {
		return false, fmt.Errorf("VIES fault: %s", resultEnvelope.Body.Fault.String)
	}
	result := resultEnvelope.Body.Response
	if result == nil {
		return false, fmt.Errorf("VIES response missing checkVatResponse")
	}
	requestDate, err := time.Parse("2006-01-02", result.RequestDate)
	if err != nil {
		return false, fmt.Errorf("VIES response has invalid request date")
	}
	raw, _ := json.Marshal(result)
	masked := countryCode + strings.Repeat("*", max(0, len(vatNumber)-4))
	if len(vatNumber) > 4 {
		masked += vatNumber[len(vatNumber)-4:]
	} else {
		masked += vatNumber
	}
	err = purserdb.New(h.db).UpsertVATValidationEvidence(ctx, purserdb.UpsertVATValidationEvidenceParams{
		TenantID: tenantID, CountryCode: countryCode, VatNumberHash: vatHash,
		VatNumberMasked: masked, Valid: result.Valid, RequestDate: requestDate,
		TraderName:    sql.NullString{String: result.Name, Valid: true},
		TraderAddress: sql.NullString{String: result.Address, Valid: true}, RawResponse: raw,
	})
	if err != nil {
		return false, err
	}
	return result.Valid, nil
}
