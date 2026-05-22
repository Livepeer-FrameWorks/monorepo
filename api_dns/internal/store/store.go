package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	fieldcrypt "github.com/Livepeer-FrameWorks/monorepo/pkg/crypto"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
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
	// IssuerCA records which ACME CA signed this cert ('letsencrypt' or
	// 'google-trust'). Renewals must route to the same CA so ARI works.
	IssuerCA string
}

type ACMEAccount struct {
	ID            string
	TenantID      sql.NullString // NULL for platform accounts, set for tenant-specific accounts
	Email         string
	Registration  string // JSON blob (CA-specific account URL)
	PrivateKeyPEM string
	CreatedAt     time.Time
	// CA identifies the ACME directory this account is registered with
	// ('letsencrypt' | 'google-trust'). Account keys are CA-specific;
	// the same email at a different CA is a separate registration.
	CA string
}

type TLSBundle struct {
	ID        string
	BundleID  string
	Domains   []string
	CertPEM   string
	KeyPEM    string
	ExpiresAt time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
	// IssuerCA tracks which ACME CA signed this bundle ('letsencrypt' |
	// 'google-trust'). Renewals must route to the same CA so the ACME
	// account, ARI hints and rate-limit pool stay consistent. Matches
	// store.Certificate.IssuerCA.
	IssuerCA string
}

