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

// IssueCertificate requests a certificate for a domain using Cloudflare DNS-01
// It implements "Cache-First" logic.
func (m *CertManager) IssueCertificate(ctx context.Context, domain, email string) (certPEM, keyPEM string, err error) {
	if domain == "" || email == "" {
		return "", "", fmt.Errorf("domain and email are required")
	}

	// 1. Check Cache (DB)
	cert, err := m.store.GetCertificate(ctx, domain)
	if err == nil {
		// Check expiry (renew if < 30 days remaining)
		if time.Until(cert.ExpiresAt) > 30*24*time.Hour {
			return cert.CertPEM, cert.KeyPEM, nil
		}
		// If expiring soon, proceed to renewal logic (below)
	} else if !errors.Is(err, store.ErrNotFound) {
		return "", "", fmt.Errorf("failed to check certificate cache: %w", err)
	}

	// 2. Load or Create ACME User
	user, err := m.getOrCreateUser(ctx, email)
	if err != nil {
		return "", "", fmt.Errorf("failed to load ACME user: %w", err)
	}

	// 3. Initialize Lego config
	config := lego.NewConfig(user)
	config.CADirURL = lego.LEDirectoryProduction
	config.Certificate.KeyType = certcrypto.EC256

	// 4. Create Lego client
	client, err := lego.NewClient(config)
	if err != nil {
		return "", "", fmt.Errorf("failed to create lego client: %w", err)
	}

	// 5. Setup Cloudflare Provider
	if os.Getenv("CLOUDFLARE_DNS_API_TOKEN") == "" && os.Getenv("CLOUDFLARE_API_TOKEN") != "" {
		os.Setenv("CLOUDFLARE_DNS_API_TOKEN", os.Getenv("CLOUDFLARE_API_TOKEN"))
	}

	provider, err := cloudflare.NewDNSProvider()
	if err != nil {
		return "", "", fmt.Errorf("failed to create cloudflare provider: %w", err)
	}

	if err := client.Challenge.SetDNS01Provider(provider); err != nil {
		return "", "", fmt.Errorf("failed to set DNS provider: %w", err)
	}

	// 6. Register User (if new)
	if user.Registration == nil {
		reg, err := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
		if err != nil {
			return "", "", fmt.Errorf("registration failed: %w", err)
		}
		user.Registration = reg
		// Save updated registration to DB
		if err := m.saveUser(ctx, user); err != nil {
			return "", "", fmt.Errorf("failed to save user registration: %w", err)
		}
	}

	// 7. Obtain Certificate
	request := certificate.ObtainRequest{
		Domains: []string{domain},
		Bundle:  true,
	}
	certificates, err := client.Certificate.Obtain(request)
	if err != nil {
		return "", "", fmt.Errorf("failed to obtain certificate: %w", err)
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

	// 9. Save to DB
	newCert := &store.Certificate{
		Domain:    domain,
		CertPEM:   string(certificates.Certificate),
		KeyPEM:    string(certificates.PrivateKey),
		ExpiresAt: expiry,
	}
	if err := m.store.SaveCertificate(ctx, newCert); err != nil {
		// Log error but return cert anyway? No, better to fail or retry.
		return "", "", fmt.Errorf("failed to save certificate: %w", err)
	}

	return newCert.CertPEM, newCert.KeyPEM, nil
}

func (m *CertManager) getOrCreateUser(ctx context.Context, email string) (*ACMEUser, error) {
	// Try DB
	acc, err := m.store.GetACMEAccount(ctx, email)
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

func (m *CertManager) saveUser(ctx context.Context, user *ACMEUser) error {
	// Serialize Key
	keyBytes, err := x509.MarshalECPrivateKey(user.key.(*ecdsa.PrivateKey))
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
	return m.store.SaveACMEAccount(ctx, acc)
}
