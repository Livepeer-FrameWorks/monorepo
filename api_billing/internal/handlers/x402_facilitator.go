package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	cdpauth "github.com/coinbase/cdp-sdk/go/auth"
	x402sdk "github.com/x402-foundation/x402/go/v2"
	x402http "github.com/x402-foundation/x402/go/v2/http"
)

const defaultCDPFacilitatorURL = "https://api.cdp.coinbase.com/platform/v2/x402"

type x402FacilitatorClient interface {
	Verify(ctx context.Context, payloadBytes, requirementsBytes []byte) (*x402sdk.VerifyResponse, error)
	Settle(ctx context.Context, payloadBytes, requirementsBytes []byte) (*x402sdk.SettleResponse, error)
	GetSupported(ctx context.Context) (x402sdk.SupportedResponse, error)
}

type cdpFacilitatorAuth struct {
	keyID     string
	keySecret string
	host      string
	basePath  string
}

func (a *cdpFacilitatorAuth) GetAuthHeaders(_ context.Context) (x402http.AuthHeaders, error) {
	generate := func(method, suffix string) (map[string]string, error) {
		token, err := cdpauth.GenerateJWT(cdpauth.JwtOptions{
			KeyID:         a.keyID,
			KeySecret:     a.keySecret,
			RequestMethod: method,
			RequestHost:   a.host,
			RequestPath:   strings.TrimRight(a.basePath, "/") + suffix,
			ExpiresIn:     120,
		})
		if err != nil {
			return nil, err
		}
		return map[string]string{"Authorization": "Bearer " + token}, nil
	}
	verify, err := generate(http.MethodPost, "/verify")
	if err != nil {
		return x402http.AuthHeaders{}, fmt.Errorf("generate facilitator verify JWT: %w", err)
	}
	settle, err := generate(http.MethodPost, "/settle")
	if err != nil {
		return x402http.AuthHeaders{}, fmt.Errorf("generate facilitator settle JWT: %w", err)
	}
	supported, err := generate(http.MethodGet, "/supported")
	if err != nil {
		return x402http.AuthHeaders{}, fmt.Errorf("generate facilitator supported JWT: %w", err)
	}
	return x402http.AuthHeaders{Verify: verify, Settle: settle, Supported: supported}, nil
}

func newX402FacilitatorFromEnv() (string, x402FacilitatorClient, error) {
	provider := strings.ToLower(strings.TrimSpace(os.Getenv("X402_FACILITATOR_PROVIDER")))
	if provider == "" {
		provider = "self"
	}
	if provider == "self" {
		return provider, nil, nil
	}
	if provider != "cdp" && provider != "hosted" {
		return provider, nil, fmt.Errorf("unsupported facilitator provider %q", provider)
	}

	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("X402_FACILITATOR_URL")), "/")
	if baseURL == "" && provider == "cdp" {
		baseURL = defaultCDPFacilitatorURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return provider, nil, fmt.Errorf("X402_FACILITATOR_URL must be an absolute HTTPS URL")
	}
	var authProvider x402http.AuthProvider
	if provider == "cdp" {
		keyID := strings.TrimSpace(os.Getenv("CDP_API_KEY_ID"))
		keySecret := strings.ReplaceAll(strings.TrimSpace(os.Getenv("CDP_API_KEY_SECRET")), `\n`, "\n")
		if keyID == "" || keySecret == "" {
			return provider, nil, fmt.Errorf("CDP_API_KEY_ID and CDP_API_KEY_SECRET are required")
		}
		authProvider = &cdpFacilitatorAuth{keyID: keyID, keySecret: keySecret, host: parsed.Host, basePath: parsed.Path}
	}
	client := x402http.NewHTTPFacilitatorClient(&x402http.FacilitatorConfig{
		URL:          baseURL,
		AuthProvider: authProvider,
		Timeout:      30 * time.Second,
		Identifier:   provider,
	})
	return provider, client, nil
}

func (h *X402Handler) GetAdvertisableX402Networks(ctx context.Context) ([]NetworkConfig, error) {
	networks := h.GetSupportedNetworks()
	if h.facilitatorProvider == "self" {
		return networks, nil
	}
	if h.facilitator == nil {
		if h.facilitatorConfigErr != nil {
			return nil, h.facilitatorConfigErr
		}
		return nil, fmt.Errorf("x402 facilitator is not configured")
	}

	h.facilitatorMu.Lock()
	defer h.facilitatorMu.Unlock()
	if time.Now().After(h.facilitatorReadyUntil) {
		supported, err := h.facilitator.GetSupported(ctx)
		if err != nil {
			return nil, fmt.Errorf("facilitator supported: %w", err)
		}
		h.facilitatorKinds = make(map[string]bool, len(supported.Kinds))
		for _, kind := range supported.Kinds {
			if kind.X402Version == 2 && kind.Scheme == "exact" {
				h.facilitatorKinds[kind.Network] = true
			}
		}
		h.facilitatorReadyUntil = time.Now().Add(5 * time.Minute)
	}
	advertisable := make([]NetworkConfig, 0, len(networks))
	for _, network := range networks {
		caip2, err := x402CAIP2Network(network)
		if err == nil && h.facilitatorKinds[caip2] {
			advertisable = append(advertisable, network)
		}
	}
	if len(advertisable) == 0 {
		return nil, fmt.Errorf("facilitator supports none of the configured x402 networks")
	}
	return advertisable, nil
}

func (h *X402Handler) verifyWithFacilitator(ctx context.Context, payload *X402PaymentPayload, quote *X402PaymentQuote) (string, error) {
	if h.facilitatorProvider == "self" {
		return "", nil
	}
	if h.facilitator == nil || quote == nil || len(payload.CanonicalPayloadJSON) == 0 {
		return "", fmt.Errorf("official x402 facilitator verification is unavailable")
	}
	result, err := h.facilitator.Verify(ctx, payload.CanonicalPayloadJSON, quote.AcceptedRequirement)
	if err != nil {
		return "", fmt.Errorf("facilitator verify: %w", err)
	}
	if result == nil || !result.IsValid {
		reason := "facilitator rejected payment"
		if result != nil && result.InvalidReason != "" {
			reason = result.InvalidReason
		}
		return "", fmt.Errorf("%s", reason)
	}
	return strings.ToLower(result.Payer), nil
}

func (h *X402Handler) settleWithFacilitator(ctx context.Context, payload *X402PaymentPayload, quote *X402PaymentQuote) (string, error) {
	if h.facilitator == nil || quote == nil || len(payload.CanonicalPayloadJSON) == 0 {
		return "", fmt.Errorf("official x402 facilitator settlement is unavailable")
	}
	result, err := h.facilitator.Settle(ctx, payload.CanonicalPayloadJSON, quote.AcceptedRequirement)
	if err != nil {
		return "", fmt.Errorf("facilitator settle: %w", err)
	}
	if result == nil || !result.Success || strings.TrimSpace(result.Transaction) == "" {
		reason := "facilitator rejected settlement"
		if result != nil && result.ErrorReason != "" {
			reason = result.ErrorReason
		}
		return "", fmt.Errorf("%s", reason)
	}
	if string(result.Network) != quote.CAIP2Network {
		return "", fmt.Errorf("facilitator returned transaction on unexpected network")
	}
	return result.Transaction, nil
}
