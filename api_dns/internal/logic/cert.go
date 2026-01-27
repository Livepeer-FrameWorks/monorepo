package logic

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"frameworks/api_dns/internal/store"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/providers/dns/cloudflare"
	"github.com/go-acme/lego/v4/registration"
)

// CertManager handles certificate issuance logic
type CertManager struct {
	store *store.Store
}

// NewCertManager creates a new CertManager
func NewCertManager(s *store.Store) *CertManager {
	return &CertManager{store: s}
}

// ACMEUser implements lego.User
type ACMEUser struct {
	Email        string
	Registration *registration.Resource
	key          crypto.PrivateKey
}

func (u *ACMEUser) GetEmail() string                        { return u.Email }
func (u *ACMEUser) GetRegistration() *registration.Resource { return u.Registration }
func (u *ACMEUser) GetPrivateKey() crypto.PrivateKey        { return u.key }

// IssueCertificate requests a certificate for a domain using Cloudflare DNS-01.
// It implements "Cache-First" logic.
// tenantID is optional - empty string means platform-wide certificate.
func (m *CertManager) IssueCertificate(ctx context.Context, tenantID, domain, email string) (certPEM, keyPEM string, expiresAt time.Time, err error) {
	if domain == "" || email == "" {
		return "", "", time.Time{}, fmt.Errorf("domain and email are required")
	}
	if !isDomainAllowed(domain) {
		return "", "", time.Time{}, fmt.Errorf("domain is not allowed for certificate issuance")
	}

	// 1. Check Cache (DB) - with tenant context
	cert, err := m.store.GetCertificate(ctx, tenantID, domain)
	if err == nil {
		// Check expiry (renew if < 30 days remaining)
		if time.Until(cert.ExpiresAt) > 30*24*time.Hour {
			return cert.CertPEM, cert.KeyPEM, cert.ExpiresAt, nil
		}
		// If expiring soon, proceed to renewal logic (below)
	} else if !errors.Is(err, store.ErrNotFound) {
		return "", "", time.Time{}, fmt.Errorf("failed to check certificate cache: %w", err)
	}

	// 2. Load or Create ACME User (with tenant context)
	user, err := m.getOrCreateUser(ctx, tenantID, email)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("failed to load ACME user: %w", err)
	}

	// 3. Initialize Lego config
	config := lego.NewConfig(user)
	switch strings.ToLower(os.Getenv("ACME_ENV")) {
	case "staging":
		config.CADirURL = lego.LEDirectoryStaging
	default:
		config.CADirURL = lego.LEDirectoryProduction
	}
	config.Certificate.KeyType = certcrypto.EC256

	// 4. Create Lego client
	client, err := lego.NewClient(config)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("failed to create lego client: %w", err)
	}

	// 5. Setup Cloudflare Provider
	if os.Getenv("CLOUDFLARE_DNS_API_TOKEN") == "" && os.Getenv("CLOUDFLARE_API_TOKEN") != "" {
		os.Setenv("CLOUDFLARE_DNS_API_TOKEN", os.Getenv("CLOUDFLARE_API_TOKEN"))
	}

	provider, err := cloudflare.NewDNSProvider()
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("failed to create cloudflare provider: %w", err)
	}

	if err := client.Challenge.SetDNS01Provider(provider); err != nil {
		return "", "", time.Time{}, fmt.Errorf("failed to set DNS provider: %w", err)
	}

	// 6. Register User (if new)
	if user.Registration == nil {
		reg, err := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
		if err != nil {
			return "", "", time.Time{}, fmt.Errorf("registration failed: %w", err)
		}
		user.Registration = reg
		// Save updated registration to DB (with tenant context)
		if err := m.saveUser(ctx, tenantID, user); err != nil {
			return "", "", time.Time{}, fmt.Errorf("failed to save user registration: %w", err)
		}
	}

	// 7. Obtain Certificate
	request := certificate.ObtainRequest{
		Domains: []string{domain},
		Bundle:  true,
	}
	certificates, err := client.Certificate.Obtain(request)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("failed to obtain certificate: %w", err)
	}

	// 8. Parse Expiry
	// We need to parse the cert to get the expiry date
	expiry := time.Now().Add(90 * 24 * time.Hour) // Fallback
	block, _ := pem.Decode(certificates.Certificate)
	if block != nil {
		if parsedCert, err := x509.ParseCertificate(block.Bytes); err == nil {
			expiry = parsedCert.NotAfter
		}
	}

	// 9. Save to DB (with tenant context)
	newCert := &store.Certificate{
		Domain:    domain,
		CertPEM:   string(certificates.Certificate),
		KeyPEM:    string(certificates.PrivateKey),
		ExpiresAt: expiry,
	}
	if err := m.store.SaveCertificate(ctx, tenantID, newCert); err != nil {
		return "", "", time.Time{}, fmt.Errorf("failed to save certificate: %w", err)
	}

	return newCert.CertPEM, newCert.KeyPEM, expiry, nil
}