type InternalCA struct {
	Role      string
	CertPEM   string
	KeyPEM    string
	ExpiresAt time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

type InternalCertificate struct {
	ID          string
	NodeID      string
	ClusterID   string
	ServiceType string
	CertPEM     string
	KeyPEM      string
	ExpiresAt   time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// TenantAlias persists per-tenant alias intent + ACME lifecycle state.
// One row per paying tenant. Driven by Quartermaster.EnsureTenantAlias.
type TenantAlias struct {
	TenantID     string
	Subdomain    string
	Status       string // cert_issuing | cert_issued | cert_failed | tearing_down
	CertIssuedAt sql.NullTime
	LastError    sql.NullString
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// TenantCustomDomain persists per-tenant custom domain state. Driven by
// Quartermaster.EnsureCustomDomain when tenants.custom_domain changes for
// a paid tenant. Navigator runs verification + ACME-DNS-01 delegation +
// cert issuance through the same RenewalWorker that drives tenant aliases.
type TenantCustomDomain struct {
	TenantID         string
	Domain           string
	Status           string // pending_verification | verified | cert_issuing | cert_issued | cert_failed | tearing_down
	AcmeDNSSubdomain string
	IssuerID         sql.NullString
	LastVerifiedAt   sql.NullTime
	CertIssuedAt     sql.NullTime
	CertExpiresAt    sql.NullTime
	LastError        sql.NullString
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// TenantEdgeApplyState records per-(tenant, edge, bundle) state. Drives
// DNS membership decisions for tenant smart record sets in cdn.{root}.
type TenantEdgeApplyState struct {
	TenantID        string
	ClusterID       string
	NodeID          string
	BundleID        string
	State           string // pending_distribute | pending_apply | applied | in_dns
	LastSeedVersion sql.NullInt64
	LastAckAt       sql.NullTime
	InDNSAt         sql.NullTime
	UpdatedAt       time.Time
}

type Store struct {
	db  *sql.DB
	enc *fieldcrypt.FieldEncryptor // nil = no encryption (backward-compatible)
}

func NewStore(db *sql.DB, enc *fieldcrypt.FieldEncryptor) *Store {
	return &Store{db: db, enc: enc}
}

func marshalDomains(domains []string) ([]byte, error) {
	if len(domains) == 0 {
		return []byte("[]"), nil
	}
	clean := append([]string(nil), domains...)
	slices.Sort(clean)
	return json.Marshal(clean)
}

func unmarshalDomains(raw []byte) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var domains []string
	if err := json.Unmarshal(raw, &domains); err != nil {
		return nil, err
	}
	return domains, nil
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
			SELECT id, tenant_id, domain, cert_pem, key_pem, expires_at, created_at, updated_at, issuer_ca
			FROM navigator.certificates
			WHERE tenant_id IS NULL AND domain = $1
		`
		args = []interface{}{domain}
	} else {
		query = `
			SELECT id, tenant_id, domain, cert_pem, key_pem, expires_at, created_at, updated_at, issuer_ca
			FROM navigator.certificates
			WHERE tenant_id = $1 AND domain = $2
		`
		args = []interface{}{tenantID, domain}
	}

	var cert Certificate
	err := s.db.QueryRowContext(ctx, query, args...).Scan(
		&cert.ID, &cert.TenantID, &cert.Domain, &cert.CertPEM, &cert.KeyPEM,
		&cert.ExpiresAt, &cert.CreatedAt, &cert.UpdatedAt, &cert.IssuerCA,
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
	issuer := cert.IssuerCA
	if issuer == "" {
		issuer = "letsencrypt"
	}
	if tenantID == "" {
		query := `
			INSERT INTO navigator.certificates (tenant_id, domain, cert_pem, key_pem, expires_at, updated_at, issuer_ca)
			VALUES (NULL, $1, $2, $3, $4, NOW(), $5)
			ON CONFLICT (domain) WHERE tenant_id IS NULL DO UPDATE SET
				cert_pem = EXCLUDED.cert_pem,
				key_pem = EXCLUDED.key_pem,
				expires_at = EXCLUDED.expires_at,
				updated_at = NOW(),
				issuer_ca = EXCLUDED.issuer_ca
			RETURNING id, tenant_id, created_at
		`
		return s.db.QueryRowContext(ctx, query,
			cert.Domain, cert.CertPEM, encryptedKey, cert.ExpiresAt, issuer,
		).Scan(&cert.ID, &cert.TenantID, &cert.CreatedAt)
	}

	query := `
		INSERT INTO navigator.certificates (tenant_id, domain, cert_pem, key_pem, expires_at, updated_at, issuer_ca)
		VALUES ($1::uuid, $2, $3, $4, $5, NOW(), $6)
		ON CONFLICT (tenant_id, domain) DO UPDATE SET
			cert_pem = EXCLUDED.cert_pem,
			key_pem = EXCLUDED.key_pem,
			expires_at = EXCLUDED.expires_at,
			updated_at = NOW(),
			issuer_ca = EXCLUDED.issuer_ca
		RETURNING id, tenant_id, created_at
	`
	return s.db.QueryRowContext(ctx, query,
		tenantID, cert.Domain, cert.CertPEM, encryptedKey, cert.ExpiresAt, issuer,
	).Scan(&cert.ID, &cert.TenantID, &cert.CreatedAt)
}

// DeleteCertificate removes a stored cert (and its encrypted key) for the
// given (tenant_id, domain). Used during custom-domain teardown so cert
// material doesn't outlive the lifecycle row. Idempotent on missing rows.
func (s *Store) DeleteCertificate(ctx context.Context, tenantID, domain string) error {
	if tenantID == "" {
		_, err := s.db.ExecContext(ctx, `
			DELETE FROM navigator.certificates
			WHERE tenant_id IS NULL AND domain = $1
		`, domain)
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM navigator.certificates
		WHERE tenant_id = $1::uuid AND domain = $2
	`, tenantID, domain)
	return err
}

// GetACMEAccount retrieves an ACME account scoped to (tenant, email, ca).
// If tenantID is empty, retrieves the platform-wide account for that CA.
// ca should be a non-empty value like "letsencrypt" or "google-trust";
// callers that pass "" are migrated to "letsencrypt" for back-compat
// with rows that pre-date per-CA scoping.
func (s *Store) GetACMEAccount(ctx context.Context, tenantID, email, ca string) (*ACMEAccount, error) {
	if ca == "" {
		ca = "letsencrypt"
	}
	var query string
	var args []interface{}

	if tenantID == "" {
		query = `
			SELECT id, tenant_id, email, registration_json, private_key_pem, created_at, ca
			FROM navigator.acme_accounts
			WHERE tenant_id IS NULL AND email = $1 AND ca = $2
		`
		args = []interface{}{email, ca}
	} else {
		query = `
			SELECT id, tenant_id, email, registration_json, private_key_pem, created_at, ca
			FROM navigator.acme_accounts
			WHERE tenant_id = $1 AND email = $2 AND ca = $3
		`
		args = []interface{}{tenantID, email, ca}
	}

	var acc ACMEAccount
	err := s.db.QueryRowContext(ctx, query, args...).Scan(
		&acc.ID, &acc.TenantID, &acc.Email, &acc.Registration, &acc.PrivateKeyPEM, &acc.CreatedAt, &acc.CA,
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

// SaveACMEAccount upserts an ACME account scoped to (tenant, email, ca).
// If acc.CA is unset, defaults to 'letsencrypt'. If tenantID is empty,
// saves as a platform-wide account.
func (s *Store) SaveACMEAccount(ctx context.Context, tenantID string, acc *ACMEAccount) error {
	if acc.CA == "" {
		acc.CA = "letsencrypt"
	}
	encryptedKey, err := s.encryptField(acc.PrivateKeyPEM)
	if err != nil {
		return fmt.Errorf("encrypt ACME private key: %w", err)
	}
	if tenantID == "" {
		query := `
			INSERT INTO navigator.acme_accounts (tenant_id, email, registration_json, private_key_pem, ca)
			VALUES (NULL, $1, $2, $3, $4)
			ON CONFLICT (email, ca) WHERE tenant_id IS NULL DO UPDATE SET
				registration_json = EXCLUDED.registration_json,
				private_key_pem = EXCLUDED.private_key_pem
			RETURNING id, tenant_id, created_at
		`
		return s.db.QueryRowContext(ctx, query,
			acc.Email, acc.Registration, encryptedKey, acc.CA,
		).Scan(&acc.ID, &acc.TenantID, &acc.CreatedAt)
	}

	query := `
		INSERT INTO navigator.acme_accounts (tenant_id, email, registration_json, private_key_pem, ca)
		VALUES ($1::uuid, $2, $3, $4, $5)
		ON CONFLICT (tenant_id, email, ca) DO UPDATE SET
			registration_json = EXCLUDED.registration_json,
			private_key_pem = EXCLUDED.private_key_pem
		RETURNING id, tenant_id, created_at
	`
	return s.db.QueryRowContext(ctx, query,
		tenantID, acc.Email, acc.Registration, encryptedKey, acc.CA,
	).Scan(&acc.ID, &acc.TenantID, &acc.CreatedAt)
}

// ListExpiringCertificates finds certs expiring within the given duration.
// Returns all certificates (platform-wide and tenant-specific) that are expiring.
func (s *Store) ListExpiringCertificates(ctx context.Context, threshold time.Duration) ([]Certificate, error) {
	expiryLimit := time.Now().Add(threshold)
	query := `
		SELECT id, tenant_id, domain, cert_pem, key_pem, expires_at, created_at, updated_at, issuer_ca
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
		if scanErr := rows.Scan(&c.ID, &c.TenantID, &c.Domain, &c.CertPEM, &c.KeyPEM, &c.ExpiresAt, &c.CreatedAt, &c.UpdatedAt, &c.IssuerCA); scanErr != nil {
			return nil, scanErr
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
			SELECT id, tenant_id, domain, cert_pem, key_pem, expires_at, created_at, updated_at, issuer_ca
			FROM navigator.certificates
			WHERE tenant_id IS NULL
			ORDER BY domain
		`
	} else {
		query = `
			SELECT id, tenant_id, domain, cert_pem, key_pem, expires_at, created_at, updated_at, issuer_ca
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
		if scanErr := rows.Scan(&c.ID, &c.TenantID, &c.Domain, &c.CertPEM, &c.KeyPEM, &c.ExpiresAt, &c.CreatedAt, &c.UpdatedAt, &c.IssuerCA); scanErr != nil {
			return nil, scanErr
		}
		if c.KeyPEM, err = s.decryptField(c.KeyPEM); err != nil {
			return nil, fmt.Errorf("decrypt certificate key for %s: %w", c.Domain, err)
		}
		certs = append(certs, c)
	}
	return certs, nil
}

func (s *Store) GetTLSBundle(ctx context.Context, bundleID string) (*TLSBundle, error) {
	query := `
		SELECT id, bundle_id, domains, cert_pem, key_pem, expires_at, created_at, updated_at, issuer_ca
		FROM navigator.tls_bundles
		WHERE bundle_id = $1
	`

	var bundle TLSBundle
	var domainsJSON []byte
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		return s.db.QueryRowContext(ctx, query, bundleID).Scan(
			&bundle.ID, &bundle.BundleID, &domainsJSON, &bundle.CertPEM, &bundle.KeyPEM,
			&bundle.ExpiresAt, &bundle.CreatedAt, &bundle.UpdatedAt, &bundle.IssuerCA,
		)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if bundle.KeyPEM, err = s.decryptField(bundle.KeyPEM); err != nil {
		return nil, fmt.Errorf("decrypt tls bundle key: %w", err)
	}
	bundle.Domains, err = unmarshalDomains(domainsJSON)
	if err != nil {
		return nil, fmt.Errorf("decode tls bundle domains: %w", err)
	}
	return &bundle, nil
}

func (s *Store) SaveTLSBundle(ctx context.Context, bundle *TLSBundle) error {
	encryptedKey, err := s.encryptField(bundle.KeyPEM)
	if err != nil {
		return fmt.Errorf("encrypt tls bundle key: %w", err)
	}
	domainsJSON, err := marshalDomains(bundle.Domains)
	if err != nil {
		return fmt.Errorf("encode tls bundle domains: %w", err)
	}

	issuer := strings.TrimSpace(bundle.IssuerCA)
	if issuer == "" {
		issuer = "letsencrypt"
	}
	query := `
		INSERT INTO navigator.tls_bundles (bundle_id, domains, cert_pem, key_pem, expires_at, issuer_ca, updated_at)
		VALUES ($1, $2::jsonb, $3, $4, $5, $6, NOW())
		ON CONFLICT (bundle_id) DO UPDATE SET
			domains = EXCLUDED.domains,
			cert_pem = EXCLUDED.cert_pem,
			key_pem = EXCLUDED.key_pem,
			expires_at = EXCLUDED.expires_at,
			issuer_ca = EXCLUDED.issuer_ca,
			updated_at = NOW()
		RETURNING id, created_at
	`
	return database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		return s.db.QueryRowContext(ctx, query,
			bundle.BundleID, string(domainsJSON), bundle.CertPEM, encryptedKey, bundle.ExpiresAt, issuer,
		).Scan(&bundle.ID, &bundle.CreatedAt)
	})
}

func (s *Store) ListExpiringTLSBundles(ctx context.Context, threshold time.Duration) ([]TLSBundle, error) {
	expiryLimit := time.Now().Add(threshold)
	query := `
		SELECT id, bundle_id, domains, cert_pem, key_pem, expires_at, created_at, updated_at, issuer_ca
		FROM navigator.tls_bundles
		WHERE expires_at < $1
		ORDER BY expires_at ASC
	`
	rows, err := s.db.QueryContext(ctx, query, expiryLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var bundles []TLSBundle
	for rows.Next() {
		var bundle TLSBundle
		var domainsJSON []byte
		if scanErr := rows.Scan(
			&bundle.ID, &bundle.BundleID, &domainsJSON, &bundle.CertPEM, &bundle.KeyPEM,
			&bundle.ExpiresAt, &bundle.CreatedAt, &bundle.UpdatedAt, &bundle.IssuerCA,
		); scanErr != nil {
			return nil, scanErr
		}
		if bundle.KeyPEM, err = s.decryptField(bundle.KeyPEM); err != nil {
			return nil, fmt.Errorf("decrypt tls bundle key for %s: %w", bundle.BundleID, err)
		}
		bundle.Domains, err = unmarshalDomains(domainsJSON)
		if err != nil {
			return nil, fmt.Errorf("decode tls bundle domains for %s: %w", bundle.BundleID, err)
		}
		bundles = append(bundles, bundle)
	}

	return bundles, nil
}

func (s *Store) GetInternalCA(ctx context.Context, role string) (*InternalCA, error) {
	query := `
		SELECT role, cert_pem, key_pem, expires_at, created_at, updated_at
		FROM navigator.internal_ca
		WHERE role = $1
	`

	var ca InternalCA
	var keyPEM sql.NullString
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		return s.db.QueryRowContext(ctx, query, role).Scan(
			&ca.Role, &ca.CertPEM, &keyPEM, &ca.ExpiresAt, &ca.CreatedAt, &ca.UpdatedAt,
		)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if keyPEM.Valid {
		ca.KeyPEM = keyPEM.String
	}
	if ca.KeyPEM != "" {
		if ca.KeyPEM, err = s.decryptField(ca.KeyPEM); err != nil {
			return nil, fmt.Errorf("decrypt internal ca key: %w", err)
		}
	}
	return &ca, nil
}

func (s *Store) SaveInternalCA(ctx context.Context, ca *InternalCA) error {
	var encryptedKey *string
	if ca.KeyPEM != "" {
		encoded, err := s.encryptField(ca.KeyPEM)
		if err != nil {
			return fmt.Errorf("encrypt internal ca key: %w", err)
		}
		encryptedKey = &encoded
	}

	query := `
		INSERT INTO navigator.internal_ca (role, cert_pem, key_pem, expires_at, updated_at)
		VALUES ($1, $2, $3, $4, NOW())
		ON CONFLICT (role) DO UPDATE SET
			cert_pem = EXCLUDED.cert_pem,
			key_pem = EXCLUDED.key_pem,
			expires_at = EXCLUDED.expires_at,
			updated_at = NOW()
		RETURNING created_at
	`
	return database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		return s.db.QueryRowContext(ctx, query, ca.Role, ca.CertPEM, encryptedKey, ca.ExpiresAt).Scan(&ca.CreatedAt)
	})
}

func (s *Store) GetInternalCertificate(ctx context.Context, nodeID, serviceType string) (*InternalCertificate, error) {
	query := `
		SELECT id, node_id, cluster_id, service_type, cert_pem, key_pem, expires_at, created_at, updated_at
		FROM navigator.internal_certificates
		WHERE node_id = $1 AND service_type = $2
	`

	var cert InternalCertificate
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		return s.db.QueryRowContext(ctx, query, nodeID, serviceType).Scan(
			&cert.ID, &cert.NodeID, &cert.ClusterID, &cert.ServiceType, &cert.CertPEM, &cert.KeyPEM,
			&cert.ExpiresAt, &cert.CreatedAt, &cert.UpdatedAt,
		)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if cert.KeyPEM, err = s.decryptField(cert.KeyPEM); err != nil {
		return nil, fmt.Errorf("decrypt internal certificate key: %w", err)
	}
	return &cert, nil
}

func (s *Store) SaveInternalCertificate(ctx context.Context, cert *InternalCertificate) error {
	encryptedKey, err := s.encryptField(cert.KeyPEM)
	if err != nil {
		return fmt.Errorf("encrypt internal certificate key: %w", err)
	}

	query := `
		INSERT INTO navigator.internal_certificates (node_id, cluster_id, service_type, cert_pem, key_pem, expires_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, NOW())
		ON CONFLICT (node_id, service_type) DO UPDATE SET
			cluster_id = EXCLUDED.cluster_id,
			cert_pem = EXCLUDED.cert_pem,
			key_pem = EXCLUDED.key_pem,
			expires_at = EXCLUDED.expires_at,
			updated_at = NOW()
		RETURNING id, created_at
	`
	return database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		return s.db.QueryRowContext(ctx, query,
			cert.NodeID, cert.ClusterID, cert.ServiceType, cert.CertPEM, encryptedKey, cert.ExpiresAt,
		).Scan(&cert.ID, &cert.CreatedAt)
	})
}
