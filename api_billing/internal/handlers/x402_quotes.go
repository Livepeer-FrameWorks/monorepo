//nolint:govet,errcheck,errorlint // Quote validation uses local decode scopes and best-effort expiry marking.
package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"strings"
	"time"

	"frameworks/api_billing/internal/database/purserdb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/google/uuid"
	x402sdk "github.com/x402-foundation/x402/go/v2"
)

const (
	defaultX402QuoteTTL              = 5 * time.Minute
	defaultX402PrepaidBufferEurCents = int64(500)
)

type X402PaymentQuote struct {
	ID                  string
	TenantID            string
	Resource            string
	ResourceClass       string
	Network             NetworkConfig
	CAIP2Network        string
	Asset               string
	PayTo               string
	AmountAtomic        string
	CreditAmountCents   int64
	EurPerUSDRate       float64
	ExpiresAt           time.Time
	AcceptedRequirement []byte
	ExtraJSON           []byte
	TaxDocumentKind     string
	TaxProfileSnapshot  []byte
}

func (h *X402Handler) loadPaymentQuote(ctx context.Context, tenantID, quoteID string) (*X402PaymentQuote, string, error) {
	if quoteID == "" {
		return nil, "", fmt.Errorf("x402 v2 quote ID is required")
	}
	stored, err := purserdb.New(h.db).GetX402PaymentQuote(ctx, purserdb.GetX402PaymentQuoteParams{QuoteID: quoteID, TenantID: tenantID})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, "", fmt.Errorf("x402 quote not found for tenant")
		}
		return nil, "", fmt.Errorf("load x402 quote: %w", err)
	}
	quote := X402PaymentQuote{
		ID: stored.ID, TenantID: stored.TenantID, Resource: stored.Resource,
		ResourceClass: stored.ResourceClass, CAIP2Network: stored.Network,
		Asset: stored.Asset, PayTo: stored.PayTo, AmountAtomic: stored.AmountAtomic,
		CreditAmountCents: stored.CreditAmountCents, AcceptedRequirement: stored.RequirementsJson,
		TaxDocumentKind: stored.TaxDocumentKind, TaxProfileSnapshot: stored.TaxProfileSnapshot,
		ExpiresAt: stored.ExpiresAt,
	}
	if _, err := fmt.Sscan(stored.EurPerUsdRate, &quote.EurPerUSDRate); err != nil || quote.EurPerUSDRate <= 0 {
		return nil, "", fmt.Errorf("x402 quote has invalid FX rate")
	}
	return &quote, stored.Status, nil
}

func (h *X402Handler) validateV2Quote(ctx context.Context, tenantID string, payload *X402PaymentPayload) (*X402PaymentQuote, NetworkConfig, error) {
	if payload == nil || payload.Accepted == nil {
		return nil, NetworkConfig{}, fmt.Errorf("accepted requirements are required")
	}
	quote, status, err := h.loadPaymentQuote(ctx, tenantID, payload.QuoteID)
	if err != nil {
		return nil, NetworkConfig{}, err
	}
	if time.Now().UTC().After(quote.ExpiresAt) {
		_ = purserdb.New(h.db).ExpireOfferedX402PaymentQuote(ctx, quote.ID)
		return nil, NetworkConfig{}, fmt.Errorf("x402 quote expired")
	}
	if status != "offered" && status != "claiming" && status != "settling" && status != "unknown" && status != "confirmed" {
		return nil, NetworkConfig{}, fmt.Errorf("x402 quote is not payable")
	}
	accepted := payload.Accepted
	var expected x402sdk.PaymentRequirements
	if err := json.Unmarshal(quote.AcceptedRequirement, &expected); err != nil {
		return nil, NetworkConfig{}, fmt.Errorf("stored x402 requirements are invalid")
	}
	var currentExtra map[string]interface{}
	if err := json.Unmarshal(accepted.ExtraJSON, &currentExtra); err != nil {
		return nil, NetworkConfig{}, fmt.Errorf("accepted requirement extensions are invalid")
	}
	if accepted.Scheme != "exact" ||
		accepted.Network != quote.CAIP2Network ||
		!strings.EqualFold(accepted.Asset, quote.Asset) ||
		accepted.Amount != quote.AmountAtomic ||
		!strings.EqualFold(accepted.PayTo, quote.PayTo) ||
		accepted.MaxTimeoutSeconds != expected.MaxTimeoutSeconds ||
		!reflect.DeepEqual(currentExtra, expected.Extra) {
		return nil, NetworkConfig{}, fmt.Errorf("accepted requirements do not match immutable quote")
	}
	if payload.Payload == nil || payload.Payload.Authorization == nil ||
		payload.Payload.Authorization.Value != quote.AmountAtomic ||
		!strings.EqualFold(payload.Payload.Authorization.To, quote.PayTo) {
		return nil, NetworkConfig{}, fmt.Errorf("authorization does not match immutable quote")
	}
	network, err := h.networkForCAIP2(quote.CAIP2Network)
	if err != nil {
		return nil, NetworkConfig{}, err
	}
	quote.Network = network
	return quote, network, nil
}

