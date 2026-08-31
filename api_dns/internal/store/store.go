package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"frameworks/api_dns/internal/database/navigatordb"
	fieldcrypt "github.com/Livepeer-FrameWorks/monorepo/pkg/crypto"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
)

var (
	ErrNotFound           = errors.New("record not found")
	ErrAuthorityLost      = errors.New("write authority no longer exists")
	ErrIssuanceInProgress = errors.New("certificate issuance already in progress")
)

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
	// Version is an opaque certificate-content revision propagated through the
	// edge apply ACK so DNS readiness cannot cross a bundle replacement.
	Version string
	// AuthoritySubdomain fences tenant bundle writes against alias rename.
	// AuthorityVersion closes same-label teardown/reactivation and a→b→a
	// ABA windows. Both are runtime authority context and are not persisted.
	AuthoritySubdomain string
	AuthorityVersion   int64
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
	TenantID         string
	Subdomain        string
	Status           string // cert_issuing | cert_issued | cert_failed | tearing_down
	AuthorityVersion int64
	CertIssuedAt     sql.NullTime
	LastError        sql.NullString
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// TenantAliasRetirement is a retired alias label awaiting Bunny record
// cleanup after a subdomain rename. The active alias lives in
// tenant_aliases (keyed by tenant_id); this row carries the old label so
// the worker can clear its records independently of the active intent.
type TenantAliasRetirement struct {
	TenantID    string
	Subdomain   string
	RequestedAt time.Time
	Attempts    int
	LastError   sql.NullString
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

// CustomDomainHasCertificateAuthority is the canonical credential-retention
// status set. Pending verification and teardown do not preserve a certificate
// or ACME account; cert_failed does, because a retry still owns its prior
// credentials.
func CustomDomainHasCertificateAuthority(status string) bool {
	switch status {
	case "verified", "cert_issuing", "cert_issued", "cert_failed":
		return true
	default:
		return false
	}
}

// CustomDomainParticipatesInTenantBundle is deliberately narrower than
// credential retention. A failed custom-domain order must not poison renewal
// of the tenant alias and every otherwise healthy custom SAN.
func CustomDomainParticipatesInTenantBundle(status string) bool {
	switch status {
	case "verified", "cert_issuing", "cert_issued":
		return true
	default:
		return false
	}
}

// TenantEdgeApplyState records per-(tenant, edge, bundle) state. Drives
// DNS membership decisions for tenant smart record sets in cdn.{root}.
type TenantEdgeApplyState struct {
	TenantID             string
	ClusterID            string
	NodeID               string
	BundleID             string
	BundleVersion        string
	CurrentBundleVersion string
	CurrentBundlePresent bool
	State                string // pending_distribute | pending_apply | applied | in_dns
	LastSeedVersion      sql.NullInt64
	LastDeliverySequence int64
	LastAckAt            sql.NullTime
	InDNSAt              sql.NullTime
	UpdatedAt            time.Time
}

type Store struct {
	db  *sql.DB
	q   *navigatordb.Queries
	enc *fieldcrypt.FieldEncryptor // nil = no encryption (backward-compatible)
}

func NewStore(db *sql.DB, enc *fieldcrypt.FieldEncryptor) *Store {
	return &Store{db: db, q: navigatordb.New(db), enc: enc}
}

func certificateFromDB(row navigatordb.NavigatorCertificate) Certificate {
	return Certificate{
		ID: row.ID, TenantID: row.TenantID, Domain: row.Domain, CertPEM: row.CertPem, KeyPEM: row.KeyPem,
		ExpiresAt: row.ExpiresAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, IssuerCA: row.IssuerCa,
	}
}

func acmeAccountFromDB(row navigatordb.NavigatorAcmeAccount) ACMEAccount {
	return ACMEAccount{
		ID: row.ID, TenantID: row.TenantID, Email: row.Email, Registration: row.RegistrationJson,
		PrivateKeyPEM: row.PrivateKeyPem, CreatedAt: row.CreatedAt, CA: row.Ca,
	}
}

func tlsBundleFromDB(row navigatordb.NavigatorTlsBundle) TLSBundle {
	return TLSBundle{
		ID: row.ID, BundleID: row.BundleID, CertPEM: row.CertPem, KeyPEM: row.KeyPem,
		ExpiresAt: row.ExpiresAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, IssuerCA: row.IssuerCa,
		Version: row.Version,
	}
}

func tlsBundleVersion(certPEM string) string {
	sum := sha256.Sum256([]byte(certPEM))
	return fmt.Sprintf("%x", sum[:])
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
	var row navigatordb.NavigatorCertificate
	var err error
	err = database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		if tenantID == "" {
			row, err = s.q.GetPlatformCertificate(ctx, domain)
		} else {
			row, err = s.q.GetTenantCertificate(ctx, navigatordb.GetTenantCertificateParams{
				TenantID: sql.NullString{String: tenantID, Valid: true}, Domain: domain,
			})
		}
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	cert := certificateFromDB(row)
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
		return database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
			row, queryErr := s.q.SavePlatformCertificate(ctx, navigatordb.SavePlatformCertificateParams{
				Domain: cert.Domain, CertPem: cert.CertPEM, KeyPem: encryptedKey, ExpiresAt: cert.ExpiresAt, IssuerCa: issuer,
			})
			if queryErr == nil {
				cert.ID, cert.TenantID, cert.CreatedAt = row.ID, row.TenantID, row.CreatedAt
			}
			return queryErr
		})
	}
	err = database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		row, queryErr := s.q.SaveTenantCertificate(ctx, navigatordb.SaveTenantCertificateParams{
			TenantID: tenantID, Domain: cert.Domain, CertPem: cert.CertPEM, KeyPem: encryptedKey,
			ExpiresAt: cert.ExpiresAt, IssuerCa: issuer,
		})
		if queryErr == nil {
			cert.ID, cert.TenantID, cert.CreatedAt = row.ID, row.TenantID, row.CreatedAt
		}
		return queryErr
	})
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAuthorityLost
	}
	return err
}

