package logic

import (
	"errors"
	"testing"
)

func TestCAOrderDefault(t *testing.T) {
	got := caOrder(IssuanceSettings{})
	if len(got) != 1 || got[0] != CALetsEncrypt {
		t.Fatalf("default CA order = %v, want [letsencrypt]", got)
	}
}

func TestCAOrderAddsGoogleTrustWhenEABConfigured(t *testing.T) {
	got := caOrder(IssuanceSettings{GoogleTrustEABKeyID: "kid-123", GoogleTrustEABHMACKey: "hmac-base64"})
	if len(got) != 2 || got[0] != CALetsEncrypt || got[1] != CAGoogleTrust {
		t.Fatalf("CA order with GTS creds = %v, want [letsencrypt, google-trust]", got)
	}
}

func TestCAOrderParse(t *testing.T) {
	got := caOrder(IssuanceSettings{CAOrder: "letsencrypt, google-trust"})
	if len(got) != 2 || got[0] != CALetsEncrypt || got[1] != CAGoogleTrust {
		t.Fatalf("CA order = %v, want [letsencrypt, google-trust]", got)
	}
}

func TestCAOrderIgnoresUnknown(t *testing.T) {
	got := caOrder(IssuanceSettings{CAOrder: "letsencrypt,zerossl,google-trust"})
	if len(got) != 2 || got[0] != CALetsEncrypt || got[1] != CAGoogleTrust {
		t.Fatalf("CA order = %v, want [letsencrypt, google-trust]", got)
	}
}

func TestCAOrderFallsBackOnAllUnknown(t *testing.T) {
	got := caOrder(IssuanceSettings{CAOrder: "zerossl,buypass"})
	if len(got) != 1 || got[0] != CALetsEncrypt {
		t.Fatalf("CA order on all-unknown = %v, want default [letsencrypt]", got)
	}
}

func TestResolveCAConfigLetsEncrypt(t *testing.T) {
	cfg, err := resolveCAConfig(CALetsEncrypt, IssuanceSettings{ACMEEnv: "production"})
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

func TestResolveCAConfigStagingDirectories(t *testing.T) {
	settings := IssuanceSettings{ACMEEnv: " Staging ", GoogleTrustEABKeyID: "kid", GoogleTrustEABHMACKey: "hmac"}
	le, err := resolveCAConfig(CALetsEncrypt, settings)
	if err != nil {
		t.Fatalf("resolveCAConfig LE: %v", err)
	}
	if le.DirectoryURL != "https://acme-staging-v02.api.letsencrypt.org/directory" {
		t.Fatalf("LE staging directory = %q", le.DirectoryURL)
	}
	gts, err := resolveCAConfig(CAGoogleTrust, settings)
	if err != nil {
		t.Fatalf("resolveCAConfig GTS: %v", err)
	}
	if gts.DirectoryURL != "https://dv.acme-v02.test-api.pki.goog/directory" {
		t.Fatalf("GTS staging directory = %q", gts.DirectoryURL)
	}
}

func TestResolveCAConfigGoogleTrustRequiresEAB(t *testing.T) {
	if _, err := resolveCAConfig(CAGoogleTrust, IssuanceSettings{}); err == nil {
		t.Fatal("expected GTS to fail without EAB creds")
	}

	cfg, err := resolveCAConfig(CAGoogleTrust, IssuanceSettings{GoogleTrustEABKeyID: "kid-123", GoogleTrustEABHMACKey: "hmac-base64"})
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
	if _, err := resolveCAConfig("zerossl", IssuanceSettings{}); err == nil {
		t.Fatal("expected error for unknown CA")
	}
}

func TestCertManagerReadsIssuanceSettingsAtUse(t *testing.T) {
	manager := NewCertManager(&fakeStore{})
	if !isDomainAllowed("anything.example.org", manager.issuanceSettings()) {
		t.Fatal("expected every domain to be allowed without a settings source")
	}

	current := IssuanceSettings{RootDomain: "frameworks.network"}
	manager.SetIssuanceSettings(func() IssuanceSettings { return current })
	if isDomainAllowed("anything.example.org", manager.issuanceSettings()) {
		t.Fatal("expected the root domain fallback to restrict issuance")
	}

	current = IssuanceSettings{RootDomain: "frameworks.network", AllowedSuffixes: "example.org"}
	if !isDomainAllowed("anything.example.org", manager.issuanceSettings()) {
		t.Fatal("expected a replaced settings value to apply to the next check")
	}
	if isDomainAllowed("edge.frameworks.network", manager.issuanceSettings()) {
		t.Fatal("expected the explicit allowlist to replace the root domain fallback")
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
