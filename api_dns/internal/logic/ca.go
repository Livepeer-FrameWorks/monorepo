package logic

import (
	"fmt"
	"os"
	"strings"

	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/registration"
)

// CAProvider identifies an ACME CA that Navigator can use to issue
// certificates. Stored in store.Certificate.issuer_ca; renewals must
// route to the same provider so ARI works.
type CAProvider string

const (
	CALetsEncrypt   CAProvider = "letsencrypt"
	CAGoogleTrust   CAProvider = "google-trust"
	CADefaultIssuer            = CALetsEncrypt
)

// caConfig holds the ACME-protocol details for a single CA.
type caConfig struct {
	Provider     CAProvider
	DirectoryURL string
	// RequiresEAB is true for CAs that require External Account Binding
	// at account creation time (Google Trust Services).
	RequiresEAB bool
	// EAB credentials (populated only when RequiresEAB is true).
	EABKeyID   string
	EABHMACKey string
}

// resolveCAConfig returns the lego configuration for the given CA in
// the current ACME environment (production vs staging from ACME_ENV).
func resolveCAConfig(p CAProvider) (caConfig, error) {
	staging := strings.EqualFold(strings.TrimSpace(os.Getenv("ACME_ENV")), "staging")
	switch p {
	case CALetsEncrypt:
		if staging {
			return caConfig{Provider: CALetsEncrypt, DirectoryURL: lego.LEDirectoryStaging}, nil
		}
		return caConfig{Provider: CALetsEncrypt, DirectoryURL: lego.LEDirectoryProduction}, nil
	case CAGoogleTrust:
		dir := strings.TrimSpace(os.Getenv("NAVIGATOR_GOOGLE_TRUST_DIRECTORY_URL"))
		if dir == "" {
			if staging {
				dir = "https://dv.acme-v02.test-api.pki.goog/directory"
			} else {
				dir = "https://dv.acme-v02.api.pki.goog/directory"
			}
		}
		kid := strings.TrimSpace(os.Getenv("NAVIGATOR_GOOGLE_TRUST_EAB_KID"))
		hmac := strings.TrimSpace(os.Getenv("NAVIGATOR_GOOGLE_TRUST_EAB_HMAC_KEY"))
		if kid == "" || hmac == "" {
			return caConfig{}, fmt.Errorf("google-trust CA requires NAVIGATOR_GOOGLE_TRUST_EAB_KID and NAVIGATOR_GOOGLE_TRUST_EAB_HMAC_KEY")
		}
		return caConfig{
			Provider:     CAGoogleTrust,
			DirectoryURL: dir,
			RequiresEAB:  true,
			EABKeyID:     kid,
			EABHMACKey:   hmac,
		}, nil
	default:
		return caConfig{}, fmt.Errorf("unknown CA provider %q", p)
	}
}

// caOrder returns the CAs to try, in preference order. Production should be
// automagic: Let's Encrypt first, with Google Trust Services added when EAB
// credentials are present. NAVIGATOR_ACME_CA_ORDER remains as a test/ops
// override, not a required deployment knob.
func caOrder() []CAProvider {
	raw := strings.TrimSpace(os.Getenv("NAVIGATOR_ACME_CA_ORDER"))
	if raw == "" {
		order := []CAProvider{CALetsEncrypt}
		if googleTrustConfigured() {
			order = append(order, CAGoogleTrust)
		}
		return order
	}
	parts := strings.Split(raw, ",")
	out := make([]CAProvider, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(strings.ToLower(p))
		if p == "" {
			continue
		}
		switch CAProvider(p) {
		case CALetsEncrypt, CAGoogleTrust:
			out = append(out, CAProvider(p))
		}
	}
	if len(out) == 0 {
		return []CAProvider{CALetsEncrypt}
	}
	return out
}

func googleTrustConfigured() bool {
	return strings.TrimSpace(os.Getenv("NAVIGATOR_GOOGLE_TRUST_EAB_KID")) != "" &&
		strings.TrimSpace(os.Getenv("NAVIGATOR_GOOGLE_TRUST_EAB_HMAC_KEY")) != ""
}

// isRateLimitError returns true if err looks like an ACME rate-limit
// rejection from the CA. Used to drive automatic fallback to the next
// CA in caOrder().
func isRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	// Let's Encrypt returns urn:ietf:params:acme:error:rateLimited with
	// a 429 status when rate limits are hit. The lego error wrapping
	// preserves these strings.
	return strings.Contains(msg, "ratelimited") ||
		strings.Contains(msg, "rate limit") ||
		strings.Contains(msg, "too many certificates") ||
		strings.Contains(msg, "too many new orders")
}

// registerACMEUser performs ACME account registration. For CAs that
// require EAB (Google Trust Services), uses
// RegisterWithExternalAccountBinding. For Let's Encrypt, uses plain
// Register.
func registerACMEUser(client acmeClient, cfg caConfig) (*registration.Resource, error) {
	if !cfg.RequiresEAB {
		return client.Register()
	}
	return client.RegisterWithEAB(registration.RegisterEABOptions{
		TermsOfServiceAgreed: true,
		Kid:                  cfg.EABKeyID,
		HmacEncoded:          cfg.EABHMACKey,
	})
}