func isDomainAllowed(domain string) bool {
	domain = strings.TrimSpace(strings.ToLower(strings.TrimSuffix(domain, ".")))
	if domain == "" {
		return false
	}
	domain = strings.TrimPrefix(domain, "*.")

	env := strings.TrimSpace(os.Getenv("NAVIGATOR_CERT_ALLOWED_SUFFIXES"))
	var suffixes []string
	if env != "" {
		for _, s := range strings.Split(env, ",") {
			s = strings.TrimSpace(strings.ToLower(strings.TrimSuffix(s, ".")))
			if s != "" {
				suffixes = append(suffixes, s)
			}
		}
	} else if root := strings.TrimSpace(os.Getenv("NAVIGATOR_ROOT_DOMAIN")); root != "" {
		suffixes = []string{strings.ToLower(strings.TrimSuffix(root, "."))}
	}

	// If no allowlist is configured, allow all (dev mode).
	if len(suffixes) == 0 {
		return true
	}

	for _, suffix := range suffixes {
		if domain == suffix || strings.HasSuffix(domain, "."+suffix) {
			return true
		}
	}
	return false
}

// GetCertificate retrieves a certificate from the store.
// tenantID is optional - empty string means platform-wide certificate.
func (m *CertManager) GetCertificate(ctx context.Context, tenantID, domain string) (*store.Certificate, error) {
	return m.store.GetCertificate(ctx, tenantID, domain)
}

func (m *CertManager) getOrCreateUser(ctx context.Context, tenantID, email string) (*ACMEUser, error) {
	// Try DB (with tenant context)
	acc, err := m.store.GetACMEAccount(ctx, tenantID, email)
	if err == nil {
		// Parse Private Key
		block, _ := pem.Decode([]byte(acc.PrivateKeyPEM))
		key, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("failed to parse stored private key: %w", err)
		}

		// Parse Registration
		var reg registration.Resource
		if err := json.Unmarshal([]byte(acc.Registration), &reg); err != nil {
			return nil, fmt.Errorf("failed to parse stored registration: %w", err)
		}

		return &ACMEUser{
			Email:        email,
			Registration: &reg,
			key:          key,
		}, nil
	}

	// Generate new
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	return &ACMEUser{
		Email: email,
		key:   key,
	}, nil
}

func (m *CertManager) saveUser(ctx context.Context, tenantID string, user *ACMEUser) error {
	// Serialize Key
	ecKey, ok := user.key.(*ecdsa.PrivateKey)
	if !ok {
		return fmt.Errorf("unexpected key type: expected *ecdsa.PrivateKey")
	}
	keyBytes, err := x509.MarshalECPrivateKey(ecKey)
	if err != nil {
		return err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})

	// Serialize Registration
	regJSON, err := json.Marshal(user.Registration)
	if err != nil {
		return err
	}

	acc := &store.ACMEAccount{
		Email:         user.Email,
		Registration:  string(regJSON),
		PrivateKeyPEM: string(keyPEM),
	}
	return m.store.SaveACMEAccount(ctx, tenantID, acc)
}
