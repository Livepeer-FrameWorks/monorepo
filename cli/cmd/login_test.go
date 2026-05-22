package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"frameworks/cli/internal/credentials"

	"github.com/spf13/cobra"
)

func TestStartDeviceAuthorizationPostsClientAndScope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/device/start" {
			t.Fatalf("path = %q, want /auth/device/start", r.URL.Path)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["client_id"] != loginClientID {
			t.Fatalf("client_id = %q, want %q", body["client_id"], loginClientID)
		}
		if body["scope"] != loginScope {
			t.Fatalf("scope = %q, want %q", body["scope"], loginScope)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code":               "device-secret",
			"user_code":                 "ABCD-EFGH",
			"verification_uri":          "https://app.example/device",
			"verification_uri_complete": "https://app.example/device?user_code=ABCD-EFGH",
			"expires_in":                300,
			"interval":                  5,
		})
	}))
	defer server.Close()

	oldClient := loginHTTPClient
	loginHTTPClient = server.Client()
	defer func() { loginHTTPClient = oldClient }()

	resp, err := startDeviceAuthorization(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("startDeviceAuthorization: %v", err)
	}
	if resp.DeviceCode != "device-secret" || resp.UserCode != "ABCD-EFGH" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestPollDeviceAuthorizationStoresReturnedTokensShape(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/device/poll" {
			t.Fatalf("path = %q, want /auth/device/poll", r.URL.Path)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["device_code"] != "device-secret" {
			t.Fatalf("device_code = %q", body["device_code"])
		}
		if body["client_id"] != loginClientID {
			t.Fatalf("client_id = %q, want %q", body["client_id"], loginClientID)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"access_token":  "access-token",
			"refresh_token": "refresh-token",
		})
	}))
	defer server.Close()

	oldClient := loginHTTPClient
	loginHTTPClient = server.Client()
	defer func() { loginHTTPClient = oldClient }()

	tokens, err := pollDeviceAuthorization(context.Background(), server.URL, deviceStartResponse{
		DeviceCode: "device-secret",
		Interval:   5,
	})
	if err != nil {
		t.Fatalf("pollDeviceAuthorization: %v", err)
	}
	if tokens.AccessToken != "access-token" || tokens.RefreshToken != "refresh-token" {
		t.Fatalf("tokens = %+v", tokens)
	}
}

func TestValidateLoginTokenUsesBearerValidationEndpoint(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/token/validate" {
			t.Fatalf("path = %q, want /auth/token/validate", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{"valid": true})
	}))
	defer server.Close()

	oldClient := loginHTTPClient
	loginHTTPClient = server.Client()
	defer func() { loginHTTPClient = oldClient }()

	if err := validateLoginToken(context.Background(), server.URL, "fw_token"); err != nil {
		t.Fatalf("validateLoginToken: %v", err)
	}
	if gotAuth != "Bearer fw_token" {
		t.Fatalf("Authorization = %q, want Bearer fw_token", gotAuth)
	}
}

func TestValidateLoginTokenRejectsInvalidToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"authentication failed"}`, http.StatusUnauthorized)
	}))
	defer server.Close()

	oldClient := loginHTTPClient
	loginHTTPClient = server.Client()
	defer func() { loginHTTPClient = oldClient }()

	if err := validateLoginToken(context.Background(), server.URL, "bad-token"); err == nil {
		t.Fatal("expected invalid token to fail validation")
	}
}

func TestSaveLoginTokensStoresRefreshAndClearsStaleRefreshForTokenFlag(t *testing.T) {
	store := &memoryCredentialStore{entries: map[string]string{
		credentials.AccountUserRefresh: "old-refresh",
	}}
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)

	if err := saveLoginTokens(cmd, store, "access-token", "refresh-token"); err != nil {
		t.Fatalf("saveLoginTokens with refresh: %v", err)
	}
	if got := store.entries[credentials.AccountUserSession]; got != "access-token" {
		t.Fatalf("user session = %q", got)
	}
	if got := store.entries[credentials.AccountUserRefresh]; got != "refresh-token" {
		t.Fatalf("refresh token = %q", got)
	}

	if err := saveLoginTokens(cmd, store, "api-token", ""); err != nil {
		t.Fatalf("saveLoginTokens token-only: %v", err)
	}
	if got := store.entries[credentials.AccountUserSession]; got != "api-token" {
		t.Fatalf("user session after token-only = %q", got)
	}
	if _, ok := store.entries[credentials.AccountUserRefresh]; ok {
		t.Fatal("token-only login should clear stale refresh token")
	}
}

type memoryCredentialStore struct {
	entries map[string]string
}

func (s *memoryCredentialStore) Get(account string) (string, error) {
	return s.entries[account], nil
}

func (s *memoryCredentialStore) Set(account, value string) error {
	if s.entries == nil {
		s.entries = map[string]string{}
	}
	s.entries[account] = value
	return nil
}

func (s *memoryCredentialStore) Delete(account string) error {
	delete(s.entries, account)
	return nil
}

func (s *memoryCredentialStore) Name() string { return "memory" }