func (h *X402Handler) networkForCAIP2(caip2 string) (NetworkConfig, error) {
	for _, network := range h.GetSupportedNetworks() {
		candidate, err := x402CAIP2Network(network)
		if err == nil && candidate == caip2 {
			return network, nil
		}
	}
	return NetworkConfig{}, fmt.Errorf("unsupported x402 network: %s", caip2)
}

func (h *X402Handler) claimPaymentQuote(ctx context.Context, quoteID string) (bool, error) {
	if quoteID == "" {
		return true, nil
	}
	claimToken := uuid.NewString()
	rows, err := purserdb.New(h.db).ClaimX402PaymentQuote(ctx, purserdb.ClaimX402PaymentQuoteParams{ClaimToken: claimToken, QuoteID: quoteID})
	if err != nil {
		return false, fmt.Errorf("claim x402 quote: %w", err)
	}
	return rows == 1, nil
}

// ExpirePaymentQuote withdraws an internal quote that was created while
// determining payment-specific tax requirements but was never exposed.
func (h *X402Handler) ExpirePaymentQuote(ctx context.Context, quoteID string) error {
	if strings.TrimSpace(quoteID) == "" {
		return nil
	}
	return purserdb.New(h.db).ExpireOfferedX402PaymentQuote(ctx, quoteID)
}

