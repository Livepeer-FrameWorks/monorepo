package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRunCryptoPublicSmokeValidatesMetadataAndDoesNotConsumeChallenge(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	challengeCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-protected-resource":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"resource": "https://bridge.example/mcp",
				"x_auth_methods": map[string]any{"wallet": map[string]any{
					"type": "eip191", "challenge_endpoint": "https://bridge.example/auth/wallet-challenge", "login_endpoint": "https://bridge.example/auth/wallet-login",
				}},
				"x_payment_methods": map[string]any{"x402": map[string]any{"type": "eip3009", "token": "USDC"}},
			})
		case "/auth/wallet-challenge":
			challengeCalls++
			if r.Method != http.MethodPost || r.Header.Get("PAYMENT-SIGNATURE") != "" || r.Header.Get("Authorization") != "" {
				t.Fatalf("challenge request used payment/auth credentials")
			}
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if request["address"] != cryptoSmokeWalletAddress {
				t.Fatalf("address = %#v", request["address"])
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "sign this challenge", "expires_at": now.Add(5 * time.Minute)})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result, err := runCryptoPublicSmoke(context.Background(), server.Client(), server.URL, now)
	if err != nil {
		t.Fatal(err)
	}
	if !result.MetadataOK || !result.WalletChallengeOK || result.ChallengeWasConsumed || challengeCalls != 1 {
		t.Fatalf("result=%+v challengeCalls=%d", result, challengeCalls)
	}
}

func TestRunCryptoPublicSmokeRejectsPaymentWallOnWalletChallenge(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/oauth-protected-resource" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"resource":          "https://bridge.example/mcp",
				"x_auth_methods":    map[string]any{"wallet": map[string]any{"type": "eip191", "challenge_endpoint": "challenge", "login_endpoint": "login"}},
				"x_payment_methods": map[string]any{"x402": map[string]any{"type": "eip3009", "token": "USDC"}},
			})
			return
		}
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"error":"payment required"}`))
	}))
	defer server.Close()

	if _, err := runCryptoPublicSmoke(context.Background(), server.Client(), server.URL, time.Now()); err == nil {
		t.Fatal("expected a wallet challenge payment wall to fail the smoke check")
	}
}
