package middleware

import (
	"context"
	"net/http"
	"testing"

	"frameworks/api_gateway/internal/clients"
)

func TestAuthenticateRequestNilRequest(t *testing.T) {
	_, err := AuthenticateRequest(context.Background(), nil, &clients.ServiceClients{}, []byte("secret"), AuthOptions{}, nil)
	if err == nil {
		t.Fatal("expected error for nil request")
	}
}

func TestAuthenticateRequestWalletMissingHeaders(t *testing.T) {
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	req.Header.Set("X-Wallet-Address", "0xabc")

	_, err := AuthenticateRequest(context.Background(), req, &clients.ServiceClients{}, []byte("secret"), AuthOptions{AllowWallet: true}, nil)
	if err == nil {
		t.Fatal("expected error for missing wallet headers")
	}
}

func TestAuthenticateRequestInvalidX402Header(t *testing.T) {
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	req.Header.Set("X-PAYMENT", "not-base64")

	_, err := AuthenticateRequest(context.Background(), req, &clients.ServiceClients{}, []byte("secret"), AuthOptions{AllowX402: true}, nil)
	if err == nil {
		t.Fatal("expected error for invalid X-PAYMENT header")
	}
}

func TestAuthenticateRequestNoAuth(t *testing.T) {
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)

	result, err := AuthenticateRequest(context.Background(), req, &clients.ServiceClients{}, []byte("secret"), AuthOptions{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil result for no auth, got %#v", result)
	}
}
