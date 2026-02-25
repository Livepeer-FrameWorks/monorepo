package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	fieldcrypt "frameworks/pkg/crypto"
)

var ErrNotFound = errors.New("record not found")

type Certificate struct {
	ID        string
	TenantID  sql.NullString // NULL for platform certificates, set for tenant subdomains (platform-managed)
	Domain    string
	CertPEM   string
	KeyPEM    string
	ExpiresAt time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

type ACMEAccount struct {
	ID            string
	TenantID      sql.NullString // NULL for platform accounts, set for tenant-specific accounts
	Email         string
	Registration  string // JSON blob
	PrivateKeyPEM string
	CreatedAt     time.Time
}

type Store struct {
	db  *sql.DB
	enc *fieldcrypt.FieldEncryptor // nil = no encryption (backward-compatible)
}

func NewStore(db *sql.DB, enc *fieldcrypt.FieldEncryptor) *Store {
	return &Store{db: db, enc: enc}
}

func (s *Store) encryptField(plaintext string) (string, error) {
	if s.enc == nil {
		return plaintext, nil
	}
	return s.enc.Encrypt(plaintext)
}

func (s *Store) decryptField(stored string) (string, error) {
	if s.enc == nil {
		return stored, nil
	}
	return s.enc.Decrypt(stored)
}

// GetCertificate retrieves a valid certificate for a domain within a tenant context.
// If tenantID is empty, retrieves platform-wide certificate (tenant_id IS NULL).
func (s *Store) GetCertificate(ctx context.Context, tenantID, domain string) (*Certificate, error) {
	var query string
	var args []interface{}

	if tenantID == "" {
		query = `
			SELECT id, tenant_id, domain, cert_pem, key_pem, expires_at, created_at, updated_at
			FROM navigator.certificates
			WHERE tenant_id IS NULL AND domain = $1
		`
		args = []interface{}{domain}
	} else {
		query = `
			SELECT id, tenant_id, domain, cert_pem, key_pem, expires_at, created_at, updated_at
			FROM navigator.certificates
			WHERE tenant_id = $1 AND domain = $2
		`
		args = []interface{}{tenantID, domain}
	}

	var cert Certificate
	err := s.db.QueryRowContext(ctx, query, args...).Scan(
		&cert.ID, &cert.TenantID, &cert.Domain, &cert.CertPEM, &cert.KeyPEM,
		&cert.ExpiresAt, &cert.CreatedAt, &cert.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if cert.KeyPEM, err = s.decryptField(cert.KeyPEM); err != nil {
		return nil, fmt.Errorf("decrypt certificate key: %w", err)
	}
	return &cert, nil
}

// SaveCertificate saves or updates a certificate for a tenant.
// If tenantID is empty, saves as a platform-wide certificate.
func (s *Store) SaveCertificate(ctx context.Context, tenantID string, cert *Certificate) error {
	encryptedKey, err := s.encryptField(cert.KeyPEM)
	if err != nil {
		return fmt.Errorf("encrypt certificate key: %w", err)
	}
	query := `
		INSERT INTO navigator.certificates (tenant_id, domain, cert_pem, key_pem, expires_at, updated_at)
		VALUES (NULLIF($1, '')::uuid, $2, $3, $4, $5, NOW())
		ON CONFLICT (tenant_id, domain) DO UPDATE SET
			cert_pem = EXCLUDED.cert_pem,
			key_pem = EXCLUDED.key_pem,
			expires_at = EXCLUDED.expires_at,
			updated_at = NOW()
		RETURNING id, tenant_id, created_at
	`
	return s.db.QueryRowContext(ctx, query,
		tenantID, cert.Domain, cert.CertPEM, encryptedKey, cert.ExpiresAt,
	).Scan(&cert.ID, &cert.TenantID, &cert.CreatedAt)
}

// GetACMEAccount retrieves an ACME account by email within a tenant context.
// If tenantID is empty, retrieves platform-wide account.
func (s *Store) GetACMEAccount(ctx context.Context, tenantID, email string) (*ACMEAccount, error) {
	var query string
	var args []interface{}

	if tenantID == "" {
		query = `
			SELECT id, tenant_id, email, registration_json, private_key_pem, created_at
			FROM navigator.acme_accounts
			WHERE tenant_id IS NULL AND email = $1
		`
		args = []interface{}{email}
	} else {
		query = `
			SELECT id, tenant_id, email, registration_json, private_key_pem, created_at
			FROM navigator.acme_accounts
			WHERE tenant_id = $1 AND email = $2
		`
		args = []interface{}{tenantID, email}
	}

	var acc ACMEAccount
	err := s.db.QueryRowContext(ctx, query, args...).Scan(
		&acc.ID, &acc.TenantID, &acc.Email, &acc.Registration, &acc.PrivateKeyPEM, &acc.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if acc.PrivateKeyPEM, err = s.decryptField(acc.PrivateKeyPEM); err != nil {
		return nil, fmt.Errorf("decrypt ACME private key: %w", err)
	}
	return &acc, nil
}

// SaveACMEAccount saves a new ACME account for a tenant.
// If tenantID is empty, saves as a platform-wide account.
func (s *Store) SaveACMEAccount(ctx context.Context, tenantID string, acc *ACMEAccount) error {
	encryptedKey, err := s.encryptField(acc.PrivateKeyPEM)
	if err != nil {
		return fmt.Errorf("encrypt ACME private key: %w", err)
	}
	query := `
		INSERT INTO navigator.acme_accounts (tenant_id, email, registration_json, private_key_pem)
		VALUES (NULLIF($1, '')::uuid, $2, $3, $4)
		ON CONFLICT (tenant_id, email) DO UPDATE SET
			registration_json = EXCLUDED.registration_json,
			private_key_pem = EXCLUDED.private_key_pem
		RETURNING id, tenant_id, created_at
	`
	return s.db.QueryRowContext(ctx, query,
		tenantID, acc.Email, acc.Registration, encryptedKey,
	).Scan(&acc.ID, &acc.TenantID, &acc.CreatedAt)
}

// ListExpiringCertificates finds certs expiring within the given duration.
// Returns all certificates (platform-wide and tenant-specific) that are expiring.
func (s *Store) ListExpiringCertificates(ctx context.Context, threshold time.Duration) ([]Certificate, error) {
	expiryLimit := time.Now().Add(threshold)
	query := `
		SELECT id, tenant_id, domain, cert_pem, key_pem, expires_at, created_at, updated_at
		FROM navigator.certificates
		WHERE expires_at < $1
		ORDER BY expires_at ASC
	`
	rows, err := s.db.QueryContext(ctx, query, expiryLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var certs []Certificate
	for rows.Next() {
		var c Certificate
		if err := rows.Scan(&c.ID, &c.TenantID, &c.Domain, &c.CertPEM, &c.KeyPEM, &c.ExpiresAt, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		if c.KeyPEM, err = s.decryptField(c.KeyPEM); err != nil {
			return nil, fmt.Errorf("decrypt certificate key for %s: %w", c.Domain, err)
		}
		certs = append(certs, c)
	}
	return certs, nil
}

// ListCertificatesForTenant returns all certificates belonging to a specific tenant.
func (s *Store) ListCertificatesForTenant(ctx context.Context, tenantID string) ([]Certificate, error) {
	var query string
	var args []interface{}

	if tenantID == "" {
		query = `
			SELECT id, tenant_id, domain, cert_pem, key_pem, expires_at, created_at, updated_at
			FROM navigator.certificates
			WHERE tenant_id IS NULL
			ORDER BY domain
		`
	} else {
		query = `
			SELECT id, tenant_id, domain, cert_pem, key_pem, expires_at, created_at, updated_at
			FROM navigator.certificates
			WHERE tenant_id = $1
			ORDER BY domain
		`
		args = []interface{}{tenantID}
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var certs []Certificate
	for rows.Next() {
		var c Certificate
		if err := rows.Scan(&c.ID, &c.TenantID, &c.Domain, &c.CertPEM, &c.KeyPEM, &c.ExpiresAt, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		if c.KeyPEM, err = s.decryptField(c.KeyPEM); err != nil {
			return nil, fmt.Errorf("decrypt certificate key for %s: %w", c.Domain, err)
		}
		certs = append(certs, c)
	}
	return certs, nil
}
