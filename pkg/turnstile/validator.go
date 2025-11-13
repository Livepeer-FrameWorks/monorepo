package turnstile

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const verifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

type Validator struct {
	secretKey  string
	httpClient *http.Client
}

type VerifyResponse struct {
	Success     bool     `json:"success"`
	ErrorCodes  []string `json:"error-codes"`
	ChallengeTS string   `json:"challenge_ts"`
	Hostname    string   `json:"hostname"`
}

func NewValidator(secretKey string) *Validator {
	return &Validator{
		secretKey: secretKey,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (v *Validator) Verify(ctx context.Context, token, remoteIP string) (*VerifyResponse, error) {
	if v.secretKey == "" {
		return &VerifyResponse{Success: true, ErrorCodes: []string{}}, nil
	}

	if token == "" {
		return &VerifyResponse{
			Success:    false,
			ErrorCodes: []string{"missing-input-response"},
		}, nil
	}

	data := url.Values{}
	data.Set("secret", v.secretKey)
	data.Set("response", token)
	if remoteIP != "" {
		data.Set("remoteip", remoteIP)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, verifyURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := v.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var result VerifyResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &result, nil
}
