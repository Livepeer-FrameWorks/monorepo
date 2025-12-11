package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var ErrNotFound = errors.New("record not found")

type Certificate struct {
	ID        string
	Domain    string
	CertPEM   string
	KeyPEM    string
	ExpiresAt time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

type ACMEAccount struct {
	Email         string
	Registration  string // JSON blob
	PrivateKeyPEM string
	CreatedAt     time.Time
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// GetCertificate retrieves a valid certificate for a domain
func (s *Store) GetCertificate(ctx context.Context, domain string) (*Certificate, error) {
	query := `
		SELECT id, domain, cert_pem, key_pem, expires_at, created_at, updated_at
		FROM navigator.certificates
		WHERE domain = $1
	`
	var cert Certificate
	err := s.db.QueryRowContext(ctx, query, domain).Scan(
		&cert.ID, &cert.Domain, &cert.CertPEM, &cert.KeyPEM,
		&cert.ExpiresAt, &cert.CreatedAt, &cert.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &cert, nil
}

// SaveCertificate saves or updates a certificate
func (s *Store) SaveCertificate(ctx context.Context, cert *Certificate) error {
	query := `
		INSERT INTO navigator.certificates (domain, cert_pem, key_pem, expires_at, updated_at)
		VALUES ($1, $2, $3, $4, NOW())
		ON CONFLICT (domain) DO UPDATE SET
			cert_pem = EXCLUDED.cert_pem,
			key_pem = EXCLUDED.key_pem,
			expires_at = EXCLUDED.expires_at,
			updated_at = NOW()
		RETURNING id, created_at
	`
	return s.db.QueryRowContext(ctx, query,
		cert.Domain, cert.CertPEM, cert.KeyPEM, cert.ExpiresAt,
	).Scan(&cert.ID, &cert.CreatedAt)
}

// GetACMEAccount retrieves an ACME account by email
func (s *Store) GetACMEAccount(ctx context.Context, email string) (*ACMEAccount, error) {
	query := `
		SELECT email, registration_json, private_key_pem, created_at
		FROM navigator.acme_accounts
		WHERE email = $1
	`
	var acc ACMEAccount
	err := s.db.QueryRowContext(ctx, query, email).Scan(
		&acc.Email, &acc.Registration, &acc.PrivateKeyPEM, &acc.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &acc, nil
}

// SaveACMEAccount saves a new ACME account
func (s *Store) SaveACMEAccount(ctx context.Context, acc *ACMEAccount) error {
	query := `
		INSERT INTO navigator.acme_accounts (email, registration_json, private_key_pem)
		VALUES ($1, $2, $3)
		ON CONFLICT (email) DO UPDATE SET
			registration_json = EXCLUDED.registration_json,
			private_key_pem = EXCLUDED.private_key_pem
		RETURNING created_at
	`
	return s.db.QueryRowContext(ctx, query,
		acc.Email, acc.Registration, acc.PrivateKeyPEM,
	).Scan(&acc.CreatedAt)
}

// ListExpiringCertificates finds certs expiring within the given duration
func (s *Store) ListExpiringCertificates(ctx context.Context, threshold time.Duration) ([]Certificate, error) {
	expiryLimit := time.Now().Add(threshold)
	query := `
		SELECT id, domain, cert_pem, key_pem, expires_at, created_at, updated_at
		FROM navigator.certificates
		WHERE expires_at < $1
	`
	rows, err := s.db.QueryContext(ctx, query, expiryLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var certs []Certificate
	for rows.Next() {
		var c Certificate
		if err := rows.Scan(&c.ID, &c.Domain, &c.CertPEM, &c.KeyPEM, &c.ExpiresAt, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		certs = append(certs, c)
	}
	return certs, nil
}