func (h *X402Handler) CreatePaymentQuote(ctx context.Context, tenantID, resource, payTo string, network NetworkConfig) (*X402PaymentQuote, error) {
	if tenantID == "" || payTo == "" {
		return nil, fmt.Errorf("tenant and payTo are required")
	}
	rate, err := h.getEurUsdRate()
	if err != nil || rate <= 0 {
		return nil, fmt.Errorf("load EUR/USD rate: %w", err)
	}

	balanceCents, err := purserdb.New(h.db).GetX402PrepaidBalanceCents(ctx, tenantID)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("load prepaid balance for quote: %w", err)
	}

	bufferCents := int64(config.GetEnvInt("X402_PREPAID_BUFFER_EUR_CENTS", int(defaultX402PrepaidBufferEurCents)))
	if bufferCents <= 0 {
		bufferCents = defaultX402PrepaidBufferEurCents
	}
	deficitCents := int64(0)
	if balanceCents < 0 {
		deficitCents = -balanceCents
	}
	targetCreditCents := deficitCents + bufferCents
	minimumCreditCents := int64(math.Round(float64(h.RequiredTopupUSDCents()) * rate))
	if targetCreditCents < minimumCreditCents {
		targetCreditCents = minimumCreditCents
	}

	usdCents := int64(math.Ceil(float64(targetCreditCents) / rate))
	creditCents := int64(math.Round(float64(usdCents) * rate))
	for creditCents < targetCreditCents {
		usdCents++
		creditCents = int64(math.Round(float64(usdCents) * rate))
	}
	atomic := new(big.Int).Mul(big.NewInt(usdCents), big.NewInt(10_000)).String()
	documentRequirement, err := h.GetCryptoDocumentRequirement(ctx, tenantID, creditCents)
	if err != nil {
		return nil, fmt.Errorf("determine crypto tax-document requirement: %w", err)
	}
	taxProfileSnapshot, err := json.Marshal(documentRequirement.Profile)
	if err != nil {
		return nil, fmt.Errorf("marshal crypto tax profile: %w", err)
	}
	quoteID := uuid.NewString()
	expiresAt := time.Now().UTC().Add(defaultX402QuoteTTL)
	caip2, err := x402CAIP2Network(network)
	if err != nil {
		return nil, err
	}
	extra := map[string]interface{}{
		"name":                x402USDCDomainName(network),
		"version":             "2",
		"assetTransferMethod": "eip3009",
		"frameworks": map[string]interface{}{
			"quoteId":       quoteID,
			"resourceClass": classifyX402Resource(resource),
			"expiresAt":     expiresAt.Format(time.RFC3339),
		},
	}
	accepted := x402sdk.PaymentRequirements{
		Scheme:            "exact",
		Network:           caip2,
		Asset:             network.USDCContract,
		Amount:            atomic,
		PayTo:             strings.ToLower(payTo),
		MaxTimeoutSeconds: 60,
		Extra:             extra,
	}
	acceptedJSON, err := json.Marshal(accepted)
	if err != nil {
		return nil, fmt.Errorf("marshal quote requirements: %w", err)
	}
	extraJSON, err := json.Marshal(extra)
	if err != nil {
		return nil, fmt.Errorf("marshal quote extra: %w", err)
	}

	err = purserdb.New(h.db).CreateX402PaymentQuote(ctx, purserdb.CreateX402PaymentQuoteParams{
		ID: quoteID, TenantID: tenantID, Resource: resource,
		ResourceClass: classifyX402Resource(resource), Network: caip2,
		Asset: network.USDCContract, PayTo: strings.ToLower(payTo), AmountAtomic: atomic,
		CreditAmountCents: creditCents, EurPerUsdRate: fmt.Sprintf("%.10f", rate),
		RequirementsJson: acceptedJSON, TaxDocumentKind: documentRequirement.DocumentKind,
		TaxProfileSnapshot: taxProfileSnapshot, ExpiresAt: expiresAt,
	})
	if err != nil {
		return nil, fmt.Errorf("persist x402 quote: %w", err)
	}

	return &X402PaymentQuote{
		ID:                  quoteID,
		TenantID:            tenantID,
		Resource:            resource,
		ResourceClass:       classifyX402Resource(resource),
		Network:             network,
		CAIP2Network:        caip2,
		Asset:               network.USDCContract,
		PayTo:               strings.ToLower(payTo),
		AmountAtomic:        atomic,
		CreditAmountCents:   creditCents,
		EurPerUSDRate:       rate,
		ExpiresAt:           expiresAt,
		AcceptedRequirement: acceptedJSON,
		ExtraJSON:           extraJSON,
		TaxDocumentKind:     documentRequirement.DocumentKind,
		TaxProfileSnapshot:  taxProfileSnapshot,
	}, nil
}

func x402USDCDomainName(network NetworkConfig) string {
	if strings.TrimSpace(network.USDCDomainName) != "" {
		return network.USDCDomainName
	}
	return "USD Coin"
}

func x402CAIP2Network(network NetworkConfig) (string, error) {
	if network.ChainID <= 0 {
		return "", fmt.Errorf("network %q has invalid chain ID", network.Name)
	}
	return fmt.Sprintf("eip155:%d", network.ChainID), nil
}

func classifyX402Resource(resource string) string {
	resource = strings.ToLower(strings.TrimSpace(resource))
	switch {
	case strings.HasPrefix(resource, "viewer://"):
		return "viewer"
	case strings.HasPrefix(resource, "graphql://"):
		return "graphql"
	case strings.HasPrefix(resource, "mcp://"):
		return "mcp"
	case strings.HasPrefix(resource, "/"):
		return "http"
	default:
		return "api"
	}
}

// EnforceQuoteRateLimits applies durable tenant/IP abuse controls before any
// receiving-address allocation or immutable quote rows are created.
func (h *X402Handler) EnforceQuoteRateLimits(ctx context.Context, tenantID, clientIP string) error {
	return h.enforceQuoteRateLimits(ctx, tenantID, clientIP)
}