// DeleteCertificate removes a stored cert (and its encrypted key) for the
// given (tenant_id, domain). Used during custom-domain teardown so cert
// material doesn't outlive the lifecycle row. Idempotent on missing rows.
func (s *Store) DeleteCertificate(ctx context.Context, tenantID, domain string) error {
	if tenantID == "" {
		return database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
			return s.q.DeletePlatformCertificate(ctx, domain)
		})
	}
	return database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		return s.q.DeleteTenantCertificate(ctx, navigatordb.DeleteTenantCertificateParams{
			TenantID: sql.NullString{String: tenantID, Valid: true}, Domain: domain,
		})
	})
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
	var row navigatordb.NavigatorAcmeAccount
	var err error
	err = database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		if tenantID == "" {
			row, err = s.q.GetPlatformACMEAccount(ctx, navigatordb.GetPlatformACMEAccountParams{Email: email, Ca: ca})
		} else {
			row, err = s.q.GetTenantACMEAccount(ctx, navigatordb.GetTenantACMEAccountParams{
				TenantID: sql.NullString{String: tenantID, Valid: true}, Email: email, Ca: ca,
			})
		}
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	acc := acmeAccountFromDB(row)
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
		return database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
			row, queryErr := s.q.SavePlatformACMEAccount(ctx, navigatordb.SavePlatformACMEAccountParams{
				Email: acc.Email, RegistrationJson: acc.Registration, PrivateKeyPem: encryptedKey, Ca: acc.CA,
			})
			if queryErr == nil {
				acc.ID, acc.TenantID, acc.CreatedAt = row.ID, row.TenantID, row.CreatedAt
			}
			return queryErr
		})
	}
	err = database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		row, queryErr := s.q.SaveTenantACMEAccount(ctx, navigatordb.SaveTenantACMEAccountParams{
			TenantID: tenantID, Email: acc.Email,
			RegistrationJson: acc.Registration, PrivateKeyPem: encryptedKey, Ca: acc.CA,
		})
		if queryErr == nil {
			acc.ID, acc.TenantID, acc.CreatedAt = row.ID, row.TenantID, row.CreatedAt
		}
		return queryErr
	})
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAuthorityLost
	}
	return err
}

// ListExpiringCertificates finds certs expiring within the given duration.
// Returns all certificates (platform-wide and tenant-specific) that are expiring.
func (s *Store) ListExpiringCertificates(ctx context.Context, threshold time.Duration) ([]Certificate, error) {
	expiryLimit := time.Now().Add(threshold)
	var rows []navigatordb.NavigatorCertificate
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		rows, queryErr = s.q.ListExpiringCertificates(ctx, expiryLimit)
		return queryErr
	})
	if err != nil {
		return nil, err
	}
	var certs []Certificate
	for _, row := range rows {
		c := certificateFromDB(row)
		if c.KeyPEM, err = s.decryptField(c.KeyPEM); err != nil {
			return nil, fmt.Errorf("decrypt certificate key for %s: %w", c.Domain, err)
		}
		certs = append(certs, c)
	}
	return certs, nil
}

