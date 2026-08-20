package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const cryptoSmokeWalletAddress = "0x000000000000000000000000000000000000dEaD"

type cryptoSmokeResult struct {
	BaseURL              string    `json:"base_url"`
	MetadataOK           bool      `json:"metadata_ok"`
	WalletChallengeOK    bool      `json:"wallet_challenge_ok"`
	ChallengeExpiresAt   time.Time `json:"challenge_expires_at"`
	ChallengeWasConsumed bool      `json:"challenge_was_consumed"`
}

func newCryptoSmokeCmd() *cobra.Command {
	var baseURL string
	cmd := &cobra.Command{
		Use:   "smoke",
		Short: "Check public wallet onboarding and x402 metadata without moving funds",
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := runCryptoPublicSmoke(cmd.Context(), &http.Client{Timeout: 15 * time.Second}, baseURL, time.Now())
			if err != nil {
				return err
			}
			return printJSONOrText(cmd, result)
		},
	}
	cmd.Flags().StringVar(&baseURL, "base-url", "https://bridge.frameworks.network", "public Bridge origin")
	return cmd
}

func runCryptoPublicSmoke(ctx context.Context, client *http.Client, rawBaseURL string, now time.Time) (*cryptoSmokeResult, error) {
	base, err := url.Parse(strings.TrimSpace(rawBaseURL))
	if err != nil || base.Scheme == "" || base.Host == "" || (base.Scheme != "https" && base.Scheme != "http") {
		return nil, fmt.Errorf("invalid --base-url")
	}
	if client == nil {
		client = http.DefaultClient
	}
	result := &cryptoSmokeResult{BaseURL: strings.TrimRight(base.String(), "/")}

	metadataURL := *base
	metadataURL.Path = "/.well-known/oauth-protected-resource"
	metadataURL.RawQuery = ""
	metadataURL.Fragment = ""
	var metadata struct {
		Resource    string `json:"resource"`
		AuthMethods map[string]struct {
			Type              string `json:"type"`
			ChallengeEndpoint string `json:"challenge_endpoint"`
			LoginEndpoint     string `json:"login_endpoint"`
		} `json:"x_auth_methods"`
		PaymentMethods map[string]struct {
			Type  string `json:"type"`
			Token string `json:"token"`
		} `json:"x_payment_methods"`
	}
	if smokeErr := cryptoSmokeJSON(ctx, client, http.MethodGet, metadataURL.String(), nil, &metadata); smokeErr != nil {
		return nil, fmt.Errorf("public agent metadata: %w", smokeErr)
	}
	wallet, walletOK := metadata.AuthMethods["wallet"]
	x402Method, x402OK := metadata.PaymentMethods["x402"]
	if metadata.Resource == "" || !walletOK || wallet.Type != "eip191" || wallet.ChallengeEndpoint == "" || wallet.LoginEndpoint == "" ||
		!x402OK || x402Method.Type != "eip3009" || !strings.EqualFold(x402Method.Token, "USDC") {
		return nil, fmt.Errorf("public agent metadata is missing the wallet or x402 contract")
	}
	result.MetadataOK = true

	challengeURL := *base
	challengeURL.Path = "/auth/wallet-challenge"
	challengeURL.RawQuery = ""
	challengeURL.Fragment = ""
	body, err := json.Marshal(map[string]any{"address": cryptoSmokeWalletAddress, "chain_id": 1})
	if err != nil {
		return nil, err
	}
	var challenge struct {
		Message   string    `json:"message"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if smokeErr := cryptoSmokeJSON(ctx, client, http.MethodPost, challengeURL.String(), body, &challenge); smokeErr != nil {
		return nil, fmt.Errorf("wallet challenge: %w", smokeErr)
	}
	if strings.TrimSpace(challenge.Message) == "" || !challenge.ExpiresAt.After(now) {
		return nil, fmt.Errorf("wallet challenge response is incomplete or already expired")
	}
	result.WalletChallengeOK = true
	result.ChallengeExpiresAt = challenge.ExpiresAt
	result.ChallengeWasConsumed = false
	return result, nil
}

func cryptoSmokeJSON(ctx context.Context, client *http.Client, method, endpoint string, body []byte, target any) error {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if len(body) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close() //nolint:errcheck // response body close has no actionable recovery
	limited := io.LimitReader(response.Body, 1<<20)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail, readErr := io.ReadAll(limited)
		if readErr != nil {
			return fmt.Errorf("read error response from %s: %w", endpoint, readErr)
		}
		return fmt.Errorf("%s returned HTTP %d: %s", endpoint, response.StatusCode, strings.TrimSpace(string(detail)))
	}
	if err := json.NewDecoder(limited).Decode(target); err != nil {
		return fmt.Errorf("decode %s: %w", endpoint, err)
	}
	return nil
}
