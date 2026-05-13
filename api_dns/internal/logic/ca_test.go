package logic

import (
	"errors"
	"testing"
)

func TestCAOrderDefault(t *testing.T) {
	t.Setenv("NAVIGATOR_ACME_CA_ORDER", "")
	t.Setenv("NAVIGATOR_GOOGLE_TRUST_EAB_KID", "")
	t.Setenv("NAVIGATOR_GOOGLE_TRUST_EAB_HMAC_KEY", "")
	got := caOrder()
	if len(got) != 1 || got[0] != CALetsEncrypt {
		t.Fatalf("default CA order = %v, want [letsencrypt]", got)
	}
}

func TestCAOrderAddsGoogleTrustWhenEABConfigured(t *testing.T) {
	t.Setenv("NAVIGATOR_ACME_CA_ORDER", "")
	t.Setenv("NAVIGATOR_GOOGLE_TRUST_EAB_KID", "kid-123")
	t.Setenv("NAVIGATOR_GOOGLE_TRUST_EAB_HMAC_KEY", "hmac-base64")
	got := caOrder()
	if len(got) != 2 || got[0] != CALetsEncrypt || got[1] != CAGoogleTrust {
		t.Fatalf("CA order with GTS creds = %v, want [letsencrypt, google-trust]", got)
	}
}

func TestCAOrderParse(t *testing.T) {
	t.Setenv("NAVIGATOR_ACME_CA_ORDER", "letsencrypt, google-trust")
	got := caOrder()
	if len(got) != 2 || got[0] != CALetsEncrypt || got[1] != CAGoogleTrust {
		t.Fatalf("CA order = %v, want [letsencrypt, google-trust]", got)
	}
}

func TestCAOrderIgnoresUnknown(t *testing.T) {
	t.Setenv("NAVIGATOR_ACME_CA_ORDER", "letsencrypt,zerossl,google-trust")
	got := caOrder()
	if len(got) != 2 || got[0] != CALetsEncrypt || got[1] != CAGoogleTrust {
		t.Fatalf("CA order = %v, want [letsencrypt, google-trust]", got)
	}
}

func TestCAOrderFallsBackOnAllUnknown(t *testing.T) {
	t.Setenv("NAVIGATOR_ACME_CA_ORDER", "zerossl,buypass")
	got := caOrder()
	if len(got) != 1 || got[0] != CALetsEncrypt {
		t.Fatalf("CA order on all-unknown = %v, want default [letsencrypt]", got)
	}
}

func TestResolveCAConfigLetsEncrypt(t *testing.T) {
	t.Setenv("ACME_ENV", "production")
	cfg, err := resolveCAConfig(CALetsEncrypt)
	if err != nil {
		t.Fatalf("resolveCAConfig: %v", err)
	}
	if cfg.RequiresEAB {
		t.Fatalf("LE should not require EAB")
	}
	if cfg.DirectoryURL == "" {
		t.Fatalf("LE directory URL empty")
	}
}

func TestResolveCAConfigGoogleTrustRequiresEAB(t *testing.T) {
	t.Setenv("NAVIGATOR_GOOGLE_TRUST_EAB_KID", "")
	t.Setenv("NAVIGATOR_GOOGLE_TRUST_EAB_HMAC_KEY", "")
	if _, err := resolveCAConfig(CAGoogleTrust); err == nil {
		t.Fatal("expected GTS to fail without EAB creds")
	}

	t.Setenv("NAVIGATOR_GOOGLE_TRUST_EAB_KID", "kid-123")
	t.Setenv("NAVIGATOR_GOOGLE_TRUST_EAB_HMAC_KEY", "hmac-base64")
	cfg, err := resolveCAConfig(CAGoogleTrust)
	if err != nil {
		t.Fatalf("resolveCAConfig with creds: %v", err)
	}
	if !cfg.RequiresEAB {
		t.Fatal("GTS must require EAB")
	}
	if cfg.EABKeyID != "kid-123" || cfg.EABHMACKey != "hmac-base64" {
		t.Errorf("EAB creds not propagated: %+v", cfg)
	}
}

func TestResolveCAConfigUnknown(t *testing.T) {
	if _, err := resolveCAConfig("zerossl"); err == nil {
		t.Fatal("expected error for unknown CA")
	}
}

func TestIsRateLimitError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"random", errors.New("connection refused"), false},
		{"rateLimited urn", errors.New("acme: error: urn:ietf:params:acme:error:rateLimited :: too many"), true},
		{"too many certificates", errors.New("too many certificates already issued for"), true},
		{"too many new orders", errors.New("too many new orders for account"), true},
		{"plain rate limit", errors.New("Rate limit exceeded"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRateLimitError(tc.err); got != tc.want {
				t.Errorf("isRateLimitError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