// ListCertificatesForTenant returns all certificates belonging to a specific tenant.
func (s *Store) ListCertificatesForTenant(ctx context.Context, tenantID string) ([]Certificate, error) {
	var rows []navigatordb.NavigatorCertificate
	var err error
	err = database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		if tenantID == "" {
			rows, err = s.q.ListPlatformCertificates(ctx)
		} else {
			rows, err = s.q.ListTenantCertificates(ctx, sql.NullString{String: tenantID, Valid: true})
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	var certs []Certificate
	for _, row := range rows {
		c := certificateFromDB(row)
		if c.KeyPEM, err = s.decryptField(c.KeyPEM); err != nil {
			return nil, fmt.Errorf("decrypt certificate key for %s: %w", c.Domain, err)
		}
		certs = append(certs, c)
	}
	return certs, nil
}

func (s *Store) GetTLSBundle(ctx context.Context, bundleID string) (*TLSBundle, error) {
	return s.getTLSBundle(ctx, bundleID, false)
}

// GetTLSBundleForIssuance is the cache/CA-pinning read. It remains available
// while active alias authority is issuing or retrying; serving policy is a
// separate contract even when both currently preserve the last-good bundle.
func (s *Store) GetTLSBundleForIssuance(ctx context.Context, bundleID string) (*TLSBundle, error) {
	return s.getTLSBundle(ctx, bundleID, true)
}

func (s *Store) getTLSBundle(ctx context.Context, bundleID string, forIssuance bool) (*TLSBundle, error) {
	var row navigatordb.NavigatorTlsBundle
	var err error
	err = database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		if forIssuance {
			row, err = s.q.GetTLSBundleForIssuance(ctx, bundleID)
		} else {
			row, err = s.q.GetTLSBundle(ctx, bundleID)
		}
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	bundle := tlsBundleFromDB(row)
	if bundle.KeyPEM, err = s.decryptField(bundle.KeyPEM); err != nil {
		return nil, fmt.Errorf("decrypt tls bundle key: %w", err)
	}
	bundle.Domains, err = unmarshalDomains(row.Domains)
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
	version := strings.TrimSpace(bundle.Version)
	if version == "" {
		version = tlsBundleVersion(bundle.CertPEM)
		bundle.Version = version
	}
	err = database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		row, queryErr := s.q.SaveTLSBundle(ctx, navigatordb.SaveTLSBundleParams{
			BundleID: bundle.BundleID, Domains: domainsJSON, CertPem: bundle.CertPEM, KeyPem: encryptedKey,
			ExpiresAt: bundle.ExpiresAt, IssuerCa: issuer, ExpectedSubdomain: bundle.AuthoritySubdomain,
			ExpectedAuthorityVersion: bundle.AuthorityVersion, Version: version,
		})
		if queryErr == nil {
			bundle.ID = row.ID
			if row.CreatedAt.Valid {
				bundle.CreatedAt = row.CreatedAt.Time
			}
		}
		return queryErr
	})
	if errors.Is(err, sql.ErrNoRows) && strings.HasPrefix(bundle.BundleID, "tenant:") {
		return ErrAuthorityLost
	}
	return err
}

func (s *Store) TryAcquireCertificateIssuanceLease(ctx context.Context, leaseKey, owner string, ttl time.Duration) (bool, error) {
	if strings.TrimSpace(leaseKey) == "" || strings.TrimSpace(owner) == "" || ttl <= 0 {
		return false, errors.New("issuance lease requires key, owner, and positive ttl")
	}
	var acquired bool
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		acquired, queryErr = s.q.TryAcquireCertificateIssuanceLease(ctx, navigatordb.TryAcquireCertificateIssuanceLeaseParams{
			LeaseKey: leaseKey, LeaseOwner: owner, LeaseSeconds: int64(ttl / time.Second),
		})
		if errors.Is(queryErr, sql.ErrNoRows) {
			return nil
		}
		return queryErr
	})
	return acquired, err
}

func (s *Store) RenewCertificateIssuanceLease(ctx context.Context, leaseKey, owner string, ttl time.Duration) (bool, error) {
	if strings.TrimSpace(leaseKey) == "" || strings.TrimSpace(owner) == "" || ttl <= 0 {
		return false, errors.New("issuance lease renewal requires key, owner, and positive ttl")
	}
	var renewed bool
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		renewed, queryErr = s.q.RenewCertificateIssuanceLease(ctx, navigatordb.RenewCertificateIssuanceLeaseParams{
			LeaseKey: leaseKey, LeaseOwner: owner, LeaseSeconds: int64(ttl / time.Second),
		})
		if errors.Is(queryErr, sql.ErrNoRows) {
			return nil
		}
		return queryErr
	})
	return renewed, err
}

func (s *Store) ReleaseCertificateIssuanceLease(ctx context.Context, leaseKey, owner string) error {
	return database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		return s.q.ReleaseCertificateIssuanceLease(ctx, navigatordb.ReleaseCertificateIssuanceLeaseParams{
			LeaseKey: leaseKey, LeaseOwner: owner,
		})
	})
}

// DeleteExpiredCertificateIssuanceLeases bounds durable lease metadata after
// crashed issuers or retired tenant/domain identities. A live owner is never
// removed; expiry is also the point after which that owner cannot publish.
func (s *Store) DeleteExpiredCertificateIssuanceLeases(ctx context.Context) (int64, error) {
	var rows int64
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		rows, queryErr = s.q.DeleteExpiredCertificateIssuanceLeases(ctx)
		return queryErr
	})
	return rows, err
}

func (s *Store) ListExpiringTLSBundles(ctx context.Context, threshold time.Duration) ([]TLSBundle, error) {
	expiryLimit := time.Now().Add(threshold)
	var rows []navigatordb.NavigatorTlsBundle
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		rows, queryErr = s.q.ListExpiringTLSBundles(ctx, expiryLimit)
		return queryErr
	})
	if err != nil {
		return nil, err
	}
	var bundles []TLSBundle
	for _, row := range rows {
		bundle := tlsBundleFromDB(row)
		if bundle.KeyPEM, err = s.decryptField(bundle.KeyPEM); err != nil {
			return nil, fmt.Errorf("decrypt tls bundle key for %s: %w", bundle.BundleID, err)
		}
		bundle.Domains, err = unmarshalDomains(row.Domains)
		if err != nil {
			return nil, fmt.Errorf("decode tls bundle domains for %s: %w", bundle.BundleID, err)
		}
		bundles = append(bundles, bundle)
	}

	return bundles, nil
}

func (s *Store) GetInternalCA(ctx context.Context, role string) (*InternalCA, error) {
	var row navigatordb.NavigatorInternalCa
	var err error
	err = database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		row, err = s.q.GetInternalCA(ctx, role)
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	ca := InternalCA{
		Role: row.Role, CertPEM: row.CertPem, ExpiresAt: row.ExpiresAt,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	if row.KeyPem.Valid {
		ca.KeyPEM = row.KeyPem.String
	}
	if ca.KeyPEM != "" {
		if ca.KeyPEM, err = s.decryptField(ca.KeyPEM); err != nil {
			return nil, fmt.Errorf("decrypt internal ca key: %w", err)
		}
	}
	return &ca, nil
}

func (s *Store) SaveInternalCA(ctx context.Context, ca *InternalCA) error {
	var encryptedKey sql.NullString
	if ca.KeyPEM != "" {
		encoded, err := s.encryptField(ca.KeyPEM)
		if err != nil {
			return fmt.Errorf("encrypt internal ca key: %w", err)
		}
		encryptedKey = sql.NullString{String: encoded, Valid: true}
	}

	return database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		createdAt, queryErr := s.q.SaveInternalCA(ctx, navigatordb.SaveInternalCAParams{
			Role: ca.Role, CertPem: ca.CertPEM, KeyPem: encryptedKey, ExpiresAt: ca.ExpiresAt,
		})
		if queryErr == nil {
			ca.CreatedAt = createdAt
		}
		return queryErr
	})
}

func (s *Store) GetInternalCertificate(ctx context.Context, nodeID, serviceType string) (*InternalCertificate, error) {
	var row navigatordb.NavigatorInternalCertificate
	var err error
	err = database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		row, err = s.q.GetInternalCertificate(ctx, navigatordb.GetInternalCertificateParams{NodeID: nodeID, ServiceType: serviceType})
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	cert := InternalCertificate{
		ID: row.ID, NodeID: row.NodeID, ClusterID: row.ClusterID, ServiceType: row.ServiceType,
		CertPEM: row.CertPem, KeyPEM: row.KeyPem, ExpiresAt: row.ExpiresAt,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
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

	return database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		row, queryErr := s.q.SaveInternalCertificate(ctx, navigatordb.SaveInternalCertificateParams{
			NodeID: cert.NodeID, ClusterID: cert.ClusterID, ServiceType: cert.ServiceType,
			CertPem: cert.CertPEM, KeyPem: encryptedKey, ExpiresAt: cert.ExpiresAt,
		})
		if queryErr == nil {
			cert.ID, cert.CreatedAt = row.ID, row.CreatedAt
		}
		return queryErr
	})
}
